package db

import (
	"context"
	"time"
)

// ConfigSettings returns the stored configuration layer: dotted config keys
// mapped to JSON-encoded values. Only keys someone changed through
// PUT /api/v1/config have a row — an absent key is not "empty", it means the
// config file or the built-in default supplies the value instead.
func (s *Store) ConfigSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, rows.Err()
}

// ApplyConfigSettings writes one batch of changes in a single transaction: a
// non-nil value upserts the key, a nil value deletes its row so the key falls
// back to the layers below. All-or-nothing, so a failed write cannot leave the
// stored configuration half-updated.
func (s *Store) ApplyConfigSettings(ctx context.Context, changes map[string]*string) error {
	if len(changes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, value := range changes {
		if value == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, key); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key,value,updated_at) VALUES (?,?,?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			key, *value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
