package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"amdl/internal/domain"
)

// schema1x is the schema the last 1.x release (a6a6b46) created, copied
// verbatim from its initSchema. It is frozen on purpose: it stands for the
// databases deployed installs carry, not for anything this package writes.
const schema1x = `
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY,
	input TEXT NOT NULL,
	type TEXT NOT NULL,
	storefront TEXT,
	title TEXT NOT NULL DEFAULT '',
	artwork_url TEXT NOT NULL DEFAULT '',
	artist_name TEXT NOT NULL DEFAULT '',
	artist_url TEXT NOT NULL DEFAULT '',
	curator_name TEXT NOT NULL DEFAULT '',
	release_date TEXT NOT NULL DEFAULT '',
	genre TEXT NOT NULL DEFAULT '',
	artwork_bg_color TEXT NOT NULL DEFAULT '',
	artwork_text_color1 TEXT NOT NULL DEFAULT '',
	artwork_text_color2 TEXT NOT NULL DEFAULT '',
	artwork_text_color3 TEXT NOT NULL DEFAULT '',
	artwork_text_color4 TEXT NOT NULL DEFAULT '',
	motion_artwork_url TEXT NOT NULL DEFAULT '',
	motion_artwork_tall_url TEXT NOT NULL DEFAULT '',
	motion_artwork_bg_color TEXT NOT NULL DEFAULT '',
	motion_artwork_text_color1 TEXT NOT NULL DEFAULT '',
	motion_artwork_text_color2 TEXT NOT NULL DEFAULT '',
	motion_artwork_text_color3 TEXT NOT NULL DEFAULT '',
	motion_artwork_text_color4 TEXT NOT NULL DEFAULT '',
	motion_artwork_tall_bg_color TEXT NOT NULL DEFAULT '',
	motion_artwork_tall_text_color1 TEXT NOT NULL DEFAULT '',
	motion_artwork_tall_text_color2 TEXT NOT NULL DEFAULT '',
	motion_artwork_tall_text_color3 TEXT NOT NULL DEFAULT '',
	motion_artwork_tall_text_color4 TEXT NOT NULL DEFAULT '',
	canonical_key TEXT NOT NULL,
	force INTEGER NOT NULL DEFAULT 0,
	overrides TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	total_items INTEGER NOT NULL DEFAULT 0,
	done_items INTEGER NOT NULL DEFAULT 0,
	failed_items INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_active_key
	ON jobs(canonical_key)
	WHERE status IN ('queued','running');
CREATE TABLE IF NOT EXISTS job_items (
	id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL,
	adam_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	idx INTEGER NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	artist TEXT NOT NULL DEFAULT '',
	album TEXT NOT NULL DEFAULT '',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	artwork_url TEXT NOT NULL DEFAULT '',
	has_lyrics INTEGER NOT NULL DEFAULT 0,
	lyrics_status TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	download_progress REAL NOT NULL DEFAULT 0,
	decrypt_progress REAL NOT NULL DEFAULT 0,
	progress_resolved INTEGER NOT NULL DEFAULT 0,
	progress_remuxed INTEGER NOT NULL DEFAULT 0,
	progress_verified INTEGER NOT NULL DEFAULT 0,
	progress_tagged INTEGER NOT NULL DEFAULT 0,
	progress_saved INTEGER NOT NULL DEFAULT 0,
	codec TEXT NOT NULL DEFAULT '',
	bit_depth INTEGER NOT NULL DEFAULT 0,
	sample_rate INTEGER NOT NULL DEFAULT 0,
	bitrate INTEGER NOT NULL DEFAULT 0,
	file_size INTEGER NOT NULL DEFAULT 0,
	retry_kind TEXT NOT NULL DEFAULT '',
	attempt INTEGER NOT NULL DEFAULT 0,
	max_attempts INTEGER NOT NULL DEFAULT 0,
	status_message TEXT NOT NULL DEFAULT '',
	output_path TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	FOREIGN KEY(job_id) REFERENCES jobs(id)
);
CREATE INDEX IF NOT EXISTS idx_job_items_job_id ON job_items(job_id, idx);
CREATE TABLE IF NOT EXISTS job_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id TEXT NOT NULL,
	item_id TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL,
	phase TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_job_events_job_id_id ON job_events(job_id, id);
CREATE TABLE IF NOT EXISTS library_sync_anchor (
	position INTEGER PRIMARY KEY,
	song_id TEXT NOT NULL,
	song_name TEXT NOT NULL DEFAULT '',
	album_name TEXT NOT NULL DEFAULT ''
);
`

// rows1x fills a 1.x database with one of each kind of row an install
// carries. Some timestamps use the variable-width form 1.x releases wrote
// before normalization existed, chosen so that ordering them as raw text gives
// the wrong earliest/latest event (".5Z" sorts before "Z").
const rows1x = `
INSERT INTO jobs(id,input,type,storefront,title,artwork_url,artist_name,canonical_key,force,overrides,status,total_items,done_items,failed_items,error,created_at,updated_at) VALUES
	('j-done','https://music.apple.com/us/album/a/1','album','us','Album A','https://art/a.jpg','Artist A','album:us:1',1,'','completed',2,2,0,'','2024-07-10T12:00:00Z','2024-07-10T12:05:00.25Z'),
	('j-failed','https://music.apple.com/us/station/ra.1','station','us','Station','','','station:us:ra.1',0,'{"media_user_token":"tok","embed_cover":false}','failed',3,2,1,'1 of 3 tracks failed','2024-07-11T08:00:00.000000000Z','2024-07-11T08:10:00.000000000Z'),
	('j-running','https://music.apple.com/us/album/b/2','album','us','Album B','','','album:us:2',1,'{"force_overwrite":false,"embed_cover":true}','running',2,1,0,'','2024-07-12T09:00:00.000000000Z','2024-07-12T09:01:00.000000000Z'),
	('j-queued','https://music.apple.com/us/song/c/3','song','us','','','','song:us:3',0,'','queued',0,0,0,'','2024-07-12T10:00:00.000000000Z','2024-07-12T10:00:00.000000000Z'),
	('j-cancelled','https://music.apple.com/us/song/d/4','song','us','','','','song:us:4',0,'','cancelled',0,0,0,'cancelled','2024-07-12T11:00:00.000000000Z','2024-07-12T11:00:30.000000000Z'),
	('j-retried','https://music.apple.com/us/song/e/5','song','us','Song E','','','song:us:5',0,'','completed',1,1,0,'','2024-07-10T12:00:00.000000000Z','2024-07-10T12:00:02Z'),
	('j-refailed','https://music.apple.com/us/song/g/7','song','us','','','','song:us:7',0,'','failed',0,0,0,'failed again','2024-07-14T13:00:00.000000000Z','2024-07-14T13:00:05.5Z'),
	('j-corrupt','https://music.apple.com/us/song/f/6','song','us','','','','song:us:6',1,'{broken','completed',0,0,0,'','2024-07-13T00:00:00.000000000Z','2024-07-13T00:00:00.000000000Z');

INSERT INTO job_items(id,job_id,adam_id,kind,idx,title,artist,album,duration_ms,has_lyrics,lyrics_status,status,download_progress,decrypt_progress,progress_resolved,progress_remuxed,progress_verified,progress_tagged,progress_saved,codec,bit_depth,sample_rate,bitrate,file_size,output_path,error,created_at,updated_at) VALUES
	('i-done-1','j-done','11','song',1,'One','Artist A','Album A',180000,1,'fetched','completed',1,1,1,1,1,1,1,'alac',24,96000,0,40000000,'/music/Artist A/Album A/01 One.m4a','','2024-07-10T12:00:01Z','2024-07-10T12:02:00.5Z'),
	('i-done-2','j-done','12','song',2,'Two','Artist A','Album A',200000,0,'none','completed',1,1,1,1,0,1,1,'aac-lc',0,44100,256000,6000000,'/music/Artist A/Album A/02 Two.m4a','','2024-07-10T12:00:01.000000000Z','2024-07-10T12:04:00.000000000Z'),
	('i-failed-1','j-failed','21','song',1,'X','','',0,0,'','completed',1,1,1,1,1,1,1,'alac',16,44100,0,30000000,'/music/x.m4a','','2024-07-11T08:00:01.000000000Z','2024-07-11T08:05:00.000000000Z'),
	('i-failed-2','j-failed','22','song',2,'Y','','',0,0,'','failed',0.4,0,1,0,0,0,0,'alac',16,44100,0,0,'','decrypt failed','2024-07-11T08:00:01.000000000Z','2024-07-11T08:06:00.000000000Z'),
	('i-failed-3','j-failed','23','song',3,'Z','','',0,0,'','skipped_existing',0,0,1,0,0,0,0,'',0,0,0,1234,'/music/z.m4a','','2024-07-11T08:00:01.000000000Z','2024-07-11T08:07:00.000000000Z'),
	('i-running-1','j-running','31','song',1,'P','','',0,0,'','completed',1,1,1,1,1,1,1,'alac',24,48000,0,1000,'/music/p.m4a','','2024-07-12T09:00:01.000000000Z','2024-07-12T09:00:30.000000000Z'),
	('i-running-2','j-running','32','song',2,'Q','','',0,0,'','downloading',0.5,0,1,0,0,0,0,'alac',24,48000,0,0,'','','2024-07-12T09:00:01.000000000Z','2024-07-12T09:00:40.000000000Z'),
	('i-retried-1','j-retried','51','song',1,'E','','',0,0,'','completed',1,1,1,1,1,1,1,'alac',16,44100,0,2000,'/music/e.m4a','','2024-07-10T12:00:00.000000000Z','2024-07-10T12:00:03.000000000Z');

INSERT INTO job_events(id,job_id,item_id,type,phase,message,payload,created_at) VALUES
	(1,'j-done','','job_queued','','job queued','{"id":"j-done","message":"job queued"}','2024-07-10T12:00:00Z'),
	(2,'j-done','','job_started','','job started','{"id":"j-done","message":"job started"}','2024-07-10T12:00:00.5Z'),
	(3,'j-done','i-done-1','item_progress','downloading','','{"id":"i-done-1","status":"downloading"}','2024-07-10T12:01:00Z'),
	(4,'j-done','i-done-1','item_completed','','done','{"id":"i-done-1","status":"completed"}','2024-07-10T12:02:00.5Z'),
	(5,'j-done','','job_finished','','completed','{"id":"j-done","status":"completed"}','2024-07-10T12:05:00.25Z'),
	(6,'j-done','','hook_succeeded','notify','','{"hook":"notify","attempt":1}','2024-07-10T12:05:01Z'),
	(7,'j-failed','','job_started','','job started','','2024-07-11T08:00:00.000000000Z'),
	(8,'j-failed','i-failed-2','item_progress','decrypting','','{"id":"i-failed-2"}','2024-07-11T08:05:30.000000000Z'),
	(9,'j-failed','i-failed-2','item_failed','','decrypt failed','{"id":"i-failed-2","status":"failed"}','2024-07-11T08:06:00.000000000Z'),
	(10,'j-failed','','job_failed','','1 of 3 tracks failed','{"id":"j-failed","status":"failed"}','2024-07-11T08:10:00.000000000Z'),
	(11,'j-running','','job_started','','job started','','2024-07-12T09:00:00.000000000Z'),
	(12,'j-running','i-running-2','item_progress','downloading','','{"id":"i-running-2","progress":{"download":0.5}}','2024-07-12T09:00:40.000000000Z'),
	(13,'j-queued','','job_queued','','job queued','','2024-07-12T10:00:00.000000000Z'),
	(14,'j-cancelled','','job_queued','','job queued','','2024-07-12T11:00:00.000000000Z'),
	(15,'j-cancelled','','job_cancelled','','job cancelled','','2024-07-12T11:00:30.000000000Z'),
	(16,'j-retried','','job_started','','job started','','2024-07-10T12:00:01Z'),
	(17,'j-retried','','job_failed','','failed','','2024-07-10T12:00:01.1Z'),
	(18,'j-retried','','job_retried','','retry','','2024-07-10T12:00:01.2Z'),
	(19,'j-retried','','job_started','','job started','','2024-07-10T12:00:01.5Z'),
	(20,'j-retried','','job_finished','','completed','','2024-07-10T12:00:02Z'),
	(21,'j-corrupt','','job_finished','','completed','','2024-07-13T00:00:00.000000000Z'),
	(22,'j-gone','','job_deleted','','','','2024-07-13T01:00:00.000000000Z'),
	(23,'j-refailed','','job_started','','job started','','2024-07-14T13:00:00Z'),
	(24,'j-refailed','','job_failed','','failed','','2024-07-14T13:00:05Z'),
	(25,'j-refailed','','job_retried','','retry','','2024-07-14T13:00:05.1Z'),
	(26,'j-refailed','','job_started','','job started','','2024-07-14T13:00:05.2Z'),
	(27,'j-refailed','','job_failed','','failed again','','2024-07-14T13:00:05.5Z');

INSERT INTO library_sync_anchor(position,song_id,song_name,album_name) VALUES
	(0,'i.song1','Newest','Album N'),
	(1,'i.song2','Older','Album O');
`

func create1xDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "amdl.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, script := range []string{schema1x, rows1x} {
		if _, err := raw.Exec(script); err != nil {
			t.Fatalf("build 1.x database: %v", err)
		}
	}
	return path
}

type tableDump map[string]map[string]any // primary key -> column -> value

func dumpTable(t *testing.T, database *sql.DB, table, key string) tableDump {
	t.Helper()
	rows, err := database.Query(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := tableDump{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, col := range cols {
			row[col] = values[i]
		}
		out[fmt.Sprint(row[key])] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func dumpAll(t *testing.T, database *sql.DB) map[string]tableDump {
	t.Helper()
	return map[string]tableDump{
		"jobs":                dumpTable(t, database, "jobs", "id"),
		"job_items":           dumpTable(t, database, "job_items", "id"),
		"job_events":          dumpTable(t, database, "job_events", "id"),
		"library_sync_anchor": dumpTable(t, database, "library_sync_anchor", "position"),
	}
}

func isTimestampColumn(col string) bool {
	return strings.HasSuffix(col, "_at")
}

// assertRowsSurvived checks that every row of before is still in after with
// the same value in every column after still has. Timestamps may have been
// rewritten to the fixed-width form, so they compare as instants.
func assertRowsSurvived(t *testing.T, table string, before, after tableDump, skip func(key, col string) bool) {
	t.Helper()
	for key, oldRow := range before {
		newRow, ok := after[key]
		if !ok {
			t.Errorf("%s row %s was lost", table, key)
			continue
		}
		for col, oldValue := range oldRow {
			newValue, kept := newRow[col]
			if !kept || (skip != nil && skip(key, col)) {
				continue
			}
			if isTimestampColumn(col) {
				oldTime, newTime := parseTime(fmt.Sprint(oldValue)), parseTime(fmt.Sprint(newValue))
				if oldTime.IsZero() || !oldTime.Equal(newTime) {
					t.Errorf("%s %s.%s = %v, want the instant %v", table, key, col, newValue, oldValue)
				}
				continue
			}
			if !reflect.DeepEqual(oldValue, newValue) {
				t.Errorf("%s %s.%s = %#v, want %#v", table, key, col, newValue, oldValue)
			}
		}
	}
}

func columnsOf(t *testing.T, database *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := database.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

func indexExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func userVersion(t *testing.T, database *sql.DB) int {
	t.Helper()
	version, err := schemaVersion(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestOpenMigrates1xDatabasePreservingRows(t *testing.T) {
	path := create1xDatabase(t)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	before := dumpAll(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("open 1.x database: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if got := userVersion(t, store.db); got != 3 {
		t.Fatalf("user_version = %d, want 3", got)
	}
	after := dumpAll(t, store.db)

	// Columns: the three 1.x counters/flags are gone, timing is new.
	jobCols := columnsOf(t, store.db, "jobs")
	for _, gone := range []string{"force", "done_items", "failed_items"} {
		if jobCols[gone] {
			t.Errorf("jobs.%s still exists after migration", gone)
		}
	}
	for _, added := range []string{"started_at", "finished_at"} {
		if !jobCols[added] {
			t.Errorf("jobs.%s missing after migration", added)
		}
	}
	if !columnsOf(t, store.db, "job_items")["download_bytes"] {
		t.Error("job_items.download_bytes missing after migration")
	}
	for _, index := range []string{"idx_jobs_active_key", "idx_job_items_job_id", "idx_job_events_job_id_id", "idx_job_events_type_created"} {
		if !indexExists(t, store.db, index) {
			t.Errorf("index %s missing after migration", index)
		}
	}

	// Every row of user data survived with its values. overrides changes only
	// where force was folded into it.
	forceFolded := map[string]bool{"j-done": true, "j-running": true}
	assertRowsSurvived(t, "jobs", before["jobs"], after["jobs"], func(key, col string) bool {
		return col == "overrides" && forceFolded[key]
	})
	assertRowsSurvived(t, "job_items", before["job_items"], after["job_items"], nil)
	assertRowsSurvived(t, "library_sync_anchor", before["library_sync_anchor"], after["library_sync_anchor"], nil)
	if len(after["jobs"]) != len(before["jobs"]) || len(after["job_items"]) != len(before["job_items"]) || len(after["library_sync_anchor"]) != 2 {
		t.Fatalf("row counts changed: jobs %d->%d items %d->%d anchor %d",
			len(before["jobs"]), len(after["jobs"]), len(before["job_items"]), len(after["job_items"]), len(after["library_sync_anchor"]))
	}

	// Events: progress of terminal jobs is gone, everything else stays.
	prunedProgress := map[string]bool{"3": true, "8": true}
	for key := range prunedProgress {
		if _, ok := after["job_events"][key]; ok {
			t.Errorf("item_progress event %s of a terminal job survived", key)
		}
		delete(before["job_events"], key)
	}
	if _, ok := after["job_events"]["12"]; !ok {
		t.Error("item_progress event of the running job was deleted")
	}
	if len(after["job_events"]) != len(before["job_events"]) {
		t.Errorf("job_events has %d rows, want %d", len(after["job_events"]), len(before["job_events"]))
	}
	assertRowsSurvived(t, "job_events", before["job_events"], after["job_events"], nil)

	// started_at / finished_at come from the events, compared as instants so
	// a raw-text MIN/MAX over unnormalized timestamps would fail here.
	wantTiming := map[string]struct{ started, finished string }{
		"j-done":      {"2024-07-10T12:00:00.5Z", "2024-07-10T12:05:00.25Z"},
		"j-failed":    {"2024-07-11T08:00:00Z", "2024-07-11T08:10:00Z"},
		"j-running":   {"2024-07-12T09:00:00Z", ""},
		"j-queued":    {"", ""},
		"j-cancelled": {"", "2024-07-12T11:00:30Z"},
		// A retried job's started_at is its latest run's first job_started.
		"j-retried":  {"2024-07-10T12:00:01.5Z", "2024-07-10T12:00:02Z"},
		"j-refailed": {"2024-07-14T13:00:05.2Z", "2024-07-14T13:00:05.5Z"},
		"j-corrupt":  {"", "2024-07-13T00:00:00Z"},
	}
	for id, want := range wantTiming {
		var started, finished sql.NullString
		if err := store.db.QueryRow(`SELECT started_at, finished_at FROM jobs WHERE id=?`, id).Scan(&started, &finished); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			name string
			got  sql.NullString
			want string
		}{{"started_at", started, want.started}, {"finished_at", finished, want.finished}} {
			switch {
			case c.want == "" && c.got.Valid:
				t.Errorf("%s.%s = %q, want NULL", id, c.name, c.got.String)
			case c.want != "" && (!c.got.Valid || !parseTime(c.got.String).Equal(mustTime(t, c.want))):
				t.Errorf("%s.%s = %v, want %s", id, c.name, c.got, c.want)
			case c.want != "" && c.got.String != formatTime(mustTime(t, c.want)):
				t.Errorf("%s.%s = %q, want fixed-width %q", id, c.name, c.got.String, formatTime(mustTime(t, c.want)))
			}
		}
	}

	// The store reads the migrated rows: counters derived from items, legacy
	// force kept as an override, credentials of the failed job kept for retry.
	done, err := store.GetJob(ctx, "j-done")
	if err != nil {
		t.Fatal(err)
	}
	if done.CompletedItems != 2 || done.SkippedItems != 0 || done.FailedItems != 0 || done.TotalItems != 2 {
		t.Errorf("j-done counters = %+v", done)
	}
	if done.Overrides == nil || done.Overrides.ForceOverwrite == nil || !*done.Overrides.ForceOverwrite {
		t.Errorf("j-done overrides = %+v, want force_overwrite true from the legacy force flag", done.Overrides)
	}
	running, err := store.GetJob(ctx, "j-running")
	if err != nil {
		t.Fatal(err)
	}
	if running.Overrides == nil || running.Overrides.ForceOverwrite == nil || !*running.Overrides.ForceOverwrite {
		t.Errorf("j-running overrides = %+v, want force_overwrite true", running.Overrides)
	}
	if running.StartedAt == nil || running.FinishedAt != nil || running.CompletedItems != 1 {
		t.Errorf("j-running = started %v finished %v completed %d", running.StartedAt, running.FinishedAt, running.CompletedItems)
	}
	failed, err := store.GetJob(ctx, "j-failed")
	if err != nil {
		t.Fatal(err)
	}
	if failed.CompletedItems != 1 || failed.SkippedItems != 1 || failed.FailedItems != 1 || failed.CancelledItems != 0 {
		t.Errorf("j-failed counters = completed %d skipped %d failed %d cancelled %d, want 1/1/1/0",
			failed.CompletedItems, failed.SkippedItems, failed.FailedItems, failed.CancelledItems)
	}
	// Its overrides held only removed keys, so the job reads as having none.
	if failed.Overrides != nil {
		t.Errorf("j-failed overrides = %+v, want none", failed.Overrides)
	}
	if got := after["jobs"]["j-corrupt"]["overrides"]; got != "{broken" {
		t.Errorf("unparseable overrides = %#v, want it left as it was", got)
	}
	var payload map[string]any
	events, err := store.ListEventsAfterLimit(ctx, "j-done", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload["message"] != "job queued" {
		t.Errorf("migrated payload = %s (%v), want the 1.x JSON object", events[0].Payload, err)
	}

	// The active-key index still dedups after the table rewrite.
	dup := domain.Job{ID: "j-dup", Input: "x", Type: "album", CanonicalKey: "album:us:2", Status: domain.JobQueued}
	if err := store.CreateJob(ctx, dup); !errors.Is(err, ErrDuplicateActive) {
		t.Fatalf("duplicate of a running job's key: err = %v, want ErrDuplicateActive", err)
	}
}

func TestOpenIsNoOpOnMigratedDatabase(t *testing.T) {
	path := create1xDatabase(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := dumpAll(t, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	if got := userVersion(t, store.db); got != latestSchemaVersion() {
		t.Fatalf("user_version after reopen = %d, want %d", got, latestSchemaVersion())
	}
	if second := dumpAll(t, store.db); !reflect.DeepEqual(first, second) {
		t.Fatal("reopening a migrated database changed its rows")
	}
}

func TestMigration1LeavesCurrent1xDatabaseUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amdl.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// A database the last 1.x release wrote and normalized itself: fixed-width
	// timestamps and completed items with their stage breakdown.
	stamp := "2024-07-10T12:00:00.000000000Z"
	for _, stmt := range []string{
		schema1x,
		`INSERT INTO jobs(id,input,type,storefront,canonical_key,force,overrides,status,total_items,done_items,failed_items,created_at,updated_at)
			VALUES('j1','u','song','us','song:us:1',1,'{"embed_cover":false}','completed',1,1,0,'` + stamp + `','` + stamp + `')`,
		`INSERT INTO job_items(id,job_id,adam_id,kind,idx,status,download_progress,decrypt_progress,progress_resolved,progress_remuxed,progress_tagged,progress_saved,created_at,updated_at)
			VALUES('i1','j1','1','song',1,'completed',1,1,1,1,1,1,'` + stamp + `','` + stamp + `')`,
		`INSERT INTO job_items(id,job_id,adam_id,kind,idx,status,created_at,updated_at)
			VALUES('i2','j1','2','song',2,'skipped_existing','` + stamp + `','` + stamp + `')`,
		`INSERT INTO job_events(job_id,type,payload,created_at) VALUES('j1','job_finished','{"id":"j1"}','` + stamp + `')`,
		`INSERT INTO library_sync_anchor(position,song_id) VALUES(0,'i.1')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	raw.SetMaxOpenConns(1)
	before := dumpAll(t, raw)
	if err := migrate(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if got := userVersion(t, raw); got != 1 {
		t.Fatalf("user_version = %d, want 1", got)
	}
	if after := dumpAll(t, raw); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration 1 changed a current 1.x database:\nbefore %v\nafter  %v", before, after)
	}
}

func TestOpenCreatesLatestSchemaInFreshDatabase(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := userVersion(t, store.db); got != 3 || got != latestSchemaVersion() {
		t.Fatalf("fresh user_version = %d, want 3 (latest %d)", got, latestSchemaVersion())
	}
	jobCols := columnsOf(t, store.db, "jobs")
	if jobCols["force"] || jobCols["done_items"] || jobCols["failed_items"] || !jobCols["started_at"] || !jobCols["finished_at"] {
		t.Fatalf("fresh jobs columns = %v", jobCols)
	}
	for _, index := range []string{"idx_jobs_active_key", "idx_job_items_job_id", "idx_job_events_job_id_id", "idx_job_events_type_created"} {
		if !indexExists(t, store.db, index) {
			t.Errorf("fresh database lacks index %s", index)
		}
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amdl.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, latestSchemaVersion()+1)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err == nil {
		store.Close()
		t.Fatal("Open accepted a database written by a newer schema")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open error = %v, want it to say the schema is newer", err)
	}
}
