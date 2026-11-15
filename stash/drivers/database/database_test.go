package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/rdb"
	"github.com/brian-nunez/bkit/rdb/drivers/sqlite"
	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/stashtest"
)

func openSQLite(t *testing.T, path string) *rdb.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), sqlite.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newBackend returns a backend over a fresh SQLite database.
func newBackend(t *testing.T, path string, opts ...Option) (*backend, *rdb.DB) {
	t.Helper()
	db := openSQLite(t, path)
	b, err := open(context.Background(), db, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return b, db
}

func TestConformance(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		stashtest.Run(t, stashtest.Config{
			NewBackend: func(t *testing.T) stash.Backend { b, _ := newBackend(t, ""); return b },
		})
	})
	t.Run("file", func(t *testing.T) {
		stashtest.Run(t, stashtest.Config{
			NewBackend: func(t *testing.T) stash.Backend {
				b, _ := newBackend(t, filepath.Join(t.TempDir(), "stash.db"))
				return b
			},
		})
	})
}

func TestQueriesFor(t *testing.T) {
	for _, d := range []rdb.Dialect{rdb.SQLite, rdb.Postgres, rdb.MySQL} {
		q, err := queriesFor(d, "kv")
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		for name, sql := range map[string]string{"get": q.get, "upsert": q.upsert, "ifAbsent": q.ifAbsent, "keys": q.keys, "selectRow": q.selectRow} {
			if !strings.Contains(sql, "kv") {
				t.Errorf("%s %s does not use the table: %s", d, name, sql)
			}
		}
		if got := strings.Count(q.ifAbsent, "?"); got != 3+q.ifAbsentNows {
			t.Errorf("%s ifAbsent has %d placeholders, want %d", d, got, 3+q.ifAbsentNows)
		}
	}
	mysql, _ := queriesFor(rdb.MySQL, "kv")
	if mysql.prefixLen("é") != 2 {
		t.Error("MySQL prefix length must count bytes")
	}
	pg, _ := queriesFor(rdb.Postgres, "kv")
	if pg.prefixLen("é") != 1 || pg.lockFirst != "" {
		t.Error("Postgres prefix length must count runes, with no lock-first statement")
	}
	if _, err := queriesFor("oracle", "kv"); !errors.Is(err, stash.ErrInvalidOption) {
		t.Errorf("unsupported dialect err = %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t, "")
	for _, name := range []string{"", "1abc", "a-b", "a b", strings.Repeat("a", 64), "t;DROP"} {
		if _, err := New(ctx, db, WithTable(name)); !errors.Is(err, stash.ErrInvalidOption) {
			t.Errorf("WithTable(%q) err = %v, want ErrInvalidOption", name, err)
		}
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(canceled, db); !errors.Is(err, context.Canceled) {
		t.Errorf("New with a canceled context err = %v", err)
	}

	other, err := rdb.OpenConnection(ctx, otherDriver{}, rdb.Connection{Connector: nopConnector{}})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := New(ctx, other); !errors.Is(err, stash.ErrInvalidOption) {
		t.Errorf("unsupported dialect err = %v", err)
	}
}

func TestWithTableAndClose(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t, "")
	s, err := New(ctx, db, WithTable("sessions_kv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	n, err := rdb.One[int](ctx, db, "SELECT count(*) FROM sessions_kv")
	if err != nil || n != 1 {
		t.Fatalf("rows in sessions_kv = %d, %v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); err != nil {
		t.Errorf("closing the store closed the database: %v", err)
	}
	// New on an existing table keeps its rows.
	again, err := New(ctx, db, WithTable("sessions_kv"))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := again.Get(ctx, "k"); err != nil || string(v) != "v" {
		t.Errorf("Get = %q, %v", v, err)
	}
}

func TestKeysPages(t *testing.T) {
	ctx := context.Background()
	b, _ := newBackend(t, "")
	const total = keysPage*2 + 7
	for i := range total {
		if err := b.Set(ctx, fmt.Sprintf("k%05d", i), nil, stash.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for k, err := range b.Keys(ctx, "k") {
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("k%05d", n); k != want {
			t.Fatalf("key %d = %q, want %q", n, k, want)
		}
		n++
	}
	if n != total {
		t.Errorf("Keys yielded %d keys, want %d", n, total)
	}
	n = 0
	for _, err := range b.Keys(ctx, "k") {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == keysPage+1 {
			break
		}
	}
	if n != keysPage+1 {
		t.Errorf("break on the second page stopped at %d", n)
	}
}

func TestSweep(t *testing.T) {
	ctx := context.Background()
	b, db := newBackend(t, "")
	for i := range 5 {
		if err := b.Set(ctx, fmt.Sprintf("old%d", i), nil, stash.SetOptions{TTL: time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	later := time.Now().Add(time.Hour)
	b.now = func() time.Time { return later }
	for range sweepEvery {
		if err := b.Set(ctx, "live", nil, stash.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := rdb.One[int](ctx, db, "SELECT count(*) FROM stash")
	if err != nil || n != 1 {
		t.Errorf("rows after the sweep = %d, %v; want 1", n, err)
	}
}

func TestIncrExpired(t *testing.T) {
	ctx := context.Background()
	b, _ := newBackend(t, "")
	if err := b.Set(ctx, "n", []byte("41"), stash.SetOptions{TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	b.now = func() time.Time { return later }
	n, err := b.Incr(ctx, "n", 1)
	if err != nil || n != 1 {
		t.Fatalf("Incr on an expired counter = %d, %v; want 1", n, err)
	}
	if d, err := b.TTL(ctx, "n"); err != nil || d != stash.NoExpiry {
		t.Errorf("TTL = %v, %v; want NoExpiry", d, err)
	}
}

func TestIncrConflicts(t *testing.T) {
	ctx := context.Background()
	b, db := newBackend(t, "")
	conflict := &rdb.Error{Op: "commit", Kind: rdb.ErrRetryable}
	real := b.tx

	fails := 3
	b.tx = func(ctx context.Context, fn func(*rdb.Tx) error) error {
		if fails > 0 {
			fails--
			return conflict
		}
		return real(ctx, fn)
	}
	if n, err := b.Incr(ctx, "n", 2); err != nil || n != 2 {
		t.Fatalf("Incr after conflicts = %d, %v", n, err)
	}

	attempts := 0
	b.tx = func(context.Context, func(*rdb.Tx) error) error {
		attempts++
		return &rdb.Error{Op: "exec", Kind: rdb.ErrUniqueViolation}
	}
	if _, err := b.Incr(ctx, "n", 1); !errors.Is(err, rdb.ErrUniqueViolation) || attempts != incrAttempts {
		t.Errorf("Incr err = %v after %d attempts, want ErrUniqueViolation after %d", err, attempts, incrAttempts)
	}

	cctx, cancel := context.WithCancel(ctx)
	b.tx = func(context.Context, func(*rdb.Tx) error) error {
		cancel()
		return conflict
	}
	if _, err := b.Incr(cctx, "n", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("Incr canceled during backoff err = %v", err)
	}

	// The transaction's statements fail.
	stmtCtx, stop := context.WithCancel(ctx)
	stop()
	b.tx = func(_ context.Context, fn func(*rdb.Tx) error) error {
		return db.Tx(context.Background(), fn)
	}
	if _, err := b.Incr(stmtCtx, "n", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("Incr with failing statements err = %v", err)
	}

	// A row that cannot be scanned.
	b.tx = real
	if _, err := db.Exec(ctx, "INSERT INTO stash (k, v, expires_at) VALUES ('bad', x'31', 'soon')"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Incr(ctx, "bad", 1); !errors.Is(err, rdb.ErrScan) {
		t.Errorf("Incr on an unreadable row err = %v, want ErrScan", err)
	}
}

func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ := newBackend(t, "")
	errs := map[string]error{
		"Set IfNotExists": b.Set(ctx, "k", nil, stash.SetOptions{IfNotExists: true}),
		"Expire":          b.Expire(ctx, "k", 0),
		"DeletePrefix":    b.DeletePrefix(ctx, "p"),
	}
	_, errs["Delete"] = b.Delete(ctx, "k")
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

// otherDriver is an rdb driver with a dialect stash does not support.
type otherDriver struct{}

func (otherDriver) Schemes() []string                     { return []string{"other"} }
func (otherDriver) Dialect() rdb.Dialect                  { return "oracle" }
func (otherDriver) Open(*url.URL) (rdb.Connection, error) { return rdb.Connection{}, nil }
func (otherDriver) Classify(error) error                  { return nil }

type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nopConn{}, nil }
func (nopConnector) Driver() driver.Driver                        { return nil }

type nopConn struct{}

func (nopConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (nopConn) Close() error                        { return nil }
func (nopConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func TestSubMillisecondTTL(t *testing.T) {
	ctx := context.Background()
	b, _ := newBackend(t, "")
	now := time.Now()
	b.now = func() time.Time { return now }

	short := stash.SetOptions{TTL: 900 * time.Microsecond, IfNotExists: true}
	if err := b.Set(ctx, "lock", []byte("a"), short); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(ctx, "lock", []byte("b"), short); !errors.Is(err, stash.ErrExists) {
		t.Fatalf("second Set with IfNotExists = %v, want ErrExists", err)
	}
	if d, err := b.TTL(ctx, "lock"); err != nil || d != time.Millisecond {
		t.Fatalf("TTL after Set = %v, %v; want 1ms", d, err)
	}

	if err := b.Set(ctx, "k", []byte("v"), stash.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Expire(ctx, "k", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if d, err := b.TTL(ctx, "k"); err != nil || d != time.Millisecond {
		t.Fatalf("TTL after Expire = %v, %v; want 1ms", d, err)
	}

	// Like the redis and valkey drivers, longer TTLs keep whole milliseconds.
	if err := b.Expire(ctx, "k", 1900*time.Microsecond); err != nil {
		t.Fatal(err)
	}
	if d, err := b.TTL(ctx, "k"); err != nil || d != time.Millisecond {
		t.Fatalf("TTL after Expire(1.9ms) = %v, %v; want 1ms", d, err)
	}
}
