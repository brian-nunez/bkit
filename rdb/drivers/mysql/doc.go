// Package mysql connects rdb to MySQL and MariaDB through
// github.com/go-sql-driver/mysql.
//
// Pass [Driver] to rdb.Open to open mysql: and mariadb: URLs, or call [Open]
// with a [Config]:
//
//	db, err := rdb.Open(ctx, "mysql://app:secret@db.internal/app", mysql.Driver())
//	db, err := mysql.Open(ctx, mysql.Config{Host: "db.internal", Database: "app"})
//
// URL query parameters and Config.Params are go-sql-driver/mysql DSN
// parameters, such as tls and timeout. Unknown parameters become session
// variables. parseTime defaults to true, so DATE and DATETIME columns scan
// into time.Time.
//
// Errors match rdb.ErrUniqueViolation, rdb.ErrForeignKeyViolation,
// rdb.ErrNotNullViolation, rdb.ErrCheckViolation, and, for deadlocks and
// lock wait timeouts, rdb.ErrRetryable. Connection settings the driver
// cannot parse fail with rdb.ErrInvalidURL, and the message never includes
// them.
//
// The driver is stateless and safe for concurrent use.
package mysql
