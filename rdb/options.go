package rdb

import (
	"database/sql"
	"time"
)

// Option configures Open. Drivers, pool settings, and query behavior are all
// options.
type Option func(*options)

// options holds the settings Open collects from its Options.
type options struct {
	drivers        []Driver
	pool           Pool
	connInit       []string
	connectTimeout time.Duration
	openTimeout    time.Duration
	openAttempts   int
	openDelay      time.Duration
	noRebind       bool
	noStrictScan   bool
}

// Pool holds connection pool settings. A zero field keeps the default.
// Fields are named so a configuration loader can fill a Pool from a file or
// the environment; durations accept values such as "30m".
type Pool struct {
	// MaxOpenConns is the most connections open at once, idle or in use.
	// Zero means unlimited.
	MaxOpenConns int

	// MaxIdleConns is the most idle connections kept for reuse. Zero means
	// MaxOpenConns when that is set, and otherwise the larger of 2 and
	// WarmConns.
	MaxIdleConns int

	// ConnMaxLifetime closes a connection once it has been open this long.
	// Zero means no limit.
	ConnMaxLifetime time.Duration

	// ConnMaxIdleTime closes a connection once it has been idle this long.
	// Zero means no limit.
	ConnMaxIdleTime time.Duration

	// WarmConns is how many connections Open establishes before it returns,
	// so early queries do not wait to connect. It is not a maintained
	// minimum: the pool may close them later under the other limits.
	WarmConns int
}

// WithDriver makes d available to Open. Open uses the first driver whose
// schemes include the URL's scheme. Driver modules wrap this in their own
// Driver function. Callers must not pass a nil Driver.
func WithDriver(d Driver) Option {
	return func(o *options) { o.drivers = append(o.drivers, d) }
}

// WithPool sets every pool setting from p, replacing earlier pool options.
// Later pool options override individual fields.
func WithPool(p Pool) Option {
	return func(o *options) { o.pool = p }
}

// WithMaxOpenConns limits the number of open connections, idle or in use.
func WithMaxOpenConns(n int) Option {
	return func(o *options) { o.pool.MaxOpenConns = n }
}

// WithMaxIdleConns limits the number of idle connections kept for reuse.
func WithMaxIdleConns(n int) Option {
	return func(o *options) { o.pool.MaxIdleConns = n }
}

// WithConnMaxLifetime closes connections once they have been open for d.
func WithConnMaxLifetime(d time.Duration) Option {
	return func(o *options) { o.pool.ConnMaxLifetime = d }
}

// WithConnMaxIdleTime closes connections once they have been idle for d.
func WithConnMaxIdleTime(d time.Duration) Option {
	return func(o *options) { o.pool.ConnMaxIdleTime = d }
}

// WithWarmConns makes Open establish n connections before it returns.
func WithWarmConns(n int) Option {
	return func(o *options) { o.pool.WarmConns = n }
}

// WithConnInit runs queries, in order, on every new connection before the
// pool uses it, such as "SET TIME ZONE 'UTC'". A failing query discards the
// connection. Queries run exactly as written, without rebinding.
func WithConnInit(queries ...string) Option {
	return func(o *options) { o.connInit = append(o.connInit, queries...) }
}

// WithConnectTimeout limits how long opening one new connection may take,
// including the WithConnInit queries.
func WithConnectTimeout(d time.Duration) Option {
	return func(o *options) { o.connectTimeout = d }
}

// WithOpenTimeout limits how long Open may take in total: every ping
// attempt, the delays between them, and warming connections.
func WithOpenTimeout(d time.Duration) Option {
	return func(o *options) { o.openTimeout = d }
}

// WithOpenRetry makes Open ping the database up to attempts times, waiting
// delay between failed attempts, such as while a database container starts.
// Without it, Open pings once.
func WithOpenRetry(attempts int, delay time.Duration) Option {
	return func(o *options) {
		o.openAttempts = attempts
		o.openDelay = delay
	}
}

// WithoutRebind sends every query exactly as written. By default, a DB
// rewrites ? placeholders into the database's own form, such as $1 for
// Postgres. Use Raw to skip rebinding for single queries instead.
func WithoutRebind() Option {
	return func(o *options) { o.noRebind = true }
}

// WithoutStrictScan makes One, All, and Each ignore result columns that
// match no struct field. By default such a column fails with ErrScan.
func WithoutStrictScan() Option {
	return func(o *options) { o.noStrictScan = true }
}

// TxOption configures a transaction started by Tx.
type TxOption func(*sql.TxOptions)

// ReadOnly starts a read-only transaction.
func ReadOnly() TxOption {
	return func(o *sql.TxOptions) { o.ReadOnly = true }
}

// Isolation starts a transaction at level. Drivers return an error for a
// level their database does not support.
func Isolation(level sql.IsolationLevel) TxOption {
	return func(o *sql.TxOptions) { o.Isolation = level }
}
