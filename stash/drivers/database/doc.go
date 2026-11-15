// Package database provides a stash store that keeps keys in a table of a
// SQL database opened with rdb: SQLite, Postgres, or MySQL and MariaDB.
//
// [New] takes the application's *rdb.DB and shares its connection pool:
//
//	db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"), sqlite.Driver(), postgres.Driver())
//	s, err := database.New(ctx, db)
//
// New creates the table, "stash" unless [WithTable] names another, and an
// index on its expiry column when they are missing. Closing the store leaves
// the database open.
//
// Expiry times are computed from the application's clock, in milliseconds.
// Expired rows are never returned, and every hundredth write deletes them.
//
// Set with IfNotExists is one statement. Incr runs in a transaction that
// locks the row, and retries when two callers create the same counter at
// once, so no increment is lost.
//
// A store is safe for concurrent use.
package database
