package stash_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

func TestConformance(t *testing.T) {
	stashtest.Run(t, stashtest.Config{
		NewBackend: func(*testing.T) stash.Backend { return newFake() },
		TTL:        50 * time.Millisecond,
	})
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	other := &fakeDriver{backend: newFake()}
	d := &fakeDriver{backend: newFake()}
	s, err := stash.Open(ctx, "FAKE://host/0", stash.WithDriver(d), stash.WithDriver(other))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if d.opened == nil || d.opened.Host != "host" || other.opened != nil {
		t.Errorf("first matching driver not used: %v, %v", d.opened, other.opened)
	}
}

func TestOpenErrors(t *testing.T) {
	ctx := context.Background()
	failing := &fakeDriver{openErr: errors.New("refused")}
	tests := []struct {
		name string
		url  string
		opts []stash.Option
		want error
	}{
		{"unparsable", "fake://user:s3cret@host/%zz", []stash.Option{stash.WithDriver(&fakeDriver{})}, stash.ErrInvalidURL},
		{"no scheme", "localhost:6379", nil, stash.ErrUnknownDriver},
		{"empty", "", nil, stash.ErrInvalidURL},
		{"unknown scheme", "other://user:s3cret@host", []stash.Option{stash.WithDriver(&fakeDriver{})}, stash.ErrUnknownDriver},
		{"driver error", "fake://user:s3cret@host", []stash.Option{stash.WithDriver(failing)}, failing.openErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := stash.Open(ctx, tt.url, tt.opts...)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("error leaks credentials: %v", err)
			}
		})
	}
}

func TestInvalidKeys(t *testing.T) {
	ctx := context.Background()
	s := stash.New(newFake())
	long := strings.Repeat("k", stash.MaxKeyLen+1)
	nsLong := s.Namespace(strings.Repeat("n", stash.MaxKeyLen))
	bad := map[string]func() error{
		"empty Get":       func() error { _, err := s.Get(ctx, ""); return err },
		"invalid UTF-8":   func() error { return s.Set(ctx, "\xff", nil) },
		"too long":        func() error { _, err := s.Delete(ctx, long); return err },
		"namespace limit": func() error { _, err := nsLong.Exists(ctx, "k"); return err },
		"Incr":            func() error { _, err := s.Incr(ctx, "", 1); return err },
		"TTL":             func() error { _, err := s.TTL(ctx, ""); return err },
		"Expire":          func() error { return s.Expire(ctx, "", time.Second) },
	}
	for name, call := range bad {
		if err := call(); !errors.Is(err, stash.ErrInvalidKey) {
			t.Errorf("%s: err = %v, want ErrInvalidKey", name, err)
		}
	}
}

func TestInvalidNamespace(t *testing.T) {
	ctx := context.Background()
	s := stash.New(newFake())
	for _, name := range []string{"", "\xff"} {
		ns := s.Namespace(name)
		checks := map[string]error{
			"Set":   ns.Set(ctx, "k", nil),
			"Clear": ns.Clear(ctx),
			"Ping":  ns.Ping(ctx),
		}
		for _, err := range ns.Keys(ctx) {
			checks["Keys"] = err
		}
		for op, err := range checks {
			if !errors.Is(err, stash.ErrInvalidKey) {
				t.Errorf("Namespace(%q).%s err = %v, want ErrInvalidKey", name, op, err)
			}
		}
	}
}

func TestBackendErrorsAreWrapped(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := stash.New(f)
	f.failAll = errors.New("connection reset")

	_, err := s.Get(ctx, "secret-session-id")
	var e *stash.Error
	if !errors.As(err, &e) || e.Op != "get" || e.Key != "secret-session-id" || !errors.Is(err, f.failAll) {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), "secret-session-id") {
		t.Errorf("error message leaks the key: %v", err)
	}
	if err.Error() != "stash: get: connection reset" {
		t.Errorf("message = %q", err.Error())
	}
	if err := s.Namespace("ns").Clear(ctx); !errors.Is(err, f.failAll) {
		t.Errorf("Clear err = %v", err)
	}
	for _, err := range s.Keys(ctx) {
		if !errors.Is(err, f.failAll) {
			t.Errorf("Keys err = %v", err)
		}
	}
	if err := s.Close(); !errors.Is(err, f.failAll) {
		t.Errorf("Close err = %v", err)
	}

	// An *Error from a backend passes through unchanged.
	inner := &stash.Error{Op: "custom", Err: stash.ErrExists}
	f2 := newFake()
	f2.failAll = inner
	if err := stash.New(f2).Set(ctx, "k", nil); err != inner { //nolint:errorlint // the same value, unwrapped
		t.Errorf("Set err = %v, want the backend's *Error", err)
	}
}

func TestCloseOnce(t *testing.T) {
	f := newFake()
	s := stash.New(f)
	ns := s.Namespace("a")
	if err := ns.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if f.closed != 1 {
		t.Errorf("backend closed %d times, want 1", f.closed)
	}
	for _, err := range s.Keys(context.Background()) {
		if !errors.Is(err, stash.ErrClosed) {
			t.Errorf("Keys after Close err = %v", err)
		}
	}
}

type badCodec struct{ marshal, unmarshal error }

func (c badCodec) Marshal(any) ([]byte, error) { return []byte("x"), c.marshal }
func (c badCodec) Unmarshal([]byte, any) error { return c.unmarshal }

func TestTypedStore(t *testing.T) {
	ctx := context.Background()
	s := stash.New(newFake())
	nums := stash.Typed[int](s, stash.JSON)
	if nums.Store() != s {
		t.Error("Store returned a different store")
	}
	if err := nums.Set(ctx, "n", 7, stash.WithTTL(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := nums.Exists(ctx, "n"); err != nil || !ok {
		t.Errorf("Exists = %v, %v", ok, err)
	}
	if d, err := nums.TTL(ctx, "n"); err != nil || d <= 0 {
		t.Errorf("TTL = %v, %v", d, err)
	}
	if err := nums.Expire(ctx, "n", 0); err != nil {
		t.Error(err)
	}
	for k, err := range nums.Keys(ctx) {
		if err != nil || k != "n" {
			t.Errorf("Keys = %q, %v", k, err)
		}
	}
	if ok, err := nums.Delete(ctx, "n"); err != nil || !ok {
		t.Errorf("Delete = %v, %v", ok, err)
	}
	if _, err := nums.Get(ctx, "n"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Get missing err = %v", err)
	}

	if err := s.Set(ctx, "text", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := nums.Get(ctx, "text"); !errors.Is(err, stash.ErrCodec) {
		t.Errorf("decode err = %v, want ErrCodec", err)
	}
	encodeErr := errors.New("cannot encode")
	bad := stash.Typed[int](s, badCodec{marshal: encodeErr})
	if err := bad.Set(ctx, "x", 1); !errors.Is(err, stash.ErrCodec) || !errors.Is(err, encodeErr) {
		t.Errorf("encode err = %v, want ErrCodec wrapping the codec's error", err)
	}
}
