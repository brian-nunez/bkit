// Package objextest is the conformance suite for objex drivers. Every driver
// runs it from its own tests so all drivers behave identically for the same
// calls.
//
// Run exercises objex.Bucket; RunManager exercises objex.Manager;
// RunPresigner exercises objex.Presigner by performing the presigned HTTP
// requests. Each function is safe to call from a driver's test with t.Parallel.
package objextest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/objex"
)

// Config describes the driver under test.
type Config struct {
	// NewBucket returns a handle to an existing, empty bucket that no other
	// test uses. Required. The driver's test owns cleanup.
	NewBucket func(t *testing.T) objex.Bucket

	// Metadata reports whether the driver stores user metadata. Drivers that
	// do not must reject WithMetadata with objex.ErrUnsupported.
	Metadata bool

	// ContentType reports whether the driver stores the content type passed
	// with WithContentType rather than deriving one.
	ContentType bool
}

// Run runs the objex.Bucket conformance tests.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.NewBucket == nil {
		t.Fatal("objextest: Config.NewBucket is required")
	}

	tests := []struct {
		name string
		fn   func(*testing.T, Config)
	}{
		{"PutAndGet", testPutAndGet},
		{"PutUnknownSizeStream", testPutUnknownSizeStream},
		{"EmptyObject", testEmptyObject},
		{"Overwrite", testOverwrite},
		{"Missing", testMissing},
		{"Range", testRange},
		{"Delete", testDelete},
		{"InvalidKeys", testInvalidKeys},
		{"IfNotExists", testIfNotExists},
		{"IfMatch", testIfMatch},
		{"List", testList},
		{"ListEmpty", testListEmpty},
		{"Copy", testCopy},
		{"ContentType", testContentType},
		{"Metadata", testMetadata},
		{"Move", testMove},
		{"Exists", testExists},
		{"ContextCanceled", testContextCanceled},
		{"Concurrent", testConcurrent},
		{"ErrorDetails", testErrorDetails},
		{"Ping", testPing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, cfg) })
	}
}

func testPutAndGet(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	body := "hello, objex"
	before := time.Now()

	info := mustPut(t, b, "dir/sub/hello.txt", body)
	if info.Key != "dir/sub/hello.txt" {
		t.Errorf("Put Key = %q, want %q", info.Key, "dir/sub/hello.txt")
	}
	if info.Size != int64(len(body)) {
		t.Errorf("Put Size = %d, want %d", info.Size, len(body))
	}
	if info.ETag == "" || strings.Contains(info.ETag, `"`) {
		t.Errorf("Put ETag = %q, want a non-empty unquoted value", info.ETag)
	}
	assertRecent(t, "Put LastModified", info.LastModified, before)

	obj, err := b.Get(ctx, "dir/sub/hello.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := readAndClose(t, obj); got != body {
		t.Errorf("Get body = %q, want %q", got, body)
	}
	if obj.Info.Size != int64(len(body)) || obj.Info.ETag != info.ETag || obj.Info.Key != info.Key {
		t.Errorf("Get Info = %+v, want Key %q Size %d ETag %q", obj.Info, info.Key, len(body), info.ETag)
	}

	stat, err := b.Stat(ctx, "dir/sub/hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Key != info.Key || stat.Size != info.Size || stat.ETag != info.ETag {
		t.Errorf("Stat = %+v, want Key %q Size %d ETag %q", stat, info.Key, info.Size, info.ETag)
	}
	if stat.ContentType == "" {
		t.Error("Stat ContentType is empty")
	}
	assertRecent(t, "Stat LastModified", stat.LastModified, before)
}

// hiddenReader hides every method but Read so drivers cannot learn the size.
type hiddenReader struct{ r io.Reader }

func (h hiddenReader) Read(p []byte) (int, error) { return h.r.Read(p) }

func testPutUnknownSizeStream(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	// Larger than the common 5 MiB multipart threshold, so streaming drivers
	// exercise their multipart path.
	data := bytes.Repeat([]byte("0123456789abcdef"), 6<<20/16+3)

	info, err := b.Put(ctx, "stream.bin", hiddenReader{bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info.Size != int64(len(data)) {
		t.Errorf("Put Size = %d, want %d", info.Size, len(data))
	}

	obj, err := b.Get(ctx, "stream.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := readAndClose(t, obj); got != string(data) {
		t.Errorf("Get returned %d bytes that differ from the %d bytes written", len(got), len(data))
	}

	sized, err := b.Put(ctx, "sized.bin", bytes.NewReader(data[:1024]), objex.WithSize(1024))
	if err != nil {
		t.Fatalf("Put WithSize: %v", err)
	}
	if sized.Size != 1024 {
		t.Errorf("Put WithSize Size = %d, want 1024", sized.Size)
	}
}

func testEmptyObject(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	info := mustPut(t, b, "empty", "")
	if info.Size != 0 {
		t.Errorf("Put Size = %d, want 0", info.Size)
	}
	obj, err := b.Get(context.Background(), "empty")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := readAndClose(t, obj); got != "" {
		t.Errorf("Get body = %q, want empty", got)
	}
}

func testOverwrite(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	first := mustPut(t, b, "key", "first version")
	second := mustPut(t, b, "key", "second, longer version")
	if first.ETag == second.ETag {
		t.Errorf("ETag unchanged after overwrite: %q", first.ETag)
	}
	if got := mustGet(t, b, "key"); got != "second, longer version" {
		t.Errorf("Get body = %q, want the second version", got)
	}
}

func testMissing(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()

	obj, err := b.Get(ctx, "missing")
	if !errors.Is(err, objex.ErrNotFound) {
		if obj != nil {
			_ = obj.Close()
		}
		t.Errorf("Get missing: err = %v, want ErrNotFound", err)
	}
	if _, err := b.Stat(ctx, "missing"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Stat missing: err = %v, want ErrNotFound", err)
	}
}

func testRange(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	mustPut(t, b, "range", "0123456789")

	cases := []struct {
		offset, length int64
		want           string
	}{
		{0, 4, "0123"},
		{3, 4, "3456"},
		{6, -1, "6789"},
		{7, 100, "789"},
		{9, 1, "9"},
	}
	for _, c := range cases {
		obj, err := b.Get(ctx, "range", objex.WithRange(c.offset, c.length))
		if err != nil {
			t.Errorf("Get range (%d, %d): %v", c.offset, c.length, err)
			continue
		}
		if obj.Info.Size != 10 {
			t.Errorf("Get range (%d, %d) Info.Size = %d, want the full size 10", c.offset, c.length, obj.Info.Size)
		}
		if got := readAndClose(t, obj); got != c.want {
			t.Errorf("Get range (%d, %d) = %q, want %q", c.offset, c.length, got, c.want)
		}
	}

	invalid := []struct{ offset, length int64 }{{10, 1}, {11, -1}, {-1, 2}, {0, 0}}
	for _, c := range invalid {
		obj, err := b.Get(ctx, "range", objex.WithRange(c.offset, c.length))
		if !errors.Is(err, objex.ErrInvalidRange) {
			if obj != nil {
				_ = obj.Close()
			}
			t.Errorf("Get range (%d, %d): err = %v, want ErrInvalidRange", c.offset, c.length, err)
		}
	}
}

func testDelete(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	mustPut(t, b, "a/b/c", "value")

	if err := b.Delete(ctx, "a/b/c"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Stat(ctx, "a/b/c"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Stat after Delete: err = %v, want ErrNotFound", err)
	}
	if err := b.Delete(ctx, "a/b/c"); err != nil {
		t.Errorf("Delete missing: err = %v, want nil", err)
	}
	if keys := listKeys(t, b); len(keys) != 0 {
		t.Errorf("List after Delete = %q, want nothing", keys)
	}
}

func testInvalidKeys(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	mustPut(t, b, "valid", "x")

	for _, key := range []string{"", "/leading", "trailing/", "a//b", "a/./b", "a/../b", "..", `a\b`, "a\x00b", "a\nb"} {
		if _, err := b.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, objex.ErrInvalidKey) {
			t.Errorf("Put(%q): err = %v, want ErrInvalidKey", key, err)
		}
		if obj, err := b.Get(ctx, key); !errors.Is(err, objex.ErrInvalidKey) {
			if obj != nil {
				_ = obj.Close()
			}
			t.Errorf("Get(%q): err = %v, want ErrInvalidKey", key, err)
		}
		if _, err := b.Stat(ctx, key); !errors.Is(err, objex.ErrInvalidKey) {
			t.Errorf("Stat(%q): err = %v, want ErrInvalidKey", key, err)
		}
		if err := b.Delete(ctx, key); !errors.Is(err, objex.ErrInvalidKey) {
			t.Errorf("Delete(%q): err = %v, want ErrInvalidKey", key, err)
		}
		if _, err := b.Copy(ctx, key, "valid"); !errors.Is(err, objex.ErrInvalidKey) {
			t.Errorf("Copy(dst %q): err = %v, want ErrInvalidKey", key, err)
		}
		if _, err := b.Copy(ctx, "valid-copy", key); !errors.Is(err, objex.ErrInvalidKey) {
			t.Errorf("Copy(src %q): err = %v, want ErrInvalidKey", key, err)
		}
	}
}

func testIfNotExists(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()

	if _, err := b.Put(ctx, "once", strings.NewReader("first"), objex.IfNotExists()); err != nil {
		t.Fatalf("Put IfNotExists on a new key: %v", err)
	}
	if _, err := b.Put(ctx, "once", strings.NewReader("second"), objex.IfNotExists()); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("Put IfNotExists on an existing key: err = %v, want ErrPreconditionFailed", err)
	}
	if got := mustGet(t, b, "once"); got != "first" {
		t.Errorf("Get after rejected Put = %q, want %q", got, "first")
	}
	if _, err := b.Put(ctx, "both", strings.NewReader("x"), objex.IfNotExists(), objex.IfMatch("etag")); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("Put IfNotExists with IfMatch: err = %v, want ErrInvalidOption", err)
	}
}

func testIfMatch(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	original := mustPut(t, b, "guarded", "original")

	if _, err := b.Put(ctx, "guarded", strings.NewReader("stale"), objex.IfMatch("not-the-etag")); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("Put IfMatch wrong ETag: err = %v, want ErrPreconditionFailed", err)
	}
	updated, err := b.Put(ctx, "guarded", strings.NewReader("updated"), objex.IfMatch(original.ETag))
	if err != nil {
		t.Fatalf("Put IfMatch current ETag: %v", err)
	}
	if got := mustGet(t, b, "guarded"); got != "updated" {
		t.Errorf("Get after IfMatch Put = %q, want %q", got, "updated")
	}
	if _, err := b.Put(ctx, "missing", strings.NewReader("x"), objex.IfMatch(original.ETag)); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Put IfMatch on a missing key: err = %v, want ErrNotFound", err)
	}

	obj, err := b.Get(ctx, "guarded", objex.IfMatch(updated.ETag))
	if err != nil {
		t.Fatalf("Get IfMatch current ETag: %v", err)
	}
	_ = readAndClose(t, obj)
	if obj, err := b.Get(ctx, "guarded", objex.IfMatch(original.ETag)); !errors.Is(err, objex.ErrPreconditionFailed) {
		if obj != nil {
			_ = obj.Close()
		}
		t.Errorf("Get IfMatch stale ETag: err = %v, want ErrPreconditionFailed", err)
	}
}

func testList(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	keys := []string{"a.txt", "b/1.txt", "b/2.txt", "b/c/3.txt", "b-sibling.txt", "c.txt"}
	for _, key := range slices.Backward(keys) {
		mustPut(t, b, key, "content of "+key)
	}
	want := slices.Clone(keys)
	slices.Sort(want)

	if got := listKeys(t, b); !slices.Equal(got, want) {
		t.Errorf("List = %q, want %q", got, want)
	}

	for info, err := range b.List(ctx) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if info.IsPrefix || info.Size != int64(len("content of "+info.Key)) || info.ETag == "" || info.LastModified.IsZero() {
			t.Errorf("List entry %+v lacks Size, ETag, or LastModified", info)
		}
	}

	if got := listKeys(t, b, objex.WithPrefix("b/")); !slices.Equal(got, []string{"b/1.txt", "b/2.txt", "b/c/3.txt"}) {
		t.Errorf("List WithPrefix(b/) = %q", got)
	}
	if got := listKeys(t, b, objex.WithStartAfter("b/2.txt")); !slices.Equal(got, []string{"b/c/3.txt", "c.txt"}) {
		t.Errorf("List WithStartAfter(b/2.txt) = %q", got)
	}

	var top []string
	for info, err := range b.List(ctx, objex.WithDelimiter("/")) {
		if err != nil {
			t.Fatalf("List WithDelimiter: %v", err)
		}
		entry := info.Key
		if info.IsPrefix {
			entry += " (prefix)"
		}
		top = append(top, entry)
	}
	if want := []string{"a.txt", "b-sibling.txt", "b/ (prefix)", "c.txt"}; !slices.Equal(top, want) {
		t.Errorf("List WithDelimiter(/) = %q, want %q", top, want)
	}

	var nested []string
	for info, err := range b.List(ctx, objex.WithPrefix("b/"), objex.WithDelimiter("/")) {
		if err != nil {
			t.Fatalf("List WithPrefix WithDelimiter: %v", err)
		}
		nested = append(nested, info.Key)
	}
	if want := []string{"b/1.txt", "b/2.txt", "b/c/"}; !slices.Equal(nested, want) {
		t.Errorf("List WithPrefix(b/) WithDelimiter(/) = %q, want %q", nested, want)
	}

	count := 0
	for _, err := range b.List(ctx) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		count++
		if count == 2 {
			break
		}
	}
	if count != 2 {
		t.Errorf("breaking out of List after 2 entries saw %d", count)
	}
}

func testListEmpty(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	if keys := listKeys(t, b); len(keys) != 0 {
		t.Errorf("List on an empty bucket = %q", keys)
	}
	if keys := listKeys(t, b, objex.WithPrefix("nothing/")); len(keys) != 0 {
		t.Errorf("List WithPrefix on an empty bucket = %q", keys)
	}
}

func testCopy(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	src := mustPut(t, b, "src.txt", "copy me")

	info, err := b.Copy(ctx, "nested/dst.txt", "src.txt")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if info.Key != "nested/dst.txt" || info.Size != src.Size || info.ETag == "" {
		t.Errorf("Copy info = %+v, want Key nested/dst.txt Size %d and an ETag", info, src.Size)
	}
	if got := mustGet(t, b, "nested/dst.txt"); got != "copy me" {
		t.Errorf("Get copy = %q", got)
	}
	if got := mustGet(t, b, "src.txt"); got != "copy me" {
		t.Errorf("Get source after Copy = %q", got)
	}

	if _, err := b.Copy(ctx, "dst2.txt", "missing"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Copy from a missing key: err = %v, want ErrNotFound", err)
	}
	if _, err := b.Copy(ctx, "dst3.txt", "src.txt", objex.IfMatch("not-the-etag")); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("Copy IfMatch wrong ETag: err = %v, want ErrPreconditionFailed", err)
	}
	if _, err := b.Copy(ctx, "dst4.txt", "src.txt", objex.IfMatch(src.ETag)); err != nil {
		t.Errorf("Copy IfMatch current ETag: %v", err)
	}
}

func testContentType(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()

	if _, err := b.Put(ctx, "page.html", strings.NewReader("<p>hi</p>"), objex.WithContentType("text/html")); err != nil {
		t.Fatalf("Put WithContentType: %v", err)
	}
	stat, err := b.Stat(ctx, "page.html")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !strings.HasPrefix(stat.ContentType, "text/html") {
		t.Errorf("Stat ContentType = %q, want text/html", stat.ContentType)
	}

	if !cfg.ContentType {
		return
	}
	if _, err := b.Put(ctx, "data", strings.NewReader("{}"), objex.WithContentType("application/vnd.objex+json")); err != nil {
		t.Fatalf("Put WithContentType: %v", err)
	}
	if stat, err := b.Stat(ctx, "data"); err != nil || stat.ContentType != "application/vnd.objex+json" {
		t.Errorf("Stat ContentType = %q, %v; want application/vnd.objex+json", stat.ContentType, err)
	}
	if info, err := b.Copy(ctx, "data-copy", "data"); err != nil {
		t.Fatalf("Copy: %v", err)
	} else if stat, err := b.Stat(ctx, info.Key); err != nil || stat.ContentType != "application/vnd.objex+json" {
		t.Errorf("Copy preserved ContentType = %q, %v", stat.ContentType, err)
	}
	if _, err := b.Copy(ctx, "data-text", "data", objex.WithContentType("text/plain")); err != nil {
		t.Fatalf("Copy WithContentType: %v", err)
	}
	if stat, err := b.Stat(ctx, "data-text"); err != nil || stat.ContentType != "text/plain" {
		t.Errorf("Copy WithContentType ContentType = %q, %v; want text/plain", stat.ContentType, err)
	}
}

func testMetadata(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	metadata := map[string]string{"Owner": "objex", "build-id": "42"}

	if !cfg.Metadata {
		mustPut(t, b, "plain", "x")
		if _, err := b.Put(ctx, "meta", strings.NewReader("x"), objex.WithMetadata(metadata)); !errors.Is(err, objex.ErrUnsupported) {
			t.Errorf("Put WithMetadata: err = %v, want ErrUnsupported", err)
		}
		if _, err := b.Copy(ctx, "meta-copy", "plain", objex.WithMetadata(metadata)); !errors.Is(err, objex.ErrUnsupported) {
			t.Errorf("Copy WithMetadata: err = %v, want ErrUnsupported", err)
		}
		return
	}

	want := map[string]string{"owner": "objex", "build-id": "42"}
	if _, err := b.Put(ctx, "meta", strings.NewReader("x"), objex.WithMetadata(metadata)); err != nil {
		t.Fatalf("Put WithMetadata: %v", err)
	}
	if stat, err := b.Stat(ctx, "meta"); err != nil || !equalMetadata(stat.Metadata, want) {
		t.Errorf("Stat Metadata = %v, %v; want %v", stat.Metadata, err, want)
	}
	obj, err := b.Get(ctx, "meta")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = readAndClose(t, obj)
	if !equalMetadata(obj.Info.Metadata, want) {
		t.Errorf("Get Info.Metadata = %v, want %v", obj.Info.Metadata, want)
	}

	if _, err := b.Copy(ctx, "meta-copy", "meta"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if stat, err := b.Stat(ctx, "meta-copy"); err != nil || !equalMetadata(stat.Metadata, want) {
		t.Errorf("Copy preserved Metadata = %v, %v; want %v", stat.Metadata, err, want)
	}
	replaced := map[string]string{"stage": "copied"}
	if _, err := b.Copy(ctx, "meta-replaced", "meta", objex.WithMetadata(replaced)); err != nil {
		t.Fatalf("Copy WithMetadata: %v", err)
	}
	if stat, err := b.Stat(ctx, "meta-replaced"); err != nil || !equalMetadata(stat.Metadata, replaced) {
		t.Errorf("Copy WithMetadata Metadata = %v, %v; want %v", stat.Metadata, err, replaced)
	}

	if _, err := b.Put(ctx, "bad", strings.NewReader("x"), objex.WithMetadata(map[string]string{"bad key": "v"})); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("Put WithMetadata invalid key: err = %v, want ErrInvalidOption", err)
	}
}

func testMove(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	mustPut(t, b, "from/here.txt", "moving")

	info, err := objex.Move(ctx, b, "to/there.txt", "from/here.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if info.Key != "to/there.txt" || info.Size != int64(len("moving")) {
		t.Errorf("Move info = %+v", info)
	}
	if got := mustGet(t, b, "to/there.txt"); got != "moving" {
		t.Errorf("Get destination = %q", got)
	}
	if _, err := b.Stat(ctx, "from/here.txt"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Stat source after Move: err = %v, want ErrNotFound", err)
	}
	if _, err := objex.Move(ctx, b, "anywhere", "missing"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Move from a missing key: err = %v, want ErrNotFound", err)
	}
}

func testExists(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	mustPut(t, b, "present", "x")

	if ok, err := objex.Exists(ctx, b, "present"); err != nil || !ok {
		t.Errorf("Exists(present) = %v, %v; want true", ok, err)
	}
	if ok, err := objex.Exists(ctx, b, "absent"); err != nil || ok {
		t.Errorf("Exists(absent) = %v, %v; want false", ok, err)
	}
	if _, err := objex.Exists(ctx, b, "/invalid"); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("Exists(/invalid): err = %v, want ErrInvalidKey", err)
	}
}

func testContextCanceled(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	mustPut(t, b, "present", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := b.Put(ctx, "new", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put: err = %v, want context.Canceled", err)
	}
	if obj, err := b.Get(ctx, "present"); !errors.Is(err, context.Canceled) {
		if obj != nil {
			_ = obj.Close()
		}
		t.Errorf("Get: err = %v, want context.Canceled", err)
	}
	if _, err := b.Stat(ctx, "present"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat: err = %v, want context.Canceled", err)
	}
	if err := b.Delete(ctx, "present"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete: err = %v, want context.Canceled", err)
	}
	if _, err := b.Copy(ctx, "copy", "present"); !errors.Is(err, context.Canceled) {
		t.Errorf("Copy: err = %v, want context.Canceled", err)
	}
	for _, err := range b.List(ctx) {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("List: err = %v, want context.Canceled", err)
		}
		break
	}
}

func testConcurrent(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	ctx := context.Background()
	const workers = 16

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Go(func() {
			key := fmt.Sprintf("concurrent/%02d", i)
			body := strings.Repeat(key, 64)
			if _, err := b.Put(ctx, key, strings.NewReader(body)); err != nil {
				errs <- fmt.Errorf("put %s: %w", key, err)
				return
			}
			obj, err := b.Get(ctx, key)
			if err != nil {
				errs <- fmt.Errorf("get %s: %w", key, err)
				return
			}
			data, err := io.ReadAll(obj)
			_ = obj.Close()
			switch {
			case err != nil:
				errs <- fmt.Errorf("read %s: %w", key, err)
			case string(data) != body:
				errs <- fmt.Errorf("get %s returned different content", key)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if keys := listKeys(t, b, objex.WithPrefix("concurrent/")); len(keys) != workers {
		t.Errorf("List after concurrent Puts found %d keys, want %d", len(keys), workers)
	}
}

func testErrorDetails(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	_, err := b.Stat(context.Background(), "missing/key")
	var objexErr *objex.Error
	if !errors.As(err, &objexErr) {
		t.Fatalf("Stat missing: err = %v (%T), want *objex.Error", err, err)
	}
	if objexErr.Key != "missing/key" || objexErr.Op == "" || objexErr.Bucket == "" {
		t.Errorf("Error = %+v, want Op, Bucket, and Key set", objexErr)
	}
}

func testPing(t *testing.T, cfg Config) {
	b := cfg.NewBucket(t)
	pinger, ok := b.(objex.Pinger)
	if !ok {
		t.Skip("driver does not implement objex.Pinger")
	}
	if err := pinger.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func mustPut(t *testing.T, b objex.Bucket, key, body string) objex.ObjectInfo {
	t.Helper()
	info, err := b.Put(context.Background(), key, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	return info
}

func mustGet(t *testing.T, b objex.Bucket, key string) string {
	t.Helper()
	obj, err := b.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return readAndClose(t, obj)
}

func readAndClose(t *testing.T, obj *objex.Object) string {
	t.Helper()
	data, err := io.ReadAll(obj)
	if closeErr := obj.Close(); closeErr != nil {
		t.Errorf("Close: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	return string(data)
}

func listKeys(t *testing.T, b objex.Bucket, opts ...objex.ListOption) []string {
	t.Helper()
	var keys []string
	for info, err := range b.List(context.Background(), opts...) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		keys = append(keys, info.Key)
	}
	return keys
}

func assertRecent(t *testing.T, what string, got, before time.Time) {
	t.Helper()
	// Remote backends round to the second and their clocks may drift from
	// the test machine's.
	if got.Before(before.Add(-2*time.Minute)) || got.After(time.Now().Add(2*time.Minute)) {
		t.Errorf("%s = %v, want close to %v", what, got, before)
	}
}

func equalMetadata(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
