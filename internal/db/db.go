package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

// JobListFilter controls GET /downloads listing: pagination, status/type/
// storefront filters, title/input substring search, and created/updated
// time windows. Zero values mean "no constraint" except Limit, which the
// store clamps to [1, 200] with a default of 50.
type JobListFilter struct {
	Limit          int
	Offset         int
	Statuses       []domain.JobStatus
	Types          []string
	Storefront     string
	Query          string
	CreatedAfter   *time.Time
	CreatedBefore  *time.Time
	UpdatedAfter   *time.Time
	UpdatedBefore  *time.Time
	Sort           string // created_at (default) or updated_at
	Order          string // desc (default) or asc
}

const (
	JobListSortCreatedAt = "created_at"
	JobListSortUpdatedAt = "updated_at"
	JobListOrderAsc      = "asc"
	JobListOrderDesc     = "desc"
)

// Normalize clamps pagination and fills default sort/order. ListJobs calls
// this; API handlers may also call it so response echo fields match the
// values actually applied.
func (f *JobListFilter) Normalize() {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	switch f.Sort {
	case JobListSortUpdatedAt:
	default:
		f.Sort = JobListSortCreatedAt
	}
	switch f.Order {
	case JobListOrderAsc:
	default:
		f.Order = JobListOrderDesc
	}
}

func (f JobListFilter) whereClause() (string, []any) {
	var (
		conds []string
		args  []any
	)
	if len(f.Statuses) > 0 {
		placeholders := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		conds = append(conds, "j.status IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(f.Types) > 0 {
		placeholders := make([]string, len(f.Types))
		for i, t := range f.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		conds = append(conds, "j.type IN ("+strings.Join(placeholders, ",")+")")
	}
	if sf := strings.TrimSpace(f.Storefront); sf != "" {
		conds = append(conds, "j.storefront=?")
		args = append(args, sf)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		conds = append(conds, `(j.title LIKE ? ESCAPE '\' OR j.input LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	if f.CreatedAfter != nil {
		conds = append(conds, "j.created_at>=?")
		args = append(args, formatTime(*f.CreatedAfter))
	}
	if f.CreatedBefore != nil {
		conds = append(conds, "j.created_at<=?")
		args = append(args, formatTime(*f.CreatedBefore))
	}
	if f.UpdatedAfter != nil {
		conds = append(conds, "j.updated_at>=?")
		args = append(args, formatTime(*f.UpdatedAfter))
	}
	if f.UpdatedBefore != nil {
		conds = append(conds, "j.updated_at<=?")
		args = append(args, formatTime(*f.UpdatedBefore))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

func (s *Store) ListJobs(ctx context.Context, filter JobListFilter) ([]domain.Job, int, error) {
	filter.Normalize()
	where, args := filter.whereClause()

	var total int
	countQuery := `SELECT COUNT(*) FROM jobs j` + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// done_items/failed_items are derived live from job_items instead of the
	// stored jobs columns: the stored counters are only written back when a job
	// finalizes, so running jobs (or jobs interrupted before finalize) would
	// report stale counts. Status buckets must mirror domain.CountItemProgress.
	orderCol := "j.created_at"
	if filter.Sort == JobListSortUpdatedAt {
		orderCol = "j.updated_at"
	}
	orderDir := "DESC"
	if filter.Order == JobListOrderAsc {
		orderDir = "ASC"
	}
	listArgs := append([]any{string(domain.ItemCompleted), string(domain.ItemSkipped), string(domain.ItemFailed)}, args...)
	listArgs = append(listArgs, filter.Limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.input,j.type,j.storefront,j.title,j.artwork_url,j.canonical_key,j.force,j.status,j.total_items,
			(SELECT COUNT(*) FROM job_items i WHERE i.job_id=j.id AND i.status IN (?,?)) AS done_items,
			(SELECT COUNT(*) FROM job_items i WHERE i.job_id=j.id AND i.status=?) AS failed_items,
			j.error,j.created_at,j.updated_at FROM jobs j`+where+` ORDER BY `+orderCol+` `+orderDir+`, j.id `+orderDir+` LIMIT ? OFFSET ?`,
		listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]domain.Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, job)
	}
	return out, total, rows.Err()
}

// DeleteJob removes a terminal (completed/failed/cancelled) job together with
// its items and old per-job events, then persists a job_deleted tombstone event
// so overview feeds can replay the deletion from a snapshot cursor. Queued or
// running jobs are refused with ErrJobNotTerminal so an in-flight worker never
// loses the rows it is still updating; cancel the job first. The status check,
// deletes, and tombstone insert run in one transaction.
//
// The row status alone cannot tell whether the manager's finalize sequence
// (terminal event insert + hook dispatch) has finished — callers must go
// through jobs.Manager.Delete, which additionally refuses while a finalize is
// still in flight.
func (s *Store) DeleteJob(ctx context.Context, id string) (domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Event{}, ErrJobNotFound
		}
		return domain.Event{}, err
	}
	if !domain.JobStatus(status).IsTerminal() {
		return domain.Event{}, ErrJobNotTerminal
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE job_id=?`, id); err != nil {
		return domain.Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_items WHERE job_id=?`, id); err != nil {
		return domain.Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, id); err != nil {
		return domain.Event{}, err
	}
	tombstone, err := addEvent(ctx, tx, domain.Event{JobID: id, Type: domain.EventDeleted})
	if err != nil {
		return domain.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Event{}, err
	}
	return tombstone, nil
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

// LatestGlobalEventID returns the id of the most recent event across all jobs
// (0 if none). The overview feed hands this to a client alongside the
// GET /downloads snapshot as the cursor to resume from, mirroring how
// LatestEventID pairs with a single job's snapshot.
func (s *Store) LatestGlobalEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM job_events`).Scan(&id)
	return id, err
}

// milestoneTypePlaceholders is the "?,?,…" fragment and matching args for
// overview-milestone types, built once from the single source of truth so the
// SQL filter can never drift from IsOverviewMilestone.
var milestoneTypePlaceholders, milestoneTypeArgs = func() (string, []any) {
	types := append([]string{}, domain.PersistedOverviewMilestones...)
	types = append(types, domain.EventDeleted)
	marks := make([]string, len(types))
	args := make([]any, len(types))
	for i, t := range types {
		marks[i] = "?"
		args[i] = t
	}
	return strings.Join(marks, ","), args
}()

// ListMilestoneEventsAfter returns, across all jobs, the events newer than
// afterID whose type changes the GET /downloads list-level view. The overview
// feed uses this to learn which jobs changed since a cursor.
func (s *Store) ListMilestoneEventsAfter(ctx context.Context, afterID int64) ([]domain.Event, error) {
	args := append([]any{afterID}, milestoneTypeArgs...)
	return s.queryEvents(ctx, `SELECT id,job_id,item_id,type,phase,message,payload,created_at FROM job_events
		WHERE id>? AND type IN (`+milestoneTypePlaceholders+`)
		ORDER BY id ASC`, args...)
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
