package mysql

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"github.com/brian-nunez/bkit/rdb"
)

// Config describes a MySQL or MariaDB database for Open.
type Config struct {
	// Host is the server's host name or IP address. Empty means 127.0.0.1.
	Host string

	// Port is the server's port. Zero means 3306.
	Port int

	// Database is the database name.
	Database string

	// User is the account to connect as.
	User string

	// Password is the account's password.
	Password string

	// Params holds driver parameters, such as "tls" or "timeout", and
	// session variables to set on every connection.
	Params map[string]string
}

// url returns the connection URL for c.
func (c Config) url() *url.URL {
	u := &url.URL{Scheme: "mysql", Host: c.Host, Path: "/" + c.Database}
	if c.Port != 0 {
		u.Host = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	}
	if c.User != "" || c.Password != "" {
		u.User = url.UserPassword(c.User, c.Password)
	}
	q := url.Values{}
	for k, v := range c.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u
}

// Driver returns an rdb.Option that lets rdb.Open open mysql: and mariadb:
// URLs.
func Driver() rdb.Option {
	return rdb.WithDriver(mysqlDriver{})
}

// Open opens the database cfg describes, applying opts like rdb.Open.
func Open(ctx context.Context, cfg Config, opts ...rdb.Option) (*rdb.DB, error) {
	conn, err := mysqlDriver{}.Open(cfg.url())
	if err != nil {
		return nil, &rdb.Error{Op: "open", Err: err}
	}
	return rdb.OpenConnection(ctx, mysqlDriver{}, conn, opts...)
}

// mysqlDriver implements rdb.Driver.
type mysqlDriver struct{}

// Schemes returns the URL schemes the driver opens.
func (mysqlDriver) Schemes() []string {
	return []string{"mysql", "mariadb"}
}

// Dialect returns rdb.MySQL.
func (mysqlDriver) Dialect() rdb.Dialect {
	return rdb.MySQL
}

// Open returns a Connection for u. Query parameters are go-sql-driver/mysql
// DSN parameters. parseTime defaults to true.
func (mysqlDriver) Open(u *url.URL) (rdb.Connection, error) {
	conn, err := mysql.MySQLDriver{}.OpenConnector(dsn(u))
	if err != nil {
		// The DSN holds the password, so the parser's message is dropped.
		return rdb.Connection{}, fmt.Errorf("%w: go-sql-driver/mysql cannot parse the connection settings", rdb.ErrInvalidURL)
	}
	return rdb.Connection{Connector: conn}, nil
}

// dsn converts u to a go-sql-driver/mysql DSN.
func dsn(u *url.URL) string {
	cfg := mysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	if u.Host != "" {
		cfg.Net = "tcp"
		cfg.Addr = u.Host
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	if !q.Has("parseTime") {
		q.Set("parseTime", "true")
	}
	var b strings.Builder
	b.WriteString(cfg.FormatDSN())
	sep := byte('?')
	for _, k := range slices.Sorted(maps.Keys(q)) {
		for _, v := range q[k] {
			b.WriteByte(sep)
			sep = '&'
			b.WriteString(dsnEscape(k))
			b.WriteByte('=')
			b.WriteString(dsnEscape(v))
		}
	}
	return b.String()
}

// dsnEscape percent-encodes s for a DSN parameter. The driver unescapes some
// values, such as loc and session variables, but reads others verbatim,
// such as the comma-separated charset list and durations like "500µs". So
// only bytes that the DSN parser or url.QueryUnescape would misread are
// encoded, and commas and non-ASCII bytes stay as they are.
func dsnEscape(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c >= utf8.RuneSelf || c == ',' || c == '-' || c == '.' || c == '_' || c == '~' ||
			('0' <= c && c <= '9') || ('a' <= c|0x20 && c|0x20 <= 'z') {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// Classify maps MySQL and MariaDB error numbers to rdb sentinels.
func (mysqlDriver) Classify(err error) error {
	e, ok := errors.AsType[*mysql.MySQLError](err)
	if !ok {
		return nil
	}
	switch e.Number {
	case 1062: // ER_DUP_ENTRY
		return rdb.ErrUniqueViolation
	case 1451, 1452: // ER_ROW_IS_REFERENCED_2, ER_NO_REFERENCED_ROW_2
		return rdb.ErrForeignKeyViolation
	case 1048, 1364: // ER_BAD_NULL_ERROR, ER_NO_DEFAULT_FOR_FIELD
		return rdb.ErrNotNullViolation
	case 3819, 4025: // MySQL ER_CHECK_CONSTRAINT_VIOLATED, MariaDB ER_CONSTRAINT_FAILED
		return rdb.ErrCheckViolation
	case 1213, 1205: // ER_LOCK_DEADLOCK, ER_LOCK_WAIT_TIMEOUT
		return rdb.ErrRetryable
	}
	return nil
}
