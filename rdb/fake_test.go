package rdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"sync"
	"time"
)

// errUnique is the fake driver's unique-violation error.
var errUnique = errors.New("fake: duplicate key")

// errBusy is the fake driver's retryable error.
var errBusy = errors.New("fake: database is busy")

// fakeDB is an in-memory database/sql backend whose behavior tests script.
type fakeDB struct {
	mu sync.Mutex

	connectErr   error         // returned by every Connect
	failAfter    int           // when positive, Connect fails after this many successes
	connectDelay time.Duration // Connect waits this long, honoring ctx
	plainConns   bool          // connections lack ExecerContext
	pingErrs     []error       // consumed by successive pings
	beginErr     error
	commitErr    error
	rollbackErr  error
	exec         func(query string, args []driver.NamedValue) (driver.Result, error)
	query        func(query string, args []driver.NamedValue) (driver.Rows, error)

	connects  int
	log       []string
	txOpts    []driver.TxOptions
	commits   int
	rollbacks int
}

func (f *fakeDB) record(q string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, q)
}

func (f *fakeDB) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// fakeConnector implements driver.Connector over a fakeDB.
type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	f := c.db
	if f.connectDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.connectDelay):
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	if f.failAfter > 0 && f.connects >= f.failAfter {
		return nil, errors.New("fake: too many connections")
	}
	f.connects++
	if f.plainConns {
		return plainConn{}, nil
	}
	return &fakeConn{db: f}, nil
}

func (fakeConnector) Driver() driver.Driver { return fakeSQLDriver{} }

type fakeSQLDriver struct{}

func (fakeSQLDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake: use the connector")
}

// plainConn is a connection with only the required driver.Conn methods.
type plainConn struct{}

func (plainConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no prepare") }
func (plainConn) Close() error                        { return nil }
func (plainConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake: no begin") }

// fakeConn implements the context-aware connection interfaces.
type fakeConn struct {
	plainConn
	db *fakeDB
}

func (c *fakeConn) Ping(ctx context.Context) error {
	f := c.db
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pingErrs) == 0 {
		return nil
	}
	err := f.pingErrs[0]
	f.pingErrs = f.pingErrs[1:]
	return err
}

func (c *fakeConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.db.record(q)
	if c.db.exec != nil {
		return c.db.exec(q, args)
	}
	return driver.RowsAffected(1), nil
}

func (c *fakeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.db.record(q)
	if c.db.query != nil {
		return c.db.query(q, args)
	}
	return &fakeRows{cols: []string{"n"}, data: [][]driver.Value{{int64(1)}}}, nil
}

func (c *fakeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	f := c.db
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	f.txOpts = append(f.txOpts, opts)
	return fakeTx{db: f}, nil
}

type fakeTx struct{ db *fakeDB }

func (t fakeTx) Commit() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	t.db.commits++
	return t.db.commitErr
}

func (t fakeTx) Rollback() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	t.db.rollbacks++
	return t.db.rollbackErr
}

// fakeRows returns fixed rows, then err (io.EOF when nil).
type fakeRows struct {
	cols []string
	data [][]driver.Value
	err  error
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

// rowsOf returns a query function that always returns cols and data.
func rowsOf(cols []string, data ...[]driver.Value) func(string, []driver.NamedValue) (driver.Rows, error) {
	return func(string, []driver.NamedValue) (driver.Rows, error) {
		return &fakeRows{cols: cols, data: data}, nil
	}
}

// testDriver implements Driver over a fakeDB.
type testDriver struct {
	schemes []string
	dialect Dialect
	db      *fakeDB
	max     int
	openErr error
	opened  *url.URL
}

func (d *testDriver) Schemes() []string { return d.schemes }
func (d *testDriver) Dialect() Dialect  { return d.dialect }

func (d *testDriver) Open(u *url.URL) (Connection, error) {
	if d.openErr != nil {
		return Connection{}, d.openErr
	}
	d.opened = u
	return Connection{Connector: fakeConnector{db: d.db}, MaxOpenConns: d.max}, nil
}

func (d *testDriver) Classify(err error) error {
	switch {
	case errors.Is(err, errUnique):
		return ErrUniqueViolation
	case errors.Is(err, errBusy):
		return ErrRetryable
	}
	return nil
}

// newDriver returns a testDriver for scheme "fake" with a fresh fakeDB.
func newDriver(dialect Dialect) *testDriver {
	return &testDriver{schemes: []string{"fake"}, dialect: dialect, db: &fakeDB{}}
}
