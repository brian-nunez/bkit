// Package rdb provides one API over relational databases, so an application
// can switch between SQLite, Postgres, MySQL, and other SQL databases by
// changing a URL.
//
// The package owns opening and tuning the connection pool, rewriting
// placeholders for each dialect, classifying driver errors into sentinel
// errors, scanning rows into Go values, running transactions, and applying
// schema migrations. Drivers live in their own modules under
// github.com/brian-nunez/bkit/rdb/drivers and are passed to [Open] by the
// application; rdb keeps no global registry.
// Callers supply the database URL, the drivers they support, and the SQL.
//
// # Placeholders
//
// Write ? placeholders. A [DB] rewrites them as $1, $2, ... for [Postgres]
// and sends them unchanged to databases that use ?. A ? inside a string
// constant, quoted identifier, or comment is left alone. [DB.Raw] and
// [Tx.Raw] send single queries exactly as written, such as a Postgres query
// that uses the jsonb ? operator; [WithoutRebind] turns rewriting off for a
// whole DB.
//
// # Migrations
//
// [DB.Migrate] applies the .sql files in an [io/fs.FS], such as an
// [embed.FS], in version order, and records them in a history table.
// [DB.MigrationStatus] reports which have run. Migrations behave the same on
// every database rdb defines a [Dialect] for: rdb splits each file into
// statements itself, sends each one exactly as written, and makes runs on
// the same database take turns. A file named for a dialect, such as
// 0001_init.postgres.sql, replaces the plain file on that dialect.
//
// SQLite and Postgres apply each migration in a transaction. MySQL and
// MariaDB commit schema changes as they run, so a failed migration there,
// or in a file marked "-- rdb:no-transaction" on any database, stays
// recorded as dirty and stops later runs with [ErrMigrationDirty].
//
// # Errors
//
// Failures are *[Error] values. They match a sentinel such as
// [ErrUniqueViolation] or [ErrNotFound] with [errors.Is] and unwrap to the
// driver's original error. Error messages never contain the database URL.
//
// # Concurrency
//
// A [DB] is safe for concurrent use; its zero value is not usable. A [Tx]
// is valid only inside the function passed to [DB.Tx]. Every query honors
// its context.
package rdb
