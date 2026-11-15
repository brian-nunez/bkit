---
status: "accepted"
date: 2026-11-15
decision-makers: Brian Nunez
---

# Run rdb migrations in the parent module, the same way on every database

## Context and Problem Statement

Applications built on rdb need to evolve their schema, and an application
can switch between SQLite, Postgres, MySQL, and MariaDB by changing its
database URL. Migrations must therefore behave the same on every database,
and per-database differences must be ones the databases themselves force.
Where does migration logic live, and how does a migration file run on
databases that read SQL differently?

## Decision Drivers

* Every database gets the same capability and the same rules. No driver
  exposes migration functions of its own.
* No new dependencies, and no change to the `rdb.Driver` interface.
* Several application instances starting at once apply each migration
  exactly once.
* A migration that may have left the schema half-changed is never run past
  silently.
* The history format is hard to change once databases hold it, so it stays
  small and portable.

## Considered Options

* rdb runs migrations itself and splits each file into statements
* rdb runs each file as one script, with new `rdb.Driver` methods for
  multi-statement execution and locking
* An external migration tool or library

## Decision Outcome

Chosen option: "rdb runs migrations itself and splits each file into
statements", because it is the only option that gives every database the
same behavior with no driver changes and no new dependencies.

* **Source.** Migrations are files in the root of an `fs.FS`, named
  `<version>_<name>.sql`. A `<version>_<name>.<dialect>.sql` file replaces
  the plain file on that dialect. Versions are positive 64-bit integers, so
  sequence numbers and timestamps both work.
* **Statements.** rdb splits a file at semicolons outside quotes and
  comments, reading quotes and comments the way the database's dialect
  does. `-- rdb:begin-statement` and `-- rdb:end-statement` mark one
  statement whose body holds semicolons, such as a trigger. Each statement
  is sent alone and exactly as written, so MySQL needs no multi-statement
  mode.
* **History.** A table, `rdb_migrations` by default, holds each applied
  migration's version, name, SHA-256 checksum, a dirty flag, and the time
  it finished, using column types every supported database accepts.
* **Atomicity.** On SQLite and Postgres a migration and its history row
  commit together. MySQL and MariaDB commit schema changes implicitly, and
  `-- rdb:no-transaction` opts a file out of the transaction on any
  database. In both cases the history row is committed as dirty first and
  cleared when the migration finishes, and a dirty row stops every later
  run with `ErrMigrationDirty`.
* **Turns.** Postgres runs take an advisory lock and MySQL runs take a
  named lock, both polled so a waiting run holds no open statement. SQLite
  has no session lock, so each step takes the database's write lock with an
  immediate transaction and reads the history inside it.
* **Checks.** Before applying anything, a run compares the history with the
  source and stops on a changed file, a missing applied migration, or a
  pending migration that sorts before an applied one.
* **Forward only.** There are no down migrations. A new migration reverses
  an old one.

### Consequences

* Good, because every database supports the same files, directives,
  history, and errors, and `rdbtest.RunMigrations` checks each driver
  against them.
* Good, because drivers and the `rdb.Driver` interface are unchanged, so
  migrations ship in an rdb minor release.
* Good, because MySQL keeps multi-statement mode off, which limits SQL
  injection.
* Bad, because rdb must lex three SQL dialects. A body with semicolons
  outside quotes needs the block directives, and MySQL with
  `NO_BACKSLASH_ESCAPES` reads backslashes differently than the splitter.
* Bad, because a SQLite migration marked no-transaction runs outside the
  write lock, since SQLite can hold a lock only inside a transaction. A
  run that starts meanwhile stops with `ErrMigrationDirty` instead of
  waiting.
* Bad, because a failure on MySQL, MariaDB, or in a no-transaction file
  needs a person to repair the schema and the history row.
* Bad, because migrations support only the dialects rdb defines. A
  third-party driver with another dialect gets an error.

### Confirmation

rdb's unit tests cover the splitter, loading, and the run logic at 100%
statement coverage with a fake driver. `rdbtest.RunMigrations` runs the same
scenarios against a real database, and each driver runs it in its
integration tests.

## Pros and Cons of the Options

### rdb runs migrations itself and splits each file into statements

* Good, because the behavior lives in one place for every database.
* Good, because errors name the file and line of the failing statement.
* Bad, because the splitter must follow each dialect's quoting rules.

### rdb runs each file as one script, with new `rdb.Driver` methods

* Good, because each database parses its own scripts.
* Bad, because every driver gains migration-specific methods, and drivers
  outside this repository break.
* Bad, because MySQL needs multi-statement mode, and Postgres runs a
  multi-statement script in one implicit transaction, so
  `CREATE INDEX CONCURRENTLY` still needs splitting.
* Bad, because rdb and all three drivers must release together.

### An external migration tool or library

* Good, because the work is already done.
* Bad, because it adds a dependency, and its database support and locking
  differ from database to database.
* Bad, because it does not use rdb's drivers, URLs, or errors.
