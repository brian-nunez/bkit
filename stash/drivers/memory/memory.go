package memory

import (
	"context"
	"iter"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brian-nunez/bkit/stash"
)

// sweepBatch is how many entries each write checks for expiry.
const sweepBatch = 20

// Driver returns a stash.Option that lets stash.Open open memory:// URLs.
// Every Open returns a new, empty store.
func Driver() stash.Option {
	return stash.WithDriver(memDriver{})
}

// New returns a new, empty in-memory store.
func New() *stash.Store {
	return stash.New(newBackend())
}

// memDriver implements stash.Driver.
type memDriver struct{}

// Schemes returns the URL schemes the driver opens.
func (memDriver) Schemes() []string {
	return []string{"memory"}
}

// Open returns a new, empty Backend. The rest of the URL is ignored.
func (memDriver) Open(context.Context, *url.URL) (stash.Backend, error) {
	return newBackend(), nil
}

// item is one stored value.
type item struct {
	value   []byte
	expires time.Time // zero when the key never expires
}

// expired reports whether it has expired at now.
func (it item) expired(now time.Time) bool {
	return !it.expires.IsZero() && !now.Before(it.expires)
}

// backend implements stash.Backend over a map.
type backend struct {
	mu   sync.Mutex
	data map[string]item
}

func newBackend() *backend {
	return &backend{data: make(map[string]item)}
}

// live returns the unexpired item for key, deleting it when expired. The
// caller holds b.mu.
func (b *backend) live(key string, now time.Time) (item, bool) {
	it, ok := b.data[key]
	if ok && it.expired(now) {
		delete(b.data, key)
		return item{}, false
	}
	return it, ok
}

// sweep deletes expired entries among up to sweepBatch entries. Map
// iteration starts at a random entry, so repeated sweeps reach every entry.
// The caller holds b.mu.
func (b *backend) sweep(now time.Time) {
	n := 0
	for k, it := range b.data {
		if it.expired(now) {
			delete(b.data, k)
		}
		if n++; n == sweepBatch {
			return
		}
	}
}

// lock checks ctx, then locks b.mu and returns the current time.
func (b *backend) lock(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	b.mu.Lock()
	return time.Now(), nil
}

// Get returns a copy of the value of key.
func (b *backend) Get(ctx context.Context, key string) ([]byte, error) {
	now, err := b.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer b.mu.Unlock()
	it, ok := b.live(key, now)
	if !ok {
		return nil, stash.ErrNotFound
	}
	return slices.Clone(it.value), nil
}

// Set stores a copy of value under key.
func (b *backend) Set(ctx context.Context, key string, value []byte, opts stash.SetOptions) error {
	now, err := b.lock(ctx)
	if err != nil {
		return err
	}
	defer b.mu.Unlock()
	if _, ok := b.live(key, now); ok && opts.IfNotExists {
		return stash.ErrExists
	}
	it := item{value: append([]byte{}, value...)}
	if opts.TTL > 0 {
		it.expires = now.Add(opts.TTL)
	}
	b.data[key] = it
	b.sweep(now)
	return nil
}

// Delete removes key.
func (b *backend) Delete(ctx context.Context, key string) (bool, error) {
	now, err := b.lock(ctx)
	if err != nil {
		return false, err
	}
	defer b.mu.Unlock()
	_, ok := b.live(key, now)
	delete(b.data, key)
	return ok, nil
}

// Exists reports whether key is set.
func (b *backend) Exists(ctx context.Context, key string) (bool, error) {
	now, err := b.lock(ctx)
	if err != nil {
		return false, err
	}
	defer b.mu.Unlock()
	_, ok := b.live(key, now)
	return ok, nil
}

// Incr adds delta to the counter under key.
func (b *backend) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	now, err := b.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer b.mu.Unlock()
	it, ok := b.live(key, now)
	var n int64
	if ok {
		if n, err = strconv.ParseInt(string(it.value), 10, 64); err != nil {
			return 0, stash.ErrNotInteger
		}
	}
	if (delta > 0 && n > math.MaxInt64-delta) || (delta < 0 && n < math.MinInt64-delta) {
		return 0, stash.ErrNotInteger
	}
	n += delta
	it.value = strconv.AppendInt(nil, n, 10)
	b.data[key] = it
	b.sweep(now)
	return n, nil
}

// TTL returns the time left before key expires.
func (b *backend) TTL(ctx context.Context, key string) (time.Duration, error) {
	now, err := b.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer b.mu.Unlock()
	it, ok := b.live(key, now)
	switch {
	case !ok:
		return 0, stash.ErrNotFound
	case it.expires.IsZero():
		return stash.NoExpiry, nil
	}
	return it.expires.Sub(now), nil
}

// Expire sets the expiry of key.
func (b *backend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	now, err := b.lock(ctx)
	if err != nil {
		return err
	}
	defer b.mu.Unlock()
	it, ok := b.live(key, now)
	if !ok {
		return stash.ErrNotFound
	}
	it.expires = time.Time{}
	if ttl > 0 {
		it.expires = now.Add(ttl)
	}
	b.data[key] = it
	b.sweep(now)
	return nil
}

// Keys yields a snapshot of the live keys that start with prefix.
func (b *backend) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		now, err := b.lock(ctx)
		if err != nil {
			yield("", err)
			return
		}
		var keys []string
		for k, it := range b.data {
			if strings.HasPrefix(k, prefix) && !it.expired(now) {
				keys = append(keys, k)
			}
		}
		b.mu.Unlock()
		for _, k := range keys {
			if !yield(k, nil) {
				return
			}
		}
	}
}

// DeletePrefix removes every key that starts with prefix.
func (b *backend) DeletePrefix(ctx context.Context, prefix string) error {
	if _, err := b.lock(ctx); err != nil {
		return err
	}
	defer b.mu.Unlock()
	for k := range b.data {
		if strings.HasPrefix(k, prefix) {
			delete(b.data, k)
		}
	}
	return nil
}

// Ping reports ctx's error, since the store is always reachable.
func (b *backend) Ping(ctx context.Context) error {
	return ctx.Err()
}

// Close drops every value.
func (b *backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = make(map[string]item)
	return nil
}
