package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"amdl/internal/domain"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrDuplicateActive is returned by CreateJob when the job's canonical_key
// collides with another queued/running job (partial unique index backstop).
var ErrDuplicateActive = errors.New("duplicate active job")

// ErrJobNotFound is returned by DeleteJob when no job has the given id.
var ErrJobNotFound = errors.New("job not found")

// ErrJobNotTerminal is returned by DeleteJob when the job is still queued or
// running; active jobs must be cancelled before they can be deleted.
var ErrJobNotTerminal = errors.New("job is not in a terminal status")

func isUniqueConstraintErr(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT
	}
	return false
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	s := &Store{db: database}
	if err := s.initSchema(context.Background()); err != nil {
		_ = database.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) initSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			input TEXT NOT NULL,
			type TEXT NOT NULL,
			storefront TEXT,
			title TEXT NOT NULL DEFAULT '',
			artwork_url TEXT NOT NULL DEFAULT '',
			canonical_key TEXT NOT NULL,
			force INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			total_items INTEGER NOT NULL DEFAULT 0,
			done_items INTEGER NOT NULL DEFAULT 0,
			failed_items INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_active_key
			ON jobs(canonical_key)
			WHERE status IN ('queued','running');`,
		`CREATE TABLE IF NOT EXISTS job_items (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			adam_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			idx INTEGER NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			artist TEXT NOT NULL DEFAULT '',
			album TEXT NOT NULL DEFAULT '',
			artwork_url TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			progress REAL NOT NULL DEFAULT 0,
			codec TEXT NOT NULL DEFAULT '',
			bit_depth INTEGER NOT NULL DEFAULT 0,
			sample_rate INTEGER NOT NULL DEFAULT 0,
			bitrate INTEGER NOT NULL DEFAULT 0,
			retry_kind TEXT NOT NULL DEFAULT '',
			attempt INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 0,
			status_message TEXT NOT NULL DEFAULT '',
			output_path TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(job_id) REFERENCES jobs(id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_items_job_id ON job_items(job_id, idx);`,
		`CREATE TABLE IF NOT EXISTS job_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id TEXT NOT NULL,
			item_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL,
			phase TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_events_job_id_id ON job_events(job_id, id);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	// Additive migrations for databases created before these columns existed;
	// CREATE TABLE IF NOT EXISTS leaves pre-existing tables untouched.
	for _, col := range []struct{ table, column, decl string }{
		{"jobs", "artwork_url", "TEXT NOT NULL DEFAULT ''"},
		{"jobs", "title", "TEXT NOT NULL DEFAULT ''"},
		{"job_items", "artwork_url", "TEXT NOT NULL DEFAULT ''"},
		{"job_items", "bit_depth", "INTEGER NOT NULL DEFAULT 0"},
		{"job_items", "sample_rate", "INTEGER NOT NULL DEFAULT 0"},
		{"job_items", "bitrate", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(ctx, col.table, col.column, col.decl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table, column, decl string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+decl)
	return err
}

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

func (s *Store) CreateJob(ctx context.Context, job domain.Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,input,type,storefront,title,artwork_url,canonical_key,force,status,total_items,done_items,failed_items,error,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Input, job.Type, job.Storefront, job.Title, job.ArtworkURL, job.CanonicalKey, job.Force, string(job.Status), job.TotalItems,
		job.DoneItems, job.FailedItems, job.Error, formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	if err != nil && isUniqueConstraintErr(err) {
		return ErrDuplicateActive
	}
	return err
}

// FindActiveJobByKey returns the queued/running job matching canonicalKey, if any.
func (s *Store) FindActiveJobByKey(ctx context.Context, canonicalKey string) (domain.Job, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,input,type,storefront,title,artwork_url,canonical_key,force,status,total_items,done_items,failed_items,error,created_at,updated_at
		FROM jobs WHERE canonical_key=? AND status IN (?,?)`, canonicalKey, string(domain.JobQueued), string(domain.JobRunning))
	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Job{}, false, nil
		}
		return domain.Job{}, false, err
	}
	return job, true, nil
}

// execer is satisfied by both *sql.DB and *sql.Tx, letting the same query
// helper run standalone or as part of a larger transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func updateJob(ctx context.Context, x execer, job domain.Job) error {
	_, err := x.ExecContext(ctx, `UPDATE jobs SET type=?, storefront=?, title=?, artwork_url=?, force=?, status=?, total_items=?, done_items=?, failed_items=?, error=?, updated_at=? WHERE id=?`,
		job.Type, job.Storefront, job.Title, job.ArtworkURL, job.Force, string(job.Status), job.TotalItems, job.DoneItems, job.FailedItems, job.Error, formatTime(job.UpdatedAt), job.ID)
	return err
}

func (s *Store) UpdateJob(ctx context.Context, job domain.Job) error {
	return updateJob(ctx, s.db, job)
}

func (s *Store) UpdateJobStatus(ctx context.Context, id string, status domain.JobStatus, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status=?, updated_at=? WHERE id=?`, string(status), formatTime(updatedAt), id)
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,input,type,storefront,title,artwork_url,canonical_key,force,status,total_items,done_items,failed_items,error,created_at,updated_at FROM jobs WHERE id=?`, id)
	return scanJob(row)
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// done_items/failed_items are derived live from job_items instead of the
	// stored jobs columns: the stored counters are only written back when a job
	// finalizes, so running jobs (or jobs interrupted before finalize) would
	// report stale counts. Status buckets must mirror domain.CountItemProgress.
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.input,j.type,j.storefront,j.title,j.artwork_url,j.canonical_key,j.force,j.status,j.total_items,
			(SELECT COUNT(*) FROM job_items i WHERE i.job_id=j.id AND i.status IN (?,?)) AS done_items,
			(SELECT COUNT(*) FROM job_items i WHERE i.job_id=j.id AND i.status=?) AS failed_items,
			j.error,j.created_at,j.updated_at FROM jobs j ORDER BY j.created_at DESC LIMIT ?`,
		string(domain.ItemCompleted), string(domain.ItemSkipped), string(domain.ItemFailed), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// DeleteJob removes a terminal (completed/failed/cancelled) job together with
// its items and events. Queued/running jobs are refused with ErrJobNotTerminal
// so an in-flight worker never loses the rows it is still updating; cancel the
// job first. The status check and the three deletes run in one transaction.
//
// The row status alone cannot tell whether the manager's finalize sequence
// (terminal event insert + hook dispatch) has finished — callers must go
// through jobs.Manager.Delete, which additionally refuses while a finalize is
// still in flight.
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		}
		return err
	}
	if !domain.JobStatus(status).IsTerminal() {
		return ErrJobNotTerminal
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE job_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_items WHERE job_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListRecoverableJobs(ctx context.Context) ([]domain.Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,input,type,storefront,title,artwork_url,canonical_key,force,status,total_items,done_items,failed_items,error,created_at,updated_at FROM jobs WHERE status IN (?,?) ORDER BY created_at ASC`,
		string(domain.JobQueued), string(domain.JobRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(row jobScanner) (domain.Job, error) {
	var job domain.Job
	var status, created, updated string
	err := row.Scan(&job.ID, &job.Input, &job.Type, &job.Storefront, &job.Title, &job.ArtworkURL, &job.CanonicalKey, &job.Force, &status, &job.TotalItems, &job.DoneItems, &job.FailedItems, &job.Error, &created, &updated)
	job.Status = domain.JobStatus(status)
	job.CreatedAt = parseTime(created)
	job.UpdatedAt = parseTime(updated)
	return job, err
}

func (s *Store) CreateItem(ctx context.Context, item domain.JobItem) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_items(id,job_id,adam_id,kind,idx,title,artist,album,artwork_url,status,progress,codec,bit_depth,sample_rate,bitrate,retry_kind,attempt,max_attempts,status_message,output_path,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.JobID, item.AdamID, item.Kind, item.Index, item.Title, item.Artist, item.Album, item.ArtworkURL,
		string(item.Status), item.Progress, item.Codec, item.BitDepth, item.SampleRate, item.Bitrate, item.RetryKind, item.Attempt, item.MaxAttempts, item.StatusMessage, item.OutputPath, item.Error, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	return err
}

func (s *Store) UpdateItem(ctx context.Context, item domain.JobItem) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items SET title=?,artist=?,album=?,artwork_url=?,status=?,progress=?,codec=?,bit_depth=?,sample_rate=?,bitrate=?,retry_kind=?,attempt=?,max_attempts=?,status_message=?,output_path=?,error=?,updated_at=? WHERE id=?`,
		item.Title, item.Artist, item.Album, item.ArtworkURL, string(item.Status), item.Progress, item.Codec, item.BitDepth, item.SampleRate, item.Bitrate, item.RetryKind, item.Attempt, item.MaxAttempts, item.StatusMessage, item.OutputPath, item.Error, formatTime(item.UpdatedAt), item.ID)
	return err
}

func (s *Store) ListItems(ctx context.Context, jobID string) ([]domain.JobItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,adam_id,kind,idx,title,artist,album,artwork_url,status,progress,codec,bit_depth,sample_rate,bitrate,retry_kind,attempt,max_attempts,status_message,output_path,error,created_at,updated_at FROM job_items WHERE job_id=? ORDER BY idx`, jobID)
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
	var status, created, updated string
	err := row.Scan(&item.ID, &item.JobID, &item.AdamID, &item.Kind, &item.Index, &item.Title, &item.Artist, &item.Album, &item.ArtworkURL, &status,
		&item.Progress, &item.Codec, &item.BitDepth, &item.SampleRate, &item.Bitrate, &item.RetryKind, &item.Attempt, &item.MaxAttempts, &item.StatusMessage, &item.OutputPath, &item.Error, &created, &updated)
	item.Status = domain.ItemStatus(status)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, err
}

func addEvent(ctx context.Context, x execer, event domain.Event) (domain.Event, error) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now()
	}
	res, err := x.ExecContext(ctx, `INSERT INTO job_events(job_id,item_id,type,phase,message,payload,created_at) VALUES(?,?,?,?,?,?,?)`,
		event.JobID, event.ItemID, event.Type, event.Phase, event.Message, event.Payload, formatTime(event.CreatedAt))
	if err != nil {
		return event, err
	}
	event.ID, _ = res.LastInsertId()
	return event, nil
}

func (s *Store) AddEvent(ctx context.Context, event domain.Event) (domain.Event, error) {
	return addEvent(ctx, s.db, event)
}

// FinalizeJob persists a job's terminal status together with its terminal
// domain event in a single transaction. A caller connection pool of size 1
// (see Open) already serializes every query onto one connection, but wrapping
// both writes in a transaction additionally guarantees no reader ever
// observes the status as terminal without the corresponding event already
// committed: previously the two writes landed as separate statements, so a
// GetJob/ListEventsAfter pair issued between them could see a terminal status
// with the terminal event still missing.
func (s *Store) FinalizeJob(ctx context.Context, job domain.Job, event domain.Event) (domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := updateJob(ctx, tx, job); err != nil {
		return domain.Event{}, err
	}
	stored, err := addEvent(ctx, tx, event)
	if err != nil {
		return domain.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Event{}, err
	}
	return stored, nil
}

// LatestEventID returns the id of the most recent event recorded for jobID, or
// 0 if none exist yet. Callers use this as the last_event_id to hand a client
// alongside a job/items snapshot, so a subsequent events/ws connection skips
// replaying history already reflected in that snapshot.
func (s *Store) LatestEventID(ctx context.Context, jobID string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM job_events WHERE job_id=?`, jobID).Scan(&id)
	return id, err
}

func (s *Store) ListEventsAfter(ctx context.Context, jobID string, afterID int64) ([]domain.Event, error) {
	return s.queryEvents(ctx, `SELECT id,job_id,item_id,type,phase,message,payload,created_at FROM job_events WHERE job_id=? AND id>? ORDER BY id ASC`, jobID, afterID)
}

// ListHookEvents returns only jobID's hook lifecycle events, so callers
// summarizing hook status (GET /downloads/{id}) don't have to scan the far
// more numerous progress events sharing the table.
func (s *Store) ListHookEvents(ctx context.Context, jobID string) ([]domain.Event, error) {
	return s.queryEvents(ctx, `SELECT id,job_id,item_id,type,phase,message,payload,created_at FROM job_events WHERE job_id=? AND type IN ('hook_started','hook_succeeded','hook_failed') ORDER BY id ASC`, jobID)
}

func (s *Store) queryEvents(ctx context.Context, query string, args ...any) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Event, 0)
	for rows.Next() {
		var ev domain.Event
		var created string
		if err := rows.Scan(&ev.ID, &ev.JobID, &ev.ItemID, &ev.Type, &ev.Phase, &ev.Message, &ev.Payload, &created); err != nil {
			return nil, err
		}
		ev.CreatedAt = parseTime(created)
		out = append(out, ev)
	}
	return out, rows.Err()
}
