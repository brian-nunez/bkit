package s3_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/objex"
	"github.com/brian-nunez/bkit/objex/drivers/s3"
	"github.com/brian-nunez/bkit/objex/objextest"
)

// The integration tests run against a real S3-compatible service when
// OBJEX_S3_ENDPOINT is set; docker-compose.yml in this directory starts one.
// OBJEX_S3_ACCESS_KEY, OBJEX_S3_SECRET_KEY, and OBJEX_S3_REGION default to
// the compose file's values.
func integrationClient(t *testing.T) *s3.Client {
	t.Helper()
	endpoint := os.Getenv("OBJEX_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set OBJEX_S3_ENDPOINT to run integration tests (see docker-compose.yml)")
	}
	client, err := s3.New(context.Background(),
		s3.WithEndpoint(endpoint),
		s3.WithPathStyle(),
		s3.WithRegion(envOr("OBJEX_S3_REGION", "us-east-1")),
		s3.WithCredentials(envOr("OBJEX_S3_ACCESS_KEY", "objextest"), envOr("OBJEX_S3_SECRET_KEY", "objextest-secret"), ""),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func uniqueBucketName(t *testing.T) string {
	t.Helper()
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	return "objextest-" + hex.EncodeToString(suffix[:])
}

// newIntegrationBucket creates a fresh bucket and removes it, with every
// object in it, when the test ends.
func newIntegrationBucket(t *testing.T) objex.Bucket {
	t.Helper()
	client := integrationClient(t)
	name := uniqueBucketName(t)
	ctx := context.Background()
	if err := client.CreateBucket(ctx, name); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	b := client.Bucket(name)
	t.Cleanup(func() {
		ctx := context.Background()
		for info, err := range b.List(ctx) {
			if err != nil {
				t.Logf("cleanup list: %v", err)
				break
			}
			_ = b.Delete(ctx, info.Key)
		}
		if err := client.DeleteBucket(ctx, name); err != nil {
			t.Logf("cleanup delete bucket %s: %v", name, err)
		}
	})
	return b
}

func TestIntegrationConformance(t *testing.T) {
	integrationClient(t)
	objextest.Run(t, objextest.Config{
		NewBucket:   newIntegrationBucket,
		Metadata:    true,
		ContentType: true,
	})
}

func TestIntegrationManagerConformance(t *testing.T) {
	client := integrationClient(t)
	objextest.RunManager(t, objextest.ManagerConfig{
		Manager:       client,
		Bucket:        func(name string) objex.Bucket { return client.Bucket(name) },
		NewBucketName: uniqueBucketName,
	})
}

func TestIntegrationPresignerConformance(t *testing.T) {
	integrationClient(t)
	objextest.RunPresigner(t, objextest.PresignerConfig{
		NewBucket:   newIntegrationBucket,
		ContentType: true,
	})
}

func TestIntegrationMultipart(t *testing.T) {
	integrationClient(t)
	endpoint := os.Getenv("OBJEX_S3_ENDPOINT")
	small, err := s3.New(context.Background(),
		s3.WithEndpoint(endpoint),
		s3.WithPathStyle(),
		s3.WithRegion(envOr("OBJEX_S3_REGION", "us-east-1")),
		s3.WithCredentials(envOr("OBJEX_S3_ACCESS_KEY", "objextest"), envOr("OBJEX_S3_SECRET_KEY", "objextest-secret"), ""),
		s3.WithPartSize(5<<20),
		s3.WithUploadConcurrency(2),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	name := newIntegrationBucket(t).(*s3.Bucket).Name()
	b := small.Bucket(name)

	data := bytes.Repeat([]byte("multipart-"), (12<<20)/10+7) // three parts, the last partial
	info, err := b.Put(ctx, "large.bin", hidden{bytes.NewReader(data)}, objex.WithContentType("application/x-test"), objex.WithMetadata(map[string]string{"kind": "multipart"}))
	if err != nil {
		t.Fatalf("multipart Put: %v", err)
	}
	if info.Size != int64(len(data)) || !strings.HasSuffix(info.ETag, "-3") {
		t.Errorf("multipart Put info = Size %d ETag %q, want Size %d and a 3-part ETag", info.Size, info.ETag, len(data))
	}
	stat, err := b.Stat(ctx, "large.bin")
	if err != nil || stat.Size != int64(len(data)) || stat.ContentType != "application/x-test" || stat.Metadata["kind"] != "multipart" {
		t.Errorf("Stat after multipart Put = %+v, %v", stat, err)
	}
	obj, err := b.Get(ctx, "large.bin", objex.WithRange(int64(len(data))-10, -1))
	if err != nil {
		t.Fatalf("Get range: %v", err)
	}
	tail, _ := io.ReadAll(obj)
	_ = obj.Close()
	if string(tail) != string(data[len(data)-10:]) {
		t.Errorf("tail of multipart object = %q", tail)
	}

	if _, err := b.Put(ctx, "large.bin", hidden{bytes.NewReader(data)}, objex.IfNotExists()); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("multipart Put IfNotExists on an existing key: err = %v, want ErrPreconditionFailed", err)
	}
	if _, err := b.Put(ctx, "large.bin", hidden{bytes.NewReader(data)}, objex.IfMatch("stale")); !errors.Is(err, objex.ErrPreconditionFailed) {
		t.Errorf("multipart Put IfMatch stale: err = %v, want ErrPreconditionFailed", err)
	}
	if _, err := b.Put(ctx, "large.bin", hidden{bytes.NewReader(data)}, objex.IfMatch(info.ETag)); err != nil {
		t.Errorf("multipart Put IfMatch current: %v", err)
	}
	if _, err := b.Put(ctx, "fresh.bin", hidden{bytes.NewReader(data)}, objex.IfNotExists()); err != nil {
		t.Errorf("multipart Put IfNotExists on a new key: %v", err)
	}
	if _, err := b.Put(ctx, "mismatch.bin", hidden{bytes.NewReader(data)}, objex.WithSize(5)); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("multipart Put with a wrong WithSize: err = %v, want ErrInvalidOption", err)
	}
	if ok, err := objex.Exists(ctx, b, "mismatch.bin"); err != nil || ok {
		t.Errorf("rejected multipart Put left an object: %v, %v", ok, err)
	}

	exact := bytes.Repeat([]byte("x"), 5<<20)
	if info, err := b.Put(ctx, "exact.bin", hidden{bytes.NewReader(exact)}); err != nil || strings.Contains(info.ETag, "-") {
		t.Errorf("Put of exactly one part = %+v, %v; want a single-request upload", info, err)
	}
}

// hidden hides every method but Read so the driver cannot learn the size.
type hidden struct{ r io.Reader }

func (h hidden) Read(p []byte) (int, error) { return h.r.Read(p) }
