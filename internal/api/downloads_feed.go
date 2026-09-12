package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"amdl/internal/db"
	"amdl/internal/domain"

	"github.com/coder/websocket"
)

// downloadsFeed is the SSE overview feed: it pushes one DownloadFeedMessage
// per change to the GET /downloads list — download_upserted (the affected
// job's latest snapshot) as jobs are queued/started/progress/finish, and
// download_deleted as jobs are removed. Resume via the Last-Event-ID header;
// the client should first GET /downloads for the full list and use that
// response's last_event_id here.
func (s *Server) downloadsFeed(w http.ResponseWriter, r *http.Request) {
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	// Subscribe before the first drain so a milestone published between the
	// backlog read and registration still wakes a subsequent drain.
	ch, cancel := s.hub.SubscribeAll()
	defer cancel()

	// Flush the response head right away so the client's EventSource opens even
	// when there's no backlog to send (mirrors the single-job events handler);
	// otherwise the 200 would wait for the first change or the 10s keepalive,
	// and intermediary proxies could time the idle connection out first.
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}

	write := func(msg domain.DownloadFeedMessage) error {
		raw, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", msg.EventID, msg.Type, raw); err != nil {
			return err
		}
		return controller.Flush()
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := func() error {
		if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	}
	s.runDownloadsFeed(r.Context(), lastID, ch, ticker.C, write, keepalive)
}

// downloadsFeedWS is the WebSocket twin of downloadsFeed. Resume via the
// last_event_id query parameter (WebSocket has no Last-Event-ID header). Each
// DownloadFeedMessage is one JSON text message with event_id as the resume
// cursor.
func (s *Server) downloadsFeedWS(w http.ResponseWriter, r *http.Request) {
	lastID, _ := strconv.ParseInt(r.URL.Query().Get("last_event_id"), 10, 64)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	ch, cancel := s.hub.SubscribeAll()
	defer cancel()

	write := func(msg domain.DownloadFeedMessage) error {
		raw, _ := json.Marshal(msg)
		return conn.Write(ctx, websocket.MessageText, raw)
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := func() error {
		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		defer pingCancel()
		return conn.Ping(pingCtx)
	}
	s.runDownloadsFeed(ctx, lastID, ch, ticker.C, write, keepalive)
}

// runDownloadsFeed drives the overview feed loop shared by the SSE and WS
// endpoints. It drains milestone events newer than the cursor into one message
// per affected job (deduped to the latest action/snapshot) and calls keepalive
// on each tick. write returning an error (client gone) ends the loop.
func (s *Server) runDownloadsFeed(ctx context.Context, lastID int64, ch <-chan domain.Event, tick <-chan time.Time, write func(domain.DownloadFeedMessage) error, keepalive func() error) {
	drain := func() error {
		for {
			events, err := s.store.ListMilestoneEventsAfterLimit(ctx, lastID, streamEventPageSize)
			if err != nil {
				return nil // keep the connection; the next wake or tick retries
			}
			if len(events) == 0 {
				return nil
			}
			counts, err := s.store.CountJobsByStatus(ctx, db.JobListFilter{})
			if err != nil {
				return nil // do not advance the cursor until counts can accompany the message
			}
			total := 0
			for _, count := range counts {
				total += count
			}
			statusCounts := makeJobStatusCounts(counts, total)
			type pendingMessage struct {
				eventID int64
				deleted bool
			}
			// One upsert per job in this bounded page, carrying that job's own
			// highest milestone id rather than a page-wide cursor.
			latest := map[string]pendingMessage{}
			var order []string
			for _, ev := range events {
				if _, seen := latest[ev.JobID]; !seen {
					order = append(order, ev.JobID)
				}
				if ev.ID > latest[ev.JobID].eventID {
					latest[ev.JobID] = pendingMessage{eventID: ev.ID, deleted: ev.Type == domain.EventDeleted}
				}
			}
			// Send in ascending per-job cursor order so Last-Event-ID only moves
			// forward and a mid-page disconnect never skips a later message.
			sort.Slice(order, func(i, j int) bool { return latest[order[i]].eventID < latest[order[j]].eventID })
			for _, jobID := range order {
				pending := latest[jobID]
				if pending.deleted {
					if err := write(domain.DownloadFeedMessage{Type: "download_deleted", JobID: jobID, EventID: pending.eventID, StatusCounts: statusCounts}); err != nil {
						return err
					}
					lastID = pending.eventID
					continue
				}
				snap, err := s.jobSnapshot(ctx, jobID)
				if errors.Is(err, sql.ErrNoRows) {
					// Advance past a stale upsert so a deletion in a later page can
					// be reached instead of replaying this row forever.
					lastID = pending.eventID
					continue
				}
				if err != nil {
					// A transient store error must not advance the cursor past a
					// milestone that may never repeat (e.g. job_finished). Stop
					// this round; the next wake or tick retries the same page.
					return nil
				}
				if err := write(domain.DownloadFeedMessage{Type: "download_upserted", Job: snap, EventID: pending.eventID, StatusCounts: statusCounts}); err != nil {
					return err
				}
				lastID = pending.eventID
			}
			if len(events) < streamEventPageSize {
				return nil
			}
		}
	}

	if err := drain(); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if err := drain(); err != nil {
				return
			}
		case <-tick:
			if err := drain(); err != nil {
				return
			}
			if keepalive != nil {
				if err := keepalive(); err != nil {
					return
				}
			}
		}
	}
}
