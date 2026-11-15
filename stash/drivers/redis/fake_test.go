package redis

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// redisErr is an error the server would send, as go-redis reports it.
type redisErr string

func (e redisErr) Error() string { return string(e) }
func (redisErr) RedisError()     {}

// fakeClient is an in-memory stand-in for a Redis server with the commands
// the driver uses. Scan returns pageSize keys per call to exercise the
// cursor loop.
type fakeClient struct {
	mu       sync.Mutex
	data     map[string]fakeEntry
	pageSize int
	fail     map[string]error // command name to the error it returns
	closed   bool

	cursors    map[uint64]string // SCAN cursor to the last key it returned
	nextCursor uint64
}

type fakeEntry struct {
	value   string
	expires time.Time
}

func newFakeClient() *fakeClient {
	return &fakeClient{data: map[string]fakeEntry{}, pageSize: 3, fail: map[string]error{}, cursors: map[uint64]string{}}
}

// begin checks ctx and injected failures, then locks the fake.
func (f *fakeClient) begin(ctx context.Context, cmd string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	if err := f.fail[cmd]; err != nil {
		f.mu.Unlock()
		return err
	}
	return nil
}

func (f *fakeClient) live(key string) (fakeEntry, bool) {
	e, ok := f.data[key]
	if ok && !e.expires.IsZero() && !time.Now().Before(e.expires) {
		delete(f.data, key)
		return fakeEntry{}, false
	}
	return e, ok
}

func toString(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v.(string)
}

func expiry(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d)
}

func (f *fakeClient) Get(ctx context.Context, key string) *goredis.StringCmd {
	if err := f.begin(ctx, "get"); err != nil {
		return goredis.NewStringResult("", err)
	}
	defer f.mu.Unlock()
	e, ok := f.live(key)
	if !ok {
		return goredis.NewStringResult("", goredis.Nil)
	}
	return goredis.NewStringResult(e.value, nil)
}

func (f *fakeClient) Set(ctx context.Context, key string, value any, exp time.Duration) *goredis.StatusCmd {
	if err := f.begin(ctx, "set"); err != nil {
		return goredis.NewStatusResult("", err)
	}
	defer f.mu.Unlock()
	f.data[key] = fakeEntry{value: toString(value), expires: expiry(exp)}
	return goredis.NewStatusResult("OK", nil)
}

func (f *fakeClient) SetNX(ctx context.Context, key string, value any, exp time.Duration) *goredis.BoolCmd {
	if err := f.begin(ctx, "setnx"); err != nil {
		return goredis.NewBoolResult(false, err)
	}
	defer f.mu.Unlock()
	if _, ok := f.live(key); ok {
		return goredis.NewBoolResult(false, nil)
	}
	f.data[key] = fakeEntry{value: toString(value), expires: expiry(exp)}
	return goredis.NewBoolResult(true, nil)
}

func (f *fakeClient) Del(ctx context.Context, keys ...string) *goredis.IntCmd {
	if err := f.begin(ctx, "del"); err != nil {
		return goredis.NewIntResult(0, err)
	}
	defer f.mu.Unlock()
	var n int64
	for _, k := range keys {
		if _, ok := f.live(k); ok {
			n++
		}
		delete(f.data, k)
	}
	return goredis.NewIntResult(n, nil)
}

func (f *fakeClient) Exists(ctx context.Context, keys ...string) *goredis.IntCmd {
	if err := f.begin(ctx, "exists"); err != nil {
		return goredis.NewIntResult(0, err)
	}
	defer f.mu.Unlock()
	var n int64
	for _, k := range keys {
		if _, ok := f.live(k); ok {
			n++
		}
	}
	return goredis.NewIntResult(n, nil)
}

func (f *fakeClient) IncrBy(ctx context.Context, key string, delta int64) *goredis.IntCmd {
	if err := f.begin(ctx, "incrby"); err != nil {
		return goredis.NewIntResult(0, err)
	}
	defer f.mu.Unlock()
	e, ok := f.live(key)
	var n int64
	if ok {
		var err error
		if n, err = strconv.ParseInt(e.value, 10, 64); err != nil {
			return goredis.NewIntResult(0, redisErr("ERR value is not an integer or out of range"))
		}
	}
	sum := n + delta
	if (delta > 0 && sum < n) || (delta < 0 && sum > n) {
		return goredis.NewIntResult(0, redisErr("ERR increment or decrement would overflow"))
	}
	e.value = strconv.FormatInt(sum, 10)
	f.data[key] = e
	return goredis.NewIntResult(sum, nil)
}

func (f *fakeClient) PTTL(ctx context.Context, key string) *goredis.DurationCmd {
	if err := f.begin(ctx, "pttl"); err != nil {
		return goredis.NewDurationResult(0, err)
	}
	defer f.mu.Unlock()
	e, ok := f.live(key)
	switch {
	case !ok:
		return goredis.NewDurationResult(-2, nil)
	case e.expires.IsZero():
		return goredis.NewDurationResult(-1, nil)
	}
	return goredis.NewDurationResult(time.Until(e.expires).Truncate(time.Millisecond), nil)
}

func (f *fakeClient) PExpire(ctx context.Context, key string, exp time.Duration) *goredis.BoolCmd {
	if err := f.begin(ctx, "pexpire"); err != nil {
		return goredis.NewBoolResult(false, err)
	}
	defer f.mu.Unlock()
	e, ok := f.live(key)
	if !ok {
		return goredis.NewBoolResult(false, nil)
	}
	e.expires = expiry(exp)
	f.data[key] = e
	return goredis.NewBoolResult(true, nil)
}

func (f *fakeClient) Persist(ctx context.Context, key string) *goredis.BoolCmd {
	if err := f.begin(ctx, "persist"); err != nil {
		return goredis.NewBoolResult(false, err)
	}
	defer f.mu.Unlock()
	e, ok := f.live(key)
	if !ok || e.expires.IsZero() {
		return goredis.NewBoolResult(false, nil)
	}
	e.expires = time.Time{}
	f.data[key] = e
	return goredis.NewBoolResult(true, nil)
}

// Scan understands only the patterns the driver builds: an escaped prefix
// followed by "*".
func (f *fakeClient) Scan(ctx context.Context, cursor uint64, match string, _ int64) *goredis.ScanCmd {
	if err := f.begin(ctx, "scan"); err != nil {
		return goredis.NewScanCmdResult(nil, 0, err)
	}
	defer f.mu.Unlock()
	prefix := unescape(strings.TrimSuffix(match, "*"))
	var all []string
	for k := range f.data {
		if _, ok := f.live(k); ok && strings.HasPrefix(k, prefix) {
			all = append(all, k)
		}
	}
	slices.Sort(all)
	// Like a real cursor, resuming after the last returned key does not
	// skip keys when earlier ones are deleted between calls.
	if cursor != 0 {
		after := f.cursors[cursor]
		all = slices.DeleteFunc(all, func(k string) bool { return k <= after })
	}
	page := all[:min(f.pageSize, len(all))]
	if len(page) == len(all) {
		return goredis.NewScanCmdResult(page, 0, nil)
	}
	f.nextCursor++
	f.cursors[f.nextCursor] = page[len(page)-1]
	return goredis.NewScanCmdResult(page, f.nextCursor, nil)
}

func unescape(p string) string {
	var sb strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			i++
		}
		sb.WriteByte(p[i])
	}
	return sb.String()
}

func (f *fakeClient) Ping(ctx context.Context) *goredis.StatusCmd {
	if err := f.begin(ctx, "ping"); err != nil {
		return goredis.NewStatusResult("", err)
	}
	defer f.mu.Unlock()
	return goredis.NewStatusResult("PONG", nil)
}

func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.fail["close"]
}
