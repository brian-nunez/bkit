package redis

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

func TestConformance(t *testing.T) {
	stashtest.Run(t, stashtest.Config{
		NewBackend: func(*testing.T) stash.Backend { return &backend{c: newFakeClient()} },
		TTL:        50 * time.Millisecond,
	})
}

// useFake makes newClient return f and records the options it was given.
func useFake(t *testing.T, f *fakeClient) **goredis.Options {
	t.Helper()
	var got *goredis.Options
	orig := newClient
	newClient = func(opts *goredis.Options) client { got = opts; return f }
	t.Cleanup(func() { newClient = orig })
	return &got
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	got := useFake(t, newFakeClient())
	s, err := stash.Open(ctx, "redis://app:s3cret@cache:6380/2", Driver())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o := *got
	if o.Addr != "cache:6380" || o.Username != "app" || o.Password != "s3cret" || o.DB != 2 || o.TLSConfig != nil {
		t.Errorf("options = %+v", o)
	}
	if _, err := stash.Open(ctx, "rediss://cache:6380/0", Driver()); err != nil {
		t.Fatal(err)
	}
	if (*got).TLSConfig == nil {
		t.Error("rediss:// did not enable TLS")
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	_, err := stash.Open(ctx, "redis://app:s3cret@cache:6379/not-a-number", Driver())
	if !errors.Is(err, stash.ErrInvalidURL) || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad database number: err = %v", err)
	}

	// The real client, against a port nothing listens on.
	_, err = stash.Open(ctx, "redis://app:s3cret@127.0.0.1:1/0?dial_timeout=1s", Driver())
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("unreachable server: err = %v", err)
	}

	f := newFakeClient()
	f.fail["ping"] = errors.New("NOAUTH")
	useFake(t, f)
	if _, err := New(ctx, Config{}); err == nil || !f.closed {
		t.Errorf("failed ping: err = %v, client closed = %v", err, f.closed)
	}
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	got := useFake(t, newFakeClient())
	s, err := New(ctx, Config{Addr: "cache:6379", Username: "u", Password: "p", DB: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if o := *got; o.Addr != "cache:6379" || o.Username != "u" || o.Password != "p" || o.DB != 3 {
		t.Errorf("options = %+v", o)
	}
	if err := s.Set(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
}

func TestGlobPrefix(t *testing.T) {
	tests := map[string]string{
		"":         "*",
		"user:":    "user:*",
		`a*b?c[d]`: `a\*b\?c\[d\]*`,
		`back\`:    `back\\*`,
		"é%_":      "é%_*",
	}
	for in, want := range tests {
		if got := globPrefix(in); got != want {
			t.Errorf("globPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection reset")
	f := newFakeClient()
	b := &backend{c: f}
	for i := range 7 {
		if err := b.Set(ctx, "p"+string(rune('a'+i)), []byte("v"), stash.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	f.fail["incrby"] = boom
	if _, err := b.Incr(ctx, "n", 1); !errors.Is(err, boom) || errors.Is(err, stash.ErrNotInteger) {
		t.Errorf("Incr err = %v, want the client error unchanged", err)
	}

	f.fail["persist"] = boom
	if err := b.Expire(ctx, "pa", 0); !errors.Is(err, boom) {
		t.Errorf("Expire(0) err = %v", err)
	}
	f.fail["persist"] = nil
	if err := b.Expire(ctx, "missing", 0); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Expire(0) on a missing key err = %v, want ErrNotFound", err)
	}
	f.fail["exists"] = boom
	if err := b.Expire(ctx, "pa", 0); !errors.Is(err, boom) {
		t.Errorf("Expire(0) existence check err = %v", err)
	}
	f.fail["exists"] = nil

	f.fail["del"] = boom
	if err := b.DeletePrefix(ctx, "p"); !errors.Is(err, boom) {
		t.Errorf("DeletePrefix err = %v", err)
	}
	f.fail["del"] = nil
	f.fail["scan"] = boom
	if err := b.DeletePrefix(ctx, "p"); !errors.Is(err, boom) {
		t.Errorf("DeletePrefix scan err = %v", err)
	}
	for _, err := range b.Keys(ctx, "p") {
		if !errors.Is(err, boom) {
			t.Errorf("Keys err = %v", err)
		}
	}
	f.fail["scan"] = nil
	f.fail["pttl"] = boom
	if _, err := b.TTL(ctx, "pa"); !errors.Is(err, boom) {
		t.Errorf("TTL err = %v", err)
	}
}

func TestPaging(t *testing.T) {
	ctx := context.Background()
	f := newFakeClient()
	b := &backend{c: f}
	want := []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "q"}
	for _, k := range want {
		if err := b.Set(ctx, k, nil, stash.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for k, err := range b.Keys(ctx, "p") {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, k)
	}
	if !slices.Equal(got, want[:7]) {
		t.Errorf("Keys across pages = %q", got)
	}
	n := 0
	for _, err := range b.Keys(ctx, "") {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == 4 {
			break
		}
	}
	if n != 4 {
		t.Errorf("break on the second page stopped at %d", n)
	}
	if err := b.DeletePrefix(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if left := len(f.data); left != 1 {
		t.Errorf("%d keys left after DeletePrefix across pages, want 1", left)
	}
	if err := b.DeletePrefix(ctx, "none"); err != nil {
		t.Errorf("DeletePrefix with no matches: %v", err)
	}
}
