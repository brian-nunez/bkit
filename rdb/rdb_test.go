package rdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func mustOpen(t *testing.T, d *testDriver, opts ...Option) *DB {
	t.Helper()
	db, err := Open(context.Background(), "fake://user:secret@host/app", append([]Option{WithDriver(d)}, opts...)...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenPicksDriverByScheme(t *testing.T) {
	other := newDriver(MySQL)
	other.schemes = []string{"other"}
	first := newDriver(SQLite)
	second := newDriver(Postgres)
	db, err := Open(context.Background(), "FAKE://host/app", WithDriver(other), WithDriver(first), WithDriver(second))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if db.Dialect() != SQLite {
		t.Errorf("Dialect = %q, want the first matching driver's %q", db.Dialect(), SQLite)
	}
	if first.opened == nil || first.opened.Host != "host" {
		t.Errorf("driver got URL %v", first.opened)
	}
	if other.opened != nil || second.opened != nil {
		t.Error("a driver that did not match the scheme was opened")
	}
}

func TestOpenErrors(t *testing.T) {
	failing := newDriver(SQLite)
	failing.openErr = errors.New("bad path")
	tests := []struct {
		name string
		url  string
		opts []Option
		kind error
	}{
		{"unparsable", "fake://user:secret@host/%zz", []Option{WithDriver(newDriver(SQLite))}, ErrInvalidURL},
		{"no scheme", "app.db", []Option{WithDriver(newDriver(SQLite))}, ErrInvalidURL},
		{"unknown scheme", "mysql://user:secret@host/app", []Option{WithDriver(newDriver(SQLite))}, ErrUnknownDriver},
		{"no drivers", "fake://host", nil, ErrUnknownDriver},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(context.Background(), tt.url, tt.opts...)
			if !errors.Is(err, tt.kind) {
				t.Fatalf("err = %v, want %v", err, tt.kind)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaks credentials: %v", err)
			}
		})
	}

	_, err := Open(context.Background(), "fake://host", WithDriver(failing))
	if !errors.Is(err, failing.openErr) {
		t.Fatalf("err = %v, want driver error", err)
	}
	if e, ok := errors.AsType[*Error](err); !ok || e.Op != "open" {
		t.Errorf("err = %#v, want *Error with op open", err)
	}
}

func TestOpenValidatesOptions(t *testing.T) {
	tests := []struct {
		name string
		max  int
		opts []Option
	}{
		{"negative max open", 0, []Option{WithMaxOpenConns(-1)}},
		{"negative max idle", 0, []Option{WithMaxIdleConns(-1)}},
		{"negative lifetime", 0, []Option{WithConnMaxLifetime(-time.Second)}},
		{"negative idle time", 0, []Option{WithConnMaxIdleTime(-time.Second)}},
		{"negative warm", 0, []Option{WithWarmConns(-1)}},
		{"negative connect timeout", 0, []Option{WithConnectTimeout(-time.Second)}},
		{"negative open timeout", 0, []Option{WithOpenTimeout(-time.Second)}},
		{"negative retry delay", 0, []Option{WithOpenRetry(2, -time.Second)}},
		{"zero attempts", 0, []Option{WithOpenRetry(0, 0)}},
		{"above database limit", 1, []Option{WithMaxOpenConns(2)}},
		{"warm above max open", 0, []Option{WithMaxOpenConns(2), WithWarmConns(3)}},
		{"warm above max idle", 0, []Option{WithMaxIdleConns(2), WithWarmConns(3)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDriver(SQLite)
			d.max = tt.max
			_, err := Open(context.Background(), "fake://host", append([]Option{WithDriver(d)}, tt.opts...)...)
			if !errors.Is(err, ErrInvalidOption) {
				t.Fatalf("err = %v, want ErrInvalidOption", err)
			}
		})
	}
}

func TestOpenAppliesPool(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d, WithPool(Pool{
		MaxOpenConns:    9,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: time.Minute,
	}), WithMaxOpenConns(4), WithWarmConns(3))
	st := db.Stats()
	if st.MaxOpenConnections != 4 {
		t.Errorf("MaxOpenConnections = %d, want 4 (later option wins)", st.MaxOpenConnections)
	}
	if st.Idle != 3 {
		t.Errorf("Idle = %d, want 3 warm connections kept (idle defaults to max open)", st.Idle)
	}
}

func TestOpenDatabaseLimit(t *testing.T) {
	d := newDriver(SQLite)
	d.max = 1
	db := mustOpen(t, d)
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want the database limit 1", got)
	}
}

func TestOpenWarmWithoutMaxOpen(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d, WithWarmConns(5))
	if got := db.Stats().Idle; got != 5 {
		t.Errorf("Idle = %d, want 5", got)
	}

	d = newDriver(SQLite)
	db = mustOpen(t, d, WithMaxIdleConns(4), WithWarmConns(2))
	if got := db.Stats().Idle; got != 2 {
		t.Errorf("Idle = %d, want 2", got)
	}
}

func TestOpenWarmFailure(t *testing.T) {
	d := newDriver(SQLite)
	d.db.failAfter = 2
	_, err := Open(context.Background(), "fake://host", WithDriver(d), WithWarmConns(4))
	if err == nil || !strings.Contains(err.Error(), "too many connections") {
		t.Fatalf("err = %v, want warm-up failure", err)
	}
}

func TestOpenRetry(t *testing.T) {
	d := newDriver(SQLite)
	d.db.pingErrs = []error{errors.New("starting"), errors.New("starting")}
	mustOpen(t, d, WithOpenRetry(3, time.Millisecond))

	d = newDriver(SQLite)
	d.db.pingErrs = []error{errUnique, errUnique}
	_, err := Open(context.Background(), "fake://host", WithDriver(d), WithOpenRetry(2, 0))
	if !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("err = %v, want the classified ping error", err)
	}
}

func TestOpenTimeoutDuringRetry(t *testing.T) {
	d := newDriver(SQLite)
	d.db.pingErrs = []error{errors.New("starting")}
	_, err := Open(context.Background(), "fake://host", WithDriver(d),
		WithOpenRetry(5, time.Hour), WithOpenTimeout(10*time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestOpenConnectFailure(t *testing.T) {
	d := newDriver(SQLite)
	d.db.connectErr = errUnique
	_, err := Open(context.Background(), "fake://host", WithDriver(d))
	if !errors.Is(err, ErrUniqueViolation) || !errors.Is(err, errUnique) {
		t.Fatalf("err = %v, want classified driver error", err)
	}
}

func TestConnInit(t *testing.T) {
	d := newDriver(Postgres)
	db := mustOpen(t, d, WithConnInit("SET a = ?"), WithConnInit("SET b"), WithWarmConns(2))
	got := d.db.queries()
	want := []string{"SET a = ?", "SET b", "SET a = ?", "SET b"}
	if !slices.Equal(got, want) {
		t.Errorf("queries = %q, want %q (init runs unrebound on every connection)", got, want)
	}
	_ = db
}

func TestConnInitFailure(t *testing.T) {
	d := newDriver(SQLite)
	d.db.exec = func(string, []driver.NamedValue) (driver.Result, error) { return nil, errors.New("denied") }
	_, err := Open(context.Background(), "fake://host", WithDriver(d), WithConnInit("SET x"))
	if err == nil || !strings.Contains(err.Error(), `connection init "SET x": denied`) {
		t.Fatalf("err = %v", err)
	}

	d = newDriver(SQLite)
	d.db.plainConns = true
	_, err = Open(context.Background(), "fake://host", WithDriver(d), WithConnInit("SET x"))
	if !errors.Is(err, errNoExec) {
		t.Fatalf("err = %v, want errNoExec", err)
	}
}

func TestConnectTimeout(t *testing.T) {
	d := newDriver(SQLite)
	d.db.connectDelay = time.Hour
	_, err := Open(context.Background(), "fake://host", WithDriver(d), WithConnectTimeout(5*time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}

	d = newDriver(SQLite)
	d.db.connectDelay = time.Millisecond
	mustOpen(t, d, WithConnectTimeout(time.Minute))
}

func TestOpenConnection(t *testing.T) {
	d := newDriver(MySQL)
	db, err := OpenConnection(context.Background(), d, Connection{Connector: fakeConnector{db: d.db}}, WithMaxOpenConns(2))
	if err != nil {
		t.Fatalf("OpenConnection: %v", err)
	}
	defer db.Close()
	if db.Dialect() != MySQL || db.Stats().MaxOpenConnections != 2 {
		t.Errorf("dialect %q, max open %d", db.Dialect(), db.Stats().MaxOpenConnections)
	}
}

func TestDBMethods(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d)
	if db.SQL() == nil {
		t.Fatal("SQL returned nil")
	}
	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	d.db.pingErrs = []error{errUnique}
	if err := db.Ping(context.Background()); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("Ping err = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// closingConnector records whether database/sql closed it.
type closingConnector struct {
	fakeConnector
	closed *bool
}

func (c closingConnector) Close() error {
	*c.closed = true
	return nil
}

func TestInitConnectorClose(t *testing.T) {
	d := newDriver(SQLite)
	closed := false
	db, err := OpenConnection(context.Background(), d,
		Connection{Connector: closingConnector{fakeConnector{db: d.db}, &closed}},
		WithConnInit("SET x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil || !closed {
		t.Fatalf("Close = %v, connector closed = %v", err, closed)
	}

	// A connector without Close is left alone.
	db, err = OpenConnection(context.Background(), d,
		Connection{Connector: fakeConnector{db: d.db}}, WithConnectTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRebinding(t *testing.T) {
	d := newDriver(Postgres)
	db := mustOpen(t, d)
	ctx := context.Background()
	q := "SELECT ? WHERE data ? 'k'"
	if _, err := db.Exec(ctx, q, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Raw().Exec(ctx, q, 1); err != nil {
		t.Fatal(err)
	}
	if db.Raw().Dialect() != Postgres || db.Raw().Raw().Dialect() != Postgres {
		t.Error("Raw lost the dialect")
	}
	want := []string{"SELECT $1 WHERE data $2 'k'", q}
	if got := d.db.queries(); !slices.Equal(got, want) {
		t.Errorf("queries = %q, want %q", got, want)
	}

	for _, tc := range []struct {
		dialect Dialect
		opts    []Option
	}{{SQLite, nil}, {MySQL, nil}, {Postgres, []Option{WithoutRebind()}}} {
		d := newDriver(tc.dialect)
		db := mustOpen(t, d, tc.opts...)
		if _, err := db.Exec(ctx, "SELECT ?", 1); err != nil {
			t.Fatal(err)
		}
		if got := d.db.queries(); !slices.Equal(got, []string{"SELECT ?"}) {
			t.Errorf("%s: queries = %q, want unchanged", tc.dialect, got)
		}
	}
}

func TestExecAndQueryErrors(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d)
	ctx := context.Background()

	res, err := db.Exec(ctx, "INSERT")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Errorf("RowsAffected = %d", n)
	}
	rows, err := db.Query(ctx, "SELECT")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	d.db.exec = func(string, []driver.NamedValue) (driver.Result, error) { return nil, errUnique }
	d.db.query = func(string, []driver.NamedValue) (driver.Rows, error) { return nil, errUnique }
	_, err = db.Exec(ctx, "INSERT")
	if e, ok := errors.AsType[*Error](err); !ok || e.Op != "exec" || !errors.Is(err, ErrUniqueViolation) || !errors.Is(err, errUnique) {
		t.Errorf("Exec err = %v", err)
	}
	_, err = db.Query(ctx, "SELECT")
	if e, ok := errors.AsType[*Error](err); !ok || e.Op != "query" || !errors.Is(err, ErrUniqueViolation) {
		t.Errorf("Query err = %v", err)
	}

	// An *Error from below passes through unchanged.
	inner := &Error{Op: "custom", Kind: ErrCheckViolation}
	d.db.exec = func(string, []driver.NamedValue) (driver.Result, error) { return nil, inner }
	if _, err := db.Exec(ctx, "INSERT"); err != inner { // the same value, unwrapped
		t.Errorf("Exec err = %v, want the inner *Error", err)
	}

	// Context errors stay matchable.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.Exec(cctx, "INSERT"); !errors.Is(err, context.Canceled) {
		t.Errorf("Exec err = %v, want Canceled", err)
	}
}

func TestErrorFormatting(t *testing.T) {
	cause := errors.New("boom")
	tests := []struct {
		err  *Error
		want string
	}{
		{&Error{Op: "exec"}, "rdb: exec"},
		{&Error{Op: "exec", Kind: ErrUniqueViolation}, "rdb: exec: unique violation"},
		{&Error{Op: "exec", Err: cause}, "rdb: exec: boom"},
		{&Error{Op: "exec", Kind: ErrUniqueViolation, Err: cause}, "rdb: exec: unique violation: boom"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
	if got := (&Error{Op: "x"}).Unwrap(); len(got) != 0 {
		t.Errorf("Unwrap = %v, want empty", got)
	}
}

func TestTxCommit(t *testing.T) {
	d := newDriver(Postgres)
	db := mustOpen(t, d)
	ctx := context.Background()
	err := db.Tx(ctx, func(tx *Tx) error {
		if tx.SQL() == nil {
			t.Error("Tx.SQL returned nil")
		}
		if _, err := tx.Exec(ctx, "UPDATE a SET b = ?", 1); err != nil {
			return err
		}
		_, err := tx.Raw().Exec(ctx, "SELECT ?")
		return err
	}, ReadOnly(), Isolation(sql.LevelSerializable))
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	if d.db.commits != 1 || d.db.rollbacks != 0 {
		t.Errorf("commits %d, rollbacks %d", d.db.commits, d.db.rollbacks)
	}
	if got := d.db.queries(); !slices.Equal(got, []string{"UPDATE a SET b = $1", "SELECT ?"}) {
		t.Errorf("queries = %q", got)
	}
	opts := d.db.txOpts[0]
	if !opts.ReadOnly || opts.Isolation != driver.IsolationLevel(sql.LevelSerializable) {
		t.Errorf("tx options = %+v", opts)
	}
}

func TestTxRollback(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d)
	ctx := context.Background()
	fnErr := errors.New("fn failed")

	if err := db.Tx(ctx, func(*Tx) error { return fnErr }); err != fnErr { // fn's error, unwrapped
		t.Fatalf("err = %v, want fn's error", err)
	}
	if d.db.rollbacks != 1 || d.db.commits != 0 {
		t.Errorf("commits %d, rollbacks %d", d.db.commits, d.db.rollbacks)
	}

	d.db.rollbackErr = errUnique
	err := db.Tx(ctx, func(*Tx) error { return fnErr })
	if !errors.Is(err, fnErr) || !errors.Is(err, errUnique) {
		t.Fatalf("err = %v, want fn and rollback errors joined", err)
	}
}

func TestTxPanic(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d)
	defer func() {
		if r := recover(); r != "boom" {
			t.Fatalf("recover = %v, want the original panic", r)
		}
		if d.db.rollbacks != 1 {
			t.Errorf("rollbacks = %d, want 1", d.db.rollbacks)
		}
	}()
	_ = db.Tx(context.Background(), func(*Tx) error { panic("boom") })
}

func TestTxBeginAndCommitErrors(t *testing.T) {
	d := newDriver(SQLite)
	db := mustOpen(t, d)
	ctx := context.Background()

	d.db.beginErr = errUnique
	err := db.Tx(ctx, func(*Tx) error { t.Error("fn ran"); return nil })
	if e, ok := errors.AsType[*Error](err); !ok || e.Op != "begin" {
		t.Fatalf("err = %v, want begin *Error", err)
	}

	d.db.beginErr = nil
	d.db.commitErr = errors.New("serialization failure")
	err = db.Tx(ctx, func(*Tx) error { return nil })
	if e, ok := errors.AsType[*Error](err); !ok || e.Op != "commit" {
		t.Fatalf("err = %v, want commit *Error", err)
	}
	if d.db.rollbacks != 0 {
		t.Errorf("rollbacks = %d after a failed commit, want 0", d.db.rollbacks)
	}
}
