// Package stashtest is the conformance suite for stash drivers. Every driver
// runs it from its own tests so all drivers behave identically for the same
// calls.
//
// Run exercises a Backend through a stash.Store, so it checks the behavior
// applications see: values, expiry, IfNotExists, Incr, Keys, namespaces, and
// typed values.
package stashtest

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/stash"
)

// Config describes the driver under test.
type Config struct {
	// NewBackend returns an empty Backend that no other test uses.
	// Required. The driver's test owns cleanup; Run closes each Store it
	// builds, which closes the Backend.
	NewBackend func(t *testing.T) stash.Backend

	// TTL is the short expiry the expiry tests wait out. Zero means 200ms.
	// Raise it for backends with coarse expiry clocks.
	TTL time.Duration
}

// Run runs the conformance tests.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.NewBackend == nil {
		t.Fatal("stashtest: Config.NewBackend is required")
	}
	if cfg.TTL == 0 {
		cfg.TTL = 200 * time.Millisecond
	}
	tests := []struct {
		name string
		fn   func(*testing.T, *stash.Store, Config)
	}{
		{"SetAndGet", testSetAndGet},
		{"Values", testValues},
		{"Missing", testMissing},
		{"Delete", testDelete},
		{"IfNotExists", testIfNotExists},
		{"IfNotExistsRace", testIfNotExistsRace},
		{"TTL", testTTL},
		{"Expiry", testExpiry},
		{"Expire", testExpire},
		{"Incr", testIncr},
		{"IncrRace", testIncrRace},
		{"Keys", testKeys},
		{"Namespace", testNamespace},
		{"Typed", testTyped},
		{"Canceled", testCanceled},
		{"Close", testClose},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stash.New(cfg.NewBackend(t))
			t.Cleanup(func() { _ = s.Close() })
			tt.fn(t, s, cfg)
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, s *stash.Store, key string) []byte {
	t.Helper()
	v, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return v
}

func keys(t *testing.T, s *stash.Store, opts ...stash.KeysOption) []string {
	t.Helper()
	var out []string
	for k, err := range s.Keys(context.Background(), opts...) {
		if err != nil {
			t.Fatalf("Keys: %v", err)
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func testSetAndGet(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	must(t, s.Set(ctx, "greeting", []byte("hello")))
	if got := get(t, s, "greeting"); string(got) != "hello" {
		t.Errorf("Get = %q, want hello", got)
	}
	must(t, s.Set(ctx, "greeting", []byte("hi")))
	if got := get(t, s, "greeting"); string(got) != "hi" {
		t.Errorf("Get after overwrite = %q, want hi", got)
	}
	ok, err := s.Exists(ctx, "greeting")
	if err != nil || !ok {
		t.Errorf("Exists = %v, %v", ok, err)
	}
}

func testValues(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	values := map[string][]byte{
		"empty":   {},
		"binary":  {0x00, 0xff, 0x10, 0x00},
		"unicode": []byte("héllo, 世界"),
		"large":   bytes.Repeat([]byte("x"), 1<<20),
	}
	for k, v := range values {
		must(t, s.Set(ctx, k, v))
		if got := get(t, s, k); !bytes.Equal(got, v) {
			t.Errorf("%s: Get returned %d bytes, want %d", k, len(got), len(v))
		}
	}
	long := string(bytes.Repeat([]byte("k"), stash.MaxKeyLen))
	must(t, s.Set(ctx, long, []byte("v")))
	if got := get(t, s, long); string(got) != "v" {
		t.Errorf("longest key: Get = %q", got)
	}
	special := "a%b_c*d?e[f]g\\h'i\"j:k"
	must(t, s.Set(ctx, special, []byte("v")))
	if got := get(t, s, special); string(got) != "v" {
		t.Errorf("special key: Get = %q", got)
	}
}

func testMissing(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", err)
	}
	if ok, err := s.Exists(ctx, "nope"); err != nil || ok {
		t.Errorf("Exists = %v, %v", ok, err)
	}
	if ok, err := s.Delete(ctx, "nope"); err != nil || ok {
		t.Errorf("Delete = %v, %v", ok, err)
	}
	if _, err := s.TTL(ctx, "nope"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("TTL err = %v, want ErrNotFound", err)
	}
	if err := s.Expire(ctx, "nope", time.Hour); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Expire err = %v, want ErrNotFound", err)
	}
}

func testDelete(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	must(t, s.Set(ctx, "k", []byte("v")))
	if ok, err := s.Delete(ctx, "k"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Get after Delete err = %v", err)
	}
}

func testIfNotExists(t *testing.T, s *stash.Store, cfg Config) {
	ctx := context.Background()
	must(t, s.Set(ctx, "lock", []byte("a"), stash.IfNotExists()))
	if err := s.Set(ctx, "lock", []byte("b"), stash.IfNotExists()); !errors.Is(err, stash.ErrExists) {
		t.Fatalf("second Set err = %v, want ErrExists", err)
	}
	if got := get(t, s, "lock"); string(got) != "a" {
		t.Errorf("Get = %q, want the first value", got)
	}

	must(t, s.Set(ctx, "short", []byte("a"), stash.WithTTL(cfg.TTL)))
	time.Sleep(2 * cfg.TTL)
	if err := s.Set(ctx, "short", []byte("b"), stash.IfNotExists(), stash.WithTTL(time.Hour)); err != nil {
		t.Fatalf("Set over an expired key: %v", err)
	}
	if got := get(t, s, "short"); string(got) != "b" {
		t.Errorf("Get = %q, want b", got)
	}
}

func testIfNotExistsRace(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	const workers = 16
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := range workers {
		wg.Go(func() {
			results <- s.Set(ctx, "race", []byte(strconv.Itoa(i)), stash.IfNotExists())
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, stash.ErrExists):
			t.Errorf("Set err = %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("%d callers won, want exactly 1", wins)
	}
}

func testTTL(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	must(t, s.Set(ctx, "forever", []byte("v")))
	if d, err := s.TTL(ctx, "forever"); err != nil || d != stash.NoExpiry {
		t.Errorf("TTL = %v, %v; want NoExpiry", d, err)
	}
	must(t, s.Set(ctx, "hour", []byte("v"), stash.WithTTL(time.Hour)))
	if d, err := s.TTL(ctx, "hour"); err != nil || d <= 59*time.Minute || d > time.Hour {
		t.Errorf("TTL = %v, %v; want just under an hour", d, err)
	}
	must(t, s.Set(ctx, "hour", []byte("v2")))
	if d, err := s.TTL(ctx, "hour"); err != nil || d != stash.NoExpiry {
		t.Errorf("TTL after Set without WithTTL = %v, %v; want NoExpiry", d, err)
	}
	if err := s.Set(ctx, "bad", []byte("v"), stash.WithTTL(-time.Second)); !errors.Is(err, stash.ErrInvalidOption) {
		t.Errorf("negative TTL err = %v", err)
	}
}

func testExpiry(t *testing.T, s *stash.Store, cfg Config) {
	ctx := context.Background()
	must(t, s.Set(ctx, "short", []byte("v"), stash.WithTTL(cfg.TTL)))
	must(t, s.Set(ctx, "long", []byte("v"), stash.WithTTL(time.Hour)))
	time.Sleep(2 * cfg.TTL)
	if _, err := s.Get(ctx, "short"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Get expired err = %v, want ErrNotFound", err)
	}
	if ok, err := s.Exists(ctx, "short"); err != nil || ok {
		t.Errorf("Exists expired = %v, %v", ok, err)
	}
	if _, err := s.TTL(ctx, "short"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("TTL expired err = %v", err)
	}
	if ok, err := s.Delete(ctx, "short"); err != nil || ok {
		t.Errorf("Delete expired = %v, %v; want false", ok, err)
	}
	if got := keys(t, s); !slices.Equal(got, []string{"long"}) {
		t.Errorf("Keys = %q, want [long]", got)
	}
}

func testExpire(t *testing.T, s *stash.Store, cfg Config) {
	ctx := context.Background()
	must(t, s.Set(ctx, "k", []byte("v")))
	must(t, s.Expire(ctx, "k", time.Hour))
	if d, err := s.TTL(ctx, "k"); err != nil || d <= 59*time.Minute {
		t.Errorf("TTL after Expire = %v, %v", d, err)
	}
	must(t, s.Expire(ctx, "k", 0))
	if d, err := s.TTL(ctx, "k"); err != nil || d != stash.NoExpiry {
		t.Errorf("TTL after Expire(0) = %v, %v; want NoExpiry", d, err)
	}
	if err := s.Expire(ctx, "k", -time.Second); !errors.Is(err, stash.ErrInvalidOption) {
		t.Errorf("negative Expire err = %v", err)
	}
	must(t, s.Expire(ctx, "k", cfg.TTL))
	time.Sleep(2 * cfg.TTL)
	if _, err := s.Get(ctx, "k"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("Get after expiry err = %v", err)
	}
}

func testIncr(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	steps := []struct {
		delta, want int64
	}{{1, 1}, {4, 5}, {-7, -2}, {0, -2}}
	for _, st := range steps {
		n, err := s.Incr(ctx, "n", st.delta)
		if err != nil || n != st.want {
			t.Fatalf("Incr(%d) = %d, %v; want %d", st.delta, n, err, st.want)
		}
	}
	if got := get(t, s, "n"); string(got) != "-2" {
		t.Errorf("Get = %q, want -2", got)
	}
	if d, err := s.TTL(ctx, "n"); err != nil || d != stash.NoExpiry {
		t.Errorf("new counter TTL = %v, %v; want NoExpiry", d, err)
	}

	must(t, s.Set(ctx, "timed", []byte("10"), stash.WithTTL(time.Hour)))
	if n, err := s.Incr(ctx, "timed", 1); err != nil || n != 11 {
		t.Fatalf("Incr = %d, %v", n, err)
	}
	if d, err := s.TTL(ctx, "timed"); err != nil || d <= 59*time.Minute {
		t.Errorf("Incr dropped the expiry: TTL = %v, %v", d, err)
	}

	for _, bad := range []string{"abc", "1.5", "", " 1"} {
		must(t, s.Set(ctx, "bad", []byte(bad)))
		if _, err := s.Incr(ctx, "bad", 1); !errors.Is(err, stash.ErrNotInteger) {
			t.Errorf("Incr on %q err = %v, want ErrNotInteger", bad, err)
		}
		if got := get(t, s, "bad"); string(got) != bad {
			t.Errorf("failed Incr changed %q to %q", bad, got)
		}
	}
	must(t, s.Set(ctx, "max", []byte(strconv.FormatInt(math.MaxInt64, 10))))
	if _, err := s.Incr(ctx, "max", 1); !errors.Is(err, stash.ErrNotInteger) {
		t.Errorf("overflowing Incr err = %v, want ErrNotInteger", err)
	}
}

func testIncrRace(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	const workers, each = 8, 25
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range each {
				if _, err := s.Incr(ctx, "hits", 1); err != nil {
					t.Errorf("Incr: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	if got := get(t, s, "hits"); string(got) != strconv.Itoa(workers*each) {
		t.Errorf("hits = %s, want %d (an increment was lost)", got, workers*each)
	}
}

func testKeys(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	if got := keys(t, s); len(got) != 0 {
		t.Fatalf("empty store Keys = %q", got)
	}
	all := []string{"user:1", "user:2", "user_x", "userX", "user%", "us*r", "u?er", "[user]", "other"}
	for _, k := range all {
		must(t, s.Set(ctx, k, []byte("v")))
	}
	want := slices.Clone(all)
	slices.Sort(want)
	if got := keys(t, s); !slices.Equal(got, want) {
		t.Errorf("Keys = %q, want %q", got, want)
	}
	prefixes := map[string][]string{
		"user:": {"user:1", "user:2"},
		"user_": {"user_x"},
		"user%": {"user%"},
		"us*":   {"us*r"},
		"u?":    {"u?er"},
		"[":     {"[user]"},
		"none":  nil,
	}
	for p, want := range prefixes {
		if got := keys(t, s, stash.WithPrefix(p)); !slices.Equal(got, want) {
			t.Errorf("Keys(WithPrefix(%q)) = %q, want %q", p, got, want)
		}
	}

	n := 0
	for _, err := range s.Keys(ctx) {
		must(t, err)
		n++
		break
	}
	if n != 1 {
		t.Errorf("breaking out of Keys after one key ran %d times", n)
	}
}

func testNamespace(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	a := s.Namespace("a_%*")
	b := s.Namespace("aX%*")
	nested := a.Namespace("inner")
	must(t, s.Set(ctx, "root", []byte("r")))
	must(t, a.Set(ctx, "k", []byte("a")))
	must(t, b.Set(ctx, "k", []byte("b")))
	must(t, nested.Set(ctx, "k", []byte("n")))

	if got := get(t, a, "k"); string(got) != "a" {
		t.Errorf("a.Get = %q", got)
	}
	if got := get(t, s, "a_%*:k"); string(got) != "a" {
		t.Errorf("root sees a's key as %q", got)
	}
	if got := get(t, s, "a_%*:inner:k"); string(got) != "n" {
		t.Errorf("root sees nested key as %q", got)
	}
	if got := keys(t, a); !slices.Equal(got, []string{"inner:k", "k"}) {
		t.Errorf("a.Keys = %q", got)
	}
	if got := keys(t, nested); !slices.Equal(got, []string{"k"}) {
		t.Errorf("nested.Keys = %q", got)
	}

	must(t, a.Clear(ctx))
	if got := keys(t, a); len(got) != 0 {
		t.Errorf("a.Keys after Clear = %q", got)
	}
	if got := get(t, b, "k"); string(got) != "b" {
		t.Errorf("Clear of a removed b's key: %q", got)
	}
	if got := get(t, s, "root"); string(got) != "r" {
		t.Errorf("Clear of a removed the root key: %q", got)
	}
	if err := s.Clear(ctx); !errors.Is(err, stash.ErrInvalidOption) {
		t.Errorf("root Clear err = %v, want ErrInvalidOption", err)
	}
}

type session struct {
	UserID int64     `json:"user_id"`
	Roles  []string  `json:"roles"`
	At     time.Time `json:"at"`
}

func testTyped(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	sessions := stash.Typed[session](s.Namespace("sessions"), stash.JSON)
	in := session{UserID: 42, Roles: []string{"admin"}, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	must(t, sessions.Set(ctx, "abc", in, stash.WithTTL(time.Hour)))
	out, err := sessions.Get(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if out.UserID != in.UserID || !slices.Equal(out.Roles, in.Roles) || !out.At.Equal(in.At) {
		t.Errorf("Get = %+v, want %+v", out, in)
	}
	if got := get(t, s, "sessions:abc"); !bytes.Contains(got, []byte(`"user_id":42`)) {
		t.Errorf("stored JSON = %s", got)
	}
}

func testCanceled(t *testing.T, s *stash.Store, _ Config) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks := map[string]error{}
	_, checks["Get"] = s.Get(ctx, "k")
	checks["Set"] = s.Set(ctx, "k", []byte("v"))
	_, checks["Incr"] = s.Incr(ctx, "k", 1)
	checks["Ping"] = s.Ping(ctx)
	for op, err := range checks {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a canceled context: err = %v, want context.Canceled", op, err)
		}
	}
}

func testClose(t *testing.T, s *stash.Store, _ Config) {
	ctx := context.Background()
	must(t, s.Ping(ctx))
	must(t, s.Close())
	must(t, s.Close())
	if _, err := s.Get(ctx, "k"); !errors.Is(err, stash.ErrClosed) {
		t.Errorf("Get after Close err = %v, want ErrClosed", err)
	}
	if err := s.Namespace("ns").Set(ctx, "k", []byte("v")); !errors.Is(err, stash.ErrClosed) {
		t.Errorf("namespace Set after Close err = %v, want ErrClosed", err)
	}
}
