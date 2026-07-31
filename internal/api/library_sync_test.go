package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"amdl/internal/librarysync"
)

type fakeLibrarySync struct {
	status     librarysync.Status
	cleared    int64
	resetCalls int
}

func (f *fakeLibrarySync) Status() librarysync.Status { return f.status }

func (f *fakeLibrarySync) ResetAnchor(context.Context) (int64, error) {
	f.resetCalls++
	f.status.AnchorSize = 0
	return f.cleared, nil
}

// Both endpoints must answer rather than panic when the watcher was never
// wired — a Server built without it is the shape every existing test uses.
func TestLibrarySyncEndpointsWithoutWatcher(t *testing.T) {
	server := &Server{}
	routes := server.Routes()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/library-sync"},
		{http.MethodPost, "/api/v1/library-sync/reset"},
	} {
		recorder := requestJSON(t, routes, tc.method, tc.path, "")
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503", tc.method, tc.path, recorder.Code)
		}
	}
}

func TestLibrarySyncStatusReportsWatcherState(t *testing.T) {
	watcher := &fakeLibrarySync{status: librarysync.Status{
		Enabled: true, IntervalMinutes: 15, AnchorSize: 50, TotalSubmitted: 3,
	}}
	server := &Server{}
	server.SetLibrarySync(watcher)
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/library-sync", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var got librarysync.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || got.IntervalMinutes != 15 || got.AnchorSize != 50 || got.TotalSubmitted != 3 {
		t.Fatalf("status = %+v, want the watcher's own values", got)
	}
}

// Reset reports how much it forgot and hands back the post-reset status, so a
// client does not need a second request to refresh its view.
func TestLibrarySyncResetClearsAnchor(t *testing.T) {
	watcher := &fakeLibrarySync{
		status:  librarysync.Status{Enabled: true, IntervalMinutes: 15, AnchorSize: 50},
		cleared: 50,
	}
	server := &Server{}
	server.SetLibrarySync(watcher)
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/library-sync/reset", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		Cleared int64              `json:"cleared"`
		Status  librarysync.Status `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Cleared != 50 {
		t.Fatalf("cleared = %d, want 50", got.Cleared)
	}
	if got.Status.AnchorSize != 0 {
		t.Fatalf("post-reset anchor_size = %d, want 0", got.Status.AnchorSize)
	}
	if watcher.resetCalls != 1 {
		t.Fatalf("reset called %d times, want 1", watcher.resetCalls)
	}
}
