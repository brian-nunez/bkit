package rdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Dialect names the SQL dialect a database speaks.
type Dialect string

// Dialects of the drivers bkit provides.
const (
	// SQLite is the dialect of SQLite. It uses ? placeholders.
	SQLite Dialect = "sqlite"

	// Postgres is the dialect of PostgreSQL. It uses $1, $2, ...
	// placeholders, so a DB rebinds ? placeholders for it.
	Postgres Dialect = "postgres"

	// MySQL is the dialect of MySQL and MariaDB. It uses ? placeholders.
	MySQL Dialect = "mysql"
)

// Driver connects rdb to one kind of database. Driver modules implement it;
// applications pass a driver to Open through the driver module's Driver
// function.
type Driver interface {
	// Schemes returns the lowercase URL schemes the driver opens, such as
	// "postgres" and "postgresql".
	Schemes() []string

	// Dialect returns the dialect of the driver's database.
	Dialect() Dialect

	// Open returns a Connection for u. It must not connect, and its errors
	// must not contain u or its credentials.
	Open(u *url.URL) (Connection, error)

	// Classify returns the sentinel error matching err, such as
	// ErrUniqueViolation, or nil when none matches. err may come from any
	// layer, including context errors.
	Classify(err error) error
}

// Connection describes how to connect to one database.
type Connection struct {
	// Connector opens connections to the database.
	Connector driver.Connector

	// MaxOpenConns, when positive, is the most connections the database
	// supports, such as 1 for an in-memory SQLite database. Open uses it as
	// the pool limit and rejects a larger WithMaxOpenConns.
	MaxOpenConns int
}

// Querier runs queries. *DB, *Tx, and the value Raw returns implement it, so
// code that accepts a Querier works both inside and outside a transaction.
type Querier interface {
	// Exec runs a query that returns no rows.
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Query runs a query and returns its rows. The caller must close them.
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)

	// Dialect returns the dialect of the database.
	Dialect() Dialect

	// Raw returns a Querier that sends queries exactly as written, without
	// rebinding placeholders.
	Raw() Querier
}

// DB is a pool of connections to one database. Create one with Open; the
// zero value is not usable. A DB is safe for concurrent use.
type DB struct {
	session
	db *sql.DB
}

// Open connects to the database that rawURL names and returns its pool.
// Pass the drivers the application supports with their Driver options; the
// URL's scheme picks one, and only that driver connects. Open pings the
// database, warms connections when WithWarmConns asks, and honors ctx.
func Open(ctx context.Context, rawURL string, opts ...Option) (*DB, error) {
	o := collect(opts)
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		// url.Parse errors quote the URL, which may hold credentials.
		return nil, &Error{Op: "open", Kind: ErrInvalidURL}
	}
	i := slices.IndexFunc(o.drivers, func(d Driver) bool {
		return slices.ContainsFunc(d.Schemes(), func(s string) bool {
			return strings.EqualFold(s, u.Scheme)
		})
	})
	if i < 0 {
		return nil, &Error{Op: "open", Kind: ErrUnknownDriver, Err: fmt.Errorf("scheme %q", u.Scheme)}
	}
	d := o.drivers[i]
	conn, err := d.Open(u)
	if err != nil {
		return nil, &Error{Op: "open", Err: err}
	}
	return open(ctx, d, conn, o)
}

// OpenConnection opens a pool over a Connection that d built, applying opts
// like Open. Driver modules call it from typed Open functions; applications
// use Open or those functions instead. Driver options in opts are ignored.
func OpenConnection(ctx context.Context, d Driver, conn Connection, opts ...Option) (*DB, error) {
	return open(ctx, d, conn, collect(opts))
}

// collect applies opts to a fresh options value.
func collect(opts []Option) *options {
	o := &options{openAttempts: 1}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// open validates the pool settings, opens the pool, pings it, and warms it.
func open(ctx context.Context, d Driver, conn Connection, o *options) (*DB, error) {
	p := o.pool
	if err := validate(p, o, conn); err != nil {
		return nil, err
	}
	if conn.MaxOpenConns > 0 && p.MaxOpenConns == 0 {
		p.MaxOpenConns = conn.MaxOpenConns
	}
	if p.MaxIdleConns == 0 {
		switch {
		case p.MaxOpenConns > 0:
			p.MaxIdleConns = p.MaxOpenConns
		case p.WarmConns > 2:
			p.MaxIdleConns = p.WarmConns
		}
	}
	if p.WarmConns > p.MaxIdleConns && p.MaxIdleConns > 0 {
		return nil, invalidOption("WarmConns %d exceeds MaxIdleConns %d", p.WarmConns, p.MaxIdleConns)
	}

	connector := conn.Connector
	if len(o.connInit) > 0 || o.connectTimeout > 0 {
		connector = &initConnector{Connector: conn.Connector, init: o.connInit, timeout: o.connectTimeout}
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(p.MaxOpenConns)
	if p.MaxIdleConns > 0 {
		db.SetMaxIdleConns(p.MaxIdleConns)
	}
	db.SetConnMaxLifetime(p.ConnMaxLifetime)
	db.SetConnMaxIdleTime(p.ConnMaxIdleTime)

	s := session{
		run:     db,
		driver:  d,
		dialect: d.Dialect(),
		rebind:  !o.noRebind,
		strict:  !o.noStrictScan,
	}
	if o.openTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.openTimeout)
		defer cancel()
	}
	if err := connect(ctx, db, o, p.WarmConns); err != nil {
		_ = db.Close()
		return nil, s.wrap("open", err)
	}
	return &DB{session: s, db: db}, nil
}

// validate rejects negative settings and limits the database cannot honor.
func validate(p Pool, o *options, conn Connection) error {
	for _, v := range []struct {
		name  string
		value int64
	}{
		{"MaxOpenConns", int64(p.MaxOpenConns)},
		{"MaxIdleConns", int64(p.MaxIdleConns)},
		{"ConnMaxLifetime", int64(p.ConnMaxLifetime)},
		{"ConnMaxIdleTime", int64(p.ConnMaxIdleTime)},
		{"WarmConns", int64(p.WarmConns)},
		{"ConnectTimeout", int64(o.connectTimeout)},
		{"OpenTimeout", int64(o.openTimeout)},
		{"OpenRetry delay", int64(o.openDelay)},
	} {
		if v.value < 0 {
			return invalidOption("%s is negative", v.name)
		}
	}
	if o.openAttempts < 1 {
		return invalidOption("OpenRetry attempts must be at least 1")
	}
	if conn.MaxOpenConns > 0 && p.MaxOpenConns > conn.MaxOpenConns {
		return invalidOption("MaxOpenConns %d exceeds the database limit of %d", p.MaxOpenConns, conn.MaxOpenConns)
	}
	if p.MaxOpenConns > 0 && p.WarmConns > p.MaxOpenConns {
		return invalidOption("WarmConns %d exceeds MaxOpenConns %d", p.WarmConns, p.MaxOpenConns)
	}
	return nil
}

// invalidOption returns an *Error of kind ErrInvalidOption.
func invalidOption(format string, args ...any) error {
	return &Error{Op: "open", Kind: ErrInvalidOption, Err: fmt.Errorf(format, args...)}
}

// connect pings db until it answers or the attempts run out, then holds warm
// connections at once so the pool keeps them idle.
func connect(ctx context.Context, db *sql.DB, o *options, warm int) error {
	var err error
	for attempt := 1; ; attempt++ {
		if err = db.PingContext(ctx); err == nil || attempt == o.openAttempts {
			break
		}
		t := time.NewTimer(o.openDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
	if err != nil {
		return err
	}
	conns := make([]*sql.Conn, 0, warm)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range warm {
		c, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		conns = append(conns, c)
	}
	return nil
}

// Close closes the pool. Queries in progress finish first.
func (db *DB) Close() error {
	return db.db.Close()
}

// Ping verifies that the database is reachable, connecting if needed.
func (db *DB) Ping(ctx context.Context) error {
	return db.wrap("ping", db.db.PingContext(ctx))
}

// Stats returns the pool's statistics.
func (db *DB) Stats() sql.DBStats {
	return db.db.Stats()
}

// SQL returns the underlying pool, for libraries that need a *sql.DB.
// Queries run through it are not rebound and their errors not classified.
func (db *DB) SQL() *sql.DB {
	return db.db
}

// initConnector runs init queries on every new connection, within an
// optional time limit.
type initConnector struct {
	driver.Connector
	init    []string
	timeout time.Duration
}

// Close closes the wrapped connector when it holds resources, as
// database/sql does for a connector that implements io.Closer.
func (c *initConnector) Close() error {
	if closer, ok := c.Connector.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// errNoExec reports a driver connection that cannot run init queries.
var errNoExec = errors.New("rdb: driver connection does not support ExecContext")

// Connect opens a connection and runs the init queries on it.
func (c *initConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if len(c.init) == 0 {
		return conn, nil
	}
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, errNoExec
	}
	for _, q := range c.init {
		if _, err := execer.ExecContext(ctx, q, nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("connection init %q: %w", q, err)
		}
	}
	return conn, nil
}
