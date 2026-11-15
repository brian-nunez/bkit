package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/brian-nunez/bkit/rdb"
)

// memory is the path of a private in-memory database.
const memory = ":memory:"

// memoryName prefixes the unique name that stands in for a private in-memory
// database.
const memoryName = "file:bkit-rdb-memory-"

// Config describes a SQLite database for Open.
type Config struct {
	// Path is the database file, created if missing. An empty path,
	// ":memory:", or a file: URI that SQLite opens in memory, such as
	// "file::memory:", opens an in-memory database.
	Path string
}

// Driver returns an rdb.Option that lets rdb.Open open sqlite: and sqlite3:
// URLs.
func Driver() rdb.Option {
	return rdb.WithDriver(sqliteDriver{})
}

// Open opens the database cfg describes, applying opts like rdb.Open.
func Open(ctx context.Context, cfg Config, opts ...rdb.Option) (*rdb.DB, error) {
	return rdb.OpenConnection(ctx, sqliteDriver{}, connection(cfg.Path, nil), opts...)
}

// sqliteDriver implements rdb.Driver.
type sqliteDriver struct{}

// Schemes returns the URL schemes the driver opens.
func (sqliteDriver) Schemes() []string {
	return []string{"sqlite", "sqlite3"}
}

// Dialect returns rdb.SQLite.
func (sqliteDriver) Dialect() rdb.Dialect {
	return rdb.SQLite
}

// Open returns a Connection for u. The path is the URL's opaque part, as in
// sqlite:app.db, or its host and path, as in sqlite:///var/lib/app.db.
func (sqliteDriver) Open(u *url.URL) (rdb.Connection, error) {
	path := u.Host + u.Path
	if u.Opaque != "" {
		var err error
		if path, err = url.PathUnescape(u.Opaque); err != nil {
			return rdb.Connection{}, fmt.Errorf("%w: bad escape in path", rdb.ErrInvalidURL)
		}
	}
	return connection(path, u.Query()), nil
}

// Classify maps SQLite result codes to rdb sentinels.
func (sqliteDriver) Classify(err error) error {
	e, ok := errors.AsType[*msqlite.Error](err)
	if !ok {
		return nil
	}
	switch e.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return rdb.ErrUniqueViolation
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return rdb.ErrForeignKeyViolation
	case sqlite3.SQLITE_CONSTRAINT_NOTNULL:
		return rdb.ErrNotNullViolation
	case sqlite3.SQLITE_CONSTRAINT_CHECK:
		return rdb.ErrCheckViolation
	}
	switch e.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return rdb.ErrRetryable
	}
	return nil
}

// connection builds the Connection for path. A _pragma in params replaces
// the default pragma of the same name.
func connection(path string, params url.Values) rdb.Connection {
	q := url.Values{}
	for k, vs := range params {
		q[k] = vs
	}
	inMemory, shared := memoryDB(path, q)
	defaults := []string{"foreign_keys(1)", "busy_timeout(5000)"}
	if !inMemory {
		defaults = append(defaults, "journal_mode(WAL)")
	}
	if inMemory && !shared {
		// A private in-memory database belongs to the one connection that
		// opened it and vanishes when the pool closes that connection. A
		// uniquely named shared-cache memory database stores data the same
		// way, a replacement connection reaches it, and the connector's
		// keep-alive connection holds it open until the DB is closed.
		path = memoryName + rand.Text()
		q.Set("mode", "memory")
		q.Set("cache", "shared")
	}
	for _, d := range defaults {
		if !slices.ContainsFunc(q["_pragma"], func(p string) bool { return pragmaName(p) == pragmaName(d) }) {
			q.Add("_pragma", d)
		}
	}
	conn := rdb.Connection{Connector: &connector{dsn: path + "?" + q.Encode(), keepAlive: inMemory}}
	if inMemory {
		// The pool keeps one connection to every in-memory database.
		// Connections that share a cache lock whole tables against each
		// other.
		conn.MaxOpenConns = 1
	}
	return conn
}

// memoryDB reports whether path and q, read the way SQLite reads them, open
// an in-memory database, and whether every connection that opens the same
// name shares it. SQLite reads mode and cache only from a file: URI, where
// the last value of a parameter wins, and the memdb VFS shares a database
// whose name starts with "/".
func memoryDB(path string, q url.Values) (inMemory, shared bool) {
	name, uri := strings.CutPrefix(path, "file:")
	var mode, cache string
	if uri {
		if authority, ok := strings.CutPrefix(name, "//"); ok {
			name = ""
			if i := strings.IndexByte(authority, '/'); i >= 0 {
				name = authority[i:]
			}
		}
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
		mode, cache = last(q["mode"]), last(q["cache"])
	}
	switch {
	case path == "" || name == memory || mode == "memory":
		return true, cache == "shared"
	case last(q["vfs"]) == "memdb":
		return true, cache == "shared" || strings.HasPrefix(name, "/")
	}
	return false, false
}

// last returns the last of vs, or "" when vs is empty.
func last(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[len(vs)-1]
}

// pragmaName returns the lowercased name of a _pragma value such as
// "busy_timeout(5000)" or "journal_mode = WAL".
func pragmaName(p string) string {
	if i := strings.IndexAny(p, "(= "); i >= 0 {
		p = p[:i]
	}
	return strings.ToLower(p)
}

// sqliteDrv opens SQLite connections.
var sqliteDrv = &msqlite.Driver{}

// openConn opens one SQLite connection. Tests replace it.
var openConn = sqliteDrv.Open

// errConnType reports a SQLite connection that lacks a driver interface the
// read-only wrapper forwards.
var errConnType = errors.New("sqlite: unexpected driver connection type")

// errClosed reports a Connect after the connector was closed.
var errClosed = errors.New("sqlite: connector is closed")

// connector opens connections to one database. With keepAlive set, it holds
// one extra connection open from the first Connect until Close, so an
// in-memory database outlives every pool connection.
type connector struct {
	dsn       string
	keepAlive bool

	mu     sync.Mutex
	keep   driver.Conn
	closed bool
}

// Connect opens a connection.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.keepAlive {
		if err := c.hold(); err != nil {
			return nil, err
		}
	}
	raw, err := openConn(c.dsn)
	if err != nil {
		return nil, err
	}
	sc, ok := raw.(sqliteConn)
	if !ok {
		_ = raw.Close()
		return nil, errConnType
	}
	return &conn{sqliteConn: sc}, nil
}

// hold opens the keep-alive connection unless it is already open.
func (c *connector) hold() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errClosed
	}
	if c.keep != nil {
		return nil
	}
	keep, err := openConn(c.dsn)
	if err != nil {
		return err
	}
	c.keep = keep
	return nil
}

// Close closes the keep-alive connection, freeing an in-memory database.
// database/sql calls it when the DB is closed.
func (c *connector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.keep == nil {
		return nil
	}
	err := c.keep.Close()
	c.keep = nil
	return err
}

// Driver returns the underlying SQLite driver.
func (*connector) Driver() driver.Driver {
	return sqliteDrv
}

// sqliteConn is the set of driver interfaces a SQLite connection implements.
// conn forwards all of them, so database/sql uses the connection exactly as
// it would unwrapped.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

// conn makes read-only transactions read-only. The SQLite driver begins every
// transaction the same way, so conn turns on PRAGMA query_only for a
// read-only one and turns it off again when the transaction ends.
type conn struct {
	sqliteConn

	// broken records that query_only could not be turned off, so the pool
	// must discard the connection.
	broken bool
}

// BeginTx begins a transaction, read-only when opts asks for one. SQLite
// transactions are serializable, which satisfies every weaker standard
// level, and readers see a snapshot. Any other level fails, as it does in
// the Postgres and MySQL drivers.
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	switch sql.IsolationLevel(opts.Isolation) {
	case sql.LevelDefault, sql.LevelReadUncommitted, sql.LevelReadCommitted,
		sql.LevelRepeatableRead, sql.LevelSnapshot, sql.LevelSerializable:
	default:
		return nil, fmt.Errorf("sqlite: unsupported isolation level: %v", opts.Isolation)
	}
	tx, err := c.sqliteConn.BeginTx(ctx, opts)
	if err != nil || !opts.ReadOnly {
		return tx, err
	}
	if _, err := c.ExecContext(ctx, "PRAGMA query_only = 1", nil); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &readOnlyTx{Tx: tx, conn: c}, nil
}

// IsValid reports whether the pool may reuse the connection.
func (c *conn) IsValid() bool {
	return !c.broken && c.sqliteConn.IsValid()
}

// readOnlyTx turns query_only off when its transaction ends.
type readOnlyTx struct {
	driver.Tx
	conn *conn
}

// Commit commits the transaction.
func (t *readOnlyTx) Commit() error {
	return t.end(t.Tx.Commit())
}

// Rollback rolls the transaction back.
func (t *readOnlyTx) Rollback() error {
	return t.end(t.Tx.Rollback())
}

// end turns query_only off and returns err, the transaction's result. When
// query_only stays on, the connection is marked broken so the pool closes it
// instead of handing it to a writer.
func (t *readOnlyTx) end(err error) error {
	if _, resetErr := t.conn.ExecContext(context.Background(), "PRAGMA query_only = 0", nil); resetErr != nil {
		t.conn.broken = true
	}
	return err
}
