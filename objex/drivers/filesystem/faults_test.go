package filesystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/objex"
)

var errInjected = errors.New("injected failure")

// faultFS wraps the real root and fails chosen operations, standing in for
// OS failures that cannot be produced reliably in a test.
type faultFS struct {
	rootFS

	// fail returns a non-nil error to fail op on name (slash-separated).
	fail func(op, name string) error
	// wrapOpen wraps files returned by Open.
	wrapOpen func(file) file
	// wrapCreate wraps files returned by OpenFile.
	wrapCreate func(file) file
	// fsys wraps the fs.FS returned by FS.
	fsys func(fs.FS) fs.FS
}

func (f *faultFS) check(op, name string) error {
	if f.fail == nil {
		return nil
	}
	return f.fail(op, filepath.ToSlash(name))
}

func (f *faultFS) FS() fs.FS {
	if f.fsys != nil {
		return f.fsys(f.rootFS.FS())
	}
	return f.rootFS.FS()
}

func (f *faultFS) Link(oldname, newname string) error {
	if err := f.check("link", newname); err != nil {
		return err
	}
	return f.rootFS.Link(oldname, newname)
}

func (f *faultFS) Lstat(name string) (fs.FileInfo, error) {
	if err := f.check("lstat", name); err != nil {
		return nil, err
	}
	return f.rootFS.Lstat(name)
}

func (f *faultFS) Mkdir(name string, perm fs.FileMode) error {
	if err := f.check("mkdir", name); err != nil {
		return err
	}
	return f.rootFS.Mkdir(name, perm)
}

func (f *faultFS) Open(name string) (file, error) {
	if err := f.check("open", name); err != nil {
		return nil, err
	}
	opened, err := f.rootFS.Open(name)
	if err == nil && f.wrapOpen != nil {
		opened = f.wrapOpen(opened)
	}
	return opened, err
}

func (f *faultFS) OpenFile(name string, flag int, perm fs.FileMode) (file, error) {
	if err := f.check("openfile", name); err != nil {
		return nil, err
	}
	opened, err := f.rootFS.OpenFile(name, flag, perm)
	if err == nil && f.wrapCreate != nil {
		opened = f.wrapCreate(opened)
	}
	return opened, err
}

func (f *faultFS) Remove(name string) error {
	if err := f.check("remove", name); err != nil {
		return err
	}
	return f.rootFS.Remove(name)
}

func (f *faultFS) Rename(oldname, newname string) error {
	if err := f.check("rename", newname); err != nil {
		return err
	}
	return f.rootFS.Rename(oldname, newname)
}

func (f *faultFS) Stat(name string) (fs.FileInfo, error) {
	if err := f.check("stat", name); err != nil {
		return nil, err
	}
	return f.rootFS.Stat(name)
}

// faultFile fails chosen file methods.
type faultFile struct {
	file
	syncErr, statErr, closeErr, seekErr error
}

func (f faultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.file.Sync()
}

func (f faultFile) Stat() (fs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.file.Stat()
}

func (f faultFile) Close() error {
	err := f.file.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func (f faultFile) Seek(offset int64, whence int) (int64, error) {
	if f.seekErr != nil {
		return 0, f.seekErr
	}
	return f.file.Seek(offset, whence)
}

// faultDirFS fails ReadDir for chosen directories and makes chosen entries'
// Info fail.
type faultDirFS struct {
	fs.FS
	readDir func(name string) error
	infoErr func(name string) error
}

func (f faultDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if f.readDir != nil {
		if err := f.readDir(name); err != nil {
			return nil, err
		}
	}
	entries, err := fs.ReadDir(f.FS, name)
	if err != nil || f.infoErr == nil {
		return entries, err
	}
	for i, entry := range entries {
		if err := f.infoErr(name + "/" + entry.Name()); err != nil {
			entries[i] = failingEntry{DirEntry: entry, err: err}
		}
	}
	return entries, nil
}

type failingEntry struct {
	fs.DirEntry
	err error
}

func (e failingEntry) Info() (fs.FileInfo, error) { return nil, e.err }

// faultClient returns a client whose root fails as configured, with an
// existing bucket named "bucket".
func faultClient(t *testing.T) (*Client, *faultFS, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := New(dir, WithPresign("https://example.com/files", bytes.Repeat([]byte("k"), 32)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.CreateBucket(context.Background(), "bucket"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	faults := &faultFS{rootFS: c.root}
	c.root = faults
	return c, faults, dir
}

func failOn(op, name string, err error) func(string, string) error {
	return func(gotOp, gotName string) error {
		if gotOp == op && (name == "" || gotName == name) {
			return err
		}
		return nil
	}
}

func put(t *testing.T, b *Bucket, key, body string) objex.ObjectInfo {
	t.Helper()
	info, err := b.Put(context.Background(), key, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	return info
}

func TestNewFailsWhenTempDirIsBlocked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tempDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Error("New succeeded with a file where the temporary directory belongs")
	}
}

func TestName(t *testing.T) {
	c, _, _ := faultClient(t)
	if got := c.Bucket("bucket").Name(); got != "bucket" {
		t.Errorf("Name() = %q", got)
	}
}

func TestManagerFailures(t *testing.T) {
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	t.Run("CreateBucketPermission", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fail = failOn("mkdir", "", fs.ErrPermission)
		if err := c.CreateBucket(ctx, "other"); !errors.Is(err, objex.ErrPermission) {
			t.Errorf("err = %v, want ErrPermission", err)
		}
	})

	t.Run("DeleteBucketChecks", func(t *testing.T) {
		c, _, _ := faultClient(t)
		if err := c.DeleteBucket(canceled, "bucket"); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled: err = %v", err)
		}
		if err := c.DeleteBucket(ctx, "BAD"); !errors.Is(err, objex.ErrInvalidBucketName) {
			t.Errorf("invalid name: err = %v", err)
		}
	})

	t.Run("DeleteBucketWalkFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, readDir: func(string) error { return errInjected }}
		}
		if err := c.DeleteBucket(ctx, "bucket"); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the walk error", err)
		}
	})

	t.Run("DeleteBucketRemoveFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fail = failOn("remove", "bucket", errInjected)
		if err := c.DeleteBucket(ctx, "bucket"); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the remove error", err)
		}
	})

	t.Run("DeleteBucketRemovesEmptyDirectories", func(t *testing.T) {
		c, _, dir := faultClient(t)
		if err := os.MkdirAll(filepath.Join(dir, "bucket", "a", "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteBucket(ctx, "bucket"); err != nil {
			t.Fatalf("DeleteBucket: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bucket")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("bucket directory remains: %v", err)
		}
	})

	t.Run("DeleteBucketNestedObject", func(t *testing.T) {
		c, _, _ := faultClient(t)
		put(t, c.Bucket("bucket"), "a/b/k", "x")
		if err := c.DeleteBucket(ctx, "bucket"); !errors.Is(err, objex.ErrBucketNotEmpty) {
			t.Errorf("err = %v, want ErrBucketNotEmpty", err)
		}
	})

	t.Run("DeleteBucketKeepsConcurrentPut", func(t *testing.T) {
		c, faults, dir := faultClient(t)
		b := c.Bucket("bucket")
		if err := os.MkdirAll(filepath.Join(dir, "bucket", "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		// The object lands after DeleteBucket has found the bucket empty and
		// removed bucket/a, just before it removes the bucket itself.
		faults.fail = func(op, name string) error {
			if op == "remove" && name == "bucket" {
				faults.fail = nil
				put(t, b, "late", "x")
			}
			return nil
		}
		if err := c.DeleteBucket(ctx, "bucket"); !errors.Is(err, objex.ErrBucketNotEmpty) {
			t.Errorf("err = %v, want ErrBucketNotEmpty", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bucket", "late")); err != nil {
			t.Errorf("DeleteBucket destroyed an object written before it returned: %v", err)
		}
	})

	t.Run("ListBucketsReadDirFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, readDir: func(string) error { return errInjected }}
		}
		if _, err := c.ListBuckets(ctx); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the read error", err)
		}
	})

	t.Run("ListBucketsInfoFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, infoErr: func(string) error { return errInjected }}
		}
		if _, err := c.ListBuckets(ctx); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the info error", err)
		}
	})

	t.Run("BucketExistsFailures", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		if _, err := c.BucketExists(canceled, "bucket"); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled: err = %v", err)
		}
		faults.fail = failOn("stat", "bucket", fs.ErrPermission)
		if _, err := c.BucketExists(ctx, "bucket"); !errors.Is(err, objex.ErrPermission) {
			t.Errorf("stat failure: err = %v, want ErrPermission", err)
		}
	})

	t.Run("RequireBucketStatFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fail = failOn("stat", "bucket", errInjected)
		if _, err := c.Bucket("bucket").Put(ctx, "k", strings.NewReader("x")); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the stat error", err)
		}
	})
}

func TestWriteFailures(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(*faultFS)
	}{
		{"OpenTemp", func(f *faultFS) { f.fail = failOn("openfile", "", errInjected) }},
		{"Sync", func(f *faultFS) { f.wrapCreate = func(x file) file { return faultFile{file: x, syncErr: errInjected} } }},
		{"Stat", func(f *faultFS) { f.wrapCreate = func(x file) file { return faultFile{file: x, statErr: errInjected} } }},
		{"Close", func(f *faultFS) {
			f.wrapCreate = func(x file) file { return faultFile{file: x, closeErr: errInjected} }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, faults, dir := faultClient(t)
			tc.setup(faults)
			if _, err := c.Bucket("bucket").Put(ctx, "k", strings.NewReader("x")); !errors.Is(err, errInjected) {
				t.Errorf("Put: err = %v, want the injected error", err)
			}
			if entries, _ := os.ReadDir(filepath.Join(dir, tempDir)); len(entries) != 0 {
				t.Errorf("failed write left %d temporary files", len(entries))
			}
		})
	}
}

func TestConditionalWriteFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("NoHardLinksCreates", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fail = failOn("link", "", syscall.ENOTSUP)
		if _, err := c.Bucket("bucket").Put(ctx, "new", strings.NewReader("x"), objex.IfNotExists()); err != nil {
			t.Errorf("Put IfNotExists without hard links: %v", err)
		}
	})

	t.Run("NoHardLinksExisting", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "taken", "x")
		faults.fail = failOn("link", "", syscall.ENOTSUP)
		if _, err := b.Put(ctx, "taken", strings.NewReader("y"), objex.IfNotExists()); !errors.Is(err, objex.ErrPreconditionFailed) {
			t.Errorf("err = %v, want ErrPreconditionFailed", err)
		}
	})

	t.Run("NoHardLinksLstatFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		faults.fail = func(op, _ string) error {
			switch op {
			case "link":
				return syscall.ENOTSUP
			case "lstat":
				return errInjected
			}
			return nil
		}
		if _, err := c.Bucket("bucket").Put(ctx, "new", strings.NewReader("x"), objex.IfNotExists()); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the lstat error", err)
		}
	})

	t.Run("IfMatchStatFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		info := put(t, b, "k", "x")
		faults.fail = failOn("stat", "bucket/k", errInjected)
		if _, err := b.Put(ctx, "k", strings.NewReader("y"), objex.IfMatch(info.ETag)); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the stat error", err)
		}
	})

	t.Run("IfMatchOnPrefix", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "dir/child", "x")
		if _, err := b.Put(ctx, "dir", strings.NewReader("y"), objex.IfMatch("etag")); !errors.Is(err, objex.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("IfMatchRenameFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		info := put(t, b, "k", "x")
		faults.fail = failOn("rename", "bucket/k", errInjected)
		if _, err := b.Put(ctx, "k", strings.NewReader("y"), objex.IfMatch(info.ETag)); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the rename error", err)
		}
	})
}

func TestReadFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("SeekFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "k", "0123456789")
		faults.wrapOpen = func(x file) file { return faultFile{file: x, seekErr: errInjected} }
		if _, err := b.Get(ctx, "k", objex.WithRange(2, 3)); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the seek error", err)
		}
	})

	t.Run("StatAfterOpenFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "k", "x")
		faults.wrapOpen = func(x file) file { return faultFile{file: x, statErr: errInjected} }
		if _, err := b.Get(ctx, "k"); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the stat error", err)
		}
	})

	t.Run("GetPrefix", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "dir/child", "x")
		if _, err := b.Get(ctx, "dir"); !errors.Is(err, objex.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestDeleteFailuresAndPruning(t *testing.T) {
	ctx := context.Background()

	t.Run("RemoveFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "k", "x")
		faults.fail = failOn("remove", "bucket/k", errInjected)
		if err := b.Delete(ctx, "k"); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the remove error", err)
		}
	})

	t.Run("PruneStopsAtNonEmptyDirectory", func(t *testing.T) {
		c, _, dir := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "a/b/c", "x")
		put(t, b, "a/d", "x")
		if err := b.Delete(ctx, "a/b/c"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bucket", "a", "b")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("empty directory a/b remains: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "bucket", "a", "d")); err != nil {
			t.Errorf("sibling a/d was removed: %v", err)
		}
	})
}

func TestListFailures(t *testing.T) {
	ctx := context.Background()

	collect := func(b *Bucket, ctx context.Context) ([]string, error) {
		var keys []string
		for info, err := range b.List(ctx) {
			if err != nil {
				return keys, err
			}
			keys = append(keys, info.Key)
		}
		return keys, nil
	}

	t.Run("WalkFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "k", "x")
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, readDir: func(string) error { return errInjected }}
		}
		if _, err := collect(b, ctx); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the walk error", err)
		}
	})

	t.Run("CanceledDuringWalk", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "k", "x")
		walkCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, readDir: func(string) error { cancel(); return nil }}
		}
		if _, err := collect(b, walkCtx); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("CanceledDuringIteration", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "a", "x")
		put(t, b, "b", "x")
		iterCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var lastErr error
		for _, err := range b.List(iterCtx) {
			if err != nil {
				lastErr = err
				break
			}
			cancel()
		}
		if !errors.Is(lastErr, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", lastErr)
		}
	})

	t.Run("InfoFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "k", "x")
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, infoErr: func(name string) error {
				if strings.HasSuffix(name, "/k") {
					return errInjected
				}
				return nil
			}}
		}
		if _, err := collect(b, ctx); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the info error", err)
		}
	})

	t.Run("InfoVanished", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "gone", "x")
		put(t, b, "kept", "x")
		faults.fsys = func(inner fs.FS) fs.FS {
			return faultDirFS{FS: inner, infoErr: func(name string) error {
				if strings.HasSuffix(name, "/gone") {
					return fs.ErrNotExist
				}
				return nil
			}}
		}
		keys, err := collect(b, ctx)
		if err != nil || len(keys) != 1 || keys[0] != "kept" {
			t.Errorf("List = %q, %v; want only kept", keys, err)
		}
	})

	t.Run("SkipsInvalidKeysOnDisk", func(t *testing.T) {
		c, _, dir := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "valid", "x")
		if err := os.WriteFile(filepath.Join(dir, "bucket", "back\\slash"), nil, 0o600); err != nil {
			t.Skipf("cannot create a file with a backslash: %v", err)
		}
		keys, err := collect(b, ctx)
		if err != nil || len(keys) != 1 || keys[0] != "valid" {
			t.Errorf("List = %q, %v; want only valid", keys, err)
		}
	})
}

func TestCopyFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("InvalidOption", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "src", "x")
		if _, err := b.Copy(ctx, "dst", "src", objex.WithMetadata(map[string]string{"bad key": "v"})); !errors.Is(err, objex.ErrInvalidOption) {
			t.Errorf("err = %v, want ErrInvalidOption", err)
		}
	})

	t.Run("TempWriteFails", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "src", "x")
		faults.fail = failOn("openfile", "", errInjected)
		if _, err := b.Copy(ctx, "dst", "src"); !errors.Is(err, errInjected) {
			t.Errorf("err = %v, want the write error", err)
		}
	})

	t.Run("PathConflict", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "src", "x")
		put(t, b, "file", "x")
		if _, err := b.Copy(ctx, "file/child", "src"); !errors.Is(err, objex.ErrUnsupported) {
			t.Errorf("err = %v, want ErrUnsupported", err)
		}
	})
}

func TestMoveFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("SourceIsPrefix", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "dir/child", "x")
		if _, err := b.Move(ctx, "dst", "dir"); !errors.Is(err, objex.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("SourceVanishes", func(t *testing.T) {
		c, faults, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "src", "x")
		faults.fail = failOn("rename", "bucket/dst", fs.ErrNotExist)
		if _, err := b.Move(ctx, "dst", "src"); !errors.Is(err, objex.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("DestinationIsDirectory", func(t *testing.T) {
		c, _, _ := faultClient(t)
		b := c.Bucket("bucket")
		put(t, b, "src", "x")
		put(t, b, "dir/child", "x")
		if _, err := b.Move(ctx, "dir", "src"); !errors.Is(err, objex.ErrUnsupported) {
			t.Errorf("err = %v, want ErrUnsupported", err)
		}
	})
}

// cancelingReader cancels its context after the first read.
type cancelingReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.ErrUnexpectedEOF
	}
	r.read = true
	r.cancel()
	return copy(p, "partial"), nil
}

func TestPutCanceledWhileReading(t *testing.T) {
	c, _, dir := faultClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Bucket("bucket").Put(ctx, "k", &cancelingReader{cancel: cancel}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bucket", "k")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("canceled Put left an object: %v", err)
	}
}

func TestHandlerRejectsUnknownCondition(t *testing.T) {
	c, _, _ := faultClient(t)
	expires := time.Now().Add(time.Minute).Unix()
	signature := c.sign(http.MethodPut, "bucket", "k", expires, "", "bogus")
	target := fmt.Sprintf("/files/bucket/k?%s=%d&%s=bogus&%s=%s", paramExpires, expires, paramCondition, paramSignature, signature)

	recorder := httptest.NewRecorder()
	c.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodPut, target, strings.NewReader("x")))
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
}

func TestWriteErrorStatus(t *testing.T) {
	cases := map[error]int{
		objex.ErrNotFound:             http.StatusNotFound,
		objex.ErrPreconditionFailed:   http.StatusPreconditionFailed,
		objex.ErrInvalidOption:        http.StatusBadRequest,
		objex.ErrInvalidKey:           http.StatusBadRequest,
		objex.ErrUnsupported:          http.StatusConflict,
		objex.ErrPermission:           http.StatusForbidden,
		errInjected:                   http.StatusInternalServerError,
		&http.MaxBytesError{Limit: 1}: http.StatusRequestEntityTooLarge,
	}
	for err, want := range cases {
		recorder := httptest.NewRecorder()
		writeError(recorder, err)
		if recorder.Code != want {
			t.Errorf("writeError(%v) = %d, want %d", err, recorder.Code, want)
		}
	}
}

func TestKindOf(t *testing.T) {
	cases := map[error]error{
		fs.ErrNotExist:   objex.ErrNotFound,
		fs.ErrPermission: objex.ErrPermission,
		fmt.Errorf("x: %w", objex.ErrInvalidOption): objex.ErrInvalidOption,
		errInjected: nil,
	}
	for err, want := range cases {
		if got := kindOf(err); !errors.Is(got, want) || (want == nil && got != nil) {
			t.Errorf("kindOf(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestPutFailsWhenTempDirIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	if err := c.CreateBucket(ctx, "bucket"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, tempDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bucket("bucket").Put(ctx, "k", strings.NewReader("x")); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound from the missing temporary directory", err)
	}
}
