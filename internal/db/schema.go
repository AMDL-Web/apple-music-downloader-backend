package db

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations is the ordered schema history: migrations[i] brings a database
// from PRAGMA user_version i to i+1, in its own transaction together with the
// version bump. A shipped migration is never edited, because databases that
// already ran it will not run it again; schema changes append a new entry.
// Every entry preserves the rows of user data (jobs, items, milestone events,
// library-sync anchor).
var migrations = []func(ctx context.Context, tx *sql.Tx) error{
	migrateTo1, // the final 1.x schema, for databases created by any 1.x release
	migrateTo2, // 2.0: job timing columns, counters derived from items, event pruning
	migrateTo3, // job_items.download_bytes
}

// latestSchemaVersion is the user_version a database has after Open.
func latestSchemaVersion() int { return len(migrations) }

func schemaVersion(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (int, error) {
	var version int
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// migrate applies every migration between the database's user_version and
// target. A database newer than this build is refused rather than written to.
func migrate(ctx context.Context, database *sql.DB, target int) error {
	current, err := schemaVersion(ctx, database)
	if err != nil {
		return err
	}
	if current > latestSchemaVersion() {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d); run a release at least as new as the one that wrote it", current, latestSchemaVersion())
	}
	for version := current + 1; version <= target; version++ {
		if err := applyMigration(ctx, database, version); err != nil {
			return fmt.Errorf("migrate database to schema version %d: %w", version, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, database *sql.DB, version int) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrations[version-1](ctx, tx); err != nil {
		return err
	}
	// PRAGMA takes no bind parameters; version is an int from this package.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return tx.Commit()
}

// execAll runs stmts in order inside tx.
func execAll(ctx context.Context, tx *sql.Tx, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", firstLine(stmt), err)
		}
	}
	return nil
}

func firstLine(stmt string) string {
	for i, r := range stmt {
		if r == '\n' {
			return stmt[:i]
		}
	}
	return stmt
}
