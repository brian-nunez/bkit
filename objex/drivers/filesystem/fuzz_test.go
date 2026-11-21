package filesystem_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/objex"
	"github.com/brian-nunez/bkit/objex/drivers/filesystem"
)

// Fixed objects the containment fuzzer stores beside the fuzzed key so Copy
// and Move have a source and a destination it controls.
const (
	fuzzBucket   = "objextest"
	fixedKey     = "fuzz-fixed"
	copiedKey    = "fuzz-copied"
	movedKey     = "fuzz-moved"
	tempDirName  = ".objex-tmp"
	maxNameBytes = 255
)

// hostileKeys are keys that try to leave the bucket or the root, or that
// stress path handling. Most are invalid; the rest are valid keys that look
// dangerous.
var hostileKeys = []string{
	"../x",
	"a/../../x",
	"../../x",
	"../../../../../../../../tmp/objex-escape",
	"..\\x",
	"a\\..\\..\\x",
	"/x",
	"/etc/passwd",
	"/tmp/objex-escape",
	"C:\\Windows\\x",
	"C:/x",
	"//server/share/x",
	"escape/secret",
	"link -> ../x",
	"a/b/../../../x",
	"x\x00../y",
	"\x00",
	"a/\x00/b",
	".",
	"..",
	"./x",
	"x/.",
	"x/..",
	"...",
	"a/.../b",
	"x/",
	"x//y",
	"/",
	"",
	"CON",
	"nul.txt",
	"COM1/LPT1",
	"aux/prn",
	"%2e%2e/x",
	"..%2fx",
	"a?b#c",
	"a b/c%20d",
	"\u00e9t\u00e9/\u2028/\u0085",
	"\xff\xfe",
	strings.Repeat("a", objex.MaxKeyLength),
	strings.Repeat("a", objex.MaxKeyLength+1),
	strings.Repeat("a", maxNameBytes+1),
	strings.Repeat(strings.Repeat("d", 31)+"/", 31) + strings.Repeat("f", 31),
	strings.Repeat("../", 300) + "x",
	tempDirName + "/x",
	"../" + tempDirName + "/x",
	fixedKey,
	fixedKey + "/child",
	"plain.txt",
}

// FuzzContainment runs every Bucket operation with a fuzzed key, in a fixed
// bucket and in a bucket with a fuzzed name, and checks after each call that
// nothing was read or written outside the root. A sentinel directory beside
// the root must stay untouched, every file under the root must sit inside a
// bucket directory, and invalid keys and bucket names must fail with an objex
// sentinel error without changing anything.
func FuzzContainment(f *testing.F) {
	buckets := []string{fuzzBucket, "..", "../x", "a/b", tempDirName, "ab", "a..b", "192.168.0.1", "Upper", "con", "a.b-c", ""}
	for i, key := range hostileKeys {
		f.Add(key, buckets[i%len(buckets)])
	}
	f.Fuzz(func(t *testing.T, key, bucket string) {
		client, root := newClient(t)
		w := newContainmentWatch(t, root, key, bucket)
		for _, name := range slices.Compact([]string{fuzzBucket, bucket}) {
			exerciseBucket(t, client, w, name, key)
		}
	})
}

// containmentWatch records the state around a client's root before any
// operation runs, and checks that the operations preserve it.
type containmentWatch struct {
	root     string
	parent   string
	siblings []string
	sentinel string
	sealed   map[string]string
	outside  []string
	buckets  map[string]bool
}

func newContainmentWatch(t *testing.T, root, key, bucket string) *containmentWatch {
	t.Helper()
	sentinel := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sentinel, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"secret": "outside the root", "x": "x", "nested/secret": "nested"} {
		if err := os.WriteFile(filepath.Join(sentinel, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	parent := filepath.Dir(root)
	if filepath.Dir(sentinel) != parent {
		t.Fatalf("sentinel %s is not beside the root %s", sentinel, root)
	}
	w := &containmentWatch{
		root:     root,
		parent:   parent,
		siblings: dirNames(t, parent),
		sentinel: sentinel,
		sealed:   snapshotTree(t, sentinel),
		buckets:  map[string]bool{fuzzBucket: true},
	}
	if objex.ValidateBucketName(bucket) == nil {
		w.buckets[bucket] = true
	}

	// The lexical targets of the key and bucket name, resolved the way a
	// driver that joined paths without confinement would. Any that fall
	// outside the root and do not exist yet must never appear.
	candidates := []string{filepath.Join(root, bucket)}
	for _, name := range []string{fuzzBucket, bucket} {
		candidates = append(candidates, filepath.Join(root, name, filepath.FromSlash(key)), filepath.Join(root, name, key))
	}
	for _, candidate := range candidates {
		if isInside(root, candidate) {
			continue
		}
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			w.outside = append(w.outside, candidate)
		}
	}
	return w
}

// verify fails the test if op changed anything outside the root, or left a
// file under the root outside a bucket directory.
func (w *containmentWatch) verify(t *testing.T, op string) {
	t.Helper()
	if got := dirNames(t, w.parent); !slices.Equal(got, w.siblings) {
		t.Fatalf("%s: entries beside the root changed: %q, want %q", op, got, w.siblings)
	}
	if got := snapshotTree(t, w.sentinel); !maps.Equal(got, w.sealed) {
		t.Fatalf("%s: sentinel directory changed: %q, want %q", op, got, w.sealed)
	}
	for _, target := range w.outside {
		if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s: %s exists outside the root (err = %v)", op, target, err)
		}
	}

	err := filepath.WalkDir(w.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == w.root {
			return nil
		}
		rel, err := filepath.Rel(w.root, p)
		if err != nil || !isInside(w.root, p) {
			t.Fatalf("%s: walked %s outside the root %s", op, p, w.root)
		}
		top, _, nested := strings.Cut(filepath.ToSlash(rel), "/")
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			t.Fatalf("%s: symlink %s under the root", op, rel)
		case !d.IsDir() && !d.Type().IsRegular():
			t.Fatalf("%s: special file %s under the root", op, rel)
		case top == tempDirName && nested:
			t.Fatalf("%s: temporary file %s left behind", op, rel)
		case top == tempDirName:
		case !w.buckets[top]:
			t.Fatalf("%s: %s is outside every bucket directory", op, rel)
		case !nested && !d.IsDir():
			t.Fatalf("%s: bucket %s is a file", op, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("%s: walk root: %v", op, err)
	}
}

// exerciseBucket runs every operation with key in the bucket called name,
// verifying containment after each one.
func exerciseBucket(t *testing.T, client *filesystem.Client, w *containmentWatch, name, key string) {
	t.Helper()
	ctx := context.Background()
	b := client.Bucket(name)
	bucketValid := objex.ValidateBucketName(name) == nil
	keyValid := objex.ValidateKey(key) == nil

	// rejected reports whether err must be an invalid-input sentinel, and
	// fails the test unless it is one. A rejected call must change nothing.
	rejected := func(op string, err error, before map[string]string) bool {
		t.Helper()
		var want error
		switch {
		case !bucketValid:
			want = objex.ErrInvalidBucketName
		case !keyValid:
			want = objex.ErrInvalidKey
		default:
			return false
		}
		if !errors.Is(err, want) {
			t.Fatalf("%s(%q, %q): err = %v, want %v", op, name, key, err, want)
		}
		if after := snapshotTree(t, w.root); !maps.Equal(after, before) {
			t.Fatalf("%s(%q, %q) was rejected but changed the root: %q, want %q", op, name, key, after, before)
		}
		return true
	}
	run := func(op string, call func() error) (bool, error) {
		t.Helper()
		before := snapshotTree(t, w.root)
		err := call()
		w.verify(t, op)
		return rejected(op, err, before), err
	}

	err := client.CreateBucket(ctx, name)
	w.verify(t, "CreateBucket")
	switch {
	case !bucketValid && !errors.Is(err, objex.ErrInvalidBucketName):
		t.Fatalf("CreateBucket(%q): err = %v, want ErrInvalidBucketName", name, err)
	case bucketValid && err != nil && !errors.Is(err, objex.ErrBucketExists):
		t.Fatalf("CreateBucket(%q): %v", name, err)
	}
	exists, err := client.BucketExists(ctx, name)
	w.verify(t, "BucketExists")
	if bucketValid && (err != nil || !exists) {
		t.Fatalf("BucketExists(%q) = %v, %v; want true", name, exists, err)
	}

	// content tracks what the fuzzed key and the fixed keys hold, so every
	// successful write can be checked on disk.
	content := map[string]string{}
	write := func(op, dst string, call func() error, data func() string) {
		t.Helper()
		if rej, err := run(op, call); !rej && err == nil {
			content[dst] = data()
			assertStored(t, w.root, name, dst, content[dst])
		}
	}

	write("Put", key, func() error {
		_, err := b.Put(ctx, key, strings.NewReader("payload"))
		return err
	}, func() string { return "payload" })
	stored := content[key] != ""

	if rej, err := run("Stat", func() error {
		info, err := b.Stat(ctx, key)
		if err == nil && (info.Key != key || info.Size != int64(len("payload"))) {
			t.Fatalf("Stat(%q) = %+v", key, info)
		}
		return err
	}); !rej && stored && err != nil {
		t.Fatalf("Stat(%q) after Put: %v", key, err)
	}

	if rej, err := run("Get", func() error {
		obj, err := b.Get(ctx, key)
		if err != nil {
			return err
		}
		defer func() { _ = obj.Close() }()
		data, err := io.ReadAll(obj)
		if err != nil || string(data) != "payload" || obj.Info.Key != key {
			t.Fatalf("Get(%q) = %q (key %q), %v", key, data, obj.Info.Key, err)
		}
		return nil
	}); !rej && stored && err != nil {
		t.Fatalf("Get(%q) after Put: %v", key, err)
	}

	before := snapshotTree(t, w.root)
	listed := false
	for info, err := range b.List(ctx, objex.WithPrefix(key)) {
		if !bucketValid {
			rejected("List", err, before)
			continue
		}
		if err != nil {
			t.Fatalf("List(prefix %q): %v", key, err)
		}
		if !strings.HasPrefix(info.Key, key) || objex.ValidateKey(info.Key) != nil {
			t.Fatalf("List(prefix %q) yielded %q", key, info.Key)
		}
		listed = listed || info.Key == key
	}
	w.verify(t, "List")
	if stored && !listed {
		t.Fatalf("List(prefix %q) left out the stored key", key)
	}

	if bucketValid {
		_, err := b.Put(ctx, fixedKey, strings.NewReader("fixed"))
		switch {
		case err == nil:
			content[fixedKey] = "fixed"
		case !errors.Is(err, objex.ErrUnsupported):
			t.Fatalf("Put(%q): %v", fixedKey, err)
		}
		w.verify(t, "Put fixed")
	}

	write("Copy to key", key, func() error {
		_, err := b.Copy(ctx, key, fixedKey)
		return err
	}, func() string { return content[fixedKey] })
	write("Copy from key", copiedKey, func() error {
		_, err := b.Copy(ctx, copiedKey, key)
		return err
	}, func() string { return content[key] })
	write("Move to key", key, func() error {
		_, err := b.Move(ctx, key, copiedKey)
		return err
	}, func() string { return content[copiedKey] })
	write("Move from key", movedKey, func() error {
		_, err := b.Move(ctx, movedKey, key)
		return err
	}, func() string { return content[key] })

	if rej, err := run("Delete", func() error { return b.Delete(ctx, key) }); !rej && err == nil {
		if _, err := b.Stat(ctx, key); !errors.Is(err, objex.ErrNotFound) {
			t.Fatalf("Stat(%q) after Delete: err = %v, want ErrNotFound", key, err)
		}
	}
	if !bucketValid {
		return
	}
	for _, k := range []string{fixedKey, copiedKey, movedKey} {
		_ = b.Delete(ctx, k)
		w.verify(t, "Delete fixed")
	}
	if err := client.DeleteBucket(ctx, name); err != nil && !errors.Is(err, objex.ErrBucketNotEmpty) {
		t.Fatalf("DeleteBucket(%q): %v", name, err)
	}
	w.verify(t, "DeleteBucket")
}

// assertStored checks, through an os.Root confined to the bucket directory,
// that key is a regular file holding want.
func assertStored(t *testing.T, root, bucket, key, want string) {
	t.Helper()
	r, err := os.OpenRoot(filepath.Join(root, bucket))
	if err != nil {
		t.Fatalf("open bucket %q: %v", bucket, err)
	}
	defer func() { _ = r.Close() }()
	info, err := r.Lstat(filepath.FromSlash(key))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("object %q in bucket %q: info %v, err %v", key, bucket, info, err)
	}
	data, err := r.ReadFile(filepath.FromSlash(key))
	if err != nil || string(data) != want {
		t.Fatalf("object %q in bucket %q holds %q, %v; want %q", key, bucket, data, err, want)
	}
}

// isInside reports whether p is root or lies below it.
func isInside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// dirNames returns the sorted names in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// snapshotTree maps every path under dir to its type and, for regular files
// and symlinks, its contents or target.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			tree[rel] = "symlink:" + target
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			tree[rel] = "file:" + string(data)
		default:
			tree[rel] = d.Type().String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return tree
}

// FuzzPresignRoundTrip uploads data through a presigned PUT URL for a fuzzed
// key and downloads it through a presigned GET URL, then checks that the
// stored object's key is exactly the fuzzed key and that URLs with a changed
// key, signature, or expiry are rejected.
func FuzzPresignRoundTrip(f *testing.F) {
	for _, key := range hostileKeys {
		f.Add(key, []byte("data"))
	}
	f.Add("dir/a file#1.txt", []byte{})
	f.Add("a+b/c;d,e/f=g&h", []byte("\x00\xff"))
	f.Add("%41/%2F/%25", []byte("percent"))
	f.Add("k", bytes.Repeat([]byte("z"), 4096))
	f.Fuzz(func(t *testing.T, key string, data []byte) {
		_, b, _ := presignServer(t)
		ctx := context.Background()

		if objex.ValidateKey(key) != nil {
			if _, err := b.PresignPut(ctx, key, time.Minute); !errors.Is(err, objex.ErrInvalidKey) {
				t.Fatalf("PresignPut(%q): err = %v, want ErrInvalidKey", key, err)
			}
			if _, err := b.PresignGet(ctx, key, time.Minute); !errors.Is(err, objex.ErrInvalidKey) {
				t.Fatalf("PresignGet(%q): err = %v, want ErrInvalidKey", key, err)
			}
			return
		}

		put, err := b.PresignPut(ctx, key, time.Minute)
		if err != nil {
			t.Fatalf("PresignPut(%q): %v", key, err)
		}
		get, err := b.PresignGet(ctx, key, time.Minute)
		if err != nil {
			t.Fatalf("PresignGet(%q): %v", key, err)
		}

		// Tampered PUT URLs must store nothing.
		for name, tampered := range tamperings(t, put.URL) {
			if resp, _ := do(t, http.MethodPut, tampered, put.Header, bytes.NewReader(data)); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
				t.Fatalf("PUT with %s for %q = %d, want 403 or 404", name, key, resp.StatusCode)
			}
		}
		if keys := listKeys(t, b); len(keys) != 0 {
			t.Fatalf("tampered PUT URLs stored %q", keys)
		}

		resp, _ := do(t, http.MethodPut, put.URL, put.Header, bytes.NewReader(data))
		if !storable(key) {
			// A segment longer than the filesystem's name limit is a valid
			// key the filesystem cannot hold.
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("PUT %q with a %d-byte segment succeeded", key, maxNameBytes+1)
			}
			return
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("presigned PUT %q = %d, want 200", key, resp.StatusCode)
		}

		resp, body := do(t, http.MethodGet, get.URL, nil, nil)
		if resp.StatusCode != http.StatusOK || body != string(data) {
			t.Fatalf("presigned GET %q = %d with %d bytes, want 200 with %d bytes", key, resp.StatusCode, len(body), len(data))
		}
		for name, tampered := range tamperings(t, get.URL) {
			if resp, _ := do(t, http.MethodGet, tampered, nil, nil); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
				t.Fatalf("GET with %s for %q = %d, want 403 or 404", name, key, resp.StatusCode)
			}
		}

		info, err := b.Stat(ctx, key)
		if err != nil || info.Key != key || info.Size != int64(len(data)) {
			t.Fatalf("Stat(%q) = %+v, %v", key, info, err)
		}
		if keys := listKeys(t, b); len(keys) != 1 || keys[0] != key {
			t.Fatalf("List = %q, want exactly %q", keys, key)
		}
	})
}

// tamperings returns copies of a presigned URL with its key, signature, or
// expiry changed, keyed by a description of the change.
func tamperings(t *testing.T, rawURL string) map[string]string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse presigned URL %q: %v", rawURL, err)
	}
	query := u.Query()

	key := *u
	key.Path += "x"
	key.RawPath = ""

	// The same signature on a key one directory deeper.
	bucketPath, rest, _ := strings.Cut(strings.TrimPrefix(u.Path, "/files/"), "/")
	deeper := *u
	deeper.Path = "/files/" + bucketPath + "/fuzz/" + rest
	deeper.RawPath = ""

	signature := *u
	sig := []byte(query.Get("X-Objex-Signature"))
	if sig[0] == '0' {
		sig[0] = '1'
	} else {
		sig[0] = '0'
	}
	signature.RawQuery = withParam(query, "X-Objex-Signature", string(sig))

	expires := *u
	when, err := strconv.ParseInt(query.Get("X-Objex-Expires"), 10, 64)
	if err != nil {
		t.Fatalf("presigned URL %q has no expiry: %v", rawURL, err)
	}
	expires.RawQuery = withParam(query, "X-Objex-Expires", strconv.FormatInt(when+1, 10))

	unsigned := *u
	unsigned.RawQuery = withParam(query, "X-Objex-Signature", "")

	return map[string]string{
		"a changed key":       key.String(),
		"a deeper key":        deeper.String(),
		"a changed signature": signature.String(),
		"a changed expiry":    expires.String(),
		"no signature":        unsigned.String(),
	}
}

func withParam(query url.Values, name, value string) string {
	changed := url.Values{}
	for k, v := range query {
		changed[k] = slices.Clone(v)
	}
	changed.Set(name, value)
	return changed.Encode()
}

// storable reports whether every segment of key fits in a file name.
func storable(key string) bool {
	for segment := range strings.SplitSeq(key, "/") {
		if len(segment) > maxNameBytes {
			return false
		}
	}
	return true
}

func listKeys(t *testing.T, b *filesystem.Bucket) []string {
	t.Helper()
	var keys []string
	for info, err := range b.List(context.Background()) {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		keys = append(keys, info.Key)
	}
	return keys
}
