package filesystem_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/objex"
	"github.com/brian-nunez/bkit/objex/drivers/filesystem"
	"github.com/brian-nunez/bkit/objex/objextest"
)

var signingKey = bytes.Repeat([]byte("k"), 32)

func newClient(t *testing.T, opts ...filesystem.Option) (*filesystem.Client, string) {
	t.Helper()
	root := t.TempDir()
	client, err := filesystem.New(root, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, root
}

func newBucket(t *testing.T, opts ...filesystem.Option) *filesystem.Bucket {
	t.Helper()
	client, _ := newClient(t, opts...)
	if err := client.CreateBucket(context.Background(), "objextest"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	return client.Bucket("objextest")
}

func TestConformance(t *testing.T) {
	objextest.Run(t, objextest.Config{
		NewBucket: func(t *testing.T) objex.Bucket { return newBucket(t) },
	})
}

func TestManagerConformance(t *testing.T) {
	client, _ := newClient(t)
	var counter atomic.Int64
	objextest.RunManager(t, objextest.ManagerConfig{
		Manager: client,
		Bucket:  func(name string) objex.Bucket { return client.Bucket(name) },
		NewBucketName: func(*testing.T) string {
			return fmt.Sprintf("objextest-%d", counter.Add(1))
		},
	})
}

func TestPresignerConformance(t *testing.T) {
	objextest.RunPresigner(t, objextest.PresignerConfig{
		NewBucket: func(t *testing.T) objex.Bucket {
			_, b, _ := presignServer(t)
			return b
		},
	})
}

func TestNew(t *testing.T) {
	if _, err := filesystem.New(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("New on a missing directory: err = %v, want ErrNotFound", err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.New(file); err == nil {
		t.Error("New on a regular file succeeded")
	}

	invalid := []filesystem.Option{
		filesystem.WithPresign("not a url", signingKey),
		filesystem.WithPresign("ftp://example.com/files", signingKey),
		filesystem.WithPresign("/relative", signingKey),
		filesystem.WithPresign("https://example.com/files?x=1", signingKey),
		filesystem.WithPresign("https://example.com/files", []byte("short")),
	}
	for i, opt := range invalid {
		if _, err := filesystem.New(t.TempDir(), opt); !errors.Is(err, objex.ErrInvalidOption) {
			t.Errorf("case %d: err = %v, want ErrInvalidOption", i, err)
		}
	}

	if _, err := filesystem.New(t.TempDir(), nil, filesystem.WithClock(nil)); err != nil {
		t.Errorf("New with nil options: %v", err)
	}
}

func TestStorageLayout(t *testing.T) {
	client, root := newClient(t)
	ctx := context.Background()
	if err := client.CreateBucket(ctx, "assets"); err != nil {
		t.Fatal(err)
	}
	b := client.Bucket("assets")
	if _, err := b.Put(ctx, "images/logo.png", strings.NewReader("png")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, "assets", "images", "logo.png"))
	if err != nil || string(data) != "png" {
		t.Errorf("file on disk = %q, %v", data, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, ".objex-tmp")); len(entries) != 0 {
		t.Errorf("temporary directory holds %d leftover files", len(entries))
	}

	if err := b.Delete(ctx, "images/logo.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "images")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty directory left after Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets")); err != nil {
		t.Errorf("Delete removed the bucket directory: %v", err)
	}
}

func TestContentTypeFromExtension(t *testing.T) {
	b := newBucket(t)
	ctx := context.Background()
	cases := map[string]string{"page.html": "text/html", "data.json": "application/json", "no-extension": "application/octet-stream"}
	for key, want := range cases {
		info, err := b.Put(ctx, key, strings.NewReader("x"), objex.WithContentType("ignored/type"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(info.ContentType, want) {
			t.Errorf("ContentType(%q) = %q, want %q", key, info.ContentType, want)
		}
	}
}

func TestSymlinkEscapeIsBlocked(t *testing.T) {
	client, root := newClient(t)
	ctx := context.Background()
	if err := client.CreateBucket(ctx, "assets"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside the root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "assets", "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	b := client.Bucket("assets")
	if obj, err := b.Get(ctx, "escape/secret"); err == nil {
		_ = obj.Close()
		t.Fatal("Get followed a symlink out of the root")
	}
	if _, err := b.Put(ctx, "escape/planted", strings.NewReader("x")); err == nil {
		t.Fatal("Put wrote through a symlink out of the root")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file created outside the root: %v", err)
	}
	for info, err := range b.List(ctx) {
		if err == nil && strings.HasPrefix(info.Key, "escape/") {
			t.Errorf("List exposed %q from outside the root", info.Key)
		}
	}
}

func TestPathConflicts(t *testing.T) {
	b := newBucket(t)
	ctx := context.Background()
	if _, err := b.Put(ctx, "a", strings.NewReader("file")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "a/b", strings.NewReader("x")); !errors.Is(err, objex.ErrUnsupported) {
		t.Errorf("Put under an existing object: err = %v, want ErrUnsupported", err)
	}
	if _, err := b.Put(ctx, "dir/child", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "dir", strings.NewReader("x")); !errors.Is(err, objex.ErrUnsupported) {
		t.Errorf("Put over an existing prefix: err = %v, want ErrUnsupported", err)
	}
	if _, err := b.Stat(ctx, "dir"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Stat on a prefix: err = %v, want ErrNotFound", err)
	}
	if obj, err := b.Get(ctx, "a/b"); !errors.Is(err, objex.ErrNotFound) {
		if obj != nil {
			_ = obj.Close()
		}
		t.Errorf("Get below an object: err = %v, want ErrNotFound", err)
	}
	if err := b.Delete(ctx, "dir"); err != nil {
		t.Errorf("Delete on a prefix: err = %v, want nil", err)
	}
	if _, err := b.Move(ctx, "a/c", "dir/child"); !errors.Is(err, objex.ErrUnsupported) {
		t.Errorf("Move under an existing object: err = %v, want ErrUnsupported", err)
	}
}

func TestMove(t *testing.T) {
	b := newBucket(t)
	ctx := context.Background()
	original, err := b.Put(ctx, "from/file.txt", strings.NewReader("move"))
	if err != nil {
		t.Fatal(err)
	}

	info, err := b.Move(ctx, "to/file.txt", "from/file.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if info.ETag != original.ETag {
		t.Errorf("native Move changed the ETag: %q -> %q", original.ETag, info.ETag)
	}
	if info, err := b.Move(ctx, "to/file.txt", "to/file.txt"); err != nil || info.Key != "to/file.txt" {
		t.Errorf("Move onto itself = %+v, %v", info, err)
	}
	if _, err := b.Move(ctx, "x", "from/file.txt"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Move a moved source: err = %v, want ErrNotFound", err)
	}
	if _, err := b.Move(ctx, "/bad", "to/file.txt"); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("Move invalid dst: err = %v, want ErrInvalidKey", err)
	}
}

func TestMissingBucket(t *testing.T) {
	client, _ := newClient(t, filesystem.WithPresign("https://example.com/files", signingKey))
	ctx := context.Background()
	b := client.Bucket("absent")

	if _, err := b.Stat(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Stat: err = %v", err)
	}
	if _, err := b.Get(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Get: err = %v", err)
	}
	if err := b.Delete(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Delete: err = %v", err)
	}
	if _, err := b.Copy(ctx, "a", "b"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Copy: err = %v", err)
	}
	if _, err := b.Move(ctx, "a", "b"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Move: err = %v", err)
	}
	if err := b.Ping(ctx); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Ping: err = %v", err)
	}
	for _, err := range b.List(ctx) {
		if !errors.Is(err, objex.ErrBucketNotFound) {
			t.Errorf("List: err = %v", err)
		}
	}

	invalid := client.Bucket("Invalid_Name")
	if _, err := invalid.Stat(ctx, "k"); !errors.Is(err, objex.ErrInvalidBucketName) {
		t.Errorf("Stat with an invalid bucket name: err = %v", err)
	}
	if err := invalid.Ping(ctx); !errors.Is(err, objex.ErrInvalidBucketName) {
		t.Errorf("Ping with an invalid bucket name: err = %v", err)
	}
	for _, err := range invalid.List(ctx) {
		if !errors.Is(err, objex.ErrInvalidBucketName) {
			t.Errorf("List with an invalid bucket name: err = %v", err)
		}
	}
}

func TestListBucketsSkipsNonBuckets(t *testing.T) {
	client, root := newClient(t)
	ctx := context.Background()
	if err := client.CreateBucket(ctx, "real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Not_A_Bucket"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Name != "real" || buckets[0].CreatedAt.IsZero() {
		t.Errorf("ListBuckets = %+v, want only real", buckets)
	}
	if ok, err := client.BucketExists(ctx, "loose-file"); err != nil || ok {
		t.Errorf("BucketExists(loose-file) = %v, %v; want false", ok, err)
	}
}

func TestCanceledManagerCalls(t *testing.T) {
	client, _ := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ListBuckets(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListBuckets: err = %v", err)
	}
	if err := client.Bucket("bucket").Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Ping: err = %v", err)
	}
	if _, err := client.Bucket("bucket").Move(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Errorf("Move: err = %v", err)
	}
}

func TestPutSizeMismatch(t *testing.T) {
	b := newBucket(t)
	if _, err := b.Put(context.Background(), "k", strings.NewReader("four"), objex.WithSize(10)); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("Put with a wrong WithSize: err = %v, want ErrInvalidOption", err)
	}
}

func TestPresignWithoutConfiguration(t *testing.T) {
	b := newBucket(t)
	ctx := context.Background()
	if _, err := b.PresignGet(ctx, "k", time.Minute); !errors.Is(err, objex.ErrUnsupported) {
		t.Errorf("PresignGet without WithPresign: err = %v, want ErrUnsupported", err)
	}

	client, _ := newClient(t)
	recorder := httptest.NewRecorder()
	client.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/files/objextest/k", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("Handler without WithPresign = %d, want 404", recorder.Code)
	}
}

func TestPresignPutRejectsMetadataAndBadOptions(t *testing.T) {
	b := newBucket(t, filesystem.WithPresign("https://example.com/files", signingKey))
	ctx := context.Background()
	if _, err := b.PresignPut(ctx, "k", time.Minute, objex.WithMetadata(map[string]string{"a": "b"})); !errors.Is(err, objex.ErrUnsupported) {
		t.Errorf("PresignPut WithMetadata: err = %v, want ErrUnsupported", err)
	}
	if _, err := b.PresignPut(ctx, "k", time.Minute, objex.WithSize(-5)); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("PresignPut invalid size: err = %v, want ErrInvalidOption", err)
	}
}

func TestPresignedURLShape(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	b := newBucket(t,
		filesystem.WithPresign("https://example.com/files/", signingKey),
		filesystem.WithClock(func() time.Time { return now }),
	)
	req, err := b.PresignGet(context.Background(), "dir/a file#1.txt", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.URL, "https://example.com/files/objextest/dir/a%20file%231.txt?") {
		t.Errorf("URL = %q", req.URL)
	}
	if !req.Expires.Equal(now.Add(time.Hour)) {
		t.Errorf("Expires = %v, want %v", req.Expires, now.Add(time.Hour))
	}
}

// presignServer starts a test server for a client configured for presigning
// and returns the client, its bucket, and the server.
func presignServer(t *testing.T, opts ...filesystem.Option) (*filesystem.Client, *filesystem.Bucket, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	client, _ := newClient(t, append([]filesystem.Option{filesystem.WithPresign(server.URL+"/files", signingKey)}, opts...)...)
	server.Config.Handler = client.Handler()
	if err := client.CreateBucket(context.Background(), "objextest"); err != nil {
		t.Fatal(err)
	}
	return client, client.Bucket("objextest"), server
}

func do(t *testing.T, method, rawURL string, header http.Header, body io.Reader) (response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return response{StatusCode: resp.StatusCode, Header: resp.Header, ContentLength: resp.ContentLength}, string(data)
}

func TestHandler(t *testing.T) {
	_, b, server := presignServer(t)
	ctx := context.Background()
	if _, err := b.Put(ctx, "doc.txt", strings.NewReader("0123456789")); err != nil {
		t.Fatal(err)
	}
	get, err := b.PresignGet(ctx, "doc.txt", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	resp, body := do(t, http.MethodGet, get.URL, http.Header{"Range": {"bytes=2-4"}}, nil)
	if resp.StatusCode != http.StatusPartialContent || body != "234" {
		t.Errorf("ranged GET = %d %q, want 206 %q", resp.StatusCode, body, "234")
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || resp.Header.Get("ETag") == "" {
		t.Errorf("GET headers = %v", resp.Header)
	}

	resp, body = do(t, http.MethodHead, get.URL, nil, nil)
	if resp.StatusCode != http.StatusOK || body != "" || resp.ContentLength != 10 {
		t.Errorf("HEAD = %d %q length %d", resp.StatusCode, body, resp.ContentLength)
	}

	resp, _ = do(t, http.MethodDelete, get.URL, nil, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want 405", resp.StatusCode)
	}

	resp, _ = do(t, http.MethodPut, get.URL, nil, strings.NewReader("x"))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT with a GET signature = %d, want 403", resp.StatusCode)
	}

	for _, path := range []string{"/files/objextest/doc.txt", "/other/objextest/doc.txt", "/files/objextest", "/files/BAD/doc.txt"} {
		resp, _ := do(t, http.MethodGet, server.URL+path, nil, nil)
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s without a signature = %d, want 403 or 404", path, resp.StatusCode)
		}
	}

	missing, err := b.PresignGet(ctx, "missing.txt", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodGet, missing.URL, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET a missing object = %d, want 404", resp.StatusCode)
	}
}

func TestHandlerPutContentType(t *testing.T) {
	_, b, _ := presignServer(t)
	ctx := context.Background()

	bound, err := b.PresignPut(ctx, "bound.json", time.Minute, objex.WithContentType("application/json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodPut, bound.URL, http.Header{"Content-Type": {"text/plain"}}, strings.NewReader("{}")); resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT with a different content type = %d, want 403", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodPut, bound.URL, bound.Header, strings.NewReader("{}")); resp.StatusCode != http.StatusOK {
		t.Errorf("PUT with the bound content type = %d, want 200", resp.StatusCode)
	}

	unbound, err := b.PresignPut(ctx, "unbound.bin", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodPut, unbound.URL, http.Header{"Content-Type": {"application/x-anything"}}, strings.NewReader("data")); resp.StatusCode != http.StatusOK {
		t.Errorf("PUT with any content type on an unbound URL = %d, want 200", resp.StatusCode)
	}
}

func TestHandlerPutIfMatch(t *testing.T) {
	_, b, _ := presignServer(t)
	ctx := context.Background()
	original, err := b.Put(ctx, "doc", strings.NewReader("v1"))
	if err != nil {
		t.Fatal(err)
	}

	stale, err := b.PresignPut(ctx, "doc", time.Minute, objex.IfMatch("stale"))
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodPut, stale.URL, nil, strings.NewReader("v2")); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("PUT IfMatch stale = %d, want 412", resp.StatusCode)
	}
	current, err := b.PresignPut(ctx, "doc", time.Minute, objex.IfMatch(original.ETag))
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodPut, current.URL, nil, strings.NewReader("v2")); resp.StatusCode != http.StatusOK {
		t.Errorf("PUT IfMatch current = %d, want 200", resp.StatusCode)
	}
}

func TestHandlerPutErrors(t *testing.T) {
	client, b, server := presignServer(t)
	ctx := context.Background()
	if _, err := b.Put(ctx, "file", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}

	conflict, err := b.PresignPut(ctx, "file/child", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodPut, conflict.URL, nil, strings.NewReader("x")); resp.StatusCode != http.StatusConflict {
		t.Errorf("PUT under an object = %d, want 409", resp.StatusCode)
	}

	server.Config.Handler = http.MaxBytesHandler(client.Handler(), 4)
	large, err := b.PresignPut(ctx, "large", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := do(t, http.MethodPut, large.URL, nil, hiddenLength{strings.NewReader("more than four bytes")}); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT over the size limit = %d, want 413", resp.StatusCode)
	}

	gone, err := client.Bucket("deleted").PresignPut(ctx, "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = client.Handler()
	if resp, _ := do(t, http.MethodPut, gone.URL, nil, strings.NewReader("x")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT into a missing bucket = %d, want 404", resp.StatusCode)
	}
}

// hiddenLength hides the reader's size so the request is sent chunked.
type hiddenLength struct{ r io.Reader }

func (h hiddenLength) Read(p []byte) (int, error) { return h.r.Read(p) }

// response is the part of an HTTP response the tests inspect; the body is
// already read and closed.
type response struct {
	StatusCode    int
	Header        http.Header
	ContentLength int64
}
