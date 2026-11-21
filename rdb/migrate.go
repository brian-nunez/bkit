package rdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"time"
)

// Migration describes one migration.
type Migration struct {
	// Version orders migrations. It is the number that starts the file name.
	Version int64

	// Name is the file name's text between the version and the extension,
	// such as "create_users" for 0001_create_users.sql.
	Name string

	// AppliedAt is when the migration finished, or the zero time when it is
	// pending.
	AppliedAt time.Time
}

// MigrateOption configures Migrate and MigrationStatus.
type MigrateOption func(*migrateOptions)

// migrateOptions holds the settings Migrate collects from its options.
type migrateOptions struct {
	table string
}

// defaultMigrationTable is the history table used without MigrationTable.
const defaultMigrationTable = "rdb_migrations"

// MigrationTable records applied migrations in the table name instead of
// rdb_migrations. Give each set of migrations that shares a database its own
// table. name is a lowercase SQL identifier of at most 63 bytes: a letter or
// underscore, then letters, digits, and underscores.
func MigrationTable(name string) MigrateOption {
	return func(o *migrateOptions) { o.table = name }
}

// tableName matches the history table names MigrationTable accepts.
var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// migrationDialects are the dialects migrations support.
var migrationDialects = []Dialect{SQLite, Postgres, MySQL}

// lockPoll is how long Migrate waits before it tries again to take a lock
// another run holds. Tests shorten it.
var lockPoll = 250 * time.Millisecond

// historyRow is one row of the history table.
type historyRow struct {
	Version   int64  `db:"version"`
	Name      string `db:"name"`
	Checksum  string `db:"checksum"`
	Dirty     int    `db:"dirty"`
	AppliedAt int64  `db:"applied_at"`
}

// migrator runs one set of migrations against a DB.
type migrator struct {
	table string
	set   []migration

	// s runs the migrator's own queries. It always rebinds, whatever the
	// DB's setting, since those queries use ? placeholders.
	s session

	// conn is the connection Migrate holds for the whole run.
	conn *sql.Conn

	// broken records that conn may still hold a lock or a transaction, so it
	// must be closed instead of returned to the pool.
	broken bool
}

// newMigrator validates the options and loads the migrations in fsys.
func (db *DB) newMigrator(fsys fs.FS, opts []MigrateOption) (*migrator, error) {
	o := migrateOptions{table: defaultMigrationTable}
	for _, opt := range opts {
		opt(&o)
	}
	if !tableName.MatchString(o.table) {
		return nil, &Error{Op: "migrate", Kind: ErrInvalidOption, Err: fmt.Errorf("migration table %q is not a lowercase identifier of at most 63 bytes", o.table)}
	}
	if !slices.Contains(migrationDialects, db.dialect) {
		return nil, &Error{Op: "migrate", Err: fmt.Errorf("migrations do not support dialect %q", db.dialect)}
	}
	set, err := loadMigrations(fsys, db.dialect)
	if err != nil {
		return nil, err
	}
	s := db.session
	s.rebind = true
	s.strict = true
	return &migrator{table: o.table, set: set, s: s}, nil
}

// Migrate applies the pending migrations in the root of fsys in version
// order and returns the ones it applied, also when it stops at a failure.
//
// Migrations are .sql files named <version>_<name>.sql, such as
// 0001_create_users.sql. A file named <version>_<name>.<dialect>.sql, such as
// 0001_create_users.postgres.sql, replaces the plain file on that dialect.
// Files that do not end in .sql, and subdirectories, are ignored; use fs.Sub
// to read a subdirectory.
//
// Each migration runs in its own transaction on SQLite and Postgres, so a
// failed migration leaves no trace. MySQL and MariaDB commit schema changes
// as they run, so there, and for a migration marked no-transaction on any
// database, a failure leaves the migration recorded as dirty, and later runs
// fail with ErrMigrationDirty until it is repaired.
//
// Runs on the same database and table take turns: a run waits, honoring
// ctx, while another run holds the database's lock, then applies whatever is
// still pending. Migrate checks the applied migrations against fsys first,
// and fails with ErrMigrationChanged, ErrMigrationMismatch, or
// ErrMigrationDirty without applying anything when they disagree.
func (db *DB) Migrate(ctx context.Context, fsys fs.FS, opts ...MigrateOption) ([]Migration, error) {
	m, err := db.newMigrator(fsys, opts)
	if err != nil {
		return nil, err
	}
	conn, err := db.db.Conn(ctx)
	if err != nil {
		return nil, db.wrap("migrate", err)
	}
	m.conn = conn
	m.s.run = conn
	defer m.release()
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer m.unlock(ctx)
	applied := []Migration{}
	for {
		mig, err := m.step(ctx)
		if err != nil || mig == nil {
			return applied, err
		}
		applied = append(applied, *mig)
	}
}

// MigrationStatus returns every migration in the root of fsys in version
// order, with AppliedAt set on those the database has applied. It changes
// nothing, and it fails with the errors Migrate would return before applying
// anything, such as ErrMigrationChanged.
func (db *DB) MigrationStatus(ctx context.Context, fsys fs.FS, opts ...MigrateOption) ([]Migration, error) {
	m, err := db.newMigrator(fsys, opts)
	if err != nil {
		return nil, err
	}
	n, err := One[int](ctx, m.s, m.existsQuery(), m.table)
	if err != nil {
		return nil, err
	}
	var hist []historyRow
	if n > 0 {
		if hist, err = All[historyRow](ctx, m.s, m.selectQuery()); err != nil {
			return nil, err
		}
	}
	if _, err := m.check(hist); err != nil {
		return nil, err
	}
	appliedAt := make(map[int64]int64, len(hist))
	for _, h := range hist {
		appliedAt[h.Version] = h.AppliedAt
	}
	status := make([]Migration, len(m.set))
	for i, mg := range m.set {
		status[i] = Migration{Version: mg.version, Name: mg.name}
		if at, ok := appliedAt[mg.version]; ok {
			status[i].AppliedAt = time.UnixMilli(at)
		}
	}
	return status, nil
}

// release returns the run's connection to the pool, or closes it when it may
// still hold a lock or a transaction. Closing a connection ends both.
func (m *migrator) release() {
	if m.broken {
		_ = m.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = m.conn.Close()
}

// lockQueries returns the queries that take and release the session lock
// for the run, and their argument. Each returns 1 on success. SQLite has no
// session lock, so its queries are empty and each step's immediate
// transaction makes runs take turns instead.
//
// The lock is polled instead of awaited, so a waiting run holds no open
// statement that a migration such as CREATE INDEX CONCURRENTLY would wait on.
func (m *migrator) lockQueries() (lock, unlock string, arg any) {
	switch m.s.dialect {
	case Postgres:
		sum := sha256.Sum256([]byte("rdb.migrate." + m.table))
		key := int64(binary.BigEndian.Uint64(sum[:8]) >> 1)
		return "SELECT CASE WHEN pg_try_advisory_lock(?) THEN 1 ELSE 0 END",
			"SELECT CASE WHEN pg_advisory_unlock(?) THEN 1 ELSE 0 END", key
	case MySQL:
		// Lock names are server-wide, so the name includes the database.
		// Hashing keeps it within the 64 characters MySQL allows.
		name := "SHA2(CONCAT('rdb.migrate.', COALESCE(DATABASE(), ''), '.', ?), 256)"
		return "SELECT GET_LOCK(" + name + ", 0)", "SELECT RELEASE_LOCK(" + name + ")", m.table
	default:
		return "", "", nil
	}
}

// lock takes the run's session lock, waiting while another run holds it.
func (m *migrator) lock(ctx context.Context) error {
	query, _, arg := m.lockQueries()
	if query == "" {
		return nil
	}
	for {
		got, err := One[sql.NullInt64](ctx, m.s, query, arg)
		if err != nil {
			m.broken = true
			return err
		}
		if !got.Valid {
			m.broken = true
			return &Error{Op: "migrate", Err: errors.New("the database could not take the migration lock")}
		}
		if got.Int64 == 1 {
			return nil
		}
		if err := sleep(ctx, lockPoll); err != nil {
			return m.s.wrap("migrate", err)
		}
	}
}

// unlock releases the run's session lock. When it cannot, the connection is
// closed instead, which releases the lock.
func (m *migrator) unlock(ctx context.Context) {
	_, query, arg := m.lockQueries()
	if query == "" || m.broken {
		return
	}
	if ctx.Err() != nil {
		m.broken = true
		return
	}
	got, err := One[sql.NullInt64](ctx, m.s, query, arg)
	if err != nil || got.Int64 != 1 {
		m.broken = true
	}
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// stepTx is the transaction one step runs in.
type stepTx struct {
	run      runner
	commit   func() error
	rollback func() error
	done     bool
}

// end commits or rolls back the transaction once.
func (t *stepTx) end(commit bool) error {
	if t.done {
		return nil
	}
	t.done = true
	if commit {
		return t.commit()
	}
	return t.rollback()
}

// begin starts a step's transaction. On Postgres it is a transaction. On
// SQLite it is an immediate transaction, which takes the database's write
// lock, so a run in another connection or process waits here. MySQL commits
// schema changes implicitly, so a step there runs on the connection itself.
func (m *migrator) begin(ctx context.Context) (*stepTx, error) {
	switch m.s.dialect {
	case Postgres:
		tx, err := m.conn.BeginTx(ctx, nil)
		if err != nil {
			return nil, m.s.wrap("begin", err)
		}
		return &stepTx{run: tx, commit: tx.Commit, rollback: tx.Rollback}, nil
	case SQLite:
		for {
			_, err := m.conn.ExecContext(ctx, "BEGIN IMMEDIATE")
			if err == nil {
				break
			}
			if err = m.s.wrap("begin", err); !errors.Is(err, ErrRetryable) {
				return nil, err
			}
			if err := sleep(ctx, lockPoll); err != nil {
				return nil, m.s.wrap("begin", err)
			}
		}
		end := func(query string) func() error {
			return func() error {
				_, err := m.conn.ExecContext(context.WithoutCancel(ctx), query)
				return err
			}
		}
		return &stepTx{run: m.conn, commit: end("COMMIT"), rollback: end("ROLLBACK")}, nil
	default:
		none := func() error { return nil }
		return &stepTx{run: m.conn, commit: none, rollback: none}, nil
	}
}

// step applies the next pending migration and returns it, or returns nil
// when none is pending.
func (m *migrator) step(ctx context.Context) (*Migration, error) {
	tx, err := m.begin(ctx)
	if err != nil {
		return nil, err
	}
	mig, err := m.apply(ctx, tx)
	if err != nil {
		if tx.end(false) != nil {
			m.broken = true
		}
		return nil, err
	}
	return mig, nil
}

// apply reads the history inside tx, checks it, and applies the next pending
// migration. A migration runs inside tx when the database can roll its
// statements back. Otherwise it is first recorded as dirty and committed, so
// a failure partway through stays visible, then runs outside tx.
func (m *migrator) apply(ctx context.Context, tx *stepTx) (*Migration, error) {
	s := m.s
	s.run = tx.run
	if _, err := s.Exec(ctx, m.createQuery()); err != nil {
		return nil, err
	}
	hist, err := All[historyRow](ctx, s, m.selectQuery())
	if err != nil {
		return nil, err
	}
	i, err := m.check(hist)
	if err != nil {
		return nil, err
	}
	if i == len(m.set) {
		return nil, s.wrap("commit", tx.end(true))
	}
	next := &m.set[i]
	insert := "INSERT INTO " + m.table + " (version, name, checksum, dirty, applied_at) VALUES (?, ?, ?, ?, ?)"
	if m.s.dialect != MySQL && !next.noTx {
		if err := m.run(ctx, tx.run, next, false); err != nil {
			return nil, err
		}
		at := time.Now().UnixMilli()
		if _, err := s.Exec(ctx, insert, next.version, next.name, next.checksum, 0, at); err != nil {
			return nil, err
		}
		if err := tx.end(true); err != nil {
			return nil, s.wrap("commit", err)
		}
		return &Migration{Version: next.version, Name: next.name, AppliedAt: time.UnixMilli(at)}, nil
	}
	if _, err := s.Exec(ctx, insert, next.version, next.name, next.checksum, 1, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	if err := tx.end(true); err != nil {
		return nil, s.wrap("commit", err)
	}
	if err := m.run(ctx, m.conn, next, true); err != nil {
		return nil, err
	}
	at := time.Now().UnixMilli()
	s.run = m.conn
	if _, err := s.Exec(ctx, "UPDATE "+m.table+" SET dirty = 0, applied_at = ? WHERE version = ?", at, next.version); err != nil {
		return nil, err
	}
	return &Migration{Version: next.version, Name: next.name, AppliedAt: time.UnixMilli(at)}, nil
}

// run executes mg's statements on r, exactly as written.
func (m *migrator) run(ctx context.Context, r runner, mg *migration, dirty bool) error {
	for _, st := range mg.statements {
		if _, err := r.ExecContext(ctx, st.sql); err != nil {
			err = fmt.Errorf("%s line %d: %w", mg.file, st.line, err)
			if dirty {
				err = fmt.Errorf("%w (the migration is recorded as dirty)", err)
			}
			return &Error{Op: "migrate", Kind: m.s.driver.Classify(err), Err: err}
		}
	}
	return nil
}

// check compares the history with the source and returns the index of the
// first pending migration, or len(m.set) when none is pending.
func (m *migrator) check(hist []historyRow) (int, error) {
	for _, h := range hist {
		if h.Dirty != 0 {
			return 0, &Error{Op: "migrate", Kind: ErrMigrationDirty, Err: fmt.Errorf("migration %d %q started but did not finish", h.Version, h.Name)}
		}
	}
	index := make(map[int64]int, len(m.set))
	for i, mg := range m.set {
		index[mg.version] = i
	}
	applied := make(map[int64]bool, len(hist))
	var last int64
	for _, h := range hist {
		i, ok := index[h.Version]
		if !ok {
			return 0, &Error{Op: "migrate", Kind: ErrMigrationMismatch, Err: fmt.Errorf("migration %d %q is applied but missing from the source", h.Version, h.Name)}
		}
		if m.set[i].checksum != h.Checksum {
			return 0, &Error{Op: "migrate", Kind: ErrMigrationChanged, Err: fmt.Errorf("%s changed after it was applied", m.set[i].file)}
		}
		applied[h.Version] = true
		last = max(last, h.Version)
	}
	for i, mg := range m.set {
		if applied[mg.version] {
			continue
		}
		if mg.version < last {
			return 0, &Error{Op: "migrate", Kind: ErrMigrationMismatch, Err: fmt.Errorf("migration %d %q is pending but sorts before applied migration %d", mg.version, mg.name, last)}
		}
		return i, nil
	}
	return len(m.set), nil
}

// createQuery creates the history table when it does not exist.
func (m *migrator) createQuery() string {
	return "CREATE TABLE IF NOT EXISTS " + m.table + " (" +
		"version BIGINT NOT NULL PRIMARY KEY, " +
		"name VARCHAR(255) NOT NULL, " +
		"checksum CHAR(64) NOT NULL, " +
		"dirty SMALLINT NOT NULL, " +
		"applied_at BIGINT NOT NULL)"
}

// selectQuery reads the history table in version order.
func (m *migrator) selectQuery() string {
	return "SELECT version, name, checksum, dirty, applied_at FROM " + m.table + " ORDER BY version"
}

// existsQuery counts the history tables named by its argument that a query
// would read.
func (m *migrator) existsQuery() string {
	switch m.s.dialect {
	case Postgres:
		return "SELECT CASE WHEN to_regclass(CAST(? AS text)) IS NULL THEN 0 ELSE 1 END"
	case MySQL:
		return "SELECT count(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	default:
		return "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	}
}
