package objextest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/objex"
)

// PresignerConfig describes the driver under test.
type PresignerConfig struct {
	// NewBucket returns a handle to an existing, empty bucket that no other
	// test uses and that implements objex.Presigner. Required.
	NewBucket func(t *testing.T) objex.Bucket

	// Client performs the presigned requests. Nil uses http.DefaultClient.
	Client *http.Client

	// ContentType reports whether the driver stores the content type bound
	// into a presigned upload.
	ContentType bool
}

// RunPresigner runs the objex.Presigner conformance tests by sending the
// presigned requests over HTTP.
func RunPresigner(t *testing.T, cfg PresignerConfig) {
	t.Helper()
	if cfg.NewBucket == nil {
		t.Fatal("objextest: PresignerConfig.NewBucket is required")
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}

	t.Run("Get", func(t *testing.T) { testPresignGet(t, cfg) })
	t.Run("Put", func(t *testing.T) { testPresignPut(t, cfg) })
	t.Run("PutIfNotExists", func(t *testing.T) { testPresignPutIfNotExists(t, cfg) })
	t.Run("Tampered", func(t *testing.T) { testPresignTampered(t, cfg) })
	t.Run("Expired", func(t *testing.T) { testPresignExpired(t, cfg) })
	t.Run("InvalidInput", func(t *testing.T) { testPresignInvalidInput(t, cfg) })
}

func presigner(t *testing.T, b objex.Bucket) objex.Presigner {
	t.Helper()
	p, ok := b.(objex.Presigner)
	if !ok {
		t.Fatalf("bucket %T does not implement objex.Presigner", b)
	}
	return p
}

func testPresignGet(t *testing.T, cfg PresignerConfig) {
	b := cfg.NewBucket(t)
	mustPut(t, b, "shared/file.txt", "presigned download")

	req, err := presigner(t, b).PresignGet(context.Background(), "shared/file.txt", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Errorf("PresignGet Method = %q, want GET", req.Method)
	}
	if time.Until(req.Expires) <= 0 || time.Until(req.Expires) > 2*time.Minute {
		t.Errorf("PresignGet Expires = %v, want about one minute from now", req.Expires)
	}

	status, body := send(t, cfg.Client, req, nil)
	if status != http.StatusOK || body != "presigned download" {
		t.Errorf("presigned GET = %d %q, want 200 %q", status, body, "presigned download")
	}
}

func testPresignPut(t *testing.T, cfg PresignerConfig) {
	b := cfg.NewBucket(t)
	req, err := presigner(t, b).PresignPut(context.Background(), "uploads/new.json", time.Minute, objex.WithContentType("application/json"))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if req.Method != http.MethodPut {
		t.Errorf("PresignPut Method = %q, want PUT", req.Method)
	}

	status, body := send(t, cfg.Client, req, strings.NewReader(`{"uploaded":true}`))
	if status < 200 || status > 299 {
		t.Fatalf("presigned PUT = %d %q, want 2xx", status, body)
	}
	if got := mustGet(t, b, "uploads/new.json"); got != `{"uploaded":true}` {
		t.Errorf("Get after presigned PUT = %q", got)
	}
	if cfg.ContentType {
		stat, err := b.Stat(context.Background(), "uploads/new.json")
		if err != nil || stat.ContentType != "application/json" {
			t.Errorf("Stat ContentType after presigned PUT = %q, %v; want application/json", stat.ContentType, err)
		}
	}
}

func testPresignPutIfNotExists(t *testing.T, cfg PresignerConfig) {
	b := cfg.NewBucket(t)
	mustPut(t, b, "taken", "original")

	req, err := presigner(t, b).PresignPut(context.Background(), "taken", time.Minute, objex.IfNotExists())
	if err != nil {
		t.Fatalf("PresignPut IfNotExists: %v", err)
	}
	if status, body := send(t, cfg.Client, req, strings.NewReader("replacement")); status != http.StatusPreconditionFailed {
		t.Errorf("presigned PUT IfNotExists on an existing key = %d %q, want 412", status, body)
	}
	if got := mustGet(t, b, "taken"); got != "original" {
		t.Errorf("Get after rejected presigned PUT = %q, want %q", got, "original")
	}
}

func testPresignTampered(t *testing.T, cfg PresignerConfig) {
	b := cfg.NewBucket(t)
	mustPut(t, b, "secret.txt", "secret")
	mustPut(t, b, "other.txt", "other")

	req, err := presigner(t, b).PresignGet(context.Background(), "secret.txt", time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}
	u.Path = strings.TrimSuffix(u.Path, "secret.txt") + "other.txt"
	u.RawPath = ""
	req.URL = u.String()

	if status, _ := send(t, cfg.Client, req, nil); status != http.StatusForbidden {
		t.Errorf("presigned GET for a different key = %d, want 403", status)
	}
}

func testPresignExpired(t *testing.T, cfg PresignerConfig) {
	b := cfg.NewBucket(t)
	mustPut(t, b, "brief.txt", "brief")

	req, err := presigner(t, b).PresignGet(context.Background(), "brief.txt", time.Second)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	time.Sleep(2100 * time.Millisecond)
	if status, _ := send(t, cfg.Client, req, nil); status != http.StatusForbidden {
		t.Errorf("expired presigned GET = %d, want 403", status)
	}
}

func testPresignInvalidInput(t *testing.T, cfg PresignerConfig) {
	b := cfg.NewBucket(t)
	p := presigner(t, b)
	ctx := context.Background()

	if _, err := p.PresignGet(ctx, "/invalid", time.Minute); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("PresignGet invalid key: err = %v, want ErrInvalidKey", err)
	}
	if _, err := p.PresignPut(ctx, "a//b", time.Minute); !errors.Is(err, objex.ErrInvalidKey) {
		t.Errorf("PresignPut invalid key: err = %v, want ErrInvalidKey", err)
	}
	if _, err := p.PresignGet(ctx, "valid", 0); !errors.Is(err, objex.ErrInvalidOption) {
		t.Errorf("PresignGet zero ttl: err = %v, want ErrInvalidOption", err)
	}
}

func send(t *testing.T, client *http.Client, req objex.PresignedRequest, body io.Reader) (int, string) {
	t.Helper()
	httpReq, err := http.NewRequestWithContext(context.Background(), req.Method, req.URL, body)
	if err != nil {
		t.Fatalf("build presigned request: %v", err)
	}
	for name, values := range req.Header {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("send presigned request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read presigned response: %v", err)
	}
	return resp.StatusCode, string(data)
}
