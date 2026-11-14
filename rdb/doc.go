// Package rdb provides one API over relational databases, so an application
// can switch between SQLite, Postgres, MySQL, and other SQL databases by
// changing a URL.
//
// The package owns opening and tuning the connection pool, rewriting
// placeholders for each dialect, classifying driver errors into sentinel
// errors, scanning rows into Go values, and running transactions. Drivers
// live in their own modules under github.com/brian-nunez/bkit/rdb/drivers
// and are passed to [Open] by the application; rdb keeps no global registry.
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
