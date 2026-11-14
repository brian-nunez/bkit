package sqlite

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
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
