package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	msqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/brian-nunez/bkit/rdb"
)

// memory is the path of a private in-memory database.
const memory = ":memory:"

// Config describes a SQLite database for Open.
type Config struct {
	// Path is the database file, created if missing. An empty path or
	// ":memory:" opens a private in-memory database.
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
	inMemory := path == "" || path == memory
	if inMemory {
		path = memory
	}
	defaults := []string{"foreign_keys(1)", "busy_timeout(5000)"}
	if !inMemory {
		defaults = append(defaults, "journal_mode(WAL)")
	}
	q := url.Values{}
	for k, vs := range params {
		q[k] = vs
	}
	for _, d := range defaults {
		if !slices.ContainsFunc(q["_pragma"], func(p string) bool { return pragmaName(p) == pragmaName(d) }) {
			q.Add("_pragma", d)
		}
	}
	conn := rdb.Connection{Connector: connector{dsn: path + "?" + q.Encode()}}
	if inMemory {
		// Every connection to :memory: would open its own empty database.
		conn.MaxOpenConns = 1
	}
	return conn
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

// connector opens connections to one database.
type connector struct {
	dsn string
}

// Connect opens a connection.
func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sqliteDrv.Open(c.dsn)
}

// Driver returns the underlying SQLite driver.
func (connector) Driver() driver.Driver {
	return sqliteDrv
}
