package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/brian-nunez/bkit/objex"
)

// fakeAPI implements api with overridable methods; unset methods fail the
// call so tests notice unexpected requests.
type fakeAPI struct {
	mu    sync.Mutex
	calls []string

	putObject      func(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
	getObject      func(*s3.GetObjectInput) (*s3.GetObjectOutput, error)
	headObject     func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error)
	listObjectsV2  func(*s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error)
	copyObject     func(*s3.CopyObjectInput) (*s3.CopyObjectOutput, error)
	createUpload   func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error)
	uploadPart     func(*s3.UploadPartInput) (*s3.UploadPartOutput, error)
	uploadPartCopy func(*s3.UploadPartCopyInput) (*s3.UploadPartCopyOutput, error)
	complete       func(*s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error)
	createBucket   func(*s3.CreateBucketInput) (*s3.CreateBucketOutput, error)
	listBuckets    func(*s3.ListBucketsInput) (*s3.ListBucketsOutput, error)
	headBucket     func(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error)
}

var errUnexpected = errors.New("unexpected call")

func (f *fakeAPI) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeAPI) called(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func call[I, O any](f *fakeAPI, name string, fn func(I) (O, error), in I) (O, error) {
	f.record(name)
	if fn == nil {
		var zero O
		return zero, fmt.Errorf("%w: %s", errUnexpected, name)
	}
	return fn(in)
}

func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return call(f, "PutObject", f.putObject, in)
}

func (f *fakeAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return call(f, "GetObject", f.getObject, in)
}

func (f *fakeAPI) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return call(f, "HeadObject", f.headObject, in)
}

func (f *fakeAPI) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.record("DeleteObject")
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return call(f, "ListObjectsV2", f.listObjectsV2, in)
}

func (f *fakeAPI) CopyObject(_ context.Context, in *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	return call(f, "CopyObject", f.copyObject, in)
}

func (f *fakeAPI) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return call(f, "CreateMultipartUpload", f.createUpload, in)
}

func (f *fakeAPI) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	return call(f, "UploadPart", f.uploadPart, in)
}

func (f *fakeAPI) UploadPartCopy(_ context.Context, in *s3.UploadPartCopyInput, _ ...func(*s3.Options)) (*s3.UploadPartCopyOutput, error) {
	return call(f, "UploadPartCopy", f.uploadPartCopy, in)
}

func (f *fakeAPI) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	return call(f, "CompleteMultipartUpload", f.complete, in)
}

func (f *fakeAPI) AbortMultipartUpload(_ context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.record("AbortMultipartUpload")
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (f *fakeAPI) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	return call(f, "CreateBucket", f.createBucket, in)
}

func (f *fakeAPI) DeleteBucket(_ context.Context, _ *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	f.record("DeleteBucket")
	return &s3.DeleteBucketOutput{}, nil
}

func (f *fakeAPI) ListBuckets(_ context.Context, in *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	return call(f, "ListBuckets", f.listBuckets, in)
}

func (f *fakeAPI) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return call(f, "HeadBucket", f.headBucket, in)
}

type fakePresign struct{}

func (fakePresign) PresignGetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return &v4.PresignedHTTPRequest{
		Method:       http.MethodGet,
		URL:          "https://s3.example.com/" + aws.ToString(in.Bucket) + "/" + aws.ToString(in.Key) + "?X-Amz-Signature=sig",
		SignedHeader: http.Header{"Host": {"s3.example.com"}},
	}, nil
}

func (fakePresign) PresignPutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	header := http.Header{"Host": {"s3.example.com"}}
	if in.ContentType != nil {
		header.Set("Content-Type", *in.ContentType)
	}
	if in.IfNoneMatch != nil {
		header.Set("If-None-Match", *in.IfNoneMatch)
	}
	return &v4.PresignedHTTPRequest{Method: http.MethodPut, URL: "https://s3.example.com/put", SignedHeader: header}, nil
}

func newTestClient(a *fakeAPI, region, endpoint string) *Client {
	return newClient(a, fakePresign{}, region, settings{
		endpoint:    endpoint,
		partSize:    minPartSize,
		concurrency: 2,
	})
}

func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code}
}

func statusError(status int) error {
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New(http.StatusText(status)),
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{apiError("NoSuchKey"), objex.ErrNotFound},
		{apiError("NotFound"), objex.ErrNotFound},
		{apiError("NoSuchBucket"), objex.ErrBucketNotFound},
		{apiError("AccessDenied"), objex.ErrPermission},
		{apiError("SignatureDoesNotMatch"), objex.ErrPermission},
		{apiError("PreconditionFailed"), objex.ErrPreconditionFailed},
		{apiError("ConditionalRequestConflict"), objex.ErrPreconditionFailed},
		{apiError("InvalidRange"), objex.ErrInvalidRange},
		{apiError("BucketAlreadyOwnedByYou"), objex.ErrBucketExists},
		{apiError("BucketAlreadyExists"), objex.ErrBucketExists},
		{apiError("BucketNotEmpty"), objex.ErrBucketNotEmpty},
		{apiError("InvalidBucketName"), objex.ErrInvalidBucketName},
		{apiError("NotImplemented"), objex.ErrUnsupported},
		{statusError(http.StatusNotFound), objex.ErrNotFound},
		{statusError(http.StatusForbidden), objex.ErrPermission},
		{statusError(http.StatusPreconditionFailed), objex.ErrPreconditionFailed},
		{statusError(http.StatusRequestedRangeNotSatisfiable), objex.ErrInvalidRange},
		{statusError(http.StatusNotImplemented), objex.ErrUnsupported},
		{statusError(http.StatusInternalServerError), nil},
		{apiError("SlowDown"), nil},
		{fmt.Errorf("request: %w", context.Canceled), nil},
		{errors.New("plain"), nil},
	}
	for _, c := range cases {
		if got := classify(c.err); !errors.Is(got, c.want) || (c.want == nil && got != nil) {
			t.Errorf("classify(%v) = %v, want %v", c.err, got, c.want)
		}
	}

	wrapped := wrap("get", "bucket", "key", fmt.Errorf("op: %w", context.Canceled))
	if !errors.Is(wrapped, context.Canceled) {
		t.Errorf("wrap lost context.Canceled: %v", wrapped)
	}
}

func TestNewOptions(t *testing.T) {
	ctx := context.Background()
	invalid := []Option{
		WithRegion(""),
		WithEndpoint("localhost:9000"),
		WithEndpoint("ftp://example.com"),
		WithCredentials("", "secret", ""),
		WithPartSize(1024),
		WithPartSize(6 << 30),
		WithUploadConcurrency(0),
	}
	for i, opt := range invalid {
		if _, err := New(ctx, opt); !errors.Is(err, objex.ErrInvalidOption) {
			t.Errorf("case %d: err = %v, want ErrInvalidOption", i, err)
		}
	}

	c, err := New(ctx, nil, WithAWSConfig(aws.Config{}), WithCredentials("id", "secret", ""), WithEndpoint("http://localhost:9000/"), WithPathStyle(), WithPartSize(16<<20), WithUploadConcurrency(3))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.region != defaultRegion || c.endpoint != "http://localhost:9000" || c.partSize != 16<<20 || c.concurrency != 3 {
		t.Errorf("Client = region %q endpoint %q part %d concurrency %d", c.region, c.endpoint, c.partSize, c.concurrency)
	}

	c, err = New(ctx, WithAWSConfig(aws.Config{Region: "eu-west-1"}))
	if err != nil || c.region != "eu-west-1" {
		t.Errorf("New with an aws.Config region = %q, %v", c.region, err)
	}
	c, err = New(ctx, WithAWSConfig(aws.Config{Region: "eu-west-1"}), WithRegion("ap-south-1"))
	if err != nil || c.region != "ap-south-1" {
		t.Errorf("WithRegion over aws.Config = %q, %v", c.region, err)
	}
	if got := c.Bucket("assets").Name(); got != "assets" {
		t.Errorf("Name() = %q", got)
	}
}

func TestHelpers(t *testing.T) {
	if got := rangeHeader(objex.Range{Offset: 5, Length: 10}); got != "bytes=5-14" {
		t.Errorf("rangeHeader = %q", got)
	}
	if got := rangeHeader(objex.Range{Offset: 5, Length: -1}); got != "bytes=5-" {
		t.Errorf("rangeHeader to end = %q", got)
	}
	if got := rangeHeader(objex.Range{Offset: 2, Length: math.MaxInt64}); got != "bytes=2-" {
		t.Errorf("rangeHeader with a huge length = %q, want %q", got, "bytes=2-")
	}
	if got := rangeHeader(objex.Range{Offset: 0, Length: math.MaxInt64}); got != fmt.Sprintf("bytes=0-%d", int64(math.MaxInt64-1)) {
		t.Errorf("rangeHeader with the largest length that fits = %q", got)
	}
	if total, ok := totalFromContentRange("bytes 0-9/1234"); !ok || total != 1234 {
		t.Errorf("totalFromContentRange = %d, %v", total, ok)
	}
	for _, bad := range []string{"", "bytes 0-9", "bytes 0-9/*"} {
		if _, ok := totalFromContentRange(bad); ok {
			t.Errorf("totalFromContentRange(%q) ok", bad)
		}
	}
	if got := copySource("bucket", "dir/a file+%.txt"); got != "bucket/dir/a%20file+%25.txt" {
		t.Errorf("copySource = %q", got)
	}
}

func TestMultipartAbortsWhenAPartFails(t *testing.T) {
	a := &fakeAPI{
		createUpload: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPart: func(in *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
			if aws.ToInt32(in.PartNumber) == 2 {
				return nil, apiError("InternalError")
			}
			return &s3.UploadPartOutput{ETag: aws.String(`"part"`)}, nil
		},
	}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")
	data := bytes.Repeat([]byte("x"), 3*minPartSize)

	if _, err := b.Put(context.Background(), "key", io.MultiReader(bytes.NewReader(data))); err == nil {
		t.Fatal("Put succeeded although a part failed")
	}
	if a.called("AbortMultipartUpload") != 1 || a.called("CompleteMultipartUpload") != 0 {
		t.Errorf("calls = %v, want an abort and no complete", a.calls)
	}
}

func TestMultipartCompletesInOrderWithConditions(t *testing.T) {
	var completed *s3.CompleteMultipartUploadInput
	a := &fakeAPI{
		createUpload: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPart: func(in *s3.UploadPartInput) (*s3.UploadPartOutput, error) {
			if aws.ToInt32(in.PartNumber) == 1 {
				time.Sleep(20 * time.Millisecond) // finish out of order
			}
			return &s3.UploadPartOutput{ETag: aws.String(fmt.Sprintf(`"p%d"`, aws.ToInt32(in.PartNumber)))}, nil
		},
		complete: func(in *s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
			completed = in
			return nil, apiError("PreconditionFailed")
		},
	}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")
	data := bytes.Repeat([]byte("y"), 2*minPartSize+10)

	_, err := b.Put(context.Background(), "key", io.MultiReader(bytes.NewReader(data)), objex.IfNotExists())
	if !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Fatalf("Put: err = %v, want ErrPreconditionFailed", err)
	}
	if completed == nil || aws.ToString(completed.IfNoneMatch) != "*" {
		t.Fatalf("CompleteMultipartUpload input = %+v, want If-None-Match *", completed)
	}
	var numbers []int32
	for _, part := range completed.MultipartUpload.Parts {
		numbers = append(numbers, aws.ToInt32(part.PartNumber))
	}
	if !slices.Equal(numbers, []int32{1, 2, 3}) {
		t.Errorf("completed parts = %v, want [1 2 3]", numbers)
	}
	if a.called("AbortMultipartUpload") != 1 {
		t.Errorf("failed complete did not abort: %v", a.calls)
	}
}

func TestMultipartCreateFails(t *testing.T) {
	a := &fakeAPI{createUpload: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
		return nil, apiError("AccessDenied")
	}}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")
	data := bytes.Repeat([]byte("z"), minPartSize+1)
	if _, err := b.Put(context.Background(), "key", io.MultiReader(bytes.NewReader(data))); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("Put: err = %v, want ErrPermission", err)
	}
}

func TestPutReadError(t *testing.T) {
	b := newTestClient(&fakeAPI{}, defaultRegion, "").Bucket("bucket")
	failing := io.MultiReader(strings.NewReader("partial"), errReader{})
	if _, err := b.Put(context.Background(), "key", failing); !errors.Is(err, errRead) {
		t.Errorf("Put: err = %v, want the reader's error", err)
	}
}

var errRead = errors.New("read failed")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errRead }

func TestCopyLargeObjectUsesUploadPartCopy(t *testing.T) {
	const size = int64(maxCopyObjectSize) + 3*minPartSize/2
	var ranges []string
	a := &fakeAPI{
		headObject: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{ContentLength: aws.Int64(size), ETag: aws.String(`"src"`), ContentType: aws.String("video/mp4")}, nil
		},
		createUpload: func(in *s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			if aws.ToString(in.ContentType) != "video/mp4" {
				t.Errorf("CreateMultipartUpload ContentType = %q", aws.ToString(in.ContentType))
			}
			return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPartCopy: func(in *s3.UploadPartCopyInput) (*s3.UploadPartCopyOutput, error) {
			if aws.ToString(in.CopySourceIfMatch) != `"src"` || aws.ToString(in.CopySource) != "bucket/big.mp4" {
				t.Errorf("UploadPartCopy source = %q if-match %q", aws.ToString(in.CopySource), aws.ToString(in.CopySourceIfMatch))
			}
			ranges = append(ranges, aws.ToString(in.CopySourceRange))
			return &s3.UploadPartCopyOutput{CopyPartResult: &types.CopyPartResult{ETag: aws.String(`"p"`)}}, nil
		},
		complete: func(*s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
			return &s3.CompleteMultipartUploadOutput{ETag: aws.String(`"done-2"`)}, nil
		},
	}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")

	info, err := b.Copy(context.Background(), "copy.mp4", "big.mp4")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if info.ETag != "done-2" || info.Size != size || info.Key != "copy.mp4" {
		t.Errorf("Copy info = %+v", info)
	}
	if ranges[0] != fmt.Sprintf("bytes=0-%d", minPartSize-1) || ranges[len(ranges)-1] != fmt.Sprintf("bytes=%d-%d", int64(len(ranges)-1)*minPartSize, size-1) {
		t.Errorf("ranges = first %q last %q (%d parts)", ranges[0], ranges[len(ranges)-1], len(ranges))
	}
}

func TestCopyLargeObjectAbortsOnFailure(t *testing.T) {
	a := &fakeAPI{
		headObject: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{ContentLength: aws.Int64(maxCopyObjectSize + 1), ETag: aws.String(`"src"`)}, nil
		},
		createUpload: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPartCopy: func(*s3.UploadPartCopyInput) (*s3.UploadPartCopyOutput, error) {
			return nil, apiError("PreconditionFailed")
		},
	}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")
	if _, err := b.Copy(context.Background(), "dst", "src"); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("Copy: err = %v, want ErrPreconditionFailed", err)
	}
	if a.called("AbortMultipartUpload") != 1 {
		t.Errorf("calls = %v, want an abort", a.calls)
	}
}

func TestCopyReplacesMetadata(t *testing.T) {
	var got *s3.CopyObjectInput
	a := &fakeAPI{
		headObject: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{ContentLength: aws.Int64(3), ETag: aws.String(`"e"`), ContentType: aws.String("text/plain"), Metadata: map[string]string{"a": "1"}}, nil
		},
		copyObject: func(in *s3.CopyObjectInput) (*s3.CopyObjectOutput, error) {
			got = in
			return &s3.CopyObjectOutput{}, nil
		},
	}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")
	info, err := b.Copy(context.Background(), "dst", "src", objex.WithMetadata(map[string]string{"b": "2"}))
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got.MetadataDirective != types.MetadataDirectiveReplace || aws.ToString(got.ContentType) != "text/plain" || got.Metadata["b"] != "2" || len(got.Metadata) != 1 {
		t.Errorf("CopyObject input = directive %q type %q metadata %v", got.MetadataDirective, aws.ToString(got.ContentType), got.Metadata)
	}
	if info.LastModified.IsZero() {
		t.Error("Copy info LastModified is zero")
	}
}

func TestListPaginationAndMerge(t *testing.T) {
	var inputs []s3.ListObjectsV2Input
	a := &fakeAPI{listObjectsV2: func(in *s3.ListObjectsV2Input) (*s3.ListObjectsV2Output, error) {
		inputs = append(inputs, *in)
		if in.ContinuationToken == nil {
			return &s3.ListObjectsV2Output{
				Contents:              []types.Object{{Key: aws.String("a.txt"), Size: aws.Int64(1), ETag: aws.String(`"1"`)}, {Key: aws.String("c.txt")}},
				CommonPrefixes:        []types.CommonPrefix{{Prefix: aws.String("b/")}},
				IsTruncated:           aws.Bool(true),
				NextContinuationToken: aws.String("next"),
			}, nil
		}
		return &s3.ListObjectsV2Output{Contents: []types.Object{{Key: aws.String("d.txt")}}}, nil
	}}
	b := newTestClient(a, defaultRegion, "").Bucket("bucket")

	var keys []string
	for info, err := range b.List(context.Background(), objex.WithDelimiter("/"), objex.WithStartAfter("0"), objex.WithPrefix("")) {
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, info.Key)
	}
	if !slices.Equal(keys, []string{"a.txt", "b/", "c.txt", "d.txt"}) {
		t.Errorf("keys = %q", keys)
	}
	if len(inputs) != 2 || aws.ToString(inputs[0].StartAfter) != "0" || inputs[1].StartAfter != nil || aws.ToString(inputs[1].ContinuationToken) != "next" {
		t.Errorf("list inputs = %+v", inputs)
	}

	inputs = nil
	for _, err := range b.List(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if len(inputs) != 1 {
		t.Errorf("breaking out of List made %d requests, want 1", len(inputs))
	}
}

func TestPresign(t *testing.T) {
	b := newTestClient(&fakeAPI{}, defaultRegion, "").Bucket("bucket")
	ctx := context.Background()

	req, err := b.PresignGet(ctx, "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Host") != "" || req.Method != http.MethodGet || time.Until(req.Expires) > time.Minute {
		t.Errorf("PresignGet = %+v", req)
	}

	req, err = b.PresignPut(ctx, "k", time.Minute, objex.WithContentType("text/plain"), objex.IfNotExists())
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Content-Type") != "text/plain" || req.Header.Get("If-None-Match") != "*" || req.Header.Get("Host") != "" {
		t.Errorf("PresignPut headers = %v", req.Header)
	}

	if _, err := b.PresignGet(ctx, "k", 8*24*time.Hour); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("ttl over seven days: err = %v", err)
	}
	if _, err := b.PresignPut(ctx, "k", time.Minute, objex.WithSize(-3)); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("invalid put option: err = %v", err)
	}
	if got := presigned(http.MethodGet, "u", nil, time.Second); got.Header == nil {
		t.Error("presigned with nil header returned a nil Header")
	}
}

func TestCreateBucketLocationConstraint(t *testing.T) {
	var got *s3.CreateBucketInput
	a := &fakeAPI{createBucket: func(in *s3.CreateBucketInput) (*s3.CreateBucketOutput, error) {
		got = in
		return &s3.CreateBucketOutput{}, nil
	}}
	ctx := context.Background()

	if err := newTestClient(a, "eu-west-1", "").CreateBucket(ctx, "bucket"); err != nil {
		t.Fatal(err)
	}
	if got.CreateBucketConfiguration == nil || got.CreateBucketConfiguration.LocationConstraint != "eu-west-1" {
		t.Errorf("AWS eu-west-1 constraint = %+v", got.CreateBucketConfiguration)
	}
	for _, c := range []*Client{newTestClient(a, defaultRegion, ""), newTestClient(a, "auto", "https://r2.example.com")} {
		if err := c.CreateBucket(ctx, "bucket"); err != nil {
			t.Fatal(err)
		}
		if got.CreateBucketConfiguration != nil {
			t.Errorf("unexpected constraint for region %q endpoint %q", c.region, c.endpoint)
		}
	}
}

func TestListBucketsPaginatesAndSorts(t *testing.T) {
	a := &fakeAPI{listBuckets: func(in *s3.ListBucketsInput) (*s3.ListBucketsOutput, error) {
		if in.ContinuationToken == nil {
			return &s3.ListBucketsOutput{Buckets: []types.Bucket{{Name: aws.String("zeta")}}, ContinuationToken: aws.String("more")}, nil
		}
		return &s3.ListBucketsOutput{Buckets: []types.Bucket{{Name: aws.String("alpha")}}}, nil
	}}
	buckets, err := newTestClient(a, defaultRegion, "").ListBuckets(context.Background())
	if err != nil || len(buckets) != 2 || buckets[0].Name != "alpha" || buckets[1].Name != "zeta" {
		t.Errorf("ListBuckets = %+v, %v", buckets, err)
	}
}

func TestBucketExistsAndPing(t *testing.T) {
	ctx := context.Background()
	status := http.StatusNotFound
	a := &fakeAPI{headBucket: func(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
		if status == http.StatusOK {
			return &s3.HeadBucketOutput{}, nil
		}
		return nil, statusError(status)
	}}
	c := newTestClient(a, defaultRegion, "")

	if ok, err := c.BucketExists(ctx, "bucket"); ok || err != nil {
		t.Errorf("BucketExists on 404 = %v, %v", ok, err)
	}
	if err := c.Bucket("bucket").Ping(ctx); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Ping on 404: err = %v", err)
	}
	status = http.StatusForbidden
	if _, err := c.BucketExists(ctx, "bucket"); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("BucketExists on 403: err = %v", err)
	}
	if err := c.Bucket("bucket").Ping(ctx); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("Ping on 403: err = %v", err)
	}
	status = http.StatusOK
	if err := c.Bucket("bucket").Ping(ctx); err != nil {
		t.Errorf("Ping on 200: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.Bucket("bucket").Ping(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("Ping canceled: err = %v", err)
	}
	for _, err := range c.Bucket("bucket").List(canceled) {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("List canceled: err = %v", err)
		}
	}
}

func TestGetUsesContentRangeTotal(t *testing.T) {
	var got *s3.GetObjectInput
	a := &fakeAPI{getObject: func(in *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
		got = in
		return &s3.GetObjectOutput{
			Body:          io.NopCloser(strings.NewReader("abc")),
			ContentLength: aws.Int64(3),
			ContentRange:  aws.String("bytes 2-4/100"),
			ETag:          aws.String(`"etag"`),
		}, nil
	}}
	obj, err := newTestClient(a, defaultRegion, "").Bucket("bucket").Get(context.Background(), "k", objex.WithRange(2, 3), objex.IfMatch("etag"))
	if err != nil {
		t.Fatal(err)
	}
	_ = obj.Close()
	if obj.Info.Size != 100 || obj.Info.ETag != "etag" {
		t.Errorf("Info = %+v", obj.Info)
	}
	if aws.ToString(got.Range) != "bytes=2-4" || aws.ToString(got.IfMatch) != `"etag"` {
		t.Errorf("GetObject input = range %q if-match %q", aws.ToString(got.Range), aws.ToString(got.IfMatch))
	}
}
