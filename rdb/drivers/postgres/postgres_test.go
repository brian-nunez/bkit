package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/brian-nunez/bkit/rdb"
)

// unreachable is a URL nothing listens on.
const unreachable = "postgres://app:s3cret-pw@127.0.0.1:1/app?sslmode=disable&connect_timeout=1"

func TestDriver(t *testing.T) {
	d := pgDriver{}
	if d.Dialect() != rdb.Postgres {
		t.Errorf("Dialect = %q", d.Dialect())
	}
	if got := d.Schemes(); len(got) != 2 || got[0] != "postgres" || got[1] != "postgresql" {
		t.Errorf("Schemes = %q", got)
	}
}

func TestOpenUnreachable(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{unreachable, strings.Replace(unreachable, "postgres:", "postgresql:", 1)} {
		_, err := rdb.Open(ctx, raw, Driver(), rdb.WithOpenTimeout(5*time.Second))
		if err == nil {
			t.Fatalf("Open(%q) succeeded", raw)
		}
		if strings.Contains(err.Error(), "s3cret-pw") {
			t.Errorf("error leaks the password: %v", err)
		}
	}

	_, err := Open(ctx, Config{Host: "127.0.0.1", Port: 1, Database: "app", User: "app", Password: "s3cret-pw", SSLMode: "disable"},
		rdb.WithOpenTimeout(5*time.Second))
	if err == nil || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("typed Open err = %v", err)
	}
}

func TestOpenInvalidSettings(t *testing.T) {
	ctx := context.Background()
	_, err := rdb.Open(ctx, "postgres://app:s3cret-pw@db/app?sslmode=bogus", Driver())
	if !errors.Is(err, rdb.ErrInvalidURL) || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("err = %v, want ErrInvalidURL without the password", err)
	}
	_, err = Open(ctx, Config{Host: "db", Password: "s3cret-pw", SSLMode: "bogus"})
	if !errors.Is(err, rdb.ErrInvalidURL) || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("typed err = %v, want ErrInvalidURL without the password", err)
	}
}

func TestConfigURL(t *testing.T) {
	tests := []struct {
		cfg  Config
		want string
	}{
		{Config{}, "postgres:///"},
		{Config{Host: "db", Database: "app"}, "postgres://db/app"},
		{Config{Host: "::1", Port: 6432, Database: "app"}, "postgres://[::1]:6432/app"},
		{Config{Host: "::1", Database: "app"}, "postgres://[::1]/app"},
		{Config{Host: "fe80::1%eth0"}, "postgres://[fe80::1%25eth0]/"},
		{Config{Host: "127.0.0.1"}, "postgres://127.0.0.1/"},
		{Config{Host: "db", User: "app", Password: "p@ss/word"}, "postgres://app:p%40ss%2Fword@db/"},
		{
			Config{Host: "db", SSLMode: "require", Params: map[string]string{"application_name": "api", "search_path": "app"}},
			"postgres://db/?application_name=api&search_path=app&sslmode=require",
		},
	}
	for _, tt := range tests {
		if got := tt.cfg.url(); got != tt.want {
			t.Errorf("url(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
		if _, err := url.Parse(tt.cfg.url()); err != nil {
			t.Errorf("url(%+v) does not parse: %v", tt.cfg, err)
		}
	}
}

func TestConfigIPv6Host(t *testing.T) {
	for _, port := range []int{0, 6432} {
		cfg, err := pgx.ParseConfig(Config{Host: "::1", Port: port, Database: "app"}.url())
		if err != nil {
			t.Fatalf("port %d: %v", port, err)
		}
		want := uint16(5432)
		if port != 0 {
			want = uint16(port)
		}
		if cfg.Host != "::1" || cfg.Port != want || cfg.Database != "app" {
			t.Errorf("port %d: host %q port %d database %q; want ::1, %d, app", port, cfg.Host, cfg.Port, cfg.Database, want)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		code string
		want error
	}{
		{"23505", rdb.ErrUniqueViolation},
		{"23503", rdb.ErrForeignKeyViolation},
		{"23502", rdb.ErrNotNullViolation},
		{"23514", rdb.ErrCheckViolation},
		{"40001", rdb.ErrRetryable},
		{"40P01", rdb.ErrRetryable},
		{"42601", nil},
	}
	for _, tt := range tests {
		err := fmt.Errorf("exec: %w", &pgconn.PgError{Code: tt.code})
		if got := (pgDriver{}).Classify(err); !errors.Is(got, tt.want) || (tt.want == nil && got != nil) {
			t.Errorf("Classify(%s) = %v, want %v", tt.code, got, tt.want)
		}
	}
	if got := (pgDriver{}).Classify(errors.New("other")); got != nil {
		t.Errorf("Classify(non-Postgres error) = %v", got)
	}
}
