package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"amdl/internal/config"
	"amdl/internal/logging"
)

func newObservedServer(t *testing.T) (*Server, *logging.System) {
	t.Helper()
	cfg := config.Default()
	cfg.Logging.Console = false
	system, err := logging.New(cfg.Logging)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Close() })
	server := NewServer(config.NewStore(cfg), nil, nil, nil, nil, nil, nil, system.Logger, system)
	return server, system
}

func TestHTTPObservationAndLogQuery(t *testing.T) {
	server, system := newObservedServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("X-Request-ID", "client-request-1")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Request-ID") != "client-request-1" {
		t.Fatalf("health response = %d, request id %q", rec.Code, rec.Header().Get("X-Request-ID"))
	}

	page := system.Store.List(logging.Filter{RequestID: "client-request-1", Limit: 10})
	if len(page.Entries) != 1 || page.Entries[0].Message != "http request" {
		t.Fatalf("access logs = %#v", page.Entries)
	}
	attrs := page.Entries[0].Attributes
	if attrs["route"] != "GET /api/v1/health" || attrs["status"] != int64(http.StatusOK) {
		t.Fatalf("access attrs = %#v", attrs)
	}

	query := httptest.NewRequest(http.MethodGet, "/api/v1/logs?request_id=client-request-1&level=info&limit=10", nil)
	queryRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(queryRec, query)
	if queryRec.Code != http.StatusOK {
		t.Fatalf("query status = %d: %s", queryRec.Code, queryRec.Body.String())
	}
	var response struct {
		Logs       []logging.Entry `json:"logs"`
		NextCursor uint64          `json:"next_cursor"`
	}
	if err := json.Unmarshal(queryRec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Logs) != 1 || response.NextCursor < 1 {
		t.Fatalf("query response = %+v", response)
	}
}

func TestHTTPObservationRecoversPanic(t *testing.T) {
	server, system := newObservedServer(t)
	handler := server.observeHTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("panic response = %d %s", rec.Code, rec.Body.String())
	}
	page := system.Store.List(logging.Filter{Levels: []string{"error"}, Limit: 10})
	if len(page.Entries) != 2 || page.Entries[0].Message != "http request panic" || page.Entries[1].Message != "http request" {
		t.Fatalf("panic logs = %#v", page.Entries)
	}
}

func TestLogStreamReplaysAndPublishes(t *testing.T) {
	server, system := newObservedServer(t)
	system.Logger.Info("backlog", "component", "test")
	httpServer := httptest.NewServer(server.Routes())
	defer httpServer.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(httpServer.URL + "/api/v1/logs/stream?component=test&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream response = %d %q: %s", resp.StatusCode, resp.Header.Get("Content-Type"), raw)
	}
	system.Logger.Warn("live", "component", "test")

	scanner := bufio.NewScanner(resp.Body)
	var dataLines []string
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(scanner.Text(), "data: "))
			if len(dataLines) == 2 {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(dataLines) != 2 || !strings.Contains(dataLines[0], `"message":"backlog"`) || !strings.Contains(dataLines[1], `"message":"live"`) {
		t.Fatalf("stream data = %#v", dataLines)
	}
}
