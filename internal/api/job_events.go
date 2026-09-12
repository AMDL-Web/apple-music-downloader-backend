package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"amdl/internal/domain"

	"github.com/coder/websocket"
)

// streamEventPageSize bounds each database allocation while old SSE/WS
// cursors are replayed. Handlers advance the persisted event id page by page.
const streamEventPageSize = 512

// eventsExhausted reloads jobID and reports whether its event stream will
// never deliver another event. Reloading is essential for connections opened
// while the job was active: retaining their initial Job snapshot would keep
// them open forever after the job transitions to a terminal state. A terminal
// job.Status alone is not enough: post-download
// hook dispatch is fire-and-forget and can keep recording hook_started/
// hook_succeeded/hook_failed events well after the job itself reached a
// terminal status (see jobs.Manager.HooksPending). And HooksPending alone
// still has a gap: the terminal status becomes visible (Store.FinalizeJob)
// before hook dispatch increments the pending count, so FinalizeInFlight
// must be consulted too — and strictly before HooksPending. Dispatch
// increments pending before the manager drops its finalize marks, so
// observing "not finalizing" guarantees pending already reflects any hooks
// that will fire; the reverse order could read pending before the increment
// and the finalize mark after its removal, wrongly concluding exhausted
// while hook events are still coming.
func (s *Server) eventsExhausted(ctx context.Context, jobID string) bool {
	job, err := s.store.GetJob(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		// A successful DELETE publishes a final job_deleted tombstone after
		// removing the row. Once the backlog drain has consumed that event there
		// can be no future per-job events, so an already-open stream must close.
		return true
	}
	if err != nil || !job.Status.IsTerminal() {
		return false
	}
	return !s.manager.FinalizeInFlight(jobID) && !s.manager.HooksPending(jobID)
}

// eventsWS is the WebSocket twin of events: it streams the same persisted job
// events — one domain.Event encoded as a JSON text message, identical to the
// SSE data payload — over a WebSocket connection. Resume works via the
// ?last_event_id= query parameter (WebSocket has no Last-Event-ID header
// convention); clients pass the id of the last event they saw. Delivery uses
// the same store-drain pattern as events: the hub only wakes the drain sooner,
// and the ticker bounds tail latency for events dropped by a full hub buffer.
// The ticker also pings the peer so half-open mobile connections are detected
// and torn down instead of holding the goroutine forever.
func (s *Server) eventsWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	lastID, _ := strconv.ParseInt(r.URL.Query().Get("last_event_id"), 10, 64)

	// Subscribe before inspecting state or reading the backlog so a concurrent
	// retry cannot change a failed job back to queued in the gap and leave us
	// waiting on a nil channel. The store remains the source of truth; the hub
	// only wakes a subsequent drain sooner.
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()
	exhausted := s.eventsExhausted(r.Context(), id)

	pending, err := s.store.ListEventsAfterLimit(r.Context(), id, lastID, streamEventPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 && exhausted {
		// Nothing left to replay and the job will never emit another event:
		// refuse the subscription instead of holding a socket open forever.
		terminalJob, _ := s.store.GetJob(r.Context(), id)
		writeError(w, http.StatusConflict, fmt.Errorf("job %s is already %s: no further events will be emitted", id, terminalJob.Status))
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return // Accept already wrote the handshake failure response
	}
	defer conn.CloseNow()

	// CloseRead answers control frames (ping/pong/close) in the background and
	// cancels the returned context when the client goes away. The protocol is
	// server-push only, so a client data frame also terminates the connection.
	ctx := conn.CloseRead(r.Context())

	write := func(ev domain.Event) error {
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, raw)
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := func() error {
		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		defer pingCancel()
		return conn.Ping(pingCtx)
	}
	s.jobReplay(id).run(ctx, lastID, pending, ch, ticker.C, eventDelivery{write: write, keepalive: keepalive})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)

	// Subscribe before inspecting state or reading the backlog so a concurrent
	// retry cannot change a failed job back to queued in the gap and leave us
	// waiting on a nil channel. The store remains the source of truth; the hub
	// only wakes a subsequent drain sooner.
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()
	exhausted := s.eventsExhausted(r.Context(), id)

	pending, err := s.store.ListEventsAfterLimit(r.Context(), id, lastID, streamEventPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 && exhausted {
		// Nothing left to replay and the job will never emit another event:
		// refuse the subscription instead of holding a connection open forever.
		terminalJob, _ := s.store.GetJob(r.Context(), id)
		writeError(w, http.StatusConflict, fmt.Errorf("job %s is already %s: no further events will be emitted", id, terminalJob.Status))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	// Flush immediately even when the initial backlog is empty.
	controller := http.NewResponseController(w)
	if err := controller.Flush(); err != nil {
		return
	}
	write := func(ev domain.Event) error { return writeSSE(w, ev) }
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := func() error {
		if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	}
	s.jobReplay(id).run(r.Context(), lastID, pending, ch, ticker.C, eventDelivery{write: write, flush: controller.Flush, keepalive: keepalive})
}

func writeSSE(w http.ResponseWriter, ev domain.Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, raw)
	return err
}

func (s *Server) jobReplay(id string) jobEventReplay {
	return jobEventReplay{
		read: func(ctx context.Context, afterID int64) ([]domain.Event, error) {
			return s.store.ListEventsAfterLimit(ctx, id, afterID, streamEventPageSize)
		},
		exhausted: func(ctx context.Context) bool { return s.eventsExhausted(ctx, id) },
	}
}
