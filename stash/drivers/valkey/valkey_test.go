package valkey

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

// openBackend connects the real client to srv.
func openBackend(t *testing.T, srv *fakeServer) *backend {
	t.Helper()
	u, _ := url.Parse("valkey://" + srv.addr() + "/0")
	b, err := valkeyDriver{}.Open(context.Background(), u)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b.(*backend)
}

func TestConformance(t *testing.T) {
	stashtest.Run(t, stashtest.Config{
		NewBackend: func(t *testing.T) stash.Backend { return openBackend(t, startServer(t)) },
		TTL:        50 * time.Millisecond,
	})
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	srv := startServer(t)
	s, err := stash.Open(ctx, "valkey://app:s3cret@"+srv.addr()+"/2", Driver())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Set(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	hello := srv.commands("HELLO")
	if len(hello) == 0 || !slices.Contains(hello[0], "s3cret") {
		t.Errorf("HELLO = %q, want the credentials", hello)
	}
	if sel := srv.commands("SELECT"); len(sel) == 0 || sel[0][1] != "2" {
		t.Errorf("SELECT = %q, want database 2", sel)
	}
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	srv := startServer(t)
	s, err := New(ctx, Config{Addr: srv.addr(), Username: "u", Password: "p", DB: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Set(ctx, "k", []byte("v"), stash.WithTTL(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sel := srv.commands("SELECT"); len(sel) == 0 || sel[0][1] != "3" {
		t.Errorf("SELECT = %q, want database 3", sel)
	}
	set := srv.commands("SET")
	if len(set) == 0 || !slices.Equal(set[len(set)-1][3:], []string{"PX", "3600000"}) {
		t.Errorf("SET = %q, want a PX expiry", set)
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	_, err := stash.Open(ctx, "valkey://app:s3cret@cache:6379/not-a-number", Driver())
	if !errors.Is(err, stash.ErrInvalidURL) || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad database number: err = %v", err)
	}

	_, err = New(ctx, Config{Addr: "127.0.0.1:1", Password: "s3cret"})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("unreachable server: err = %v", err)
	}

	// The server accepts the connection but rejects PING.
	srv := startServer(t)
	srv.fail["PING"] = "NOAUTH Authentication required."
	if _, err := New(ctx, Config{Addr: srv.addr()}); err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Errorf("failed PING: err = %v", err)
	}
}

func TestDefaultAddr(t *testing.T) {
	// Nothing is expected to listen on the default address in tests; the
	// call must still try it and fail cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s, err := New(ctx, Config{}); err == nil {
		_ = s.Close()
		t.Skip("a server is listening on localhost:6379")
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
	srv := startServer(t)
	b := openBackend(t, srv)
	for _, k := range []string{"pa", "pb", "pc", "pd", "pe"} {
		if err := b.Set(ctx, k, []byte("v"), stash.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	srv.fail["INCRBY"] = "ERR something else"
	if _, err := b.Incr(ctx, "n", 1); err == nil || errors.Is(err, stash.ErrNotInteger) {
		t.Errorf("Incr err = %v, want the server error unchanged", err)
	}
	srv.fail["PERSIST"] = "ERR boom"
	if err := b.Expire(ctx, "pa", 0); err == nil {
		t.Error("Expire(0) succeeded despite PERSIST failing")
	}
	delete(srv.fail, "PERSIST")
	if err := b.Expire(ctx, "missing", 0); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Expire(0) on a missing key err = %v, want ErrNotFound", err)
	}
	srv.fail["EXISTS"] = "ERR boom"
	if err := b.Expire(ctx, "pa", 0); err == nil {
		t.Error("Expire(0) succeeded despite EXISTS failing")
	}
	delete(srv.fail, "EXISTS")
	srv.fail["PTTL"] = "ERR boom"
	if _, err := b.TTL(ctx, "pa"); err == nil {
		t.Error("TTL succeeded despite PTTL failing")
	}
	srv.fail["DEL"] = "ERR boom"
	if err := b.DeletePrefix(ctx, "p"); err == nil {
		t.Error("DeletePrefix succeeded despite DEL failing")
	}
	delete(srv.fail, "DEL")
	srv.fail["SCAN"] = "ERR boom"
	if err := b.DeletePrefix(ctx, "p"); err == nil {
		t.Error("DeletePrefix succeeded despite SCAN failing")
	}
	for _, err := range b.Keys(ctx, "p") {
		if err == nil {
			t.Error("Keys succeeded despite SCAN failing")
		}
	}
}

func TestPaging(t *testing.T) {
	ctx := context.Background()
	srv := startServer(t)
	b := openBackend(t, srv)
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
	if left := len(srv.data); left != 1 {
		t.Errorf("%d keys left after DeletePrefix across pages, want 1", left)
	}
	if err := b.DeletePrefix(ctx, "none"); err != nil {
		t.Errorf("DeletePrefix with no matches: %v", err)
	}
}
