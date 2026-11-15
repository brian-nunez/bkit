package mysql

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/brian-nunez/bkit/rdb"
)

// unreachable is a URL nothing listens on.
const unreachable = "mysql://app:s3cret-pw@127.0.0.1:1/app?timeout=1s"

func TestDriver(t *testing.T) {
	d := mysqlDriver{}
	if d.Dialect() != rdb.MySQL {
		t.Errorf("Dialect = %q", d.Dialect())
	}
	if got := d.Schemes(); len(got) != 2 || got[0] != "mysql" || got[1] != "mariadb" {
		t.Errorf("Schemes = %q", got)
	}
}

func TestDSN(t *testing.T) {
	tests := []struct {
		url        string
		user, pass string
		net, addr  string
		db         string
		parseTime  bool
		params     map[string]string
	}{
		{url: "mysql://app:secret@db.internal/app", user: "app", pass: "secret", net: "tcp", addr: "db.internal:3306", db: "app", parseTime: true},
		{url: "mysql://app:p%40ss%2Fw%3Ford@db:3307/app", user: "app", pass: "p@ss/w?ord", net: "tcp", addr: "db:3307", db: "app", parseTime: true},
		{url: "mysql://[::1]:3306/app", net: "tcp", addr: "[::1]:3306", db: "app", parseTime: true},
		{url: "mysql:///app?parseTime=false", net: "tcp", addr: "127.0.0.1:3306", db: "app"},
		{url: "mariadb://u@db/app?time_zone=%27%2B00%3A00%27&loc=America%2FNew_York", user: "u", net: "tcp", addr: "db:3306", db: "app", parseTime: true,
			params: map[string]string{"time_zone": "'+00:00'"}},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := mysql.ParseDSN(dsn(u))
			if err != nil {
				t.Fatalf("ParseDSN(%q): %v", dsn(u), err)
			}
			if cfg.User != tt.user || cfg.Passwd != tt.pass || cfg.Net != tt.net || cfg.Addr != tt.addr ||
				cfg.DBName != tt.db || cfg.ParseTime != tt.parseTime {
				t.Errorf("config = user %q pass %q net %q addr %q db %q parseTime %v",
					cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName, cfg.ParseTime)
			}
			for k, v := range tt.params {
				if cfg.Params[k] != v {
					t.Errorf("param %s = %q, want %q", k, cfg.Params[k], v)
				}
			}
		})
	}
}

func TestDSNParamValues(t *testing.T) {
	params := map[string]string{
		"charset":     "utf8mb4,utf8",
		"collation":   "utf8mb4_unicode_ci",
		"readTimeout": "500µs",
		"timeout":     "1m30s",
		"loc":         "America/New_York",
		"tls":         "skip-verify",
		"time_zone":   "'+00:00'",
		"sql_mode":    "'STRICT_ALL_TABLES,NO_ZERO_DATE'",
		"init":        "a&b=c%d e/f?g#h",
	}
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	for _, u := range []*url.URL{
		{Scheme: "mysql", Host: "db", Path: "/app", RawQuery: q.Encode()},
		Config{Host: "db", Database: "app", Params: params}.url(),
	} {
		got := dsn(u)
		cfg, err := mysql.ParseDSN(got)
		if err != nil {
			t.Fatalf("ParseDSN(%q): %v", got, err)
		}
		if cfg.DBName != "app" || cfg.Addr != "db:3306" {
			t.Errorf("db %q addr %q", cfg.DBName, cfg.Addr)
		}
		// The parsed charset list is unexported; FormatDSN writes it back.
		if !strings.Contains(cfg.FormatDSN(), "charset=utf8mb4,utf8") {
			t.Errorf("charset lost: %q", cfg.FormatDSN())
		}
		if cfg.Collation != "utf8mb4_unicode_ci" || cfg.ReadTimeout != 500*time.Microsecond ||
			cfg.Timeout != 90*time.Second || cfg.Loc.String() != "America/New_York" || cfg.TLSConfig != "skip-verify" {
			t.Errorf("config = collation %q readTimeout %v timeout %v loc %v tls %q",
				cfg.Collation, cfg.ReadTimeout, cfg.Timeout, cfg.Loc, cfg.TLSConfig)
		}
		for _, k := range []string{"time_zone", "sql_mode", "init"} {
			if cfg.Params[k] != params[k] {
				t.Errorf("param %s = %q, want %q", k, cfg.Params[k], params[k])
			}
		}
	}
}

func TestConfigURL(t *testing.T) {
	tests := []struct {
		cfg  Config
		want string
	}{
		{Config{}, "mysql:///"},
		{Config{Host: "db", Database: "app"}, "mysql://db/app"},
		{Config{Host: "db", Port: 3307, User: "app", Password: "p@ss", Params: map[string]string{"tls": "true"}},
			"mysql://app:p%40ss@db:3307/?tls=true"},
	}
	for _, tt := range tests {
		if got := tt.cfg.url().String(); got != tt.want {
			t.Errorf("url(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

func TestOpenUnreachable(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{unreachable, strings.Replace(unreachable, "mysql:", "mariadb:", 1)} {
		_, err := rdb.Open(ctx, raw, Driver(), rdb.WithOpenTimeout(5*time.Second))
		if err == nil {
			t.Fatalf("Open(%q) succeeded", raw)
		}
		if strings.Contains(err.Error(), "s3cret-pw") {
			t.Errorf("error leaks the password: %v", err)
		}
	}
	_, err := Open(ctx, Config{Host: "127.0.0.1", Port: 1, User: "app", Password: "s3cret-pw", Params: map[string]string{"timeout": "1s"}},
		rdb.WithOpenTimeout(5*time.Second))
	if err == nil || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("typed Open err = %v", err)
	}
}

func TestOpenInvalidSettings(t *testing.T) {
	ctx := context.Background()
	_, err := rdb.Open(ctx, "mysql://app:s3cret-pw@db/app?parseTime=maybe", Driver())
	if !errors.Is(err, rdb.ErrInvalidURL) || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("err = %v, want ErrInvalidURL without the password", err)
	}
	_, err = Open(ctx, Config{Host: "db", Password: "s3cret-pw", Params: map[string]string{"tls": "no-such-config"}})
	if !errors.Is(err, rdb.ErrInvalidURL) || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("typed err = %v, want ErrInvalidURL without the password", err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		number uint16
		want   error
	}{
		{1062, rdb.ErrUniqueViolation},
		{1451, rdb.ErrForeignKeyViolation},
		{1452, rdb.ErrForeignKeyViolation},
		{1048, rdb.ErrNotNullViolation},
		{1364, rdb.ErrNotNullViolation},
		{3819, rdb.ErrCheckViolation},
		{4025, rdb.ErrCheckViolation},
		{1213, rdb.ErrRetryable},
		{1205, rdb.ErrRetryable},
		{1064, nil},
	}
	for _, tt := range tests {
		err := fmt.Errorf("exec: %w", &mysql.MySQLError{Number: tt.number})
		if got := (mysqlDriver{}).Classify(err); !errors.Is(got, tt.want) || (tt.want == nil && got != nil) {
			t.Errorf("Classify(%d) = %v, want %v", tt.number, got, tt.want)
		}
	}
	if got := (mysqlDriver{}).Classify(errors.New("other")); got != nil {
		t.Errorf("Classify(non-MySQL error) = %v", got)
	}
}
