package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brian-nunez/bkit/rdb"
)

func open(t *testing.T, rawURL string, opts ...rdb.Option) *rdb.DB {
	t.Helper()
	db, err := rdb.Open(context.Background(), rawURL, append([]rdb.Option{Driver()}, opts...)...)
	if err != nil {
		t.Fatalf("Open(%q): %v", rawURL, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func pragma[T any](t *testing.T, db *rdb.DB, name string) T {
	t.Helper()
	v, err := rdb.One[T](context.Background(), db, "PRAGMA "+name)
	if err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

func TestURLForms(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, url string
	}{
		{"opaque", "sqlite:" + filepath.Join(dir, "a.db")},
		{"escaped opaque", "sqlite:" + filepath.Join(dir, "b%20c.db")},
		{"absolute", "sqlite://" + filepath.Join(dir, "d.db")},
		{"sqlite3 scheme", "sqlite3:" + filepath.Join(dir, "e.db")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := open(t, tt.url)
			if got := pragma[string](t, db, "journal_mode"); got != "wal" {
				t.Errorf("journal_mode = %q, want wal", got)
			}
			if got := pragma[int](t, db, "foreign_keys"); got != 1 {
				t.Errorf("foreign_keys = %d, want 1", got)
			}
			if got := pragma[int](t, db, "busy_timeout"); got != 5000 {
				t.Errorf("busy_timeout = %d, want 5000", got)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "b c.db")); err != nil {
		t.Errorf("escaped path: %v", err)
	}
}

func TestBadEscape(t *testing.T) {
	_, err := rdb.Open(context.Background(), "sqlite:a%zz.db", Driver())
	if !errors.Is(err, rdb.ErrInvalidURL) {
		t.Fatalf("err = %v, want ErrInvalidURL", err)
	}
}

func TestParamsOverrideDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db := open(t, "sqlite:"+path+"?_pragma=Busy_Timeout(10)&_pragma=journal_mode%20=%20DELETE")
	if got := pragma[int](t, db, "busy_timeout"); got != 10 {
		t.Errorf("busy_timeout = %d, want 10", got)
	}
	if got := pragma[string](t, db, "journal_mode"); got != "delete" {
		t.Errorf("journal_mode = %q, want delete", got)
	}
}

func TestInMemory(t *testing.T) {
	ctx := context.Background()
	for _, rawURL := range []string{"sqlite::memory:", "sqlite:"} {
		db := open(t, rawURL)
		if got := db.Stats().MaxOpenConnections; got != 1 {
			t.Errorf("%s: MaxOpenConnections = %d, want 1", rawURL, got)
		}
		if got := pragma[int](t, db, "foreign_keys"); got != 1 {
			t.Errorf("%s: foreign_keys = %d, want 1", rawURL, got)
		}
	}
	_, err := rdb.Open(ctx, "sqlite::memory:", Driver(), rdb.WithMaxOpenConns(2))
	if !errors.Is(err, rdb.ErrInvalidOption) {
		t.Errorf("err = %v, want ErrInvalidOption", err)
	}
}

func TestInMemoryOutlivesConnections(t *testing.T) {
	ctx := context.Background()
	db := open(t, "sqlite::memory:")
	// With no idle connections allowed, the pool closes the connection after
	// every statement, so each statement runs on a new one.
	db.SQL().SetMaxIdleConns(0)
	for _, q := range []string{"CREATE TABLE t (n INTEGER)", "INSERT INTO t VALUES (1)"} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n, err := rdb.One[int](ctx, db, "SELECT count(*) FROM t"); err != nil || n != 1 {
		t.Fatalf("count = %d, %v; want 1, nil", n, err)
	}
	if got := db.Stats().MaxIdleClosed; got < 2 {
		t.Errorf("MaxIdleClosed = %d, want at least 2 replaced connections", got)
	}
	if got := pragma[int](t, db, "foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d, want 1", got)
	}

	other := open(t, "sqlite::memory:")
	if _, err := other.Exec(ctx, "SELECT * FROM t"); err == nil {
		t.Error("a second in-memory database sees the first one's table")
	}
}

func TestInMemoryFreedOnClose(t *testing.T) {
	ctx := context.Background()
	c := connection(memory, nil).Connector.(*connector)
	db := sql.OpenDB(c)
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Connect(ctx); !errors.Is(err, errClosed) {
		t.Errorf("Connect after Close: err = %v, want errClosed", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	after := sql.OpenDB(&connector{dsn: c.dsn})
	defer after.Close()
	var n int
	if err := after.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&n); err != nil || n != 0 {
		t.Errorf("tables after Close = %d, %v; want 0, nil", n, err)
	}
}

func TestTypedOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "typed.db")
	db, err := Open(ctx, Config{Path: path}, rdb.WithMaxOpenConns(3))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Dialect() != rdb.SQLite || db.Stats().MaxOpenConnections != 3 {
		t.Errorf("dialect %q, max open %d", db.Dialect(), db.Stats().MaxOpenConnections)
	}

	mem, err := Open(ctx, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	if mem.Stats().MaxOpenConnections != 1 {
		t.Errorf("in-memory max open = %d", mem.Stats().MaxOpenConnections)
	}
}

func TestOpenFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "dir", "x.db")
	_, err := Open(context.Background(), Config{Path: missing})
	if err == nil {
		t.Fatal("Open in a missing directory succeeded")
	}
}

func TestClassify(t *testing.T) {
	ctx := context.Background()
	db := open(t, "sqlite::memory:")
	schema := []string{
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		`CREATE TABLE kids (
			id INTEGER PRIMARY KEY,
			email TEXT UNIQUE,
			name TEXT NOT NULL,
			age INTEGER CHECK (age >= 0),
			parent_id INTEGER REFERENCES parents(id)
		)`,
		"INSERT INTO kids (id, email, name, age) VALUES (1, 'a@x', 'a', 1)",
	}
	for _, q := range schema {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	tests := []struct {
		name, query string
		want        error
	}{
		{"unique", "INSERT INTO kids (email, name) VALUES ('a@x', 'b')", rdb.ErrUniqueViolation},
		{"primary key", "INSERT INTO kids (id, name) VALUES (1, 'b')", rdb.ErrUniqueViolation},
		{"not null", "INSERT INTO kids (email) VALUES ('c@x')", rdb.ErrNotNullViolation},
		{"check", "INSERT INTO kids (name, age) VALUES ('d', -1)", rdb.ErrCheckViolation},
		{"foreign key", "INSERT INTO kids (name, parent_id) VALUES ('e', 99)", rdb.ErrForeignKeyViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.Exec(ctx, tt.query); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}

	_, err := db.Exec(ctx, "NOT SQL")
	var e *rdb.Error
	if !errors.As(err, &e) || e.Kind != nil {
		t.Errorf("syntax error: err = %v, want an unclassified *rdb.Error", err)
	}
	if got := (sqliteDriver{}).Classify(errors.New("other")); got != nil {
		t.Errorf("Classify(non-SQLite error) = %v, want nil", got)
	}
}

func TestBusy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy.db")
	db := open(t, "sqlite:"+path+"?_pragma=busy_timeout(0)")
	if _, err := db.Exec(ctx, "CREATE TABLE t (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	holder, err := db.SQL().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = holder.ExecContext(ctx, "ROLLBACK") }()

	if _, err := db.Exec(ctx, "INSERT INTO t VALUES (1)"); !errors.Is(err, rdb.ErrRetryable) {
		t.Fatalf("err = %v, want ErrRetryable", err)
	}
}

func TestConnector(t *testing.T) {
	c := connection("", url.Values{}).Connector
	if c.Driver() == nil {
		t.Fatal("Driver returned nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect err = %v, want Canceled", err)
	}
}

func TestReadOnlyTx(t *testing.T) {
	ctx := context.Background()
	db := open(t, "sqlite::memory:") // one connection, so every step reuses it
	if _, err := db.Exec(ctx, "CREATE TABLE t (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	insert := func(q rdb.Querier) error {
		_, err := q.Exec(ctx, "INSERT INTO t VALUES (1)")
		return err
	}

	errStop := errors.New("stop")
	for _, end := range []error{nil, errStop} { // commit, then rollback
		err := db.Tx(ctx, func(tx *rdb.Tx) error {
			if _, err := rdb.One[int](ctx, tx, "SELECT count(*) FROM t"); err != nil {
				t.Errorf("read in read-only tx: %v", err)
			}
			if err := insert(tx); err == nil || !strings.Contains(err.Error(), "readonly") {
				t.Errorf("write in read-only tx: err = %v, want a readonly error", err)
			}
			return end
		}, rdb.ReadOnly())
		if !errors.Is(err, end) {
			t.Fatalf("Tx err = %v, want %v", err, end)
		}
		if got := pragma[int](t, db, "query_only"); got != 0 {
			t.Errorf("query_only = %d after the read-only tx, want 0", got)
		}
		if err := insert(db); err != nil {
			t.Errorf("write after the read-only tx: %v", err)
		}
	}

	if err := db.Tx(ctx, func(tx *rdb.Tx) error { return insert(tx) }); err != nil {
		t.Errorf("read-write tx: %v", err)
	}
}

// failingConn fails ExecContext for one query.
type failingConn struct {
	sqliteConn
	fail string
}

func (c failingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if q == c.fail {
		return nil, errors.New("exec failed")
	}
	return c.sqliteConn.ExecContext(ctx, q, args)
}

func rawConn(t *testing.T) sqliteConn {
	t.Helper()
	raw, err := sqliteDrv.Open(memory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw.(sqliteConn)
}

func TestReadOnlyTxFailures(t *testing.T) {
	ctx := context.Background()
	ro := driver.TxOptions{ReadOnly: true}

	// The begin itself fails: a transaction is already open.
	c := &conn{sqliteConn: rawConn(t)}
	tx, err := c.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginTx(ctx, ro); err == nil {
		t.Error("nested BeginTx succeeded")
	}
	_ = tx.Rollback()

	// Turning query_only on fails: the transaction is rolled back.
	c = &conn{sqliteConn: failingConn{sqliteConn: rawConn(t), fail: "PRAGMA query_only = 1"}}
	if _, err := c.BeginTx(ctx, ro); err == nil {
		t.Fatal("BeginTx succeeded")
	}
	tx, err = c.BeginTx(ctx, driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx after a failed read-only begin: %v (the first was not rolled back)", err)
	}
	_ = tx.Rollback()
	if !c.IsValid() {
		t.Error("connection invalid after a failed begin")
	}

	// Turning query_only off fails: the pool must discard the connection.
	c = &conn{sqliteConn: failingConn{sqliteConn: rawConn(t), fail: "PRAGMA query_only = 0"}}
	tx, err = c.BeginTx(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if c.IsValid() {
		t.Error("connection still valid with query_only left on")
	}
}

func TestConnectFailures(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "no", "such", "x.db")
	if _, err := (&connector{dsn: missing}).Connect(ctx); err == nil {
		t.Error("Connect to a missing directory succeeded")
	}
	if _, err := (&connector{dsn: missing, keepAlive: true}).Connect(ctx); err == nil {
		t.Error("Connect with a keep-alive to a missing directory succeeded")
	}

	defer func(orig func(string) (driver.Conn, error)) { openConn = orig }(openConn)
	closed := false
	openConn = func(string) (driver.Conn, error) { return plainConn{closed: &closed}, nil }
	if _, err := (&connector{dsn: memory}).Connect(ctx); !errors.Is(err, errConnType) || !closed {
		t.Errorf("Connect err = %v, closed = %v; want errConnType and a closed connection", err, closed)
	}
}

// plainConn implements only driver.Conn.
type plainConn struct{ closed *bool }

func (plainConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (plainConn) Begin() (driver.Tx, error)           { return nil, errors.New("no begin") }
func (c plainConn) Close() error                      { *c.closed = true; return nil }
