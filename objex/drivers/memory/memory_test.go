package memory_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/objex"
	"github.com/brian-nunez/bkit/objex/drivers/memory"
	"github.com/brian-nunez/bkit/objex/objextest"
)

func newBucket(t *testing.T) objex.Bucket {
	t.Helper()
	client := memory.New()
	if err := client.CreateBucket(context.Background(), "objextest"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	return client.Bucket("objextest")
}

func TestConformance(t *testing.T) {
	objextest.Run(t, objextest.Config{
		NewBucket:   newBucket,
		Metadata:    true,
		ContentType: true,
	})
}

func TestManagerConformance(t *testing.T) {
	client := memory.New()
	var counter atomic.Int64
	objextest.RunManager(t, objextest.ManagerConfig{
		Manager: client,
		Bucket:  func(name string) objex.Bucket { return client.Bucket(name) },
		NewBucketName: func(*testing.T) string {
			return fmt.Sprintf("objextest-%d", counter.Add(1))
		},
	})
}

func TestWithClock(t *testing.T) {
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))
	client := memory.New(memory.WithClock(func() time.Time { return fixed }), nil)
	ctx := context.Background()
	if err := client.CreateBucket(ctx, "clock"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	info, err := client.Bucket("clock").Put(ctx, "k", strings.NewReader("v"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !info.LastModified.Equal(fixed) || info.LastModified.Location() != time.UTC {
		t.Errorf("LastModified = %v, want %v in UTC", info.LastModified, fixed)
	}

	buckets, err := client.ListBuckets(ctx)
	if err != nil || len(buckets) != 1 || !buckets[0].CreatedAt.Equal(fixed) {
		t.Errorf("ListBuckets = %+v, %v", buckets, err)
	}
}

func TestBucketName(t *testing.T) {
	if got := memory.New().Bucket("named").Name(); got != "named" {
		t.Errorf("Name() = %q, want %q", got, "named")
	}
}

func TestMissingBucket(t *testing.T) {
	ctx := context.Background()
	b := memory.New().Bucket("absent")

	if _, err := b.Stat(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Stat: err = %v, want ErrBucketNotFound", err)
	}
	if _, err := b.Get(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Get: err = %v, want ErrBucketNotFound", err)
	}
	if err := b.Delete(ctx, "k"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Delete: err = %v, want ErrBucketNotFound", err)
	}
	if _, err := b.Copy(ctx, "a", "b"); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Copy: err = %v, want ErrBucketNotFound", err)
	}
	if err := b.Ping(ctx); !errors.Is(err, objex.ErrBucketNotFound) {
		t.Errorf("Ping: err = %v, want ErrBucketNotFound", err)
	}
	for _, err := range b.List(ctx) {
		if !errors.Is(err, objex.ErrBucketNotFound) {
			t.Errorf("List: err = %v, want ErrBucketNotFound", err)
		}
	}
}

func TestPutSizeMismatch(t *testing.T) {
	b := newBucket(t)
	_, err := b.Put(context.Background(), "k", strings.NewReader("four"), objex.WithSize(10))
	if !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("Put with a wrong WithSize: err = %v, want ErrInvalidOption", err)
	}
}

func TestMoveOntoItself(t *testing.T) {
	b := newBucket(t).(*memory.Bucket)
	ctx := context.Background()
	if _, err := b.Put(ctx, "same", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info, err := b.Move(ctx, "same", "same"); err != nil || info.Key != "same" {
		t.Errorf("Move onto itself = %+v, %v", info, err)
	}
	if _, err := b.Move(ctx, "same", "absent"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Move missing: err = %v, want ErrNotFound", err)
	}
	if _, err := b.Move(ctx, "/bad", "same"); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("Move invalid dst: err = %v, want ErrInvalidKey", err)
	}
}

func TestReturnedMetadataIsACopy(t *testing.T) {
	b := newBucket(t)
	ctx := context.Background()
	info, err := b.Put(ctx, "k", strings.NewReader("v"), objex.WithMetadata(map[string]string{"a": "1"}))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	info.Metadata["a"] = "changed"
	if stat, err := b.Stat(ctx, "k"); err != nil || stat.Metadata["a"] != "1" {
		t.Errorf("Stat Metadata = %v, %v; stored metadata was modified through a returned map", stat.Metadata, err)
	}
}

func TestCanceledContext(t *testing.T) {
	client := memory.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := client.CreateBucket(ctx, "bucket"); !errors.Is(err, context.Canceled) {
		t.Errorf("CreateBucket: err = %v", err)
	}
	if err := client.DeleteBucket(ctx, "bucket"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteBucket: err = %v", err)
	}
	if _, err := client.ListBuckets(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListBuckets: err = %v", err)
	}
	if _, err := client.BucketExists(ctx, "bucket"); !errors.Is(err, context.Canceled) {
		t.Errorf("BucketExists: err = %v", err)
	}
	if err := client.Bucket("bucket").Ping(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Ping: err = %v", err)
	}
	if _, err := client.Bucket("bucket").Move(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Errorf("Move: err = %v", err)
	}
}

func TestInvalidBucketNames(t *testing.T) {
	ctx := context.Background()
	client := memory.New()
	if err := client.DeleteBucket(ctx, "BAD"); !errors.Is(err, objex.ErrInvalidBucketName) {
		t.Errorf("DeleteBucket: err = %v", err)
	}
	if _, err := client.BucketExists(ctx, "BAD"); !errors.Is(err, objex.ErrInvalidBucketName) {
		t.Errorf("BucketExists: err = %v", err)
	}
}

// cancelingReader cancels its context after the first read, so the next
// read observes the cancellation.
type cancelingReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("read after cancel")
	}
	r.read = true
	r.cancel()
	return copy(p, "partial"), nil
}

func TestPutCanceledWhileReading(t *testing.T) {
	b := newBucket(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := b.Put(ctx, "k", &cancelingReader{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Put: err = %v, want context.Canceled", err)
	}
	if ok, err := objex.Exists(context.Background(), b, "k"); err != nil || ok {
		t.Errorf("canceled Put stored an object: %v, %v", ok, err)
	}
}

func TestListCanceledDuringIteration(t *testing.T) {
	b := newBucket(t)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := b.Put(context.Background(), key, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var keys []string
	var lastErr error
	for info, err := range b.List(ctx) {
		if err != nil {
			lastErr = err
			break
		}
		keys = append(keys, info.Key)
		cancel()
	}
	if len(keys) != 1 || !errors.Is(lastErr, context.Canceled) {
		t.Errorf("List after cancel = %q, %v; want one key then context.Canceled", keys, lastErr)
	}
}

func TestCopyInvalidOption(t *testing.T) {
	b := newBucket(t)
	if _, err := b.Put(context.Background(), "src", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	_, err := b.Copy(context.Background(), "dst", "src", objex.WithMetadata(map[string]string{"bad key": "v"}))
	if !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("Copy with invalid metadata: err = %v, want ErrInvalidOption", err)
	}
}

func TestGetRangeHugeLength(t *testing.T) {
	b := newBucket(t)
	ctx := context.Background()
	if _, err := b.Put(ctx, "k", strings.NewReader("abcdef")); err != nil {
		t.Fatal(err)
	}
	obj, err := b.Get(ctx, "k", objex.WithRange(1, math.MaxInt64))
	if err != nil {
		t.Fatalf("Get with a huge range length: %v", err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil || string(data) != "bcdef" {
		t.Errorf("Get with a huge range length = %q, %v; want %q", data, err, "bcdef")
	}
}

func TestMoveMissingOntoItself(t *testing.T) {
	b := newBucket(t).(*memory.Bucket)
	if _, err := b.Move(context.Background(), "absent", "absent"); !errors.Is(err, objex.ErrNotFound) {
		t.Errorf("Move a missing key onto itself: err = %v, want ErrNotFound", err)
	}
}
