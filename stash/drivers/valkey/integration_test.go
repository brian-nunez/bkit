package valkey

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

// prefixSeq gives every conformance subtest its own key prefix.
var prefixSeq atomic.Int64

// TestIntegration runs the conformance suite against the server named by
// STASH_VALKEY_URL. See docker-compose.yml. Each subtest works under its own
// key prefix and deletes it afterwards, so the suite never flushes the
// database.
func TestIntegration(t *testing.T) {
	raw := os.Getenv("STASH_VALKEY_URL")
	if raw == "" {
		t.Skip("set STASH_VALKEY_URL to run integration tests (see docker-compose.yml)")
	}
	stashtest.Run(t, stashtest.Config{
		NewBackend: func(t *testing.T) stash.Backend {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := valkeyDriver{}.Open(context.Background(), u)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			prefix := fmt.Sprintf("stashtest:%d:%d:", os.Getpid(), prefixSeq.Add(1))
			// Run closes the store before this cleanup runs, so the
			// cleanup opens its own connection.
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if c, err := (valkeyDriver{}).Open(ctx, u); err == nil {
					_ = c.DeletePrefix(ctx, prefix)
					_ = c.Close()
				}
			})
			return prefixed{Backend: opened, prefix: prefix}
		},
	})
}

// prefixed confines a Backend to keys under prefix.
type prefixed struct {
	stash.Backend
	prefix string
}

func (p prefixed) Get(ctx context.Context, k string) ([]byte, error) {
	return p.Backend.Get(ctx, p.prefix+k)
}

func (p prefixed) Set(ctx context.Context, k string, v []byte, o stash.SetOptions) error {
	return p.Backend.Set(ctx, p.prefix+k, v, o)
}

func (p prefixed) Delete(ctx context.Context, k string) (bool, error) {
	return p.Backend.Delete(ctx, p.prefix+k)
}

func (p prefixed) Exists(ctx context.Context, k string) (bool, error) {
	return p.Backend.Exists(ctx, p.prefix+k)
}

func (p prefixed) Incr(ctx context.Context, k string, d int64) (int64, error) {
	return p.Backend.Incr(ctx, p.prefix+k, d)
}

func (p prefixed) TTL(ctx context.Context, k string) (time.Duration, error) {
	return p.Backend.TTL(ctx, p.prefix+k)
}

func (p prefixed) Expire(ctx context.Context, k string, ttl time.Duration) error {
	return p.Backend.Expire(ctx, p.prefix+k, ttl)
}

func (p prefixed) DeletePrefix(ctx context.Context, prefix string) error {
	return p.Backend.DeletePrefix(ctx, p.prefix+prefix)
}

func (p prefixed) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for k, err := range p.Backend.Keys(ctx, p.prefix+prefix) {
			if !yield(strings.TrimPrefix(k, p.prefix), err) {
				return
			}
		}
	}
}
