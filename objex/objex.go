package objex

import (
	"context"
	"io"
	"iter"
	"net/http"
	"time"
)

// Bucket is a handle to a single bucket. Implementations are safe for
// concurrent use and never change which bucket they address.
//
// Keys must satisfy [ValidateKey]; operations given an invalid key return an
// error matching [ErrInvalidKey].
type Bucket interface {
	// Put stores the contents of r under key, replacing any existing object
	// unless a condition option says otherwise. Put reads r until io.EOF and
	// never requires the size in advance.
	Put(ctx context.Context, key string, r io.Reader, opts ...PutOption) (ObjectInfo, error)

	// Get opens the object stored under key. A missing object returns an
	// error matching ErrNotFound immediately. The caller must close the
	// returned Object.
	Get(ctx context.Context, key string, opts ...GetOption) (*Object, error)

	// Stat returns the object's metadata without reading its contents.
	Stat(ctx context.Context, key string) (ObjectInfo, error)

	// Delete removes the object stored under key. Deleting a missing object
	// succeeds.
	Delete(ctx context.Context, key string) error

	// List yields objects in ascending key order (byte-wise), fetching
	// further pages as iteration proceeds. Iteration stops after the first
	// error, which is yielded with a zero ObjectInfo.
	List(ctx context.Context, opts ...ListOption) iter.Seq2[ObjectInfo, error]

	// Copy copies the object at src to dst within the bucket and returns the
	// new object's metadata. Content type and metadata are preserved unless
	// replaced through options.
	Copy(ctx context.Context, dst, src string, opts ...CopyOption) (ObjectInfo, error)
}

// Object is an open object returned by [Bucket.Get]. Reading yields the
// requested bytes: the whole object, or the range selected with [WithRange].
// The caller must call Close.
type Object struct {
	io.ReadCloser

	// Info describes the whole object, including its full size even when a
	// range was requested.
	Info ObjectInfo
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	// Key is the object's key, or the common prefix when IsPrefix is true.
	Key string

	// Size is the object's length in bytes.
	Size int64

	// ContentType is the object's media type.
	ContentType string

	// ETag identifies the object's current contents. It is opaque: compare
	// it only for equality, and only with ETags from the same driver. It
	// never includes surrounding quotes.
	ETag string

	// LastModified is when the object was last written.
	LastModified time.Time

	// Metadata holds user-defined metadata with lowercase keys.
	Metadata map[string]string

	// IsPrefix reports that this entry is a common prefix produced by
	// listing with a delimiter rather than an object. Only Key is set.
	IsPrefix bool
}

// BucketInfo describes a bucket returned by [Manager.ListBuckets].
type BucketInfo struct {
	// Name is the bucket's name.
	Name string

	// CreatedAt is when the bucket was created, as reported by the backend.
	CreatedAt time.Time
}

// Presigner is implemented by buckets that can issue time-limited URLs
// granting access to a single object without further credentials.
type Presigner interface {
	// PresignGet returns a request that downloads the object at key until
	// ttl elapses.
	PresignGet(ctx context.Context, key string, ttl time.Duration) (PresignedRequest, error)

	// PresignPut returns a request that uploads an object to key until ttl
	// elapses. Options such as WithContentType and IfNotExists are bound
	// into the request and enforced by the backend.
	PresignPut(ctx context.Context, key string, ttl time.Duration, opts ...PutOption) (PresignedRequest, error)
}

// PresignedRequest is an HTTP request authorized ahead of time.
type PresignedRequest struct {
	// Method is the HTTP method the request must use.
	Method string

	// URL is the full request URL, including its signature.
	URL string

	// Header holds headers the client must send unchanged.
	Header http.Header

	// Expires is when the request stops being accepted.
	Expires time.Time
}

// Mover is implemented by buckets that can move an object natively, for
// example with an atomic rename. [Move] uses it when available.
type Mover interface {
	// Move moves the object at src to dst within the bucket.
	Move(ctx context.Context, dst, src string) (ObjectInfo, error)
}

// Pinger is implemented by buckets that can verify the bucket is reachable
// with the configured credentials.
type Pinger interface {
	// Ping checks that the bucket exists and is accessible.
	Ping(ctx context.Context) error
}

// Manager is implemented by driver clients that manage buckets.
type Manager interface {
	// CreateBucket creates a bucket. An existing bucket returns an error
	// matching ErrBucketExists.
	CreateBucket(ctx context.Context, name string) error

	// DeleteBucket deletes an empty bucket. A bucket that still holds
	// objects returns an error matching ErrBucketNotEmpty; a missing bucket
	// returns an error matching ErrBucketNotFound.
	DeleteBucket(ctx context.Context, name string) error

	// ListBuckets returns every bucket visible to the client, sorted by name.
	ListBuckets(ctx context.Context) ([]BucketInfo, error)

	// BucketExists reports whether the bucket exists.
	BucketExists(ctx context.Context, name string) (bool, error)
}
