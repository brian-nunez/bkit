package rdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// histDB simulates a database's history table for migration tests.
type histDB struct {
	mu     sync.Mutex
	table  bool
	rows   []historyRow
	stmts  []string
	fails  map[string]*failure
	locks  []driver.Value // successive lock results; the last repeats
	unlock driver.Value
	hook   func(q string) // runs before every query
}

// failure makes queries containing a key fail with err, left more times,
// or forever when left is negative.
type failure struct {
	err  error
	left int
}

func newHistDB() *histDB {
	return &histDB{fails: map[string]*failure{}, locks: []driver.Value{int64(1)}, unlock: int64(1)}
}

// fail makes queries containing substr fail with err, times times, or
// forever when times is negative.
func (h *histDB) fail(substr string, err error, times int) {
	h.fails[substr] = &failure{err: err, left: times}
}

// start runs the hook and returns the failure for q, if any.
func (h *histDB) start(q string) error {
	if h.hook != nil {
		h.hook(q)
	}
	for substr, f := range h.fails {
		if strings.Contains(q, substr) && f.left != 0 {
			f.left--
			return f.err
		}
	}
	return nil
}

func (h *histDB) exec(q string, args []driver.NamedValue) (driver.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.start(q); err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS"):
		h.table = true
	case strings.Contains(q, "(version, name, checksum, dirty, applied_at)"):
		h.rows = append(h.rows, historyRow{
			Version:   args[0].Value.(int64),
			Name:      args[1].Value.(string),
			Checksum:  args[2].Value.(string),
			Dirty:     int(args[3].Value.(int64)),
			AppliedAt: args[4].Value.(int64),
		})
	case strings.Contains(q, "SET dirty = 0"):
		for i := range h.rows {
			if h.rows[i].Version == args[1].Value.(int64) {
				h.rows[i].Dirty = 0
				h.rows[i].AppliedAt = args[0].Value.(int64)
			}
		}
	case q == "BEGIN IMMEDIATE" || q == "COMMIT" || q == "ROLLBACK":
	default:
		h.stmts = append(h.stmts, q)
	}
	return driver.RowsAffected(1), nil
}

func (h *histDB) query(q string, _ []driver.NamedValue) (driver.Rows, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.start(q); err != nil {
		return nil, err
	}
	one := func(v driver.Value) driver.Rows {
		return &fakeRows{cols: []string{"v"}, data: [][]driver.Value{{v}}}
	}
	switch {
	case strings.Contains(q, "pg_try_advisory_lock") || strings.Contains(q, "GET_LOCK"):
		v := h.locks[0]
		if len(h.locks) > 1 {
			h.locks = h.locks[1:]
		}
		return one(v), nil
	case strings.Contains(q, "pg_advisory_unlock") || strings.Contains(q, "RELEASE_LOCK"):
		return one(h.unlock), nil
	case strings.HasPrefix(q, "SELECT version"):
		rows := &fakeRows{cols: []string{"version", "name", "checksum", "dirty", "applied_at"}}
		for _, r := range h.rows {
			rows.data = append(rows.data, []driver.Value{r.Version, r.Name, r.Checksum, int64(r.Dirty), r.AppliedAt})
		}
		return rows, nil
	case strings.Contains(q, "to_regclass") || strings.Contains(q, "information_schema") || strings.Contains(q, "sqlite_master"):
		if h.table {
			return one(int64(1)), nil
		}
		return one(int64(0)), nil
	}
	return nil, fmt.Errorf("unexpected query %q", q)
}

// statements returns the migration statements run so far.
func (h *histDB) statements() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.stmts)
}

// history returns a copy of the history rows.
func (h *histDB) history() []historyRow {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.rows)
}

// openHist opens a DB of dialect d over a fresh histDB.
func openHist(t *testing.T, d Dialect, opts ...Option) (*DB, *histDB, *testDriver) {
	t.Helper()
	h := newHistDB()
	drv := newDriver(d)
	drv.db.exec = h.exec
	drv.db.query = h.query
	return mustOpen(t, drv, opts...), h, drv
}

// shortPoll makes lock and busy retries fast for one test.
func shortPoll(t *testing.T, d time.Duration) {
	t.Helper()
	old := lockPoll
	lockPoll = d
	t.Cleanup(func() { lockPoll = old })
}

// connects returns how many connections the fake has opened.
func connects(drv *testDriver) int {
	drv.db.mu.Lock()
	defer drv.db.mu.Unlock()
	return drv.db.connects
}

var twoMigrations = fstest.MapFS{
	"1_create.sql": file("CREATE TABLE a (x INT);\nINSERT INTO a VALUES (1);"),
	"2_index.sql":  file("-- rdb:no-transaction\nCREATE INDEX i ON a (x)"),
}

var allDialects = []Dialect{SQLite, Postgres, MySQL}

func TestMigrate(t *testing.T) {
	for _, d := range allDialects {
		t.Run(string(d), func(t *testing.T) {
			db, h, drv := openHist(t, d, WithoutRebind())
			applied, err := db.Migrate(t.Context(), twoMigrations)
			if err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			if len(applied) != 2 || applied[0].Version != 1 || applied[0].Name != "create" || applied[1].Version != 2 || applied[1].AppliedAt.IsZero() {
				t.Fatalf("applied = %+v", applied)
			}
			want := []string{"CREATE TABLE a (x INT)", "INSERT INTO a VALUES (1)", "CREATE INDEX i ON a (x)"}
			if got := h.statements(); !slices.Equal(got, want) {
				t.Fatalf("statements = %q, want %q", got, want)
			}
			for _, r := range h.history() {
				if r.Dirty != 0 || r.AppliedAt == 0 {
					t.Fatalf("history row %+v is not finished", r)
				}
			}
			if d == Postgres && !slices.ContainsFunc(drv.db.queries(), func(q string) bool { return strings.Contains(q, "VALUES ($1, $2, $3, $4, $5)") }) {
				t.Error("migration queries were not rebound for Postgres")
			}
			applied, err = db.Migrate(t.Context(), twoMigrations)
			if err != nil || len(applied) != 0 {
				t.Fatalf("second Migrate = %v, %v; want nothing applied", applied, err)
			}
			status, err := db.MigrationStatus(t.Context(), twoMigrations)
			if err != nil {
				t.Fatalf("MigrationStatus: %v", err)
			}
			if len(status) != 2 || status[0].AppliedAt.IsZero() || status[1].AppliedAt.IsZero() {
				t.Fatalf("status = %+v, want both applied", status)
			}
		})
	}
}

func TestMigrationStatusBeforeMigrate(t *testing.T) {
	for _, d := range allDialects {
		t.Run(string(d), func(t *testing.T) {
			db, h, _ := openHist(t, d)
			status, err := db.MigrationStatus(t.Context(), twoMigrations, MigrationTable("app_history"))
			if err != nil {
				t.Fatalf("MigrationStatus: %v", err)
			}
			if len(status) != 2 || !status[0].AppliedAt.IsZero() || status[1].Name != "index" {
				t.Fatalf("status = %+v, want two pending", status)
			}
			if h.table {
				t.Fatal("MigrationStatus created the history table")
			}
		})
	}
}

func TestMigrationStatusErrors(t *testing.T) {
	db, h, _ := openHist(t, SQLite)
	if _, err := db.MigrationStatus(t.Context(), fstest.MapFS{}); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("empty source: %v", err)
	}
	h.fail("sqlite_master", errUnique, 1)
	if _, err := db.MigrationStatus(t.Context(), twoMigrations); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("exists query failure: %v", err)
	}
	h.table = true
	h.fail("SELECT version", errUnique, 1)
	if _, err := db.MigrationStatus(t.Context(), twoMigrations); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("history query failure: %v", err)
	}
	h.rows = []historyRow{{Version: 1, Name: "create", Checksum: "different"}}
	if _, err := db.MigrationStatus(t.Context(), twoMigrations); !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("changed migration: %v", err)
	}
}

func TestMigrateHistoryChecks(t *testing.T) {
	sum := checksum
	three := fstest.MapFS{
		"1_a.sql": file("SELECT 1"),
		"2_b.sql": file("SELECT 2"),
		"3_c.sql": file("SELECT 3"),
	}
	tests := []struct {
		name string
		rows []historyRow
		want error
		msg  string
	}{
		{"dirty", []historyRow{{Version: 1, Name: "a", Checksum: sum("SELECT 1"), Dirty: 1}}, ErrMigrationDirty, `migration 1 "a" started but did not finish`},
		{"missing", []historyRow{{Version: 4, Name: "d", Checksum: "x"}}, ErrMigrationMismatch, `migration 4 "d" is applied but missing from the source`},
		{"changed", []historyRow{{Version: 2, Name: "b", Checksum: sum("SELECT 22")}}, ErrMigrationChanged, "2_b.sql changed after it was applied"},
		{"out of order", []historyRow{{Version: 1, Name: "a", Checksum: sum("SELECT 1")}, {Version: 3, Name: "c", Checksum: sum("SELECT 3")}}, ErrMigrationMismatch, `migration 2 "b" is pending but sorts before applied migration 3`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, h, _ := openHist(t, Postgres)
			h.table = true
			h.rows = tt.rows
			applied, err := db.Migrate(t.Context(), three)
			if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("Migrate error = %v, want %v with %q", err, tt.want, tt.msg)
			}
			if len(applied) != 0 || len(h.statements()) != 0 {
				t.Fatalf("Migrate applied %v and ran %q", applied, h.statements())
			}
			if _, err := db.MigrationStatus(t.Context(), three); !errors.Is(err, tt.want) {
				t.Fatalf("MigrationStatus error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestMigratePartiallyApplied(t *testing.T) {
	db, h, _ := openHist(t, SQLite)
	h.table = true
	h.rows = []historyRow{{Version: 1, Name: "create", Checksum: checksum("CREATE TABLE a (x INT);\nINSERT INTO a VALUES (1);"), AppliedAt: 1000}}
	applied, err := db.Migrate(t.Context(), twoMigrations)
	if err != nil || len(applied) != 1 || applied[0].Version != 2 {
		t.Fatalf("Migrate = %+v, %v; want only migration 2", applied, err)
	}
	status, err := db.MigrationStatus(t.Context(), twoMigrations)
	if err != nil || !status[0].AppliedAt.Equal(time.UnixMilli(1000)) {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestMigrateOptionErrors(t *testing.T) {
	db, _, _ := openHist(t, SQLite)
	for _, name := range []string{"", "History", "app-history", "1history", "schema.history", strings.Repeat("a", 64)} {
		if _, err := db.Migrate(t.Context(), twoMigrations, MigrationTable(name)); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("MigrationTable(%q): error = %v, want ErrInvalidOption", name, err)
		}
	}
	if _, err := db.Migrate(t.Context(), twoMigrations, MigrationTable(strings.Repeat("a", 63))); err != nil {
		t.Errorf("63-byte table name: %v", err)
	}
	other, _, _ := openHist(t, Dialect("oracle"))
	if _, err := other.Migrate(t.Context(), twoMigrations); err == nil || !strings.Contains(err.Error(), `migrations do not support dialect "oracle"`) {
		t.Errorf("unsupported dialect: %v", err)
	}
	if _, err := db.Migrate(t.Context(), fstest.MapFS{}); !errors.Is(err, ErrInvalidMigration) {
		t.Errorf("empty source: %v", err)
	}
}

func TestMigrateCustomTable(t *testing.T) {
	db, _, drv := openHist(t, MySQL)
	if _, err := db.Migrate(t.Context(), twoMigrations, MigrationTable("app_history")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, q := range drv.db.queries() {
		if strings.Contains(q, "rdb_migrations") {
			t.Fatalf("query %q uses the default table", q)
		}
	}
}

func TestMigrateClosedDB(t *testing.T) {
	db, _, _ := openHist(t, SQLite)
	_ = db.Close()
	if _, err := db.Migrate(t.Context(), twoMigrations); err == nil || !strings.Contains(err.Error(), "rdb: migrate") {
		t.Fatalf("error = %v, want a migrate error", err)
	}
}

func TestMigrateWaitsForLock(t *testing.T) {
	shortPoll(t, time.Millisecond)
	for _, d := range []Dialect{Postgres, MySQL} {
		t.Run(string(d), func(t *testing.T) {
			db, h, drv := openHist(t, d)
			h.locks = []driver.Value{int64(0), int64(0), int64(1)}
			if _, err := db.Migrate(t.Context(), twoMigrations); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			n := 0
			for _, q := range drv.db.queries() {
				if strings.Contains(q, "pg_try_advisory_lock") || strings.Contains(q, "GET_LOCK") {
					n++
				}
			}
			if n != 3 {
				t.Fatalf("tried the lock %d times, want 3", n)
			}
		})
	}
}

func TestMigrateLockErrors(t *testing.T) {
	t.Run("null result", func(t *testing.T) {
		db, h, drv := openHist(t, MySQL)
		h.locks = []driver.Value{nil}
		if _, err := db.Migrate(t.Context(), twoMigrations); err == nil || !strings.Contains(err.Error(), "could not take the migration lock") {
			t.Fatalf("error = %v", err)
		}
		requireDiscarded(t, db, drv)
	})
	t.Run("query failure", func(t *testing.T) {
		db, h, drv := openHist(t, Postgres)
		h.fail("pg_try_advisory_lock", errUnique, 1)
		if _, err := db.Migrate(t.Context(), twoMigrations); !errors.Is(err, ErrUniqueViolation) {
			t.Fatalf("error = %v", err)
		}
		requireDiscarded(t, db, drv)
	})
	t.Run("canceled while waiting", func(t *testing.T) {
		shortPoll(t, time.Hour)
		db, h, _ := openHist(t, Postgres)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		h.locks = []driver.Value{int64(0)}
		if _, err := db.Migrate(ctx, twoMigrations); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
	})
}

// requireDiscarded checks that the run's connection was closed instead of
// returned to the pool: the next query needs a new connection.
func requireDiscarded(t *testing.T, db *DB, drv *testDriver) {
	t.Helper()
	before := connects(drv)
	if err := db.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if connects(drv) != before+1 {
		t.Fatal("the run's connection went back to the pool")
	}
}

func TestMigrateUnlockFailures(t *testing.T) {
	t.Run("not held", func(t *testing.T) {
		db, h, drv := openHist(t, Postgres)
		h.unlock = int64(0)
		if _, err := db.Migrate(t.Context(), twoMigrations); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		requireDiscarded(t, db, drv)
	})
	t.Run("query failure", func(t *testing.T) {
		db, h, drv := openHist(t, MySQL)
		h.fail("RELEASE_LOCK", errUnique, 1)
		if _, err := db.Migrate(t.Context(), twoMigrations); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		requireDiscarded(t, db, drv)
	})
	t.Run("canceled", func(t *testing.T) {
		db, h, drv := openHist(t, MySQL)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		h.hook = func(q string) {
			if strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS") {
				cancel()
			}
		}
		// The fake ignores ctx, so later queries may still succeed. Either
		// way the lock cannot be released with a canceled context.
		if _, err := db.Migrate(ctx, twoMigrations); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want nil or context.Canceled", err)
		}
		requireDiscarded(t, db, drv)
	})
}

func TestMigrateSQLiteWaitsForWriteLock(t *testing.T) {
	shortPoll(t, time.Millisecond)
	db, h, _ := openHist(t, SQLite)
	h.fail("BEGIN IMMEDIATE", errBusy, 2)
	applied, err := db.Migrate(t.Context(), twoMigrations)
	if err != nil || len(applied) != 2 {
		t.Fatalf("Migrate = %v, %v", applied, err)
	}
}

func TestMigrateSQLiteBeginErrors(t *testing.T) {
	t.Run("not retryable", func(t *testing.T) {
		db, h, _ := openHist(t, SQLite)
		h.fail("BEGIN IMMEDIATE", errUnique, 1)
		if _, err := db.Migrate(t.Context(), twoMigrations); !errors.Is(err, ErrUniqueViolation) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("canceled while busy", func(t *testing.T) {
		shortPoll(t, time.Hour)
		db, h, _ := openHist(t, SQLite)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		h.fail("BEGIN IMMEDIATE", errBusy, -1)
		if _, err := db.Migrate(ctx, twoMigrations); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
	})
}

func TestMigrateTransactionErrors(t *testing.T) {
	t.Run("postgres begin", func(t *testing.T) {
		db, _, drv := openHist(t, Postgres)
		drv.db.beginErr = errUnique
		if _, err := db.Migrate(t.Context(), twoMigrations); !errors.Is(err, ErrUniqueViolation) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("postgres commit", func(t *testing.T) {
		db, _, drv := openHist(t, Postgres)
		drv.db.commitErr = errUnique
		_, err := db.Migrate(t.Context(), twoMigrations)
		var e *Error
		if !errors.As(err, &e) || e.Op != "commit" {
			t.Fatalf("error = %v, want a commit error", err)
		}
	})
	t.Run("postgres rollback", func(t *testing.T) {
		db, h, drv := openHist(t, Postgres)
		drv.db.rollbackErr = errUnique
		h.fail("CREATE TABLE a", errBusy, 1)
		if _, err := db.Migrate(t.Context(), twoMigrations); !errors.Is(err, ErrRetryable) {
			t.Fatalf("error = %v", err)
		}
		requireDiscarded(t, db, drv)
	})
	t.Run("sqlite commit when nothing is pending", func(t *testing.T) {
		db, h, _ := openHist(t, SQLite)
		if _, err := db.Migrate(t.Context(), twoMigrations); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		h.fail("COMMIT", errUnique, 1)
		_, err := db.Migrate(t.Context(), twoMigrations)
		var e *Error
		if !errors.As(err, &e) || e.Op != "commit" {
			t.Fatalf("error = %v, want a commit error", err)
		}
	})
	t.Run("sqlite commit of the dirty record", func(t *testing.T) {
		db, h, _ := openHist(t, SQLite)
		h.table = true
		h.rows = []historyRow{{Version: 1, Name: "create", Checksum: checksum("CREATE TABLE a (x INT);\nINSERT INTO a VALUES (1);")}}
		h.fail("COMMIT", errUnique, 1)
		_, err := db.Migrate(t.Context(), twoMigrations)
		var e *Error
		if !errors.As(err, &e) || e.Op != "commit" {
			t.Fatalf("error = %v, want a commit error", err)
		}
		if len(h.statements()) != 0 {
			t.Fatalf("ran %q after the dirty record failed to commit", h.statements())
		}
	})
}

func TestMigrateStepErrors(t *testing.T) {
	tests := []struct {
		name    string
		dialect Dialect
		fail    string
		dirty   bool // the failure leaves migration 1 recorded as dirty
		msg     string
	}{
		{"create history", SQLite, "CREATE TABLE IF NOT EXISTS", false, ""},
		{"read history", MySQL, "SELECT version", false, ""},
		{"statement in a transaction", SQLite, "INSERT INTO a", false, "1_create.sql line 2"},
		{"record in a transaction", Postgres, "INSERT INTO rdb_migrations", false, ""},
		{"record before a statement", MySQL, "INSERT INTO rdb_migrations", false, ""},
		{"statement outside a transaction", MySQL, "INSERT INTO a", true, "1_create.sql line 2: fake: duplicate key (the migration is recorded as dirty)"},
		{"finish record", MySQL, "UPDATE rdb_migrations", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, h, _ := openHist(t, tt.dialect)
			h.fail(tt.fail, errUnique, 1)
			applied, err := db.Migrate(t.Context(), twoMigrations)
			if !errors.Is(err, ErrUniqueViolation) {
				t.Fatalf("error = %v, want ErrUniqueViolation", err)
			}
			if tt.msg != "" && !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.msg)
			}
			if len(applied) != 0 {
				t.Fatalf("applied = %v", applied)
			}
			rows := h.history()
			if tt.dirty != (len(rows) == 1 && rows[0].Dirty == 1) {
				t.Fatalf("history = %+v, want dirty %v", rows, tt.dirty)
			}
			if tt.dirty {
				if _, err := db.Migrate(t.Context(), twoMigrations); !errors.Is(err, ErrMigrationDirty) {
					t.Fatalf("rerun error = %v, want ErrMigrationDirty", err)
				}
			}
		})
	}
}
