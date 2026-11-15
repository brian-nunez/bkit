package rdb

import (
	"errors"
	"strings"
)

// Sentinel errors. They arrive wrapped in *Error; match them with errors.Is.
var (
	// ErrNotFound reports that a query for one row returned no rows. The
	// error also matches sql.ErrNoRows.
	ErrNotFound = errors.New("rdb: not found")

	// ErrUniqueViolation reports a write that would duplicate a value in a
	// unique index or primary key.
	ErrUniqueViolation = errors.New("rdb: unique violation")

	// ErrForeignKeyViolation reports a write that would break a foreign key.
	ErrForeignKeyViolation = errors.New("rdb: foreign key violation")

	// ErrNotNullViolation reports a write that would store null in a
	// NOT NULL column.
	ErrNotNullViolation = errors.New("rdb: not null violation")

	// ErrCheckViolation reports a write that would fail a CHECK constraint.
	ErrCheckViolation = errors.New("rdb: check violation")

	// ErrRetryable reports a failure that may succeed if the whole
	// transaction runs again, such as a deadlock, a serialization failure,
	// or a locked database.
	ErrRetryable = errors.New("rdb: retryable")

	// ErrScan reports rows that cannot be scanned into the requested type:
	// a column with no matching field, two fields for one column, a column
	// that appears twice, or a value that does not convert.
	ErrScan = errors.New("rdb: scan failed")

	// ErrUnknownDriver reports a URL whose scheme matches no driver passed
	// to Open.
	ErrUnknownDriver = errors.New("rdb: unknown driver")

	// ErrInvalidURL reports a database URL that cannot be parsed or has no
	// scheme.
	ErrInvalidURL = errors.New("rdb: invalid url")

	// ErrInvalidOption reports an option value that is out of range or
	// conflicts with another option or with the database.
	ErrInvalidOption = errors.New("rdb: invalid option")

	// ErrInvalidMigration reports a migration source that cannot run: a file
	// name that does not follow the naming rules, two files for one version
	// and dialect, a version with no file for the database's dialect, a
	// misused directive, or no migrations at all.
	ErrInvalidMigration = errors.New("rdb: invalid migration")

	// ErrMigrationChanged reports an applied migration whose file has
	// changed since it ran. Add a new migration instead of editing one that
	// has run.
	ErrMigrationChanged = errors.New("rdb: migration changed")

	// ErrMigrationMismatch reports a database whose applied migrations do
	// not fit the source: an applied migration is missing from the source,
	// as when an older build starts after a newer one migrated, or a pending
	// migration sorts before one that has run.
	ErrMigrationMismatch = errors.New("rdb: migration mismatch")

	// ErrMigrationDirty reports a migration that started but did not finish
	// on a database that cannot roll it back, so the schema may be partly
	// changed. Repair the schema, then update the history table.
	ErrMigrationDirty = errors.New("rdb: migration dirty")
)

// Error describes a failed operation. It matches its Kind and its Err with
// errors.Is and errors.As. Its message never contains the database URL.
type Error struct {
	// Op names the operation, such as "open", "exec", "query", "scan", or
	// "commit".
	Op string

	// Kind is the sentinel error classifying the failure, or nil when the
	// failure fits no sentinel.
	Kind error

	// Err is the underlying error, such as the driver's error, or nil.
	Err error
}

// Error formats the operation, classification, and cause.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("rdb: ")
	b.WriteString(e.Op)
	if e.Kind != nil {
		b.WriteString(": ")
		b.WriteString(strings.TrimPrefix(e.Kind.Error(), "rdb: "))
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns Kind and Err, omitting nil values.
func (e *Error) Unwrap() []error {
	errs := make([]error, 0, 2)
	if e.Kind != nil {
		errs = append(errs, e.Kind)
	}
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	return errs
}
