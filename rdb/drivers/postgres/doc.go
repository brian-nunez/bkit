// Package postgres connects rdb to PostgreSQL through pgx.
//
// Pass [Driver] to rdb.Open to open postgres: and postgresql: URLs, or call
// [Open] with a [Config]:
//
//	db, err := rdb.Open(ctx, "postgres://app:secret@db.internal/app", postgres.Driver())
//	db, err := postgres.Open(ctx, postgres.Config{Host: "db.internal", Database: "app"})
//
// URL query parameters and Config.Params are pgx connection parameters, such
// as sslmode and application_name. Settings left empty fall back to the pgx
// defaults, which read the standard PG* environment variables.
//
// rdb rewrites ? placeholders as $1, $2, and so on for this driver.
//
// Errors match rdb.ErrUniqueViolation, rdb.ErrForeignKeyViolation,
// rdb.ErrNotNullViolation, rdb.ErrCheckViolation, and, for serialization
// failures and deadlocks, rdb.ErrRetryable. Connection settings pgx cannot
// parse fail with rdb.ErrInvalidURL, and the message never includes them.
//
// The driver is stateless and safe for concurrent use.
package postgres
