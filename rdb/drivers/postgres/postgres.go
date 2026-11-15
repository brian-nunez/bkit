package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/brian-nunez/bkit/rdb"
)

// Config describes a Postgres database for Open. Empty fields fall back to
// the pgx defaults, including the standard PG* environment variables.
type Config struct {
	// Host is the server's host name or IP address.
	Host string

	// Port is the server's port. Zero means 5432.
	Port int

	// Database is the database name.
	Database string

	// User is the role to connect as.
	User string

	// Password is the role's password.
	Password string

	// SSLMode is the TLS mode, such as "disable", "require", or
	// "verify-full". Empty means "prefer".
	SSLMode string

	// Params holds other connection parameters, such as
	// "application_name" or "search_path".
	Params map[string]string
}

// url returns the connection URL for c.
func (c Config) url() string {
	u := url.URL{Scheme: "postgres", Host: c.Host, Path: "/" + c.Database}
	if c.Port != 0 {
		u.Host = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	} else if ip, err := netip.ParseAddr(c.Host); err == nil && ip.Is6() {
		// A URL host needs brackets around an IPv6 address.
		u.Host = "[" + c.Host + "]"
	}
	if c.User != "" || c.Password != "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	q := url.Values{}
	for k, v := range c.Params {
		q.Set(k, v)
	}
	if c.SSLMode != "" {
		q.Set("sslmode", c.SSLMode)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Driver returns an rdb.Option that lets rdb.Open open postgres: and
// postgresql: URLs.
func Driver() rdb.Option {
	return rdb.WithDriver(pgDriver{})
}

// Open opens the database cfg describes, applying opts like rdb.Open.
func Open(ctx context.Context, cfg Config, opts ...rdb.Option) (*rdb.DB, error) {
	conn, err := connection(cfg.url())
	if err != nil {
		return nil, &rdb.Error{Op: "open", Err: err}
	}
	return rdb.OpenConnection(ctx, pgDriver{}, conn, opts...)
}

// pgDriver implements rdb.Driver.
type pgDriver struct{}

// Schemes returns the URL schemes the driver opens.
func (pgDriver) Schemes() []string {
	return []string{"postgres", "postgresql"}
}

// Dialect returns rdb.Postgres.
func (pgDriver) Dialect() rdb.Dialect {
	return rdb.Postgres
}

// Open returns a Connection for u. Query parameters are pgx connection
// parameters.
func (pgDriver) Open(u *url.URL) (rdb.Connection, error) {
	return connection(u.String())
}

// Classify maps Postgres SQLSTATE codes to rdb sentinels.
func (pgDriver) Classify(err error) error {
	e, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return nil
	}
	switch e.Code {
	case "23505": // unique_violation
		return rdb.ErrUniqueViolation
	case "23503": // foreign_key_violation
		return rdb.ErrForeignKeyViolation
	case "23502": // not_null_violation
		return rdb.ErrNotNullViolation
	case "23514": // check_violation
		return rdb.ErrCheckViolation
	case "40001", "40P01": // serialization_failure, deadlock_detected
		return rdb.ErrRetryable
	}
	return nil
}

// connection parses connString into a Connection. Parse errors from pgx
// quote the connection string, so they are replaced.
func connection(connString string) (rdb.Connection, error) {
	cfg, err := pgx.ParseConfig(connString)
	if err != nil {
		return rdb.Connection{}, fmt.Errorf("%w: pgx cannot parse the connection settings", rdb.ErrInvalidURL)
	}
	return rdb.Connection{Connector: stdlib.GetConnector(*cfg)}, nil
}
