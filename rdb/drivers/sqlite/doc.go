// Package sqlite connects rdb to SQLite through modernc.org/sqlite, a pure
// Go build of SQLite that needs no cgo.
//
// Pass [Driver] to rdb.Open to open sqlite: URLs, or call [Open] with a
// [Config]:
//
//	db, err := rdb.Open(ctx, "sqlite:app.db", sqlite.Driver())
//	db, err := sqlite.Open(ctx, sqlite.Config{Path: "app.db"})
//
// Every connection turns on foreign keys and waits up to five seconds for a
// lock. File databases use write-ahead logging, so readers run alongside a
// writer. An in-memory database is limited to one connection, because each
// connection to ":memory:" would see its own empty database. URL query
// parameters pass to the SQLite driver, so ?_pragma=busy_timeout(1000)
// overrides a default.
//
// Errors match rdb.ErrUniqueViolation, rdb.ErrForeignKeyViolation,
// rdb.ErrNotNullViolation, rdb.ErrCheckViolation, and, for a busy or locked
// database, rdb.ErrRetryable.
//
// The driver is stateless and safe for concurrent use.
package sqlite
