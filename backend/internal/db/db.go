package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"amdl/backend/internal/domain"
	_ "modernc.org/sqlite"
)

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
	if err := s.migrate(context.Background()); err != nil {
		_ = database.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			input TEXT NOT NULL,
			type TEXT NOT NULL,
			storefront TEXT,
			force INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			total_items INTEGER NOT NULL DEFAULT 0,
			done_items INTEGER NOT NULL DEFAULT 0,
			failed_items INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS job_items (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			adam_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			idx INTEGER NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			artist TEXT NOT NULL DEFAULT '',
			album TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			progress REAL NOT NULL DEFAULT 0,
			codec TEXT NOT NULL DEFAULT '',
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
	for _, stmt := range []string{
		`ALTER TABLE jobs ADD COLUMN force INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE job_items ADD COLUMN retry_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE job_items ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE job_items ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE job_items ADD COLUMN status_message TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	return nil
}

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

func (s *Store) CreateJob(ctx context.Context, job domain.Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,input,type,storefront,force,status,total_items,done_items,failed_items,error,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Input, job.Type, job.Storefront, job.Force, string(job.Status), job.TotalItems,
		job.DoneItems, job.FailedItems, job.Error, formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	return err
}

func (s *Store) UpdateJob(ctx context.Context, job domain.Job) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET type=?, storefront=?, force=?, status=?, total_items=?, done_items=?, failed_items=?, error=?, updated_at=? WHERE id=?`,
		job.Type, job.Storefront, job.Force, string(job.Status), job.TotalItems, job.DoneItems, job.FailedItems, job.Error, formatTime(job.UpdatedAt), job.ID)
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (domain.Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,input,type,storefront,force,status,total_items,done_items,failed_items,error,created_at,updated_at FROM jobs WHERE id=?`, id)
	return scanJob(row)
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input,type,storefront,force,status,total_items,done_items,failed_items,error,created_at,updated_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
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
	err := row.Scan(&job.ID, &job.Input, &job.Type, &job.Storefront, &job.Force, &status, &job.TotalItems, &job.DoneItems, &job.FailedItems, &job.Error, &created, &updated)
	job.Status = domain.JobStatus(status)
	job.CreatedAt = parseTime(created)
	job.UpdatedAt = parseTime(updated)
	return job, err
}

func (s *Store) CreateItem(ctx context.Context, item domain.JobItem) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_items(id,job_id,adam_id,kind,idx,title,artist,album,status,progress,codec,retry_kind,attempt,max_attempts,status_message,output_path,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.JobID, item.AdamID, item.Kind, item.Index, item.Title, item.Artist, item.Album,
		string(item.Status), item.Progress, item.Codec, item.RetryKind, item.Attempt, item.MaxAttempts, item.StatusMessage, item.OutputPath, item.Error, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	return err
}

func (s *Store) UpdateItem(ctx context.Context, item domain.JobItem) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job_items SET title=?,artist=?,album=?,status=?,progress=?,codec=?,retry_kind=?,attempt=?,max_attempts=?,status_message=?,output_path=?,error=?,updated_at=? WHERE id=?`,
		item.Title, item.Artist, item.Album, string(item.Status), item.Progress, item.Codec, item.RetryKind, item.Attempt, item.MaxAttempts, item.StatusMessage, item.OutputPath, item.Error, formatTime(item.UpdatedAt), item.ID)
	return err
}

func (s *Store) ListItems(ctx context.Context, jobID string) ([]domain.JobItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,adam_id,kind,idx,title,artist,album,status,progress,codec,retry_kind,attempt,max_attempts,status_message,output_path,error,created_at,updated_at FROM job_items WHERE job_id=? ORDER BY idx`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.JobItem
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
	err := row.Scan(&item.ID, &item.JobID, &item.AdamID, &item.Kind, &item.Index, &item.Title, &item.Artist, &item.Album, &status,
		&item.Progress, &item.Codec, &item.RetryKind, &item.Attempt, &item.MaxAttempts, &item.StatusMessage, &item.OutputPath, &item.Error, &created, &updated)
	item.Status = domain.ItemStatus(status)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, err
}

func (s *Store) AddEvent(ctx context.Context, event domain.Event) (domain.Event, error) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO job_events(job_id,item_id,type,phase,message,payload,created_at) VALUES(?,?,?,?,?,?,?)`,
		event.JobID, event.ItemID, event.Type, event.Phase, event.Message, event.Payload, formatTime(event.CreatedAt))
	if err != nil {
		return event, err
	}
	event.ID, _ = res.LastInsertId()
	return event, nil
}

func (s *Store) ListEventsAfter(ctx context.Context, jobID string, afterID int64) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,item_id,type,phase,message,payload,created_at FROM job_events WHERE job_id=? AND id>? ORDER BY id ASC`, jobID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
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
