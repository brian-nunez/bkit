// Package rdbtest is the conformance suite for rdb drivers. Every driver
// runs it from its own tests against a real database, so migrations behave
// the same on every database.
//
// RunMigrations exercises rdb's migrations through a *rdb.DB: applying,
// re-running, dialect-specific files, statement splitting, failures,
// no-transaction migrations, history checks, and concurrent runs. Every
// table it creates has a random name and is dropped when the test ends, so
// the suite can share a database with other tests.
package rdbtest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/brian-nunez/bkit/rdb"
)

// Config describes the database under test.
type Config struct {
	// Open returns a new pool to the database under test. Required. Every
	// call within one test must reach the same database, so the suite can
	// run migrations from several pools at once. The suite closes each DB
	// it opens.
	Open func(t *testing.T) *rdb.DB
}

// RunMigrations runs the migration conformance tests.
func RunMigrations(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.Open == nil {
		t.Fatal("rdbtest: Config.Open is required")
	}
	tests := []struct {
		name string
		fn   func(t *testing.T, e *env)
	}{
		{"Apply", testApply},
		{"Status", testStatus},
		{"Pending", testPending},
		{"DialectFile", testDialectFile},
		{"Splitting", testSplitting},
		{"Block", testBlock},
		{"Failure", testFailure},
		{"NoTransaction", testNoTransaction},
		{"NoTransactionFailure", testNoTransactionFailure},
		{"Changed", testChanged},
		{"MissingFromSource", testMissingFromSource},
		{"OutOfOrder", testOutOfOrder},
		{"Tables", testTables},
		{"Concurrent", testConcurrent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn(t, newEnv(t, cfg))
		})
	}
}

// env is one test's database, table prefix, and cleanup list.
type env struct {
	cfg    Config
	db     *rdb.DB
	prefix string
}

// newEnv opens the database and drops the test's tables when it ends.
func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	e := &env{cfg: cfg, prefix: "rdbt_" + strings.ToLower(rand.Text()[:10])}
	e.db = e.open(t)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, name := range []string{"history", "history_b", "users", "items", "log", "kind", "steps", "tags"} {
			if _, err := e.db.Exec(ctx, "DROP TABLE IF EXISTS "+e.name(name)); err != nil {
				t.Errorf("drop %s: %v", e.name(name), err)
			}
		}
		var routine string
		switch e.db.Dialect() {
		case rdb.Postgres:
			routine = "DROP FUNCTION IF EXISTS " + e.name("double") + "(integer)"
		case rdb.MySQL:
			routine = "DROP PROCEDURE IF EXISTS " + e.name("fill")
		}
		if routine != "" {
			if _, err := e.db.Exec(ctx, routine); err != nil {
				t.Errorf("drop routine: %v", err)
			}
		}
	})
	return e
}

// open returns a new pool and closes it when the test ends.
func (e *env) open(t *testing.T) *rdb.DB {
	t.Helper()
	db := e.cfg.Open(t)
	if db == nil {
		t.Fatal("rdbtest: Config.Open returned nil")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// name returns the test's name for a table.
func (e *env) name(table string) string {
	return e.prefix + "_" + table
}

// sql replaces {p} in query with the test's table prefix.
func (e *env) sql(query string) string {
	return strings.ReplaceAll(query, "{p}", e.prefix)
}

// files builds a migration source, expanding {p} and {dialect} in names
// and {p} in contents.
func (e *env) files(kv ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 0; i < len(kv); i += 2 {
		name := strings.ReplaceAll(kv[i], "{dialect}", string(e.db.Dialect()))
		fsys[name] = &fstest.MapFile{Data: []byte(e.sql(kv[i+1]))}
	}
	return fsys
}

// opts returns the options that point Migrate at the test's history table.
func (e *env) opts() []rdb.MigrateOption {
	return []rdb.MigrateOption{rdb.MigrationTable(e.name("history"))}
}

// migrate runs Migrate on the test's history table.
func (e *env) migrate(t *testing.T, fsys fstest.MapFS) ([]rdb.Migration, error) {
	t.Helper()
	return e.db.Migrate(t.Context(), fsys, e.opts()...)
}

// mustMigrate runs Migrate and requires it to apply want migrations.
func (e *env) mustMigrate(t *testing.T, fsys fstest.MapFS, want ...int64) {
	t.Helper()
	applied, err := e.migrate(t, fsys)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := versions(applied); !equal(got, want) {
		t.Fatalf("Migrate applied %v, want %v", got, want)
	}
}

// count returns the number of rows in table.
func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	n, err := rdb.One[int](t.Context(), e.db, "SELECT count(*) FROM "+e.name(table))
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// exists reports whether the test's table exists.
func (e *env) exists(t *testing.T, table string) bool {
	t.Helper()
	_, err := e.db.Exec(t.Context(), "SELECT 1 FROM "+e.name(table)+" WHERE 1 = 0")
	return err == nil
}

// history returns the dirty flag of every recorded migration, by version.
func (e *env) history(t *testing.T) map[int64]int {
	t.Helper()
	type row struct {
		Version int64
		Dirty   int
	}
	rows, err := rdb.All[row](t.Context(), e.db, "SELECT version, dirty FROM "+e.name("history"))
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	h := map[int64]int{}
	for _, r := range rows {
		h[r.Version] = r.Dirty
	}
	return h
}

// versions returns the versions of ms.
func versions(ms []rdb.Migration) []int64 {
	v := make([]int64, len(ms))
	for i, m := range ms {
		v[i] = m.Version
	}
	return v
}

// equal reports whether a and b hold the same versions in order.
func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// wantErr requires err to match target.
func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

const createUsers = "CREATE TABLE {p}_users (id INTEGER PRIMARY KEY, email VARCHAR(100) NOT NULL)"

func testApply(t *testing.T, e *env) {
	fsys := e.files(
		"0001_create_users.sql", createUsers+";\n",
		"0002_seed_users.sql", "INSERT INTO {p}_users (id, email) VALUES (1, 'a@example.com');\n"+
			"INSERT INTO {p}_users (id, email) VALUES (2, 'b@example.com');\n",
		"README.md", "not a migration",
	)
	applied, err := e.migrate(t, fsys)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 2 || applied[0].Name != "create_users" || applied[1].Name != "seed_users" {
		t.Fatalf("Migrate applied %+v", applied)
	}
	for _, m := range applied {
		if m.AppliedAt.IsZero() {
			t.Errorf("migration %d has no AppliedAt", m.Version)
		}
	}
	if n := e.count(t, "users"); n != 2 {
		t.Fatalf("users = %d, want 2", n)
	}
	e.mustMigrate(t, fsys)
	if n := e.count(t, "users"); n != 2 {
		t.Fatalf("users after rerun = %d, want 2", n)
	}
}

func testStatus(t *testing.T, e *env) {
	fsys := e.files(
		"0001_create_users.sql", createUsers,
		"0002_seed_users.sql", "INSERT INTO {p}_users (id, email) VALUES (1, 'a@example.com')",
	)
	status, err := e.db.MigrationStatus(t.Context(), fsys, e.opts()...)
	if err != nil {
		t.Fatalf("MigrationStatus: %v", err)
	}
	if len(status) != 2 || !status[0].AppliedAt.IsZero() || !status[1].AppliedAt.IsZero() {
		t.Fatalf("status before Migrate = %+v, want two pending", status)
	}
	if e.exists(t, "history") {
		t.Fatal("MigrationStatus created the history table")
	}
	e.mustMigrate(t, fsys, 1, 2)
	status, err = e.db.MigrationStatus(t.Context(), fsys, e.opts()...)
	if err != nil {
		t.Fatalf("MigrationStatus: %v", err)
	}
	if len(status) != 2 || status[0].AppliedAt.IsZero() || status[1].AppliedAt.IsZero() {
		t.Fatalf("status after Migrate = %+v, want two applied", status)
	}
}

func testPending(t *testing.T, e *env) {
	first := e.files("0001_create_users.sql", createUsers)
	e.mustMigrate(t, first, 1)
	both := e.files(
		"0001_create_users.sql", createUsers,
		"0002_seed_users.sql", "INSERT INTO {p}_users (id, email) VALUES (1, 'a@example.com')",
	)
	e.mustMigrate(t, both, 2)
	if n := e.count(t, "users"); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

func testDialectFile(t *testing.T, e *env) {
	other := "sqlite"
	if e.db.Dialect() == rdb.SQLite {
		other = "postgres"
	}
	fsys := e.files(
		"0001_kind.sql", "CREATE TABLE {p}_kind (source VARCHAR(20) NOT NULL);\nINSERT INTO {p}_kind (source) VALUES ('plain')",
		"0001_kind.{dialect}.sql", "CREATE TABLE {p}_kind (source VARCHAR(20) NOT NULL);\nINSERT INTO {p}_kind (source) VALUES ('dialect')",
		"0001_kind."+other+".sql", "this file must not run",
	)
	e.mustMigrate(t, fsys, 1)
	source, err := rdb.One[string](t.Context(), e.db, e.sql("SELECT source FROM {p}_kind"))
	if err != nil {
		t.Fatalf("read kind: %v", err)
	}
	if source != "dialect" {
		t.Fatalf("ran the %s file, want the dialect file", source)
	}
}

func testSplitting(t *testing.T, e *env) {
	fsys := e.files("0001_tags.sql", `-- Semicolons in comments; strings; and quoted names do not end statements.
CREATE TABLE {p}_tags (
	id INTEGER PRIMARY KEY, /* a; comment */
	label VARCHAR(50) NOT NULL
);
INSERT INTO {p}_tags (id, label) VALUES (1, 'a;b');
INSERT INTO {p}_tags (id, label) VALUES (2, 'it''s; fine');

;
INSERT INTO {p}_tags (id, label) VALUES (3, 'last') -- no final semicolon`)
	e.mustMigrate(t, fsys, 1)
	labels, err := rdb.All[string](t.Context(), e.db, e.sql("SELECT label FROM {p}_tags ORDER BY id"))
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	if strings.Join(labels, "|") != "a;b|it's; fine|last" {
		t.Fatalf("labels = %q", labels)
	}
}

func testBlock(t *testing.T, e *env) {
	// Each block is one statement whose body holds semicolons.
	var block, after, check string
	switch e.db.Dialect() {
	case rdb.Postgres:
		block = "CREATE FUNCTION {p}_double(n integer) RETURNS integer LANGUAGE sql\nBEGIN ATOMIC\n  SELECT n * 2;\nEND;"
		check = "SELECT {p}_double(21)"
	case rdb.MySQL:
		// A procedure, since a trigger needs SUPER when binary logging is on.
		block = "CREATE PROCEDURE {p}_fill()\nBEGIN\n  INSERT INTO {p}_log (id) VALUES (1);\n  INSERT INTO {p}_log (id) VALUES (41);\nEND;"
		after = "CALL {p}_fill();\n"
		check = "SELECT sum(id) FROM {p}_log"
	default:
		block = "CREATE TRIGGER {p}_trg AFTER INSERT ON {p}_items\nBEGIN\n  INSERT INTO {p}_log (id) VALUES (NEW.id);\n  INSERT INTO {p}_log (id) VALUES (NEW.id + 40);\nEND;"
		after = "INSERT INTO {p}_items (id) VALUES (1);\n"
		check = "SELECT sum(id) FROM {p}_log"
	}
	fsys := e.files("0001_block.sql", "CREATE TABLE {p}_items (id INTEGER PRIMARY KEY);\n"+
		"CREATE TABLE {p}_log (id INTEGER NOT NULL);\n"+
		"-- rdb:begin-statement\n"+block+"\n-- rdb:end-statement\n"+after)
	e.mustMigrate(t, fsys, 1)
	got, err := rdb.One[int](t.Context(), e.db, e.sql(check))
	if err != nil {
		t.Fatalf("check block: %v", err)
	}
	if got != 42 {
		t.Fatalf("block result = %d, want 42", got)
	}
}

func testFailure(t *testing.T, e *env) {
	broken := e.files(
		"0001_create_users.sql", createUsers,
		"0002_steps.sql", "CREATE TABLE {p}_steps (id INTEGER PRIMARY KEY);\nINSERT INTO {p}_steps (id) VALUES (1);\nSELECT * FROM {p}_missing",
	)
	applied, err := e.migrate(t, broken)
	if err == nil {
		t.Fatal("Migrate succeeded with a failing statement")
	}
	if !strings.Contains(err.Error(), "0002_steps.sql line 3") {
		t.Errorf("error %q does not name the file and line", err)
	}
	if !equal(versions(applied), []int64{1}) {
		t.Fatalf("Migrate applied %v, want [1]", versions(applied))
	}
	if e.db.Dialect() == rdb.MySQL {
		// MySQL commits schema changes as they run.
		if !e.exists(t, "steps") {
			t.Fatal("the table created before the failure is missing")
		}
		if h := e.history(t); h[2] != 1 {
			t.Fatalf("history = %v, want migration 2 dirty", h)
		}
		_, err := e.migrate(t, broken)
		wantErr(t, err, rdb.ErrMigrationDirty)
		_, err = e.db.MigrationStatus(t.Context(), broken, e.opts()...)
		wantErr(t, err, rdb.ErrMigrationDirty)
		return
	}
	if e.exists(t, "steps") {
		t.Fatal("the failed migration was not rolled back")
	}
	if h := e.history(t); len(h) != 1 {
		t.Fatalf("history = %v, want only migration 1", h)
	}
	fixed := e.files(
		"0001_create_users.sql", createUsers,
		"0002_steps.sql", "CREATE TABLE {p}_steps (id INTEGER PRIMARY KEY);\nINSERT INTO {p}_steps (id) VALUES (1)",
	)
	e.mustMigrate(t, fixed, 2)
}

func testNoTransaction(t *testing.T, e *env) {
	fsys := e.files(
		"0001_create_users.sql", createUsers,
		"0002_index.sql", "-- rdb:no-transaction\nCREATE INDEX {p}_users_email ON {p}_users (email)",
		"0002_index.postgres.sql", "-- rdb:no-transaction\nCREATE INDEX CONCURRENTLY {p}_users_email ON {p}_users (email)",
	)
	e.mustMigrate(t, fsys, 1, 2)
	if h := e.history(t); h[1] != 0 || h[2] != 0 {
		t.Fatalf("history = %v, want both clean", h)
	}
}

func testNoTransactionFailure(t *testing.T, e *env) {
	fsys := e.files(
		"0001_steps.sql", "-- rdb:no-transaction\nCREATE TABLE {p}_steps (id INTEGER PRIMARY KEY);\nSELECT * FROM {p}_missing",
	)
	if _, err := e.migrate(t, fsys); err == nil {
		t.Fatal("Migrate succeeded with a failing statement")
	}
	if !e.exists(t, "steps") {
		t.Fatal("the statement before the failure did not run outside a transaction")
	}
	if h := e.history(t); h[1] != 1 {
		t.Fatalf("history = %v, want migration 1 dirty", h)
	}
	_, err := e.migrate(t, fsys)
	wantErr(t, err, rdb.ErrMigrationDirty)
}

func testChanged(t *testing.T, e *env) {
	e.mustMigrate(t, e.files("0001_create_users.sql", createUsers+"\n"), 1)
	_, err := e.migrate(t, e.files("0001_create_users.sql", createUsers+" -- edited"))
	wantErr(t, err, rdb.ErrMigrationChanged)
	_, err = e.db.MigrationStatus(t.Context(), e.files("0001_create_users.sql", createUsers+" -- edited"), e.opts()...)
	wantErr(t, err, rdb.ErrMigrationChanged)
	// A CRLF checkout of the same file is unchanged.
	e.mustMigrate(t, e.files("0001_create_users.sql", strings.ReplaceAll(createUsers+"\n", "\n", "\r\n")))
}

func testMissingFromSource(t *testing.T, e *env) {
	e.mustMigrate(t, e.files(
		"0001_create_users.sql", createUsers,
		"0002_seed_users.sql", "INSERT INTO {p}_users (id, email) VALUES (1, 'a@example.com')",
	), 1, 2)
	_, err := e.migrate(t, e.files("0001_create_users.sql", createUsers))
	wantErr(t, err, rdb.ErrMigrationMismatch)
}

func testOutOfOrder(t *testing.T, e *env) {
	e.mustMigrate(t, e.files(
		"0001_create_users.sql", createUsers,
		"0003_seed_users.sql", "INSERT INTO {p}_users (id, email) VALUES (1, 'a@example.com')",
	), 1, 3)
	_, err := e.migrate(t, e.files(
		"0001_create_users.sql", createUsers,
		"0002_late.sql", "INSERT INTO {p}_users (id, email) VALUES (2, 'b@example.com')",
		"0003_seed_users.sql", "INSERT INTO {p}_users (id, email) VALUES (1, 'a@example.com')",
	))
	wantErr(t, err, rdb.ErrMigrationMismatch)
}

func testTables(t *testing.T, e *env) {
	e.mustMigrate(t, e.files("0001_create_users.sql", createUsers), 1)
	other := e.files("0001_create_tags.sql", "CREATE TABLE {p}_tags (id INTEGER PRIMARY KEY)")
	applied, err := e.db.Migrate(t.Context(), other, rdb.MigrationTable(e.name("history_b")))
	if err != nil {
		t.Fatalf("Migrate with a second table: %v", err)
	}
	if !equal(versions(applied), []int64{1}) {
		t.Fatalf("second set applied %v, want [1]", versions(applied))
	}
}

func testConcurrent(t *testing.T, e *env) {
	kv := []string{"0001_create_steps.sql", "CREATE TABLE {p}_steps (id INTEGER PRIMARY KEY)"}
	for i := 2; i <= 8; i++ {
		kv = append(kv, fmt.Sprintf("%04d_step.sql", i), fmt.Sprintf("INSERT INTO {p}_steps (id) VALUES (%d)", i))
	}
	fsys := e.files(kv...)
	const runs = 4
	dbs := make([]*rdb.DB, runs)
	for i := range dbs {
		dbs[i] = e.open(t)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	errs := make([]error, runs)
	for i, db := range dbs {
		wg.Go(func() {
			applied, err := db.Migrate(t.Context(), fsys, e.opts()...)
			errs[i] = err
			mu.Lock()
			total += len(applied)
			mu.Unlock()
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
	if total != 8 {
		t.Fatalf("runs applied %d migrations in total, want 8", total)
	}
	if n := e.count(t, "steps"); n != 7 {
		t.Fatalf("steps = %d, want 7", n)
	}
}
