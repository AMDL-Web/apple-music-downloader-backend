package db

import (
	"context"

	"amdl/internal/domain"
)

// LoadLibrarySyncAnchor returns the remembered library songs in order, newest
// first. An empty result means the watcher has never anchored (or was reset)
// and should re-anchor without submitting anything.
func (s *Store) LoadLibrarySyncAnchor(ctx context.Context) ([]domain.LibraryAnchorEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT song_id,song_name,album_name FROM library_sync_anchor ORDER BY position ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.LibraryAnchorEntry, 0)
	for rows.Next() {
		var e domain.LibraryAnchorEntry
		if err := rows.Scan(&e.SongID, &e.Name, &e.AlbumName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ReplaceLibrarySyncAnchor swaps the whole anchor in one transaction. Whole
// replacement rather than an incremental update because the anchor is a
// positional window, not a set: entry N's meaning depends on what sits above
// it, so a half-applied update would leave a window that never existed and
// could make an old song look new.
func (s *Store) ReplaceLibrarySyncAnchor(ctx context.Context, entries []domain.LibraryAnchorEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_sync_anchor`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO library_sync_anchor (position,song_id,song_name,album_name) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, e := range entries {
		if _, err := stmt.ExecContext(ctx, i, e.SongID, e.Name, e.AlbumName); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClearLibrarySyncAnchor drops the anchor and reports how many entries went.
// The watcher re-anchors to the current library on its next tick and submits
// nothing for it, so this forgets history rather than replaying it.
func (s *Store) ClearLibrarySyncAnchor(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM library_sync_anchor`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
