package objextest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/objex"
)

// ManagerConfig describes the driver client under test.
type ManagerConfig struct {
	// Manager is the client under test. Required.
	Manager objex.Manager

	// Bucket returns a handle to the named bucket. Required.
	Bucket func(name string) objex.Bucket

	// NewBucketName returns a valid bucket name that does not exist yet and
	// no other test uses. Required.
	NewBucketName func(t *testing.T) string
}

// RunManager runs the objex.Manager conformance tests. The tests create and
// delete their own buckets.
func RunManager(t *testing.T, cfg ManagerConfig) {
	t.Helper()
	if cfg.Manager == nil || cfg.Bucket == nil || cfg.NewBucketName == nil {
		t.Fatal("objextest: ManagerConfig.Manager, Bucket, and NewBucketName are required")
	}

	t.Run("Lifecycle", func(t *testing.T) { testBucketLifecycle(t, cfg) })
	t.Run("ListBuckets", func(t *testing.T) { testListBuckets(t, cfg) })
	t.Run("InvalidNames", func(t *testing.T) { testInvalidBucketNames(t, cfg) })
}

func testBucketLifecycle(t *testing.T, cfg ManagerConfig) {
	ctx := context.Background()
	m := cfg.Manager
	name := cfg.NewBucketName(t)

	if ok, err := m.BucketExists(ctx, name); err != nil || ok {
		t.Fatalf("BucketExists before CreateBucket = %v, %v; want false", ok, err)
	}
	if err := m.CreateBucket(ctx, name); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	t.Cleanup(func() { _ = m.DeleteBucket(context.Background(), name) })

	if ok, err := m.BucketExists(ctx, name); err != nil || !ok {
		t.Errorf("BucketExists after CreateBucket = %v, %v; want true", ok, err)
	}
	if err := m.CreateBucket(ctx, name); !errors.Is(err, objex.ErrBucketExists) {
		t.Errorf("CreateBucket twice: err = %v, want ErrBucketExists", err)
	}

	b := cfg.Bucket(name)
	if _, err := b.Put(ctx, "nested/object", strings.NewReader("x")); err != nil {
		t.Fatalf("Put into the new bucket: %v", err)
	}
	if err := m.DeleteBucket(ctx, name); !errors.Is(err, objex.ErrBucketNotEmpty) {
		t.Errorf("DeleteBucket with an object: err = %v, want ErrBucketNotEmpty", err)
	}
	if err := b.Delete(ctx, "nested/object"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := m.DeleteBucket(ctx, name); err != nil {
		t.Fatalf("DeleteBucket when empty: %v", err)
	}

	if ok, err := m.BucketExists(ctx, name); err != nil || ok {
		t.Errorf("BucketExists after DeleteBucket = %v, %v; want false", ok, err)
	}
	if err := m.DeleteBucket(ctx, name); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("DeleteBucket twice: err = %v, want ErrBucketNotFound", err)
	}
	if _, err := b.Put(ctx, "object", strings.NewReader("x")); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Put into a deleted bucket: err = %v, want ErrBucketNotFound", err)
	}
}

func testListBuckets(t *testing.T, cfg ManagerConfig) {
	ctx := context.Background()
	m := cfg.Manager
	names := []string{cfg.NewBucketName(t), cfg.NewBucketName(t)}
	for _, name := range names {
		if err := m.CreateBucket(ctx, name); err != nil {
			t.Fatalf("CreateBucket(%q): %v", name, err)
		}
		t.Cleanup(func() { _ = m.DeleteBucket(context.Background(), name) })
	}

	buckets, err := m.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	var listed []string
	for _, bucket := range buckets {
		listed = append(listed, bucket.Name)
	}
	if !slices.IsSorted(listed) {
		t.Errorf("ListBuckets = %q, want sorted by name", listed)
	}
	for _, name := range names {
		if !slices.Contains(listed, name) {
			t.Errorf("ListBuckets = %q, want it to include %q", listed, name)
		}
	}
}

func testInvalidBucketNames(t *testing.T, cfg ManagerConfig) {
	ctx := context.Background()
	for _, name := range []string{"", "ab", "UPPER", "bad_name", "-leading", "trailing-", "a..b", "192.168.1.1"} {
		if err := cfg.Manager.CreateBucket(ctx, name); !errors.Is(err, objex.ErrInvalidBucketName) {
			t.Errorf("CreateBucket(%q): err = %v, want ErrInvalidBucketName", name, err)
		}
	}
}
