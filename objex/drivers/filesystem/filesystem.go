package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/brian-nunez/bkit/objex"
)

const (
	tempDir            = ".objex-tmp"
	dirPerm            = 0o755
	filePerm           = 0o644
	defaultContentType = "application/octet-stream"
	minSigningKeyBytes = 32
)

// Option configures a Client.
type Option func(*Client) error

// WithPresign enables presigned URLs. baseURL is the absolute http or https
// URL where the application mounts Client.Handler, such as
// "https://example.com/files". signingKey authenticates the URLs and must be
// at least 32 bytes of secret, random data shared by every process serving
// the same root.
func WithPresign(baseURL string, signingKey []byte) Option {
	return func(c *Client) error {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: presign base URL %q must be an absolute http or https URL without a query", objex.ErrInvalidOption, baseURL)
		}
		if len(signingKey) < minSigningKeyBytes {
			return fmt.Errorf("%w: presign signing key must be at least %d bytes", objex.ErrInvalidOption, minSigningKeyBytes)
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		u.RawPath = ""
		c.baseURL = u
		c.signingKey = slices.Clone(signingKey)
		return nil
	}
}

// WithClock sets the function used to issue and check presigned URL expiry.
// It defaults to time.Now.
func WithClock(now func() time.Time) Option {
	return func(c *Client) error {
		if now != nil {
			c.now = now
		}
		return nil
	}
}

// Client stores buckets as directories under a root. It implements
// objex.Manager.
type Client struct {
	root       rootFS
	baseURL    *url.URL
	signingKey []byte
	now        func() time.Time

	// conditional serializes conditional writes so their check-then-write
	// sequence is atomic within the process.
	conditional sync.Mutex
}

var _ objex.Manager = (*Client)(nil)

// New opens the existing directory at root.
func New(root string, opts ...Option) (*Client, error) {
	c := &Client{now: time.Now}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(c); err != nil {
			return nil, objex.NewError("open", "", "", objex.ErrInvalidOption, err)
		}
	}

	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, objex.NewError("open", "", "", kindOf(err), err)
	}
	if err := r.MkdirAll(tempDir, dirPerm); err != nil {
		_ = r.Close()
		return nil, objex.NewError("open", "", "", kindOf(err), err)
	}
	c.root = osRoot{r}
	return c, nil
}

// rootFS is the subset of *os.Root the driver uses. Every path the driver
// touches goes through it, which keeps all access confined to the root.
type rootFS interface {
	Close() error
	FS() fs.FS
	Link(oldname, newname string) error
	Lstat(name string) (fs.FileInfo, error)
	Mkdir(name string, perm fs.FileMode) error
	MkdirAll(name string, perm fs.FileMode) error
	Open(name string) (file, error)
	OpenFile(name string, flag int, perm fs.FileMode) (file, error)
	Remove(name string) error
	Rename(oldname, newname string) error
	Stat(name string) (fs.FileInfo, error)
}

// file is the subset of *os.File the driver uses.
type file interface {
	io.ReadWriteSeeker
	io.Closer
	Stat() (fs.FileInfo, error)
	Sync() error
}

// osRoot adapts *os.Root to rootFS.
type osRoot struct{ *os.Root }

func (r osRoot) Open(name string) (file, error) {
	f, err := r.Root.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (r osRoot) OpenFile(name string, flag int, perm fs.FileMode) (file, error) {
	f, err := r.Root.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Close releases the root directory. Handles from the Client must not be
// used afterward.
func (c *Client) Close() error {
	return c.root.Close()
}

// Bucket returns a handle to the named bucket. It performs no I/O.
func (c *Client) Bucket(name string) *Bucket {
	return &Bucket{client: c, name: name}
}

// CreateBucket creates a bucket directory.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	const op = "create bucket"
	if err := checkBucketCall(ctx, op, name); err != nil {
		return err
	}
	if err := c.root.Mkdir(name, dirPerm); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return objex.NewError(op, name, "", objex.ErrBucketExists, err)
		}
		return objex.NewError(op, name, "", kindOf(err), err)
	}
	return nil
}

// DeleteBucket deletes an empty bucket directory.
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	const op = "delete bucket"
	if err := checkBucketCall(ctx, op, name); err != nil {
		return err
	}
	if err := c.requireBucket(op, name, ""); err != nil {
		return err
	}
	switch err := c.removeEmptyDirs(name); {
	case err == nil:
		return nil
	case errors.Is(err, errDirNotEmpty):
		return objex.NewError(op, name, "", objex.ErrBucketNotEmpty, nil)
	default:
		return objex.NewError(op, name, "", kindOf(err), err)
	}
}

// errDirNotEmpty reports that removeEmptyDirs found something other than a
// directory.
var errDirNotEmpty = errors.New("directory is not empty")

// removeEmptyDirs removes dir and every directory under it, deepest first.
// It removes each directory with Remove, which fails on a non-empty
// directory, so an object written concurrently is never deleted: it returns
// errDirNotEmpty instead, leaving the object and its parents in place.
func (c *Client) removeEmptyDirs(dir string) error {
	entries, err := fs.ReadDir(c.root.FS(), dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return errDirNotEmpty
		}
		if err := c.removeEmptyDirs(dir + "/" + entry.Name()); err != nil {
			return err
		}
	}
	if err := c.root.Remove(filepath.FromSlash(dir)); err != nil {
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, fs.ErrExist) {
			return errDirNotEmpty
		}
		return err
	}
	return nil
}

// ListBuckets returns every bucket directory, sorted by name. CreatedAt is
// the directory's modification time.
func (c *Client) ListBuckets(ctx context.Context) ([]objex.BucketInfo, error) {
	const op = "list buckets"
	if err := ctx.Err(); err != nil {
		return nil, objex.NewError(op, "", "", nil, err)
	}
	entries, err := fs.ReadDir(c.root.FS(), ".")
	if err != nil {
		return nil, objex.NewError(op, "", "", kindOf(err), err)
	}
	buckets := make([]objex.BucketInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || objex.ValidateBucketName(entry.Name()) != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, objex.NewError(op, entry.Name(), "", kindOf(err), err)
		}
		buckets = append(buckets, objex.BucketInfo{Name: entry.Name(), CreatedAt: info.ModTime().UTC()})
	}
	return buckets, nil
}

// BucketExists reports whether the bucket directory exists.
func (c *Client) BucketExists(ctx context.Context, name string) (bool, error) {
	const op = "bucket exists"
	if err := checkBucketCall(ctx, op, name); err != nil {
		return false, err
	}
	info, err := c.root.Stat(name)
	switch {
	case err == nil:
		return info.IsDir(), nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, objex.NewError(op, name, "", kindOf(err), err)
	}
}

func checkBucketCall(ctx context.Context, op, name string) error {
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, name, "", nil, err)
	}
	if err := objex.ValidateBucketName(name); err != nil {
		return objex.NewError(op, name, "", objex.ErrInvalidBucketName, err)
	}
	return nil
}

// requireBucket returns ErrBucketNotFound unless the bucket directory exists.
func (c *Client) requireBucket(op, bucket, key string) error {
	info, err := c.root.Stat(bucket)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil, errors.Is(err, fs.ErrNotExist):
		return objex.NewError(op, bucket, key, objex.ErrBucketNotFound, err)
	default:
		return objex.NewError(op, bucket, key, kindOf(err), err)
	}
}

// Bucket is a handle to one bucket directory. It implements objex.Bucket,
// objex.Mover, objex.Pinger, and objex.Presigner.
type Bucket struct {
	client *Client
	name   string
}

var (
	_ objex.Bucket    = (*Bucket)(nil)
	_ objex.Mover     = (*Bucket)(nil)
	_ objex.Pinger    = (*Bucket)(nil)
	_ objex.Presigner = (*Bucket)(nil)
)

// Name returns the bucket's name.
func (b *Bucket) Name() string { return b.name }

func (b *Bucket) check(ctx context.Context, op, key string) error {
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, b.name, key, nil, err)
	}
	if err := objex.ValidateBucketName(b.name); err != nil {
		return objex.NewError(op, b.name, key, objex.ErrInvalidBucketName, err)
	}
	if err := objex.ValidateKey(key); err != nil {
		return objex.NewError(op, b.name, key, objex.ErrInvalidKey, err)
	}
	return nil
}

// objectPath returns the root-relative path of key.
func (b *Bucket) objectPath(key string) string {
	return filepath.FromSlash(b.name + "/" + key)
}

// Put writes the contents of r under key atomically.
func (b *Bucket) Put(ctx context.Context, key string, r io.Reader, opts ...objex.PutOption) (objex.ObjectInfo, error) {
	const op = "put"
	if err := b.check(ctx, op, key); err != nil {
		return objex.ObjectInfo{}, err
	}
	o, err := objex.ResolvePutOptions(opts...)
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrInvalidOption, err)
	}
	if len(o.Metadata) > 0 {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrUnsupported, errors.New("the filesystem driver does not store user metadata"))
	}
	if err := b.client.requireBucket(op, b.name, key); err != nil {
		return objex.ObjectInfo{}, err
	}

	tmp, stat, err := b.client.writeTemp(ctx, r, o.Size)
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, kindOf(err), err)
	}
	if err := b.commit(op, key, tmp, o.IfNotExists, o.IfMatch); err != nil {
		return objex.ObjectInfo{}, err
	}
	return objectInfo(key, stat), nil
}

// writeTemp copies r into a new temporary file and returns its root-relative
// path and final FileInfo. The caller owns removing the file.
func (c *Client) writeTemp(ctx context.Context, r io.Reader, wantSize int64) (string, fs.FileInfo, error) {
	var suffix [16]byte
	_, _ = rand.Read(suffix[:])
	tmp := filepath.Join(tempDir, hex.EncodeToString(suffix[:]))

	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, fs.FileInfo, error) {
		_ = f.Close()
		_ = c.root.Remove(tmp)
		return "", nil, err
	}

	n, err := io.Copy(f, contextReader{ctx: ctx, r: r})
	if err != nil {
		return fail(err)
	}
	if wantSize >= 0 && n != wantSize {
		return fail(fmt.Errorf("%w: read %d bytes; WithSize declared %d", objex.ErrInvalidOption, n, wantSize))
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	stat, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = c.root.Remove(tmp)
		return "", nil, err
	}
	return tmp, stat, nil
}

// commit moves the temporary file at tmp to key, enforcing the conditions.
// It always removes tmp.
func (b *Bucket) commit(op, key, tmp string, ifNotExists bool, ifMatch string) error {
	c := b.client
	defer func() { _ = c.root.Remove(tmp) }()

	dst := b.objectPath(key)
	if err := b.mkdirParents(dst); err != nil {
		return objex.NewError(op, b.name, key, pathConflictKind(err), err)
	}
	rename := func() error { return c.root.Rename(tmp, dst) }

	if !ifNotExists && ifMatch == "" {
		if err := b.placeFile(dst, rename); err != nil {
			return objex.NewError(op, b.name, key, pathConflictKind(err), err)
		}
		return nil
	}

	c.conditional.Lock()
	defer c.conditional.Unlock()

	if ifNotExists {
		// A hard link fails if dst exists, which keeps create-only writes
		// atomic even across processes.
		err := b.placeFile(dst, func() error { return c.root.Link(tmp, dst) })
		switch {
		case err == nil:
			return nil
		case errors.Is(err, fs.ErrExist):
			return objex.NewError(op, b.name, key, objex.ErrPreconditionFailed, err)
		}
		// The filesystem does not support hard links; check, then rename.
		if _, err := c.root.Lstat(dst); err == nil {
			return objex.NewError(op, b.name, key, objex.ErrPreconditionFailed, nil)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return objex.NewError(op, b.name, key, kindOf(err), err)
		}
	} else {
		current, err := c.root.Stat(dst)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return objex.NewError(op, b.name, key, objex.ErrNotFound, err)
		case err != nil:
			return objex.NewError(op, b.name, key, kindOf(err), err)
		case current.IsDir():
			return objex.NewError(op, b.name, key, objex.ErrNotFound, nil)
		case etag(current) != ifMatch:
			return objex.NewError(op, b.name, key, objex.ErrPreconditionFailed, nil)
		}
	}

	if err := b.placeFile(dst, rename); err != nil {
		return objex.NewError(op, b.name, key, pathConflictKind(err), err)
	}
	return nil
}

// placeRetries bounds how many times a write recreates its destination's
// parent directories after a concurrent Delete, Move, or DeleteBucket
// removed them as empty.
const placeRetries = 8

// mkdirParents creates the parent directories of dst, an object's
// root-relative path. A concurrent Delete, Move, or DeleteBucket can remove
// a directory while MkdirAll walks through it, which fails with ENOENT,
// EINVAL, or EEXIST, so mkdirParents tries again while the bucket exists.
func (b *Bucket) mkdirParents(dst string) error {
	err := b.client.root.MkdirAll(filepath.Dir(dst), dirPerm)
	for range placeRetries {
		pruned := errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EINVAL) || errors.Is(err, fs.ErrExist)
		if !pruned || !b.exists() {
			break
		}
		err = b.client.root.MkdirAll(filepath.Dir(dst), dirPerm)
	}
	return err
}

// placeFile calls place, which puts a file at dst. When a concurrent
// Delete, Move, or DeleteBucket removed dst's parent directory as empty
// after mkdirParents created it, place fails with ENOENT, or EINVAL on
// macOS, so placeFile recreates the directory and calls place again while
// the bucket exists. A deleted bucket is never recreated.
func (b *Bucket) placeFile(dst string, place func() error) error {
	err := place()
	for range placeRetries {
		pruned := errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EINVAL)
		if !pruned || !b.exists() || b.mkdirParents(dst) != nil {
			break
		}
		err = place()
	}
	return err
}

// exists reports whether the bucket directory exists.
func (b *Bucket) exists() bool {
	info, err := b.client.root.Stat(b.name)
	return err == nil && info.IsDir()
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

	f, stat, err := b.open(op, key)
	if err != nil {
		return nil, err
	}
	fail := func(kind, cause error) (*objex.Object, error) {
		_ = f.Close()
		return nil, objex.NewError(op, b.name, key, kind, cause)
	}

	if o.IfMatch != "" && etag(stat) != o.IfMatch {
		return fail(objex.ErrPreconditionFailed, nil)
	}

	var reader io.Reader = f
	if o.Range != nil {
		if o.Range.Offset >= stat.Size() {
			return fail(objex.ErrInvalidRange, fmt.Errorf("offset %d is at or beyond the object size %d", o.Range.Offset, stat.Size()))
		}
		if _, err := f.Seek(o.Range.Offset, io.SeekStart); err != nil {
			return fail(kindOf(err), err)
		}
		if o.Range.Length > 0 {
			reader = io.LimitReader(f, o.Range.Length)
		}
	}

	return &objex.Object{
		ReadCloser: readCloser{Reader: reader, Closer: f},
		Info:       objectInfo(key, stat),
	}, nil
}

// open opens the object file for reading and returns its FileInfo.
func (b *Bucket) open(op, key string) (file, fs.FileInfo, error) {
	f, err := b.client.root.Open(b.objectPath(key))
	if err != nil {
		return nil, nil, b.missing(op, key, err)
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, objex.NewError(op, b.name, key, kindOf(err), err)
	}
	if !stat.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, objex.NewError(op, b.name, key, objex.ErrNotFound, nil)
	}
	return f, stat, nil
}

// missing classifies an error from looking up key: a missing bucket, a
// missing object, or something else.
func (b *Bucket) missing(op, key string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		if bucketErr := b.client.requireBucket(op, b.name, key); bucketErr != nil {
			return bucketErr
		}
		return objex.NewError(op, b.name, key, objex.ErrNotFound, err)
	}
	return objex.NewError(op, b.name, key, kindOf(err), err)
}

// Stat returns the object's metadata.
func (b *Bucket) Stat(ctx context.Context, key string) (objex.ObjectInfo, error) {
	const op = "stat"
	if err := b.check(ctx, op, key); err != nil {
		return objex.ObjectInfo{}, err
	}
	stat, err := b.client.root.Stat(b.objectPath(key))
	if err != nil {
		return objex.ObjectInfo{}, b.missing(op, key, err)
	}
	if !stat.Mode().IsRegular() {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, key, objex.ErrNotFound, nil)
	}
	return objectInfo(key, stat), nil
}

// Delete removes the object stored under key and any directories it leaves
// empty.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	const op = "delete"
	if err := b.check(ctx, op, key); err != nil {
		return err
	}
	p := b.objectPath(key)
	stat, err := b.client.root.Lstat(p)
	if err != nil {
		if missingErr := b.missing(op, key, err); !errors.Is(missingErr, objex.ErrNotFound) {
			return missingErr
		}
		return nil
	}
	if stat.IsDir() {
		return b.client.requireBucket(op, b.name, key)
	}
	if err := b.client.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return objex.NewError(op, b.name, key, kindOf(err), err)
	}
	b.pruneEmptyParents(key)
	return nil
}

// pruneEmptyParents removes directories between key and the bucket that no
// longer contain anything. Failures are ignored: a non-empty directory is
// the expected stopping point.
func (b *Bucket) pruneEmptyParents(key string) {
	for dir := path.Dir(key); dir != "."; dir = path.Dir(dir) {
		if err := b.client.root.Remove(b.objectPath(dir)); err != nil {
			return
		}
	}
}

// List yields the bucket's objects in ascending key order. It walks the
// directory under the prefix, then sorts, so memory use grows with the
// number of matching objects.
func (b *Bucket) List(ctx context.Context, opts ...objex.ListOption) iter.Seq2[objex.ObjectInfo, error] {
	o := objex.ResolveListOptions(opts...)
	return func(yield func(objex.ObjectInfo, error) bool) {
		const op = "list"
		if err := ctx.Err(); err != nil {
			yield(objex.ObjectInfo{}, objex.NewError(op, b.name, "", nil, err))
			return
		}
		if err := objex.ValidateBucketName(b.name); err != nil {
			yield(objex.ObjectInfo{}, objex.NewError(op, b.name, "", objex.ErrInvalidBucketName, err))
			return
		}
		if err := b.client.requireBucket(op, b.name, ""); err != nil {
			yield(objex.ObjectInfo{}, err)
			return
		}

		entries, err := b.collect(ctx, o.Prefix)
		if err != nil {
			yield(objex.ObjectInfo{}, objex.NewError(op, b.name, "", kindOf(err), err))
			return
		}
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

// collect walks the deepest directory that can contain keys starting with
// prefix and returns every regular file with a valid key.
func (b *Bucket) collect(ctx context.Context, prefix string) ([]objex.ObjectInfo, error) {
	start := b.name
	if i := strings.LastIndex(prefix, "/"); i > 0 && objex.ValidateKey(prefix[:i]) == nil {
		start = b.name + "/" + prefix[:i]
	}

	var entries []objex.ObjectInfo
	err := fs.WalkDir(b.client.root.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == start && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// Files whose names are not valid keys (created outside objex) are
		// not objects and are left out.
		key := strings.TrimPrefix(p, b.name+"/")
		if listable := strings.HasPrefix(key, prefix) && objex.ValidateKey(key) == nil; !listable {
			return nil
		}
		info, infoErr := d.Info()
		switch {
		case infoErr == nil:
			entries = append(entries, objectInfo(key, info))
		case !errors.Is(infoErr, fs.ErrNotExist):
			return infoErr
		}
		// A file deleted during the walk is simply not listed.
		return nil
	})
	return entries, err
}

// Copy copies the object at src to dst atomically. The content type always
// follows dst's extension; WithContentType does not change it.
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
	if len(o.Metadata) > 0 {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, dst, objex.ErrUnsupported, errors.New("the filesystem driver does not store user metadata"))
	}

	f, stat, err := b.open(op, src)
	if err != nil {
		return objex.ObjectInfo{}, err
	}
	defer func() { _ = f.Close() }()
	if o.IfMatch != "" && etag(stat) != o.IfMatch {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, src, objex.ErrPreconditionFailed, nil)
	}

	tmp, copied, err := b.client.writeTemp(ctx, f, stat.Size())
	if err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, dst, kindOf(err), err)
	}
	if err := b.commit(op, dst, tmp, false, ""); err != nil {
		return objex.ObjectInfo{}, err
	}
	return objectInfo(dst, copied), nil
}

// Move renames the object at src to dst atomically, replacing any object at
// dst.
func (b *Bucket) Move(ctx context.Context, dst, src string) (objex.ObjectInfo, error) {
	const op = "move"
	if err := b.check(ctx, op, src); err != nil {
		return objex.ObjectInfo{}, err
	}
	if err := b.check(ctx, op, dst); err != nil {
		return objex.ObjectInfo{}, err
	}

	stat, err := b.client.root.Stat(b.objectPath(src))
	if err != nil {
		return objex.ObjectInfo{}, b.missing(op, src, err)
	}
	if !stat.Mode().IsRegular() {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, src, objex.ErrNotFound, nil)
	}
	if dst == src {
		return objectInfo(src, stat), nil
	}

	target := b.objectPath(dst)
	if err := b.mkdirParents(target); err != nil {
		return objex.ObjectInfo{}, objex.NewError(op, b.name, dst, pathConflictKind(err), err)
	}
	if err := b.placeFile(target, func() error { return b.client.root.Rename(b.objectPath(src), target) }); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return objex.ObjectInfo{}, b.missing(op, src, err)
		}
		return objex.ObjectInfo{}, objex.NewError(op, b.name, dst, pathConflictKind(err), err)
	}
	b.pruneEmptyParents(src)
	return objectInfo(dst, stat), nil
}

// Ping reports whether the bucket directory exists.
func (b *Bucket) Ping(ctx context.Context) error {
	const op = "ping"
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, b.name, "", nil, err)
	}
	if err := objex.ValidateBucketName(b.name); err != nil {
		return objex.NewError(op, b.name, "", objex.ErrInvalidBucketName, err)
	}
	return b.client.requireBucket(op, b.name, "")
}

func objectInfo(key string, stat fs.FileInfo) objex.ObjectInfo {
	return objex.ObjectInfo{
		Key:          key,
		Size:         stat.Size(),
		ContentType:  contentType(key),
		ETag:         etag(stat),
		LastModified: stat.ModTime().UTC(),
	}
}

// etag identifies a file version by modification time and size. Every write
// replaces the file through a rename, so a new version gets a new
// modification time.
func etag(stat fs.FileInfo) string {
	return fmt.Sprintf("%x-%x", stat.ModTime().UnixNano(), stat.Size())
}

func contentType(key string) string {
	if ct := mime.TypeByExtension(path.Ext(key)); ct != "" {
		return ct
	}
	return defaultContentType
}

// kindOf maps filesystem errors to objex sentinels.
func kindOf(err error) error {
	switch {
	case errors.Is(err, objex.ErrInvalidOption):
		return objex.ErrInvalidOption
	case errors.Is(err, fs.ErrNotExist):
		return objex.ErrNotFound
	case errors.Is(err, fs.ErrPermission):
		return objex.ErrPermission
	default:
		return nil
	}
}

// pathConflictKind classifies failures to create a key's path. A file where
// a directory is needed, or a directory where the file goes, means the key
// collides with another key's prefix, which a filesystem cannot represent.
func pathConflictKind(err error) error {
	if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR) || errors.Is(err, fs.ErrExist) {
		return objex.ErrUnsupported
	}
	return kindOf(err)
}

type readCloser struct {
	io.Reader
	io.Closer
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
