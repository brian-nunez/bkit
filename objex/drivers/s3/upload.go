package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/brian-nunez/bkit/objex"
)

// abortTimeout bounds cleanup of a failed multipart upload, which runs even
// after the caller's context is canceled.
const abortTimeout = 30 * time.Second

// upload sends r as one PutObject request when it fits in a part, and as a
// multipart upload otherwise. It returns the ETag and the bytes sent.
func (b *Bucket) upload(ctx context.Context, key string, r io.Reader, o objex.PutOptions) (string, int64, error) {
	first, err := readPart(r, b.client.partSize, o.Size)
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return b.putSingle(ctx, key, first, o)
	case err != nil:
		return "", 0, err
	}

	// A full first part may still be the whole body.
	var probe [1]byte
	m, err := io.ReadFull(r, probe[:])
	if errors.Is(err, io.EOF) {
		return b.putSingle(ctx, key, first, o)
	}
	if err != nil {
		return "", 0, err
	}
	return b.putMultipart(ctx, key, io.MultiReader(bytes.NewReader(first), bytes.NewReader(probe[:m]), r), o)
}

// initialPartBuffer is the first buffer capacity readPart uses when the body
// size is unknown.
const initialPartBuffer = 32 << 10

// readPart reads up to limit bytes from r, like io.ReadFull into a
// limit-sized buffer. Its buffer starts with room for sizeHint bytes and
// io.EOF when sizeHint is not negative, and at initialPartBuffer otherwise,
// then doubles as data arrives, never past limit, so a small body does not
// allocate a whole part. It returns a nil error once limit bytes are read,
// and the reader's error, io.EOF included, when r ends or fails first.
func readPart(r io.Reader, limit, sizeHint int64) ([]byte, error) {
	capacity := min(int64(initialPartBuffer), limit)
	if sizeHint >= 0 {
		capacity = min(sizeHint, limit-1) + 1
	}
	buf := make([]byte, 0, capacity)
	for {
		if len(buf) == cap(buf) {
			grown := make([]byte, len(buf), min(2*int64(cap(buf)), limit))
			copy(grown, buf)
			buf = grown
		}
		n, err := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if int64(len(buf)) == limit {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
}

func (b *Bucket) putSingle(ctx context.Context, key string, data []byte, o objex.PutOptions) (string, int64, error) {
	size := int64(len(data))
	if o.Size >= 0 && size != o.Size {
		return "", 0, objex.NewError("put", b.name, key, objex.ErrInvalidOption, fmt.Errorf("read %d bytes; WithSize declared %d", size, o.Size))
	}
	in := &s3.PutObjectInput{
		Bucket:        aws.String(b.name),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(o.ContentType),
		Metadata:      o.Metadata,
	}
	applyPutConditions(o, &in.IfNoneMatch, &in.IfMatch)
	out, err := b.client.api.PutObject(ctx, in)
	if err != nil {
		return "", 0, err
	}
	return unquote(aws.ToString(out.ETag)), size, nil
}

func applyPutConditions(o objex.PutOptions, ifNoneMatch, ifMatch **string) {
	if o.IfNotExists {
		*ifNoneMatch = aws.String("*")
	}
	if o.IfMatch != "" {
		*ifMatch = aws.String(quote(o.IfMatch))
	}
}

// putMultipart streams r as a multipart upload. Parts are read sequentially
// and sent by up to the client's concurrency limit; conditions are checked
// by S3 when the upload completes.
func (b *Bucket) putMultipart(ctx context.Context, key string, r io.Reader, o objex.PutOptions) (string, int64, error) {
	created, err := b.client.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(b.name),
		Key:         aws.String(key),
		ContentType: aws.String(o.ContentType),
		Metadata:    o.Metadata,
	})
	if err != nil {
		return "", 0, err
	}
	uploadID := created.UploadId

	parts, size, err := b.uploadParts(ctx, key, uploadID, r)
	if err == nil && o.Size >= 0 && size != o.Size {
		err = objex.NewError("put", b.name, key, objex.ErrInvalidOption, fmt.Errorf("read %d bytes; WithSize declared %d", size, o.Size))
	}
	if err != nil {
		b.abort(ctx, key, uploadID)
		return "", 0, err
	}

	complete := &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(b.name),
		Key:             aws.String(key),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}
	applyPutConditions(o, &complete.IfNoneMatch, &complete.IfMatch)
	out, err := b.client.api.CompleteMultipartUpload(ctx, complete)
	if err != nil {
		b.abort(ctx, key, uploadID)
		return "", 0, err
	}
	return unquote(aws.ToString(out.ETag)), size, nil
}

// uploadParts reads r in part-size chunks and uploads them concurrently,
// returning the completed parts in order and the total size.
func (b *Bucket) uploadParts(ctx context.Context, key string, uploadID *string, r io.Reader) ([]types.CompletedPart, int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		parts    []types.CompletedPart
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	slots := make(chan struct{}, b.client.concurrency)

	var size int64
	for number := int32(1); ; number++ {
		if number > b.client.maxParts {
			fail(fmt.Errorf("body exceeds %d parts of %d bytes; increase WithPartSize", b.client.maxParts, b.client.partSize))
			break
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}

		buf := make([]byte, b.client.partSize)
		n, err := io.ReadFull(r, buf)
		if n == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			<-slots
			break
		}
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			<-slots
			fail(err)
			break
		}
		size += int64(n)
		last := err != nil

		wg.Go(func() {
			defer func() { <-slots }()
			out, err := b.client.api.UploadPart(ctx, &s3.UploadPartInput{
				Bucket:        aws.String(b.name),
				Key:           aws.String(key),
				UploadId:      uploadID,
				PartNumber:    aws.Int32(number),
				Body:          bytes.NewReader(buf[:n]),
				ContentLength: aws.Int64(int64(n)),
			})
			if err != nil {
				fail(err)
				return
			}
			mu.Lock()
			parts = append(parts, types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(number)})
			mu.Unlock()
		})
		if last {
			break
		}
	}
	wg.Wait()

	if firstErr != nil {
		return nil, 0, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	slices.SortFunc(parts, func(a, b types.CompletedPart) int { return int(aws.ToInt32(a.PartNumber) - aws.ToInt32(b.PartNumber)) })
	return parts, size, nil
}

// abort cancels a multipart upload so its parts stop accruing storage.
// Failures are ignored; bucket lifecycle rules for incomplete uploads are the
// backstop.
func (b *Bucket) abort(ctx context.Context, key string, uploadID *string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()
	_, _ = b.client.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(b.name),
		Key:      aws.String(key),
		UploadId: uploadID,
	})
}

// multipartCopy copies an object larger than CopyObject's 5 GiB limit with
// UploadPartCopy, pinned to the source ETag.
func (b *Bucket) multipartCopy(ctx context.Context, dst, src string, source, info objex.ObjectInfo) (string, error) {
	created, err := b.client.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(b.name),
		Key:         aws.String(dst),
		ContentType: aws.String(info.ContentType),
		Metadata:    info.Metadata,
	})
	if err != nil {
		return "", err
	}
	uploadID := created.UploadId

	partSize := max(b.client.partSize, (source.Size+maxParts-1)/maxParts)
	var parts []types.CompletedPart
	for number, offset := int32(1), int64(0); offset < source.Size; number, offset = number+1, offset+partSize {
		end := min(offset+partSize, source.Size) - 1
		out, err := b.client.api.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket:            aws.String(b.name),
			Key:               aws.String(dst),
			UploadId:          uploadID,
			PartNumber:        aws.Int32(number),
			CopySource:        aws.String(copySource(b.name, src)),
			CopySourceRange:   aws.String(fmt.Sprintf("bytes=%d-%d", offset, end)),
			CopySourceIfMatch: aws.String(quote(source.ETag)),
		})
		if err != nil {
			b.abort(ctx, dst, uploadID)
			return "", err
		}
		var etag *string
		if out.CopyPartResult != nil {
			etag = out.CopyPartResult.ETag
		}
		parts = append(parts, types.CompletedPart{ETag: etag, PartNumber: aws.Int32(number)})
	}

	out, err := b.client.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(b.name),
		Key:             aws.String(dst),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		b.abort(ctx, dst, uploadID)
		return "", err
	}
	return unquote(aws.ToString(out.ETag)), nil
}
