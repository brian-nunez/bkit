package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/brian-nunez/bkit/objex"
	"github.com/brian-nunez/bkit/objex/objextest"
)

// fakeS3 is an in-memory implementation of the S3 operations the driver
// uses, following S3's semantics and error codes closely enough to run the
// objex conformance suite without a server. ListObjectsV2 returns small
// pages so pagination is exercised.
type fakeS3 struct {
	mu       sync.Mutex
	buckets  map[string]*fakeBucket
	uploads  map[string]*fakeUpload
	nextID   int
	pageSize int
}

type fakeBucket struct {
	created time.Time
	objects map[string]*fakeObject
}

type fakeObject struct {
	data        []byte
	etag        string
	contentType string
	metadata    map[string]string
	modified    time.Time
}

type fakeUpload struct {
	bucket, key string
	contentType string
	metadata    map[string]string
	parts       map[int32][]byte
}

func newFakeS3() *fakeS3 {
	return &fakeS3{buckets: map[string]*fakeBucket{}, uploads: map[string]*fakeUpload{}, pageSize: 3}
}

func notFoundStatus() error { return statusError(http.StatusNotFound) }

func md5ETag(data []byte) string {
	sum := md5.Sum(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (f *fakeS3) bucket(name string) (*fakeBucket, error) {
	b, ok := f.buckets[name]
	if !ok {
		return nil, apiError("NoSuchBucket")
	}
	return b, nil
}

// checkWrite applies If-None-Match and If-Match to a write of key.
func checkWrite(b *fakeBucket, key string, ifNoneMatch, ifMatch *string) error {
	existing, exists := b.objects[key]
	if aws.ToString(ifNoneMatch) == "*" && exists {
		return apiError("PreconditionFailed")
	}
	if ifMatch != nil {
		if !exists {
			return apiError("NoSuchKey")
		}
		if existing.etag != aws.ToString(ifMatch) {
			return apiError("PreconditionFailed")
		}
	}
	return nil
}

func (f *fakeS3) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(aws.ToString(in.Bucket))
	if err != nil {
		return nil, err
	}
	if err := checkWrite(b, aws.ToString(in.Key), in.IfNoneMatch, in.IfMatch); err != nil {
		return nil, err
	}
	etag := md5ETag(data)
	b.objects[aws.ToString(in.Key)] = &fakeObject{
		data: data, etag: etag, contentType: aws.ToString(in.ContentType),
		metadata: maps.Clone(in.Metadata), modified: time.Now().UTC().Truncate(time.Second),
	}
	return &s3.PutObjectOutput{ETag: aws.String(etag)}, nil
}

func (f *fakeS3) object(bucket, key string) (*fakeObject, error) {
	b, err := f.bucket(bucket)
	if err != nil {
		return nil, err
	}
	obj, ok := b.objects[key]
	if !ok {
		return nil, apiError("NoSuchKey")
	}
	return obj, nil
}

func (f *fakeS3) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, err := f.object(aws.ToString(in.Bucket), aws.ToString(in.Key))
	if err != nil {
		return nil, err
	}
	if in.IfMatch != nil && aws.ToString(in.IfMatch) != obj.etag {
		return nil, apiError("PreconditionFailed")
	}
	data := obj.data
	out := &s3.GetObjectOutput{
		ETag: aws.String(obj.etag), ContentType: aws.String(obj.contentType),
		LastModified: aws.Time(obj.modified), Metadata: maps.Clone(obj.metadata),
	}
	if r := aws.ToString(in.Range); r != "" {
		start, end, err := parseRange(r, int64(len(data)))
		if err != nil {
			return nil, err
		}
		out.ContentRange = aws.String(fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		data = data[start : end+1]
	}
	out.Body = io.NopCloser(bytes.NewReader(data))
	out.ContentLength = aws.Int64(int64(len(data)))
	return out, nil
}

func parseRange(header string, size int64) (int64, int64, error) {
	spec := strings.TrimPrefix(header, "bytes=")
	first, last, _ := strings.Cut(spec, "-")
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start >= size {
		return 0, 0, apiError("InvalidRange")
	}
	end := size - 1
	if last != "" {
		if end, err = strconv.ParseInt(last, 10, 64); err != nil {
			return 0, 0, apiError("InvalidRange")
		}
		end = min(end, size-1)
	}
	return start, end, nil
}

func (f *fakeS3) HeadObject(ctx context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, err := f.object(aws.ToString(in.Bucket), aws.ToString(in.Key))
	if err != nil {
		// HEAD responses carry no error code, for missing keys and buckets.
		return nil, notFoundStatus()
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(obj.data))), ETag: aws.String(obj.etag),
		ContentType: aws.String(obj.contentType), LastModified: aws.Time(obj.modified),
		Metadata: maps.Clone(obj.metadata),
	}, nil
}

func (f *fakeS3) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(aws.ToString(in.Bucket))
	if err != nil {
		return nil, err
	}
	delete(b.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(aws.ToString(in.Bucket))
	if err != nil {
		return nil, err
	}

	prefix, delimiter := aws.ToString(in.Prefix), aws.ToString(in.Delimiter)
	after := aws.ToString(in.StartAfter)
	if token := aws.ToString(in.ContinuationToken); token != "" {
		after = token
	}

	keys := slices.Sorted(maps.Keys(b.objects))
	type entry struct {
		key      string
		isPrefix bool
	}
	var entries []entry
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		e := entry{key: key}
		if delimiter != "" {
			if i := strings.Index(key[len(prefix):], delimiter); i >= 0 {
				e = entry{key: key[:len(prefix)+i+len(delimiter)], isPrefix: true}
			}
		}
		if e.key <= after || (len(entries) > 0 && entries[len(entries)-1].key == e.key) {
			continue
		}
		entries = append(entries, e)
	}

	out := &s3.ListObjectsV2Output{}
	if len(entries) > f.pageSize {
		entries = entries[:f.pageSize]
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(entries[len(entries)-1].key)
	}
	for _, e := range entries {
		if e.isPrefix {
			out.CommonPrefixes = append(out.CommonPrefixes, types.CommonPrefix{Prefix: aws.String(e.key)})
			continue
		}
		obj := b.objects[e.key]
		out.Contents = append(out.Contents, types.Object{
			Key: aws.String(e.key), Size: aws.Int64(int64(len(obj.data))),
			ETag: aws.String(obj.etag), LastModified: aws.Time(obj.modified),
		})
	}
	return out, nil
}

func parseCopySource(source string) (string, string) {
	bucket, escaped, _ := strings.Cut(source, "/")
	key, err := url.PathUnescape(escaped)
	if err != nil {
		key = escaped
	}
	return bucket, key
}

func (f *fakeS3) CopyObject(ctx context.Context, in *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	srcBucket, srcKey := parseCopySource(aws.ToString(in.CopySource))
	src, err := f.object(srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	if in.CopySourceIfMatch != nil && aws.ToString(in.CopySourceIfMatch) != src.etag {
		return nil, apiError("PreconditionFailed")
	}
	// S3 rejects copying an object onto itself without replacing anything.
	if srcBucket == aws.ToString(in.Bucket) && srcKey == aws.ToString(in.Key) && in.MetadataDirective != types.MetadataDirectiveReplace {
		return nil, apiError("InvalidRequest")
	}
	dst, err := f.bucket(aws.ToString(in.Bucket))
	if err != nil {
		return nil, err
	}
	copied := *src
	copied.metadata = maps.Clone(src.metadata)
	if in.MetadataDirective == types.MetadataDirectiveReplace {
		copied.contentType = aws.ToString(in.ContentType)
		copied.metadata = maps.Clone(in.Metadata)
	}
	copied.modified = time.Now().UTC().Truncate(time.Second)
	dst.objects[aws.ToString(in.Key)] = &copied
	return &s3.CopyObjectOutput{CopyObjectResult: &types.CopyObjectResult{
		ETag: aws.String(copied.etag), LastModified: aws.Time(copied.modified),
	}}, nil
}

func (f *fakeS3) CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.bucket(aws.ToString(in.Bucket)); err != nil {
		return nil, err
	}
	f.nextID++
	id := strconv.Itoa(f.nextID)
	f.uploads[id] = &fakeUpload{
		bucket: aws.ToString(in.Bucket), key: aws.ToString(in.Key),
		contentType: aws.ToString(in.ContentType), metadata: maps.Clone(in.Metadata),
		parts: map[int32][]byte{},
	}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (f *fakeS3) UploadPart(ctx context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	upload, ok := f.uploads[aws.ToString(in.UploadId)]
	if !ok {
		return nil, apiError("NoSuchUpload")
	}
	upload.parts[aws.ToInt32(in.PartNumber)] = data
	return &s3.UploadPartOutput{ETag: aws.String(md5ETag(data))}, nil
}

func (f *fakeS3) UploadPartCopy(ctx context.Context, in *s3.UploadPartCopyInput, _ ...func(*s3.Options)) (*s3.UploadPartCopyOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	srcBucket, srcKey := parseCopySource(aws.ToString(in.CopySource))
	src, err := f.object(srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	start, end, err := parseRange(aws.ToString(in.CopySourceRange), int64(len(src.data)))
	if err != nil {
		return nil, err
	}
	upload := f.uploads[aws.ToString(in.UploadId)]
	upload.parts[aws.ToInt32(in.PartNumber)] = src.data[start : end+1]
	return &s3.UploadPartCopyOutput{CopyPartResult: &types.CopyPartResult{ETag: aws.String(md5ETag(src.data[start : end+1]))}}, nil
}

func (f *fakeS3) CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	upload, ok := f.uploads[aws.ToString(in.UploadId)]
	if !ok {
		return nil, apiError("NoSuchUpload")
	}
	b, err := f.bucket(upload.bucket)
	if err != nil {
		return nil, err
	}
	if err := checkWrite(b, upload.key, in.IfNoneMatch, in.IfMatch); err != nil {
		return nil, err
	}
	var data []byte
	for _, part := range in.MultipartUpload.Parts {
		data = append(data, upload.parts[aws.ToInt32(part.PartNumber)]...)
	}
	etag := strings.TrimSuffix(md5ETag(data), `"`) + fmt.Sprintf("-%d\"", len(in.MultipartUpload.Parts))
	b.objects[upload.key] = &fakeObject{
		data: data, etag: etag, contentType: upload.contentType,
		metadata: upload.metadata, modified: time.Now().UTC().Truncate(time.Second),
	}
	delete(f.uploads, aws.ToString(in.UploadId))
	return &s3.CompleteMultipartUploadOutput{ETag: aws.String(etag)}, nil
}

func (f *fakeS3) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.uploads, aws.ToString(in.UploadId))
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (f *fakeS3) CreateBucket(ctx context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Bucket)
	if _, ok := f.buckets[name]; ok {
		return nil, apiError("BucketAlreadyOwnedByYou")
	}
	f.buckets[name] = &fakeBucket{created: time.Now().UTC(), objects: map[string]*fakeObject{}}
	return &s3.CreateBucketOutput{}, nil
}

func (f *fakeS3) DeleteBucket(ctx context.Context, in *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(aws.ToString(in.Bucket))
	if err != nil {
		return nil, err
	}
	if len(b.objects) > 0 {
		return nil, apiError("BucketNotEmpty")
	}
	delete(f.buckets, aws.ToString(in.Bucket))
	return &s3.DeleteBucketOutput{}, nil
}

func (f *fakeS3) ListBuckets(ctx context.Context, _ *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &s3.ListBucketsOutput{}
	for name, b := range f.buckets {
		out.Buckets = append(out.Buckets, types.Bucket{Name: aws.String(name), CreationDate: aws.Time(b.created)})
	}
	return out, nil
}

func (f *fakeS3) HeadBucket(ctx context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.buckets[aws.ToString(in.Bucket)]; !ok {
		return nil, notFoundStatus()
	}
	return &s3.HeadBucketOutput{}, nil
}

func newFakeClient(fake *fakeS3) *Client {
	return newClient(fake, fakePresign{}, defaultRegion, settings{partSize: minPartSize, concurrency: 2})
}

func TestConformanceAgainstFake(t *testing.T) {
	objextest.Run(t, objextest.Config{
		NewBucket: func(t *testing.T) objex.Bucket {
			c := newFakeClient(newFakeS3())
			if err := c.CreateBucket(context.Background(), "objextest"); err != nil {
				t.Fatal(err)
			}
			return c.Bucket("objextest")
		},
		Metadata:    true,
		ContentType: true,
	})
}

func TestManagerConformanceAgainstFake(t *testing.T) {
	c := newFakeClient(newFakeS3())
	var mu sync.Mutex
	counter := 0
	objextest.RunManager(t, objextest.ManagerConfig{
		Manager: c,
		Bucket:  func(name string) objex.Bucket { return c.Bucket(name) },
		NewBucketName: func(*testing.T) string {
			mu.Lock()
			defer mu.Unlock()
			counter++
			return fmt.Sprintf("objextest-%d", counter)
		},
	})
}

// fakeBucketClient returns a client over a fresh fake S3 with a bucket.
func fakeBucketClient(t *testing.T) (*Client, *fakeS3, *Bucket) {
	t.Helper()
	fake := newFakeS3()
	c := newFakeClient(fake)
	if err := c.CreateBucket(context.Background(), "bucket"); err != nil {
		t.Fatal(err)
	}
	return c, fake, c.Bucket("bucket")
}

// sizedReader yields n bytes, then err (io.EOF when nil), hiding its size.
type sizedReader struct {
	n   int
	err error
}

func (r *sizedReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		if r.err != nil {
			return 0, r.err
		}
		return 0, io.EOF
	}
	n := min(len(p), r.n)
	for i := range n {
		p[i] = 'x'
	}
	r.n -= n
	return n, nil
}

func TestPutPartBoundaries(t *testing.T) {
	ctx := context.Background()
	_, _, b := fakeBucketClient(t)

	info, err := b.Put(ctx, "exact", &sizedReader{n: minPartSize})
	if err != nil || strings.Contains(info.ETag, "-") || info.Size != minPartSize {
		t.Errorf("exactly one part = %+v, %v; want a single PutObject", info, err)
	}
	info, err = b.Put(ctx, "two-parts", &sizedReader{n: 2 * minPartSize})
	if err != nil || !strings.HasSuffix(info.ETag, "-2") || info.Size != 2*minPartSize {
		t.Errorf("exactly two parts = %+v, %v; want a 2-part upload", info, err)
	}
}

func TestGetRangeHugeLength(t *testing.T) {
	ctx := context.Background()
	_, _, b := fakeBucketClient(t)
	if _, err := b.Put(ctx, "k", strings.NewReader("abcdef")); err != nil {
		t.Fatal(err)
	}
	obj, err := b.Get(ctx, "k", objex.WithRange(2, math.MaxInt64))
	if err != nil {
		t.Fatalf("Get with a huge range length: %v", err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil || string(data) != "cdef" || obj.Info.Size != 6 {
		t.Errorf("Get with a huge range length = %q (size %d), %v; want %q", data, obj.Info.Size, err, "cdef")
	}
}

func TestPutSizeMismatch(t *testing.T) {
	ctx := context.Background()
	_, fake, b := fakeBucketClient(t)

	if _, err := b.Put(ctx, "small", strings.NewReader("four"), objex.WithSize(10)); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("single-part mismatch: err = %v, want ErrInvalidOption", err)
	}
	if _, err := b.Put(ctx, "large", &sizedReader{n: minPartSize + 1}, objex.WithSize(3)); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("multipart mismatch: err = %v, want ErrInvalidOption", err)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("%d multipart uploads left open after a mismatch", len(fake.uploads))
	}
}

func TestPutReadFailures(t *testing.T) {
	ctx := context.Background()
	_, fake, b := fakeBucketClient(t)

	if _, err := b.Put(ctx, "probe", &sizedReader{n: minPartSize, err: errRead}); !errors.Is(err, errRead) {
		t.Errorf("error right after the first part: err = %v, want the read error", err)
	}
	if _, err := b.Put(ctx, "mid", &sizedReader{n: minPartSize + 10, err: errRead}); !errors.Is(err, errRead) {
		t.Errorf("error in a later part: err = %v, want the read error", err)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("%d multipart uploads left open after a read error", len(fake.uploads))
	}
}

func TestPutTooManyParts(t *testing.T) {
	c, fake, b := fakeBucketClient(t)
	c.maxParts = 1
	if _, err := b.Put(context.Background(), "big", &sizedReader{n: 2*minPartSize + 1}); err == nil || !strings.Contains(err.Error(), "parts") {
		t.Errorf("err = %v, want the part-limit error", err)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("%d multipart uploads left open", len(fake.uploads))
	}
}

func TestPutExactlyMaxParts(t *testing.T) {
	ctx := context.Background()
	c, fake, b := fakeBucketClient(t)
	c.maxParts = 2

	info, err := b.Put(ctx, "full", &sizedReader{n: 2 * minPartSize})
	if err != nil || !strings.HasSuffix(info.ETag, "-2") || info.Size != 2*minPartSize {
		t.Errorf("body of exactly maxParts parts = %+v, %v; want a 2-part upload", info, err)
	}
	if _, err := b.Put(ctx, "failing", &sizedReader{n: 2 * minPartSize, err: errRead}); !errors.Is(err, errRead) {
		t.Errorf("read error after maxParts parts: err = %v, want the read error", err)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("%d multipart uploads left open", len(fake.uploads))
	}
}

// blockingS3 holds the first UploadPart until the request is canceled, then
// reports success, so cancellation is observed by the upload loop itself.
type blockingS3 struct {
	*fakeS3
	started chan struct{}
	once    sync.Once
}

func (f *blockingS3) UploadPart(ctx context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	return &s3.UploadPartOutput{ETag: aws.String(`"part"`)}, nil
}

func TestPutCanceledWhileWaitingForAPartSlot(t *testing.T) {
	fake := newFakeS3()
	blocking := &blockingS3{fakeS3: fake, started: make(chan struct{})}
	c := newClient(blocking, fakePresign{}, defaultRegion, settings{partSize: minPartSize, concurrency: 1})
	if err := c.CreateBucket(context.Background(), "bucket"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-blocking.started
		cancel()
	}()

	if _, err := c.Bucket("bucket").Put(ctx, "big", &sizedReader{n: 3 * minPartSize}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(fake.uploads) != 0 {
		t.Errorf("%d multipart uploads left open after cancellation", len(fake.uploads))
	}
}

func TestBucketOperationsOnMissingBucket(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(newFakeS3())
	b := c.Bucket("absent")

	if _, err := b.Put(ctx, "k", strings.NewReader("x")); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Put: err = %v, want ErrBucketNotFound", err)
	}
	if _, err := b.Get(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Get: err = %v, want ErrBucketNotFound", err)
	}
	if _, err := b.Stat(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) || errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Stat: err = %v, want ErrBucketNotFound only", err)
	}
	if _, err := b.Copy(ctx, "a", "b"); !errors.Is(err, objex.ErrBucketNotFound) || errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Copy: err = %v, want ErrBucketNotFound only", err)
	}
	if err := b.Delete(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Delete: err = %v, want ErrBucketNotFound", err)
	}
	for _, err := range b.List(ctx) {
		if !errors.Is(err, objex.ErrBucketNotFound) {
			t.Errorf("List: err = %v, want ErrBucketNotFound", err)
		}
	}
}

func TestCopyOntoItself(t *testing.T) {
	ctx := context.Background()
	_, _, b := fakeBucketClient(t)
	put, err := b.Put(ctx, "same", strings.NewReader("data"), objex.WithContentType("text/plain"), objex.WithMetadata(map[string]string{"a": "1"}))
	if err != nil {
		t.Fatal(err)
	}

	info, err := b.Copy(ctx, "same", "same")
	if err != nil {
		t.Fatalf("Copy onto itself: %v", err)
	}
	if info.Key != "same" || info.Size != 4 || info.ETag != put.ETag || info.ContentType != "text/plain" || info.Metadata["a"] != "1" || len(info.Metadata) != 1 {
		t.Errorf("Copy onto itself = %+v", info)
	}
	stat, err := b.Stat(ctx, "same")
	if err != nil || stat.ContentType != "text/plain" || stat.Metadata["a"] != "1" || len(stat.Metadata) != 1 {
		t.Errorf("Stat after copy onto itself = %+v, %v", stat, err)
	}
	if _, err := b.Copy(ctx, "same", "same", objex.WithContentType("application/json")); err != nil {
		t.Errorf("Copy onto itself with a new content type: %v", err)
	}
	if stat, err := b.Stat(ctx, "same"); err != nil || stat.ContentType != "application/json" || stat.Metadata["a"] != "1" {
		t.Errorf("Stat after a content-type copy onto itself = %+v, %v", stat, err)
	}
}

func TestCopyFailures(t *testing.T) {
	ctx := context.Background()
	_, _, b := fakeBucketClient(t)
	if _, err := b.Put(ctx, "src", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Copy(ctx, "dst", "src", objex.WithMetadata(map[string]string{"bad key": "v"})); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("invalid option: err = %v, want ErrInvalidOption", err)
	}

	a := &fakeAPI{
		headObject: func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
			return &s3.HeadObjectOutput{ContentLength: aws.Int64(1), ETag: aws.String(`"e"`)}, nil
		},
		copyObject: func(*s3.CopyObjectInput) (*s3.CopyObjectOutput, error) {
			return nil, apiError("PreconditionFailed")
		},
	}
	if _, err := newTestClient(a, defaultRegion, "").Bucket("bucket").Copy(ctx, "dst", "src"); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("source changed during copy: err = %v, want ErrPreconditionFailed", err)
	}

	a.copyObject = func(*s3.CopyObjectInput) (*s3.CopyObjectOutput, error) {
		return &s3.CopyObjectOutput{CopyObjectResult: &types.CopyObjectResult{ETag: aws.String(`"new"`)}}, nil
	}
	info, err := newTestClient(a, defaultRegion, "").Bucket("bucket").Copy(ctx, "dst", "src")
	if err != nil || info.ETag != "new" || info.LastModified.IsZero() {
		t.Errorf("copy without a server time = %+v, %v; want a client timestamp", info, err)
	}
}

func TestMultipartCopyFailures(t *testing.T) {
	ctx := context.Background()
	head := func(*s3.HeadObjectInput) (*s3.HeadObjectOutput, error) {
		return &s3.HeadObjectOutput{ContentLength: aws.Int64(maxCopyObjectSize + 1), ETag: aws.String(`"src"`)}, nil
	}

	a := &fakeAPI{headObject: head, createUpload: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
		return nil, apiError("AccessDenied")
	}}
	if _, err := newTestClient(a, defaultRegion, "").Bucket("bucket").Copy(ctx, "dst", "src"); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("create fails: err = %v, want ErrPermission", err)
	}

	a = &fakeAPI{
		headObject: head,
		createUpload: func(*s3.CreateMultipartUploadInput) (*s3.CreateMultipartUploadOutput, error) {
			return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload")}, nil
		},
		uploadPartCopy: func(*s3.UploadPartCopyInput) (*s3.UploadPartCopyOutput, error) {
			return &s3.UploadPartCopyOutput{}, nil
		},
		complete: func(*s3.CompleteMultipartUploadInput) (*s3.CompleteMultipartUploadOutput, error) {
			return nil, apiError("InternalError")
		},
	}
	if _, err := newTestClient(a, defaultRegion, "").Bucket("bucket").Copy(ctx, "dst", "src"); err == nil {
		t.Error("complete fails: Copy succeeded")
	}
	if a.called("AbortMultipartUpload") != 1 {
		t.Errorf("calls = %v, want an abort", a.calls)
	}
}

type failingPresign struct{}

func (failingPresign) PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return nil, apiError("AccessDenied")
}

func (failingPresign) PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return nil, apiError("AccessDenied")
}

func TestPresignFailuresAndConditions(t *testing.T) {
	ctx := context.Background()
	b := newClient(&fakeAPI{}, failingPresign{}, defaultRegion, settings{partSize: minPartSize, concurrency: 1}).Bucket("bucket")
	if _, err := b.PresignGet(ctx, "k", time.Minute); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("PresignGet signer failure: err = %v, want ErrPermission", err)
	}
	if _, err := b.PresignPut(ctx, "k", time.Minute); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("PresignPut signer failure: err = %v, want ErrPermission", err)
	}

	b = newTestClient(&fakeAPI{}, defaultRegion, "").Bucket("bucket")
	if _, err := b.PresignGet(ctx, "/bad", time.Minute); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("PresignGet invalid key: err = %v", err)
	}
	if _, err := b.PresignPut(ctx, "a//b", time.Minute); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("PresignPut invalid key: err = %v", err)
	}
	if _, err := b.PresignPut(ctx, "k", time.Minute, objex.IfMatch("etag"), objex.WithMetadata(map[string]string{"a": "1"})); err != nil {
		t.Errorf("PresignPut IfMatch: %v", err)
	}
}

func TestManagerCallFailures(t *testing.T) {
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	c := newFakeClient(newFakeS3())

	if err := c.DeleteBucket(canceled, "bucket"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteBucket canceled: err = %v", err)
	}
	if _, err := c.BucketExists(canceled, "bucket"); !errors.Is(err, context.Canceled) {
		t.Errorf("BucketExists canceled: err = %v", err)
	}
	failing := newTestClient(&fakeAPI{listBuckets: func(*s3.ListBucketsInput) (*s3.ListBucketsOutput, error) {
		return nil, apiError("AccessDenied")
	}}, defaultRegion, "")
	if _, err := failing.ListBuckets(ctx); !errors.Is(err, objex.ErrPermission) {
		t.Errorf("ListBuckets failure: err = %v, want ErrPermission", err)
	}
}

func TestNewLoadsSharedConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", dir+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", dir+"/credentials")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")

	c, err := New(context.Background(), WithRegion("eu-central-1"), WithCredentials("id", "secret", ""))
	if err != nil || c.region != "eu-central-1" {
		t.Fatalf("New = %+v, %v", c, err)
	}

	c, err = New(context.Background())
	if err != nil || c.region != defaultRegion {
		t.Errorf("New without a configured region = %q, %v; want %q", c.region, err, defaultRegion)
	}

	t.Setenv("AWS_PROFILE", "objex-profile-that-does-not-exist")
	if _, err := New(context.Background()); err == nil {
		t.Error("New with a missing shared config profile succeeded")
	}
}
