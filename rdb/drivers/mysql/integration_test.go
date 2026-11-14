package mysql

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/brian-nunez/bkit/rdb"
)

// TestIntegration runs against the server named by RDB_MYSQL_URL. See
// docker-compose.yml.
func TestIntegration(t *testing.T) {
	raw := os.Getenv("RDB_MYSQL_URL")
	if raw == "" {
		t.Skip("set RDB_MYSQL_URL to run integration tests (see docker-compose.yml)")
	}
	ctx := context.Background()
	db, err := rdb.Open(ctx, raw, Driver(),
		rdb.WithMaxOpenConns(4),
		rdb.WithConnInit("SET time_zone = '+00:00'"),
		rdb.WithOpenRetry(30, time.Second))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	setup := []string{
		"DROP TABLE IF EXISTS rdb_kids, rdb_parents",
		"CREATE TABLE rdb_parents (id BIGINT PRIMARY KEY) ENGINE=InnoDB",
		`CREATE TABLE rdb_kids (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			email VARCHAR(255) UNIQUE,
			name VARCHAR(255) NOT NULL,
			age INT CHECK (age >= 0),
			parent_id BIGINT,
			created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
			FOREIGN KEY (parent_id) REFERENCES rdb_parents(id)
		) ENGINE=InnoDB`,
	}
	for _, q := range setup {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), "DROP TABLE IF EXISTS rdb_kids, rdb_parents") })

	if _, err := db.Exec(ctx, "INSERT INTO rdb_kids (email, name, age) VALUES (?, ?, ?)", "a@x", "Ada", 36); err != nil {
		t.Fatalf("insert: %v", err)
	}

	type kid struct {
		ID        int64 `db:"id"`
		Email     string
		Name      string
		Age       int
		CreatedAt time.Time
	}
	k, err := rdb.One[kid](ctx, db, "SELECT id, email, name, age, created_at FROM rdb_kids WHERE email = ?", "a@x")
	if err != nil || k.Name != "Ada" || k.CreatedAt.IsZero() {
		t.Fatalf("One = %+v, %v", k, err)
	}

	violations := []struct {
		name, query string
		want        error
	}{
		{"unique", "INSERT INTO rdb_kids (email, name) VALUES ('a@x', 'b')", rdb.ErrUniqueViolation},
		{"not null", "INSERT INTO rdb_kids (email, name) VALUES ('c@x', NULL)", rdb.ErrNotNullViolation},
		{"no default", "INSERT INTO rdb_kids (email) VALUES ('c@x')", rdb.ErrNotNullViolation},
		{"check", "INSERT INTO rdb_kids (name, age) VALUES ('d', -1)", rdb.ErrCheckViolation},
		{"foreign key", "INSERT INTO rdb_kids (name, parent_id) VALUES ('e', 99)", rdb.ErrForeignKeyViolation},
	}
	for _, v := range violations {
		if _, err := db.Exec(ctx, v.query); !errors.Is(err, v.want) {
			t.Errorf("%s: err = %v, want %v", v.name, err, v.want)
		}
	}

	errStop := errors.New("stop")
	err = db.Tx(ctx, func(tx *rdb.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO rdb_kids (name) VALUES (?)", "rolled back"); err != nil {
			return err
		}
		return errStop
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("Tx err = %v", err)
	}
	names, err := rdb.All[string](ctx, db, "SELECT name FROM rdb_kids ORDER BY id")
	if err != nil || len(names) != 1 {
		t.Fatalf("names = %v, %v", names, err)
	}
}
