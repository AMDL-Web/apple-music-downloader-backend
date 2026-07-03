package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/media"
)

type acceptAllProcessor struct{}

func (acceptAllProcessor) ValidateRequest(_ context.Context, url string) (jobs.ValidationResult, error) {
	return jobs.ValidationResult{Type: "song", Storefront: "cn", ID: url}, nil
}

func (acceptAllProcessor) ProcessJob(context.Context, domain.Job, jobs.Reporter) error { return nil }

type multiUserFixture struct {
	server *Server
	store  *db.Store
	admin  domain.User
	user   domain.User
}

func newMultiUserFixture(t *testing.T) multiUserFixture {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	admin, err := store.CreateUser(ctx, domain.User{Username: "boss", Role: domain.RoleAdmin, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, domain.User{Username: "alice", Role: domain.RoleUser, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Auth = config.AuthConfig{Enabled: true, InternalSecret: testInternalSecret, BootstrapAdmin: "boss"}
	manager := jobs.NewManager(store, events.NewHub(), acceptAllProcessor{}, 1, slog.Default())
	server := &Server{cfg: cfg, store: store, manager: manager, wrapper: &fakeWrapperService{}}
	return multiUserFixture{server: server, store: store, admin: admin, user: user}
}

const testInternalSecret = "test-internal-secret"

func authedRequest(t *testing.T, handler http.Handler, method, path, body, xUser string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", testInternalSecret)
	if xUser != "" {
		req.Header.Set("X-User", xUser)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestPermissionMatrix(t *testing.T) {
	fx := newMultiUserFixture(t)
	routes := fx.server.Routes()

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		xUser  string
		status int
	}{
		{name: "health is public", method: http.MethodGet, path: "/api/v1/health", status: http.StatusOK},
		{name: "unknown user rejected", method: http.MethodGet, path: "/api/v1/downloads", xUser: "stranger", status: http.StatusForbidden},
		{name: "user wrapper status forbidden", method: http.MethodGet, path: "/api/v1/wrapper/status", xUser: "alice", status: http.StatusForbidden},
		{name: "user wrapper login forbidden", method: http.MethodPost, path: "/api/v1/wrapper/login", body: `{"username":"a","password":"b"}`, xUser: "alice", status: http.StatusForbidden},
		{name: "user wrapper logout forbidden", method: http.MethodPost, path: "/api/v1/wrapper/logout", body: `{"username":"a"}`, xUser: "alice", status: http.StatusForbidden},
		{name: "user users list forbidden", method: http.MethodGet, path: "/api/v1/users", xUser: "alice", status: http.StatusForbidden},
		{name: "user users create forbidden", method: http.MethodPost, path: "/api/v1/users", body: `{"username":"x"}`, xUser: "alice", status: http.StatusForbidden},
		{name: "admin wrapper status allowed", method: http.MethodGet, path: "/api/v1/wrapper/status", xUser: "boss", status: http.StatusOK},
		{name: "admin users list allowed", method: http.MethodGet, path: "/api/v1/users", xUser: "boss", status: http.StatusOK},
		{name: "user capabilities allowed", method: http.MethodGet, path: "/api/v1/capabilities", xUser: "alice", status: http.StatusOK},
		{name: "user me allowed", method: http.MethodGet, path: "/api/v1/me", xUser: "alice", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path == "/api/v1/capabilities" {
				fx.server.tools = media.NewToolChecker(configureTestTools())
			}
			recorder := authedRequest(t, routes, tt.method, tt.path, tt.body, tt.xUser)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tt.status, recorder.Body.String())
			}
		})
	}
}

func TestMeReturnsProfile(t *testing.T) {
	fx := newMultiUserFixture(t)
	recorder := authedRequest(t, fx.server.Routes(), http.MethodGet, "/api/v1/me", "", "alice")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["username"] != "alice" || body["role"] != "user" || body["user_id"] != fx.user.ID {
		t.Fatalf("me = %#v", body)
	}
}

func TestMeSingleUserMode(t *testing.T) {
	server := &Server{}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/me", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["role"] != "admin" || body["user_id"] != "" {
		t.Fatalf("me = %#v", body)
	}
}

func TestDownloadOwnership(t *testing.T) {
	fx := newMultiUserFixture(t)
	routes := fx.server.Routes()
	ctx := context.Background()
	now := time.Now().UTC()
	aliceJob := domain.Job{ID: "job-alice", UserID: fx.user.ID, Input: "https://music.apple.com/cn/song/a/1", Type: "song", CanonicalKey: "song:cn:1", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	adminJob := domain.Job{ID: "job-boss", UserID: fx.admin.ID, Input: "https://music.apple.com/cn/song/b/2", Type: "song", CanonicalKey: "song:cn:2", Status: domain.JobQueued, CreatedAt: now.Add(time.Second), UpdatedAt: now}
	for _, job := range []domain.Job{aliceJob, adminJob} {
		if err := fx.store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	// Non-owned job is hidden as 404 for get and cancel.
	for _, probe := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/downloads/job-boss"},
		{http.MethodPost, "/api/v1/downloads/job-boss/cancel"},
		{http.MethodGet, "/api/v1/downloads/job-boss/events"},
	} {
		recorder := authedRequest(t, routes, probe.method, probe.path, "", "alice")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", probe.method, probe.path, recorder.Code)
		}
	}

	// Owner and admin can read the job.
	if recorder := authedRequest(t, routes, http.MethodGet, "/api/v1/downloads/job-alice", "", "alice"); recorder.Code != http.StatusOK {
		t.Fatalf("owner get status = %d", recorder.Code)
	}
	if recorder := authedRequest(t, routes, http.MethodGet, "/api/v1/downloads/job-alice", "", "boss"); recorder.Code != http.StatusOK {
		t.Fatalf("admin get status = %d", recorder.Code)
	}

	// List: user sees own jobs only; admin sees all and can filter.
	decodeJobs := func(recorder *httptest.ResponseRecorder) []domain.Job {
		var out []domain.Job
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode list: %v (%s)", err, recorder.Body.String())
		}
		return out
	}
	own := decodeJobs(authedRequest(t, routes, http.MethodGet, "/api/v1/downloads", "", "alice"))
	if len(own) != 1 || own[0].ID != "job-alice" {
		t.Fatalf("alice list = %+v", own)
	}
	all := decodeJobs(authedRequest(t, routes, http.MethodGet, "/api/v1/downloads", "", "boss"))
	if len(all) != 2 {
		t.Fatalf("admin list = %+v", all)
	}
	filtered := decodeJobs(authedRequest(t, routes, http.MethodGet, "/api/v1/downloads?user=alice", "", "boss"))
	if len(filtered) != 1 || filtered[0].ID != "job-alice" {
		t.Fatalf("admin filtered list = %+v", filtered)
	}
	empty := decodeJobs(authedRequest(t, routes, http.MethodGet, "/api/v1/downloads?user=nobody", "", "boss"))
	if len(empty) != 0 {
		t.Fatalf("unknown user list = %+v", empty)
	}
}

func TestCreateDownloadAttributesOwner(t *testing.T) {
	fx := newMultiUserFixture(t)
	recorder := authedRequest(t, fx.server.Routes(), http.MethodPost, "/api/v1/downloads", `{"urls":["https://music.apple.com/cn/song/a/1"]}`, "alice")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp domain.BatchSubmitResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 1 || len(resp.Results) != 1 || resp.Results[0].Job == nil {
		t.Fatalf("resp = %+v", resp)
	}
	job := *resp.Results[0].Job
	if job.UserID != fx.user.ID {
		t.Fatalf("job.UserID = %q, want %q", job.UserID, fx.user.ID)
	}
	stored, err := fx.store.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UserID != fx.user.ID || stored.Username != "alice" {
		t.Fatalf("stored attribution = %q/%q", stored.UserID, stored.Username)
	}
}

// TestSubmitDedupIsScopedPerUser: two users downloading the same content into
// their own directories must not collide on the canonical key.
func TestSubmitDedupIsScopedPerUser(t *testing.T) {
	fx := newMultiUserFixture(t)
	routes := fx.server.Routes()
	body := `{"urls":["https://music.apple.com/cn/song/a/1"]}`
	if recorder := authedRequest(t, routes, http.MethodPost, "/api/v1/downloads", body, "alice"); recorder.Code != http.StatusAccepted {
		t.Fatalf("alice submit status = %d", recorder.Code)
	}
	recorder := authedRequest(t, routes, http.MethodPost, "/api/v1/downloads", body, "boss")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("boss submit status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	// Same user resubmitting stays deduplicated.
	var resp domain.BatchSubmitResponse
	recorder = authedRequest(t, routes, http.MethodPost, "/api/v1/downloads", body, "alice")
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 0 || resp.Results[0].Status != domain.SubmitDuplicateActive {
		t.Fatalf("alice resubmit = %+v, want duplicate_active", resp)
	}
}

func TestUserManagementAPI(t *testing.T) {
	fx := newMultiUserFixture(t)
	routes := fx.server.Routes()

	// Create.
	recorder := authedRequest(t, routes, http.MethodPost, "/api/v1/users",
		`{"username":"carol","role":"user","aliases":["cc"],"emails":["carol@example.com"]}`, "boss")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var carol domain.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &carol); err != nil {
		t.Fatal(err)
	}

	// Created user can authenticate via alias.
	if recorder := authedRequest(t, routes, http.MethodGet, "/api/v1/me", "", "cc"); recorder.Code != http.StatusOK {
		t.Fatalf("alias auth status = %d", recorder.Code)
	}

	// Invalid username and duplicates.
	if recorder := authedRequest(t, routes, http.MethodPost, "/api/v1/users", `{"username":"Bad Name"}`, "boss"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid username status = %d", recorder.Code)
	}
	if recorder := authedRequest(t, routes, http.MethodPost, "/api/v1/users", `{"username":"carol"}`, "boss"); recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate username status = %d", recorder.Code)
	}

	// Patch: promote and replace aliases.
	recorder = authedRequest(t, routes, http.MethodPatch, "/api/v1/users/"+carol.ID, `{"role":"admin","aliases":["c3"]}`, "boss")
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var patched domain.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Role != domain.RoleAdmin || len(patched.Aliases) != 1 || patched.Aliases[0] != "c3" || len(patched.Emails) != 1 {
		t.Fatalf("patched = %+v", patched)
	}

	// Delete disables and the account stops authenticating.
	if recorder := authedRequest(t, routes, http.MethodDelete, "/api/v1/users/"+carol.ID, "", "boss"); recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	if recorder := authedRequest(t, routes, http.MethodGet, "/api/v1/me", "", "carol"); recorder.Code != http.StatusForbidden {
		t.Fatalf("disabled auth status = %d, want 403", recorder.Code)
	}

	// Unknown user id.
	if recorder := authedRequest(t, routes, http.MethodGet, "/api/v1/users/nope", "", "boss"); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown user status = %d", recorder.Code)
	}
}

func TestAdminLockoutGuards(t *testing.T) {
	fx := newMultiUserFixture(t)
	routes := fx.server.Routes()

	// boss is the only admin. Self-demotion and self-disable are refused so an
	// admin cannot lock themselves out.
	if r := authedRequest(t, routes, http.MethodPatch, "/api/v1/users/"+fx.admin.ID, `{"role":"user"}`, "boss"); r.Code != http.StatusForbidden {
		t.Fatalf("self-demote status = %d, want 403, body = %s", r.Code, r.Body.String())
	}
	if r := authedRequest(t, routes, http.MethodPatch, "/api/v1/users/"+fx.admin.ID, `{"enabled":false}`, "boss"); r.Code != http.StatusForbidden {
		t.Fatalf("self-disable via patch status = %d, want 403", r.Code)
	}
	if r := authedRequest(t, routes, http.MethodDelete, "/api/v1/users/"+fx.admin.ID, "", "boss"); r.Code != http.StatusForbidden {
		t.Fatalf("self-delete status = %d, want 403", r.Code)
	}
	// boss is still an enabled admin and can still act.
	if r := authedRequest(t, routes, http.MethodGet, "/api/v1/users", "", "boss"); r.Code != http.StatusOK {
		t.Fatalf("boss still admin status = %d, want 200", r.Code)
	}

	// Promote alice: with two admins, demoting the non-self one is allowed.
	if r := authedRequest(t, routes, http.MethodPatch, "/api/v1/users/"+fx.user.ID, `{"role":"admin"}`, "boss"); r.Code != http.StatusOK {
		t.Fatalf("promote alice status = %d, body = %s", r.Code, r.Body.String())
	}
	if r := authedRequest(t, routes, http.MethodPatch, "/api/v1/users/"+fx.user.ID, `{"role":"user"}`, "boss"); r.Code != http.StatusOK {
		t.Fatalf("demote alice (two admins) status = %d, body = %s", r.Code, r.Body.String())
	}
}

// TestLastAdminInvariantSingleUserMode exercises the non-self last-admin guard,
// reachable when the caller is the built-in admin (auth disabled, empty UserID):
// the sole enabled admin account cannot be disabled, which also prevents a
// lockout if auth is later enabled.
func TestLastAdminInvariantSingleUserMode(t *testing.T) {
	fx := newMultiUserFixture(t)
	fx.server.cfg.Auth.Enabled = false
	routes := fx.server.Routes()

	if r := authedRequest(t, routes, http.MethodDelete, "/api/v1/users/"+fx.admin.ID, "", ""); r.Code != http.StatusConflict {
		t.Fatalf("delete last admin status = %d, want 409, body = %s", r.Code, r.Body.String())
	}
	if r := authedRequest(t, routes, http.MethodPatch, "/api/v1/users/"+fx.admin.ID, `{"role":"user"}`, ""); r.Code != http.StatusConflict {
		t.Fatalf("demote last admin status = %d, want 409", r.Code)
	}
}
