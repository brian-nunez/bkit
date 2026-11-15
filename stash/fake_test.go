package stash_test

import (
	"context"
	"iter"
	"maps"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brian-nunez/bkit/stash"
)

// fakeBackend is a minimal in-memory Backend for the core's tests.
type fakeBackend struct {
	mu      sync.Mutex
	data    map[string]fakeItem
	failAll error // when set, every call returns it
	closed  int
}

type fakeItem struct {
	value   []byte
	expires time.Time
}

func newFake() *fakeBackend { return &fakeBackend{data: map[string]fakeItem{}} }

func (f *fakeBackend) live(key string) (fakeItem, bool) {
	it, ok := f.data[key]
	if ok && !it.expires.IsZero() && !time.Now().Before(it.expires) {
		delete(f.data, key)
		return fakeItem{}, false
	}
	return it, ok
}

func (f *fakeBackend) start(ctx context.Context) error {
	if f.failAll != nil {
		return f.failAll
	}
	return ctx.Err()
}

func (f *fakeBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if err := f.start(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.live(key)
	if !ok {
		return nil, stash.ErrNotFound
	}
	return slices.Clone(it.value), nil
}

func (f *fakeBackend) Set(ctx context.Context, key string, value []byte, o stash.SetOptions) error {
	if err := f.start(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live(key); ok && o.IfNotExists {
		return stash.ErrExists
	}
	it := fakeItem{value: slices.Clone(value)}
	if o.TTL > 0 {
		it.expires = time.Now().Add(o.TTL)
	}
	f.data[key] = it
	return nil
}

func (f *fakeBackend) Delete(ctx context.Context, key string) (bool, error) {
	if err := f.start(ctx); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.live(key)
	delete(f.data, key)
	return ok, nil
}

func (f *fakeBackend) Exists(ctx context.Context, key string) (bool, error) {
	if err := f.start(ctx); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.live(key)
	return ok, nil
}

func (f *fakeBackend) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	if err := f.start(ctx); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.live(key)
	var n int64
	if ok {
		var err error
		if n, err = strconv.ParseInt(string(it.value), 10, 64); err != nil {
			return 0, stash.ErrNotInteger
		}
	}
	if (delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta) {
		return 0, stash.ErrNotInteger
	}
	n += delta
	it.value = []byte(strconv.FormatInt(n, 10))
	f.data[key] = it
	return n, nil
}

func (f *fakeBackend) TTL(ctx context.Context, key string) (time.Duration, error) {
	if err := f.start(ctx); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.live(key)
	switch {
	case !ok:
		return 0, stash.ErrNotFound
	case it.expires.IsZero():
		return stash.NoExpiry, nil
	}
	return time.Until(it.expires), nil
}

func (f *fakeBackend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if err := f.start(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.live(key)
	if !ok {
		return stash.ErrNotFound
	}
	it.expires = time.Time{}
	if ttl > 0 {
		it.expires = time.Now().Add(ttl)
	}
	f.data[key] = it
	return nil
}

func (f *fakeBackend) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if err := f.start(ctx); err != nil {
			yield("", err)
			return
		}
		f.mu.Lock()
		var out []string
		for _, k := range slices.Sorted(maps.Keys(f.data)) {
			if _, ok := f.live(k); ok && strings.HasPrefix(k, prefix) {
				out = append(out, k)
			}
		}
		f.mu.Unlock()
		for _, k := range out {
			if !yield(k, nil) {
				return
			}
		}
	}
}

func (f *fakeBackend) DeletePrefix(ctx context.Context, prefix string) error {
	if err := f.start(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
		}
	}
	return nil
}

func (f *fakeBackend) Ping(ctx context.Context) error { return f.start(ctx) }

func (f *fakeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return f.failAll
}

// fakeDriver opens a fakeBackend for scheme "fake".
type fakeDriver struct {
	backend *fakeBackend
	openErr error
	opened  *url.URL
}

func (d *fakeDriver) Schemes() []string { return []string{"fake"} }

func (d *fakeDriver) Open(_ context.Context, u *url.URL) (stash.Backend, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	d.opened = u
	return d.backend, nil
}
