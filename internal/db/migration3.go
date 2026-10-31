package db

import (
	"context"
	"database/sql"
)

// migrateTo3 adds job_items.download_bytes, the bytes of encrypted media an
// item's current attempt has received (domain.ItemProgress.DownloadBytes).
// Existing rows get 0, which is also what a transfer that cannot be measured
// reports, so no row loses information. Frozen like every shipped migration.
func migrateTo3(ctx context.Context, tx *sql.Tx) error {
	return execAll(ctx, tx, []string{
		`ALTER TABLE job_items ADD COLUMN download_bytes INTEGER NOT NULL DEFAULT 0`,
	})
}
