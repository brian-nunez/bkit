package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

func TestConformance(t *testing.T) {
	stashtest.Run(t, stashtest.Config{
		NewBackend: func(*testing.T) stash.Backend { return newBackend() },
		TTL:        50 * time.Millisecond,
	})
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	a, err := stash.Open(ctx, "memory://", Driver())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b := New()
	defer b.Close()
	if err := a.Set(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, "k"); !errors.Is(err, stash.ErrNotFound) {
		t.Errorf("stores share data: err = %v", err)
	}
}

func TestSweep(t *testing.T) {
	ctx := context.Background()
	b := newBackend()
	for i := range 200 {
		key := string(rune('a'+i%26)) + time.Duration(i).String()
		if err := b.Set(ctx, key, nil, stash.SetOptions{TTL: time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(5 * time.Millisecond)
	// Each write checks a few entries from a random starting point, so
	// writes keep going until every expired entry is gone.
	left := 0
	for range 5000 {
		if err := b.Set(ctx, "live", nil, stash.SetOptions{}); err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		left = len(b.data)
		b.mu.Unlock()
		if left == 1 {
			return
		}
	}
	t.Errorf("%d entries left after 5000 writes, want only the live key", left)
}

func TestCopies(t *testing.T) {
	ctx := context.Background()
	b := newBackend()
	in := []byte("abc")
	if err := b.Set(ctx, "k", in, stash.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	in[0] = 'x'
	out, _ := b.Get(ctx, "k")
	out[1] = 'y'
	again, _ := b.Get(ctx, "k")
	if string(again) != "abc" {
		t.Errorf("stored value changed through a caller's slice: %q", again)
	}
}

func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := newBackend()
	errs := map[string]error{"DeletePrefix": b.DeletePrefix(ctx, "p"), "Expire": b.Expire(ctx, "k", 0)}
	_, errs["Delete"] = b.Delete(ctx, "k")
	_, errs["Exists"] = b.Exists(ctx, "k")
	_, errs["TTL"] = b.TTL(ctx, "k")
	for _, err := range b.Keys(ctx, "") {
		errs["Keys"] = err
	}
	for op, err := range errs {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s err = %v, want context.Canceled", op, err)
		}
	}
}
