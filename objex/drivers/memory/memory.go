package memory

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brian-nunez/bkit/objex"
)

const defaultContentType = "application/octet-stream"

// Option configures a Client.
type Option func(*Client)

// WithClock sets the function used to timestamp buckets and objects. It
// defaults to time.Now.
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		if now != nil {
			c.now = now
		}
	}
}

// Client stores buckets in memory. It implements objex.Manager.
type Client struct {
	mu      sync.RWMutex
	buckets map[string]*bucketData
	now     func() time.Time
}

type bucketData struct {
	createdAt time.Time
	objects   map[string]*object
}

// object is immutable once stored; writes replace the whole value.
type object struct {
	data []byte
	info objex.ObjectInfo
}

var _ objex.Manager = (*Client)(nil)

// New returns an empty Client.
func New(opts ...Option) *Client {
	c := &Client{buckets: make(map[string]*bucketData), now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Bucket returns a handle to the named bucket. It performs no I/O; operations
// on a bucket that does not exist return objex.ErrBucketNotFound.
func (c *Client) Bucket(name string) *Bucket {
	return &Bucket{client: c, name: name}
}

func (c *Client) timestamp() time.Time { return c.now().UTC() }

// CreateBucket creates a bucket.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	const op = "create bucket"
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, name, "", nil, err)
	}
	if err := objex.ValidateBucketName(name); err != nil {
		return objex.NewError(op, name, "", objex.ErrInvalidBucketName, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.buckets[name]; ok {
		return objex.NewError(op, name, "", objex.ErrBucketExists, nil)
	}
	c.buckets[name] = &bucketData{createdAt: c.timestamp(), objects: make(map[string]*object)}
	return nil
}

// DeleteBucket deletes an empty bucket.
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	const op = "delete bucket"
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, name, "", nil, err)
	}
	if err := objex.ValidateBucketName(name); err != nil {
		return objex.NewError(op, name, "", objex.ErrInvalidBucketName, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.buckets[name]
	if !ok {
		return objex.NewError(op, name, "", objex.ErrBucketNotFound, nil)
	}
	if len(data.objects) > 0 {
		return objex.NewError(op, name, "", objex.ErrBucketNotEmpty, nil)
	}
	delete(c.buckets, name)
	return nil
}

// ListBuckets returns every bucket, sorted by name.
func (c *Client) ListBuckets(ctx context.Context) ([]objex.BucketInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, objex.NewError("list buckets", "", "", nil, err)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	buckets := make([]objex.BucketInfo, 0, len(c.buckets))
	for name, data := range c.buckets {
		buckets = append(buckets, objex.BucketInfo{Name: name, CreatedAt: data.createdAt})
	}
	slices.SortFunc(buckets, func(a, b objex.BucketInfo) int { return strings.Compare(a.Name, b.Name) })
	return buckets, nil
}

// BucketExists reports whether the bucket exists.
func (c *Client) BucketExists(ctx context.Context, name string) (bool, error) {
	const op = "bucket exists"
	if err := ctx.Err(); err != nil {
		return false, objex.NewError(op, name, "", nil, err)
	}
	if err := objex.ValidateBucketName(name); err != nil {
		return false, objex.NewError(op, name, "", objex.ErrInvalidBucketName, err)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.buckets[name]
	return ok, nil
}

// Bucket is a handle to one in-memory bucket. It implements objex.Bucket,
// objex.Mover, and objex.Pinger.
type Bucket struct {
	client *Client
	name   string
}

var (
	_ objex.Bucket = (*Bucket)(nil)
	_ objex.Mover  = (*Bucket)(nil)
	_ objex.Pinger = (*Bucket)(nil)
)

// Name returns the bucket's name.
func (b *Bucket) Name() string { return b.name }

// bucketLocked returns the bucket's data; the caller holds b.client.mu.
func (b *Bucket) bucketLocked(op, key string) (*bucketData, error) {
	data, ok := b.client.buckets[b.name]
	if !ok {
		return nil, objex.NewError(op, b.name, key, objex.ErrBucketNotFound, nil)
	}
	return data, nil
}

func (b *Bucket) check(ctx context.Context, op, key string) error {
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, b.name, key, nil, err)
	}
	if err := objex.ValidateKey(key); err != nil {
		return objex.NewError(op, b.name, key, objex.ErrInvalidKey, err)
	}
	return nil
}

// Put stores the contents of r under key.
func (b *Bucket) Put(ctx context.Context, key string, r io.Reader, opts ...objex.PutOption) (objex.ObjectInfo, error) {
	const op = "put"
	if err := b.check(ctx, op, key); err != nil {
		return objex.ObjectInfo{}, err
	}
	o, err := objex.ResolvePutOptions(opts...)
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrInvalidOption, err)
	}

	data, err := io.ReadAll(contextReader{ctx: ctx, r: r})
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, nil, err)
	}
	if o.Size >= 0 && int64(len(data)) != o.Size {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrInvalidOption,
			fmt.Errorf("read %d bytes; WithSize declared %d", len(data), o.Size))
	}

	contentType := o.ContentType
	if contentType == "" {
		contentType = defaultContentType
	}
	sum := md5.Sum(data)

	b.client.mu.Lock()
	defer b.client.mu.Unlock()
	bucket, err := b.bucketLocked(op, key)
	if err != nil {
		return objex.ObjectInfo{}, err
	}
	existing, exists := bucket.objects[key]
	switch {
	case o.IfNotExists && exists:
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrPreconditionFailed, nil)
	case o.IfMatch != "" && !exists:
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrNotFound, nil)
	case o.IfMatch != "" && existing.info.ETag != o.IfMatch:
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrPreconditionFailed, nil)
	}

	stored := &object{
		data: data,
		info: objex.ObjectInfo{
			Key:          key,
			Size:         int64(len(data)),
			ContentType:  contentType,
			ETag:         hex.EncodeToString(sum[:]),
			LastModified: b.client.timestamp(),
			Metadata:     o.Metadata,
		},
	}
	bucket.objects[key] = stored
	return cloneInfo(stored.info), nil
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

	b.client.mu.RLock()
	defer b.client.mu.RUnlock()
	stored, err := b.objectLocked(op, key)
	if err != nil {
		return nil, err
	}
	if o.IfMatch != "" && stored.info.ETag != o.IfMatch {
		return nil, objex.NewError(op, b.name, key, objex.ErrPreconditionFailed, nil)
	}

	data := stored.data
	if o.Range != nil {
		size := int64(len(data))
		if o.Range.Offset >= size {
			return nil, objex.NewError(op, b.name, key, objex.ErrInvalidRange,
				fmt.Errorf("offset %d is at or beyond the object size %d", o.Range.Offset, size))
		}
		end := size
		// Compare against the remaining bytes so a huge Length cannot
		// overflow Offset+Length; it reads to the end of the object.
		if o.Range.Length > 0 && o.Range.Length < size-o.Range.Offset {
			end = o.Range.Offset + o.Range.Length
		}
		data = data[o.Range.Offset:end]
	}

	return &objex.Object{
		ReadCloser: io.NopCloser(bytes.NewReader(data)),
		Info:       cloneInfo(stored.info),
	}, nil
}

// objectLocked returns the stored object; the caller holds b.client.mu.
func (b *Bucket) objectLocked(op, key string) (*object, error) {
	bucket, err := b.bucketLocked(op, key)
	if err != nil {
		return nil, err
	}
	stored, ok := bucket.objects[key]
	if !ok {
		return nil, objex.NewError(op, b.name, key, objex.ErrNotFound, nil)
	}
	return stored, nil
}

// Stat returns the object's metadata.
func (b *Bucket) Stat(ctx context.Context, key string) (objex.ObjectInfo, error) {
	const op = "stat"
	if err := b.check(ctx, op, key); err != nil {
		return objex.ObjectInfo{}, err
	}
	b.client.mu.RLock()
	defer b.client.mu.RUnlock()
	stored, err := b.objectLocked(op, key)
	if err != nil {
		return objex.ObjectInfo{}, err
	}
	return cloneInfo(stored.info), nil
}

// Delete removes the object stored under key.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	const op = "delete"
	if err := b.check(ctx, op, key); err != nil {
		return err
	}
	b.client.mu.Lock()
	defer b.client.mu.Unlock()
	bucket, err := b.bucketLocked(op, key)
	if err != nil {
		return err
	}
	delete(bucket.objects, key)
	return nil
}

// List yields the bucket's objects in ascending key order.
func (b *Bucket) List(ctx context.Context, opts ...objex.ListOption) iter.Seq2[objex.ObjectInfo, error] {
	o := objex.ResolveListOptions(opts...)
	return func(yield func(objex.ObjectInfo, error) bool) {
		const op = "list"
		if err := ctx.Err(); err != nil {
			yield(objex.ObjectInfo{}, objex.NewError(op, b.name, "", nil, err))
			return
		}

		b.client.mu.RLock()
		bucket, err := b.bucketLocked(op, "")
		if err != nil {
			b.client.mu.RUnlock()
			yield(objex.ObjectInfo{}, err)
			return
		}
		entries := make([]objex.ObjectInfo, 0, len(bucket.objects))
		for _, stored := range bucket.objects {
			entries = append(entries, cloneInfo(stored.info))
		}
		b.client.mu.RUnlock()

		for _, entry := range objex.FilterList(entries, o) {
			if err := ctx.Err(); err != nil {
				yield(objex.ObjectInfo{}, objex.NewError(op, b.name, "", nil, err))
				return
			}
			if !yield(entry, nil) {
				return
			}
		}
	}
}

// Copy copies the object at src to dst.
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

	b.client.mu.Lock()
	defer b.client.mu.Unlock()
	return b.copyLocked(op, dst, src, o)
}

// copyLocked copies src to dst; the caller holds b.client.mu for writing.
func (b *Bucket) copyLocked(op, dst, src string, o objex.CopyOptions) (objex.ObjectInfo, error) {
	stored, err := b.objectLocked(op, src)
	if err != nil {
		return objex.ObjectInfo{}, err
	}
	if o.IfMatch != "" && stored.info.ETag != o.IfMatch {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, src, objex.ErrPreconditionFailed, nil)
	}

	info := cloneInfo(stored.info)
	info.Key = dst
	info.LastModified = b.client.timestamp()
	if o.ContentType != "" {
		info.ContentType = o.ContentType
	}
	if o.ReplaceMetadata {
		info.Metadata = o.Metadata
	}
	copied := &object{data: stored.data, info: info}
	b.client.buckets[b.name].objects[dst] = copied
	return cloneInfo(copied.info), nil
}

// Move atomically moves the object at src to dst.
func (b *Bucket) Move(ctx context.Context, dst, src string) (objex.ObjectInfo, error) {
	const op = "move"
	if err := b.check(ctx, op, src); err != nil {
		return objex.ObjectInfo{}, err
	}
	if err := b.check(ctx, op, dst); err != nil {
		return objex.ObjectInfo{}, err
	}

	b.client.mu.Lock()
	defer b.client.mu.Unlock()
	if dst == src {
		stored, err := b.objectLocked(op, src)
		if err != nil {
			return objex.ObjectInfo{}, err
		}
		return cloneInfo(stored.info), nil
	}
	info, err := b.copyLocked(op, dst, src, objex.CopyOptions{})
	if err != nil {
		return objex.ObjectInfo{}, err
	}
	delete(b.client.buckets[b.name].objects, src)
	return info, nil
}

// Ping reports whether the bucket exists.
func (b *Bucket) Ping(ctx context.Context) error {
	const op = "ping"
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, b.name, "", nil, err)
	}
	b.client.mu.RLock()
	defer b.client.mu.RUnlock()
	_, err := b.bucketLocked(op, "")
	return err
}

func cloneInfo(info objex.ObjectInfo) objex.ObjectInfo {
	info.Metadata = maps.Clone(info.Metadata)
	return info
}

// contextReader stops reading once ctx is done.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
