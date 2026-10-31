package db

import (
	"context"
	"strings"
	"time"

	"amdl/internal/domain"
)

func (s *Store) CreateItem(ctx context.Context, item domain.JobItem) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_items(id,job_id,adam_id,kind,idx,title,artist,album,duration_ms,artwork_url,has_lyrics,lyrics_status,status,download_progress,decrypt_progress,download_bytes,progress_resolved,progress_remuxed,progress_verified,progress_tagged,progress_saved,codec,bit_depth,sample_rate,bitrate,file_size,retry_kind,attempt,max_attempts,status_message,output_path,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.JobID, item.AdamID, item.Kind, item.Index, item.Title, item.Artist, item.Album, item.DurationMS, item.ArtworkURL, item.HasLyrics, string(item.LyricsStatus),
		string(item.Status), item.Progress.Download, item.Progress.Decrypt, item.Progress.DownloadBytes, item.Progress.Resolved, item.Progress.Remuxed, item.Progress.Verified, item.Progress.Tagged, item.Progress.Saved,
		item.Codec, item.BitDepth, item.SampleRate, item.Bitrate, item.FileSize, item.RetryKind, item.Attempt, item.MaxAttempts, item.StatusMessage, item.OutputPath, item.Error, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	return err
}

func (s *Store) UpdateItem(ctx context.Context, item domain.JobItem) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items SET title=?,artist=?,album=?,duration_ms=?,artwork_url=?,has_lyrics=?,lyrics_status=?,status=?,download_progress=?,decrypt_progress=?,download_bytes=?,progress_resolved=?,progress_remuxed=?,progress_verified=?,progress_tagged=?,progress_saved=?,codec=?,bit_depth=?,sample_rate=?,bitrate=?,file_size=?,retry_kind=?,attempt=?,max_attempts=?,status_message=?,output_path=?,error=?,updated_at=? WHERE id=?`,
		item.Title, item.Artist, item.Album, item.DurationMS, item.ArtworkURL, item.HasLyrics, string(item.LyricsStatus), string(item.Status),
		item.Progress.Download, item.Progress.Decrypt, item.Progress.DownloadBytes, item.Progress.Resolved, item.Progress.Remuxed, item.Progress.Verified, item.Progress.Tagged, item.Progress.Saved,
		item.Codec, item.BitDepth, item.SampleRate, item.Bitrate, item.FileSize, item.RetryKind, item.Attempt, item.MaxAttempts, item.StatusMessage, item.OutputPath, item.Error, formatTime(item.UpdatedAt), item.ID)
	return err
}

// ResetUnfinishedItems returns every item of jobID that has not finished
// (i.e. is neither completed nor skipped_existing) to its pre-download queued
// state in a single statement, so callers holding scheduling locks don't pay
// one round-trip per item on large collections. The cleared fields must stay
// in sync with domain.JobItem.ResetForRetry, which is the per-item form of
// the same reset.
func (s *Store) ResetUnfinishedItems(ctx context.Context, jobID string, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items
		SET status=?, download_progress=0, decrypt_progress=0, download_bytes=0,
			progress_resolved=0, progress_remuxed=0, progress_verified=0, progress_tagged=0, progress_saved=0,
			codec='', bit_depth=0, sample_rate=0, bitrate=0, file_size=0,
			lyrics_status='', retry_kind='', attempt=0, max_attempts=0, status_message='', error='', updated_at=?
		WHERE job_id=? AND status NOT IN (?,?)`,
		string(domain.ItemQueued), formatTime(updatedAt), jobID,
		string(domain.ItemCompleted), string(domain.ItemSkipped))
	return err
}

// CancelUnfinishedItems ends every item of jobID that has not reached a
// terminal state (completed, skipped_existing, failed or cancelled) as
// cancelled with statusMessage, in one statement. It is the queued-job form of
// the per-item cancel internal/media writes while a job runs; other columns
// keep the item's last state.
func (s *Store) CancelUnfinishedItems(ctx context.Context, jobID, statusMessage string, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items SET status=?, status_message=?, updated_at=?
		WHERE job_id=? AND status NOT IN (?,?,?,?)`,
		string(domain.ItemCancelled), statusMessage, formatTime(updatedAt), jobID,
		string(domain.ItemCompleted), string(domain.ItemSkipped), string(domain.ItemFailed), string(domain.ItemCancelled))
	return err
}

// DeleteItem removes a single item row. Used when a re-run of a job (retry or
// post-restart requeue) resolves a collection that no longer contains a track
// a previous run created an item for, so stale rows don't distort the job's
// progress counters.
func (s *Store) DeleteItem(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_items WHERE id=?`, id)
	return err
}

// ItemProgress returns the items of each of jobIDs with only what a job's
// progress is computed from: status and the progress columns. One query
// serves a whole list page.
func (s *Store) ItemProgress(ctx context.Context, jobIDs []string) (map[string][]domain.JobItem, error) {
	out := make(map[string][]domain.JobItem, len(jobIDs))
	if len(jobIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(jobIDs))
	for i, id := range jobIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT job_id,status,download_progress,decrypt_progress,progress_resolved,progress_remuxed,progress_verified,progress_tagged,progress_saved FROM job_items WHERE job_id IN (?`+strings.Repeat(",?", len(jobIDs)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it domain.JobItem
		var status string
		if err := rows.Scan(&it.JobID, &status, &it.Progress.Download, &it.Progress.Decrypt, &it.Progress.Resolved, &it.Progress.Remuxed, &it.Progress.Verified, &it.Progress.Tagged, &it.Progress.Saved); err != nil {
			return nil, err
		}
		it.Status = domain.ItemStatus(status)
		out[it.JobID] = append(out[it.JobID], it)
	}
	return out, rows.Err()
}

func (s *Store) ListItems(ctx context.Context, jobID string) ([]domain.JobItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,adam_id,kind,idx,title,artist,album,duration_ms,artwork_url,has_lyrics,lyrics_status,status,download_progress,decrypt_progress,download_bytes,progress_resolved,progress_remuxed,progress_verified,progress_tagged,progress_saved,codec,bit_depth,sample_rate,bitrate,file_size,retry_kind,attempt,max_attempts,status_message,output_path,error,created_at,updated_at FROM job_items WHERE job_id=? ORDER BY idx`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.JobItem, 0)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanItem(row jobScanner) (domain.JobItem, error) {
	var item domain.JobItem
	var lyricsStatus, status, created, updated string
	err := row.Scan(&item.ID, &item.JobID, &item.AdamID, &item.Kind, &item.Index, &item.Title, &item.Artist, &item.Album, &item.DurationMS, &item.ArtworkURL, &item.HasLyrics, &lyricsStatus, &status,
		&item.Progress.Download, &item.Progress.Decrypt, &item.Progress.DownloadBytes, &item.Progress.Resolved, &item.Progress.Remuxed, &item.Progress.Verified, &item.Progress.Tagged, &item.Progress.Saved,
		&item.Codec, &item.BitDepth, &item.SampleRate, &item.Bitrate, &item.FileSize, &item.RetryKind, &item.Attempt, &item.MaxAttempts, &item.StatusMessage, &item.OutputPath, &item.Error, &created, &updated)
	item.LyricsStatus = domain.LyricsStatus(lyricsStatus)
	item.Status = domain.ItemStatus(status)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, err
}
