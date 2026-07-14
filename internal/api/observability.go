package api

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"amdl/internal/logging"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (s *Server) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if !requestIDPattern.MatchString(requestID) {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		logger := s.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger = logger.With("component", "api", "request_id", requestID)
		r = r.WithContext(logging.NewContext(r.Context(), logger))
		tracked := &trackingResponseWriter{ResponseWriter: w}

		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				logger.Error("http request panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				if !tracked.wroteHeader {
					writeError(tracked, http.StatusInternalServerError, errors.New("internal server error"))
				} else {
					panic(recovered)
				}
			}
			if s.currentConfig().Logging.AccessLog {
				status := tracked.status
				if status == 0 {
					status = http.StatusOK
				}
				level := slog.LevelInfo
				if status >= 500 {
					level = slog.LevelError
				} else if status >= 400 {
					level = slog.LevelWarn
				}
				logger.Log(r.Context(), level, "http request",
					"method", r.Method, "path", r.URL.Path, "route", r.Pattern,
					"status", status, "bytes", tracked.bytes,
					"duration_ms", time.Since(started).Milliseconds(), "remote_ip", remoteIP(r.RemoteAddr),
				)
			}
		}()

		next.ServeHTTP(tracked, r)
	})
}

func newRequestID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func remoteIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return address
}

type trackingResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *trackingResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *trackingResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *trackingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *trackingResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *trackingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *trackingResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (w *trackingResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(reader)
		w.bytes += n
		return n, err
	}
	n, err := io.Copy(struct{ io.Writer }{w.ResponseWriter}, reader)
	w.bytes += n
	return n, err
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	filter, err := parseLogFilter(r, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.logStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"logs": []logging.Entry{}, "next_cursor": 0, "oldest_sequence": 0, "truncated": false})
		return
	}
	page := s.logStore.List(filter)
	writeJSON(w, http.StatusOK, map[string]any{
		"logs": page.Entries, "next_cursor": page.NextCursor,
		"oldest_sequence": page.OldestSequence, "truncated": page.Truncated,
	})
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	filter, err := parseLogFilter(r, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.logStore == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("log buffer is not configured"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	backlog, live, unsubscribe := s.logStore.Subscribe(filter)
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	for _, entry := range backlog {
		if err := writeLogEvent(w, entry); err != nil {
			return
		}
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case entry, ok := <-live:
			if !ok || writeLogEvent(w, entry) != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeLogEvent(w io.Writer, entry logging.Entry) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", entry.Sequence, raw)
	return err
}

func parseLogFilter(r *http.Request, stream bool) (logging.Filter, error) {
	q := r.URL.Query()
	filter := logging.Filter{
		Component: strings.TrimSpace(q.Get("component")),
		RequestID: strings.TrimSpace(q.Get("request_id")),
		JobID:     strings.TrimSpace(q.Get("job_id")),
		Query:     strings.TrimSpace(q.Get("q")),
		Limit:     200,
	}
	if stream {
		filter.Limit = 500
	}
	for _, level := range strings.Split(q.Get("level"), ",") {
		if level = strings.TrimSpace(strings.ToLower(level)); level != "" {
			switch level {
			case "debug", "info", "warn", "error":
				filter.Levels = append(filter.Levels, level)
			default:
				return filter, fmt.Errorf("level %q is not supported", level)
			}
		}
	}
	if raw := strings.TrimSpace(q.Get("after")); raw != "" {
		after, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return filter, errors.New("after must be a non-negative integer")
		}
		filter.After = after
	} else if stream {
		if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
			after, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return filter, errors.New("Last-Event-ID must be a non-negative integer")
			}
			filter.After = after
		}
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			return filter, errors.New("limit must be an integer between 1 and 1000")
		}
		filter.Limit = limit
	}
	var err error
	if filter.Since, err = parseLogTime(q.Get("since")); err != nil {
		return filter, fmt.Errorf("since: %w", err)
	}
	if filter.Until, err = parseLogTime(q.Get("until")); err != nil {
		return filter, fmt.Errorf("until: %w", err)
	}
	if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Since.After(filter.Until) {
		return filter, errors.New("since must be before or equal to until")
	}
	return filter, nil
}

func parseLogTime(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, errors.New("must be an RFC3339 timestamp")
	}
	return parsed, nil
}
