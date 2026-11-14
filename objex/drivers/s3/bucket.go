package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/brian-nunez/bkit/objex"
)

const (
	defaultContentType = "application/octet-stream"
	maxPresignTTL      = 7 * 24 * time.Hour
	maxCopyObjectSize  = 5 << 30
)

// Bucket is a handle to one S3 bucket. It implements objex.Bucket,
// objex.Pinger, and objex.Presigner.
type Bucket struct {
	client *Client
	name   string
}

var (
	_ objex.Bucket    = (*Bucket)(nil)
	_ objex.Pinger    = (*Bucket)(nil)
	_ objex.Presigner = (*Bucket)(nil)
)

// Name returns the bucket's name.
func (b *Bucket) Name() string { return b.name }

func (b *Bucket) check(ctx context.Context, op, key string) error {
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, b.name, key, nil, err)
	}
	if err := objex.ValidateKey(key); err != nil {
		return objex.NewError(op, b.name, key, objex.ErrInvalidKey, err)
	}
	return nil
}

// Put uploads the contents of r under key. Bodies up to the part size are
// sent with one PutObject request; larger or unknown-size bodies stream as a
// multipart upload, one part in memory per concurrent upload slot.
func (b *Bucket) Put(ctx context.Context, key string, r io.Reader, opts ...objex.PutOption) (objex.ObjectInfo, error) {
	const op = "put"
	if err := b.check(ctx, op, key); err != nil {
		return objex.ObjectInfo{}, err
	}
	o, err := objex.ResolvePutOptions(opts...)
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrInvalidOption, err)
	}
	if o.ContentType == "" {
		o.ContentType = defaultContentType
	}

	etag, size, err := b.upload(ctx, key, r, o)
	if err != nil {
		var objexErr *objex.Error
		if errors.As(err, &objexErr) {
			return objex.ObjectInfo{}, err
		}
		return objex.ObjectInfo{}, wrap(op, b.name, key, err)
	}
	return objex.ObjectInfo{
		Key:          key,
		Size:         size,
		ContentType:  o.ContentType,
		ETag:         etag,
		LastModified: time.Now().UTC(),
		Metadata:     maps.Clone(o.Metadata),
	}, nil
}

// Get opens the object stored under key.
func (b *Bucket) Get(ctx context.Context, key string, opts ...objex.GetOption) (*objex.Object, error) {
	const op = "get"
	if err := b.check(ctx, op, key); err != nil {
		return nil, err
	}
	o, err := objex.ResolveGetOptions(opts...)
	if err != nil {
		return nil, objex.NewError(op, b.name, key, objex.ErrInvalidRange, err)
	}

	in := &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)}
	if o.Range != nil {
		in.Range = aws.String(rangeHeader(*o.Range))
	}
	if o.IfMatch != "" {
		in.IfMatch = aws.String(quote(o.IfMatch))
	}
	out, err := b.client.api.GetObject(ctx, in)
	if err != nil {
		return nil, wrap(op, b.name, key, err)
	}

	size := aws.ToInt64(out.ContentLength)
	if total, ok := totalFromContentRange(aws.ToString(out.ContentRange)); ok {
		size = total
	}
	return &objex.Object{
		ReadCloser: out.Body,
		Info: objex.ObjectInfo{
			Key:          key,
			Size:         size,
			ContentType:  aws.ToString(out.ContentType),
			ETag:         unquote(aws.ToString(out.ETag)),
			LastModified: aws.ToTime(out.LastModified).UTC(),
			Metadata:     out.Metadata,
		},
	}, nil
}

// Stat returns the object's metadata.
func (b *Bucket) Stat(ctx context.Context, key string) (objex.ObjectInfo, error) {
	const op = "stat"
	if err := b.check(ctx, op, key); err != nil {
		return objex.ObjectInfo{}, err
	}
	return b.head(ctx, op, key)
}

func (b *Bucket) head(ctx context.Context, op, key string) (objex.ObjectInfo, error) {
	out, err := b.client.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)})
	if err != nil {
		return objex.ObjectInfo{}, wrap(op, b.name, key, err)
	}
	return objex.ObjectInfo{
		Key:          key,
		Size:         aws.ToInt64(out.ContentLength),
		ContentType:  aws.ToString(out.ContentType),
		ETag:         unquote(aws.ToString(out.ETag)),
		LastModified: aws.ToTime(out.LastModified).UTC(),
		Metadata:     out.Metadata,
	}, nil
}

// Delete removes the object stored under key.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	const op = "delete"
	if err := b.check(ctx, op, key); err != nil {
		return err
	}
	if _, err := b.client.api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)}); err != nil {
		return wrap(op, b.name, key, err)
	}
	return nil
}

// List yields the bucket's objects in ascending key order, requesting
// further pages from S3 as iteration proceeds.
func (b *Bucket) List(ctx context.Context, opts ...objex.ListOption) iter.Seq2[objex.ObjectInfo, error] {
	o := objex.ResolveListOptions(opts...)
	return func(yield func(objex.ObjectInfo, error) bool) {
		const op = "list"
		in := &s3.ListObjectsV2Input{Bucket: aws.String(b.name)}
		if o.Prefix != "" {
			in.Prefix = aws.String(o.Prefix)
		}
		if o.Delimiter != "" {
			in.Delimiter = aws.String(o.Delimiter)
		}
		if o.StartAfter != "" {
			in.StartAfter = aws.String(o.StartAfter)
		}

		for {
			if err := ctx.Err(); err != nil {
				yield(objex.ObjectInfo{}, objex.NewError(op, b.name, "", nil, err))
				return
			}
			out, err := b.client.api.ListObjectsV2(ctx, in)
			if err != nil {
				yield(objex.ObjectInfo{}, wrap(op, b.name, "", err))
				return
			}
			for _, entry := range mergePage(out) {
				if !yield(entry, nil) {
					return
				}
			}
			if !aws.ToBool(out.IsTruncated) || aws.ToString(out.NextContinuationToken) == "" {
				return
			}
			in.ContinuationToken = out.NextContinuationToken
			in.StartAfter = nil
		}
	}
}

// mergePage interleaves a page's objects and common prefixes in key order.
// S3 returns each list already sorted.
func mergePage(out *s3.ListObjectsV2Output) []objex.ObjectInfo {
	entries := make([]objex.ObjectInfo, 0, len(out.Contents)+len(out.CommonPrefixes))
	objects, prefixes := out.Contents, out.CommonPrefixes
	for len(objects) > 0 || len(prefixes) > 0 {
		takeObject := len(prefixes) == 0 ||
			(len(objects) > 0 && aws.ToString(objects[0].Key) < aws.ToString(prefixes[0].Prefix))
		if takeObject {
			obj := objects[0]
			objects = objects[1:]
			entries = append(entries, objex.ObjectInfo{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				ETag:         unquote(aws.ToString(obj.ETag)),
				LastModified: aws.ToTime(obj.LastModified).UTC(),
			})
			continue
		}
		entries = append(entries, objex.ObjectInfo{Key: aws.ToString(prefixes[0].Prefix), IsPrefix: true})
		prefixes = prefixes[1:]
	}
	return entries
}

// Copy copies the object at src to dst on the server. The copy is pinned to
// the source version observed first, so a concurrent overwrite of src fails
// with objex.ErrPreconditionFailed instead of copying different data.
func (b *Bucket) Copy(ctx context.Context, dst, src string, opts ...objex.CopyOption) (objex.ObjectInfo, error) {
	const op = "copy"
	if err := b.check(ctx, op, src); err != nil {
		return objex.ObjectInfo{}, err
	}
	if err := b.check(ctx, op, dst); err != nil {
		return objex.ObjectInfo{}, err
	}
	o, err := objex.ResolveCopyOptions(opts...)
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, dst, objex.ErrInvalidOption, err)
	}

	source, err := b.head(ctx, op, src)
	if err != nil {
		return objex.ObjectInfo{}, err
	}
	if o.IfMatch != "" && o.IfMatch != source.ETag {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, src, objex.ErrPreconditionFailed, nil)
	}

	info := source
	info.Key = dst
	if o.ContentType != "" {
		info.ContentType = o.ContentType
	}
	if o.ReplaceMetadata {
		info.Metadata = o.Metadata
	}
	replace := o.ContentType != "" || o.ReplaceMetadata

	var etag string
	var modified time.Time
	if source.Size > maxCopyObjectSize {
		etag, err = b.multipartCopy(ctx, dst, src, source, info)
		modified = time.Now().UTC()
	} else {
		etag, modified, err = b.copyObject(ctx, dst, src, source.ETag, info, replace)
	}
	if err != nil {
		return objex.ObjectInfo{}, wrap(op, b.name, dst, err)
	}
	info.ETag = etag
	info.LastModified = modified
	info.Metadata = maps.Clone(info.Metadata)
	return info, nil
}

func (b *Bucket) copyObject(ctx context.Context, dst, src, sourceETag string, info objex.ObjectInfo, replace bool) (string, time.Time, error) {
	in := &s3.CopyObjectInput{
		Bucket:            aws.String(b.name),
		Key:               aws.String(dst),
		CopySource:        aws.String(copySource(b.name, src)),
		CopySourceIfMatch: aws.String(quote(sourceETag)),
	}
	if replace {
		in.MetadataDirective = types.MetadataDirectiveReplace
		in.ContentType = aws.String(info.ContentType)
		in.Metadata = info.Metadata
	}
	out, err := b.client.api.CopyObject(ctx, in)
	if err != nil {
		return "", time.Time{}, err
	}
	if out.CopyObjectResult == nil {
		return "", time.Now().UTC(), nil
	}
	modified := aws.ToTime(out.CopyObjectResult.LastModified).UTC()
	if modified.IsZero() {
		modified = time.Now().UTC()
	}
	return unquote(aws.ToString(out.CopyObjectResult.ETag)), modified, nil
}

// Ping checks that the bucket exists and the credentials can access it.
func (b *Bucket) Ping(ctx context.Context) error {
	const op = "ping"
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, b.name, "", nil, err)
	}
	_, err := b.client.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(b.name)})
	if err == nil {
		return nil
	}
	wrapped := wrap(op, b.name, "", err)
	if errors.Is(wrapped, objex.ErrNotFound) {
		return objex.NewError(op, b.name, "", objex.ErrBucketNotFound, err)
	}
	return wrapped
}

// PresignGet returns a SigV4-presigned GET request for key. ttl must be
// positive and at most seven days.
func (b *Bucket) PresignGet(ctx context.Context, key string, ttl time.Duration) (objex.PresignedRequest, error) {
	const op = "presign get"
	if err := b.checkPresign(ctx, op, key, ttl); err != nil {
		return objex.PresignedRequest{}, err
	}
	out, err := b.client.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)},
		s3.WithPresignExpires(ttl))
	if err != nil {
		return objex.PresignedRequest{}, wrap(op, b.name, key, err)
	}
	return presigned(out.Method, out.URL, out.SignedHeader, ttl), nil
}

// PresignPut returns a SigV4-presigned PUT request for key. WithContentType,
// WithMetadata, IfNotExists, and IfMatch become signed headers the client
// must send unchanged. ttl must be positive and at most seven days.
func (b *Bucket) PresignPut(ctx context.Context, key string, ttl time.Duration, opts ...objex.PutOption) (objex.PresignedRequest, error) {
	const op = "presign put"
	if err := b.checkPresign(ctx, op, key, ttl); err != nil {
		return objex.PresignedRequest{}, err
	}
	o, err := objex.ResolvePutOptions(opts...)
	if err != nil {
		return objex.PresignedRequest{}, objex.NewError(op, b.name, key, objex.ErrInvalidOption, err)
	}
	in := &s3.PutObjectInput{Bucket: aws.String(b.name), Key: aws.String(key), Metadata: o.Metadata}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.IfNotExists {
		in.IfNoneMatch = aws.String("*")
	}
	if o.IfMatch != "" {
		in.IfMatch = aws.String(quote(o.IfMatch))
	}
	out, err := b.client.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return objex.PresignedRequest{}, wrap(op, b.name, key, err)
	}
	return presigned(out.Method, out.URL, out.SignedHeader, ttl), nil
}

func (b *Bucket) checkPresign(ctx context.Context, op, key string, ttl time.Duration) error {
	if err := b.check(ctx, op, key); err != nil {
		return err
	}
	if ttl <= 0 || ttl > maxPresignTTL {
		return objex.NewError(op, b.name, key, objex.ErrInvalidOption, fmt.Errorf("ttl must be positive and at most %s", maxPresignTTL))
	}
	return nil
}

// presigned converts an SDK presigned request. The Host header is dropped:
// HTTP clients derive it from the URL.
func presigned(method, rawURL string, signed http.Header, ttl time.Duration) objex.PresignedRequest {
	header := signed.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Del("Host")
	return objex.PresignedRequest{
		Method:  method,
		URL:     rawURL,
		Header:  header,
		Expires: time.Now().Add(ttl).UTC(),
	}
}

func rangeHeader(r objex.Range) string {
	if r.Length < 0 {
		return fmt.Sprintf("bytes=%d-", r.Offset)
	}
	return fmt.Sprintf("bytes=%d-%d", r.Offset, r.Offset+r.Length-1)
}

// totalFromContentRange parses the total size from "bytes 0-9/100".
func totalFromContentRange(contentRange string) (int64, bool) {
	i := strings.LastIndexByte(contentRange, '/')
	if i < 0 {
		return 0, false
	}
	total, err := strconv.ParseInt(contentRange[i+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return total, true
}

// copySource builds the URL-encoded CopySource value for bucket and key.
func copySource(bucket, key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return bucket + "/" + strings.Join(segments, "/")
}

func quote(etag string) string { return `"` + etag + `"` }

func unquote(etag string) string { return strings.Trim(etag, `"`) }
