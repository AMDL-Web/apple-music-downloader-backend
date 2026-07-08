package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/hooks"
	"amdl/internal/jobs"
	"amdl/internal/media"
	"amdl/internal/wrapper"
	"github.com/coder/websocket"
	"gopkg.in/yaml.v3"
)

type fakeWrapperService struct {
	statusCalls  int
	statusResult wrapper.Status
	statusErr    error
	startResult  wrapper.LoginResult
	startErr     error
	verifyResult wrapper.LoginResult
	verifyErr    error
	logoutErr    error
	username     string
	password     string
	loginID      string
	twoStepCode  string
}

func (f *fakeWrapperService) Status(context.Context) (wrapper.Status, error) {
	f.statusCalls++
	return f.statusResult, f.statusErr
}

func (f *fakeWrapperService) StartLogin(_ context.Context, username, password string) (wrapper.LoginResult, error) {
	f.username, f.password = username, password
	return f.startResult, f.startErr
}

func (f *fakeWrapperService) SubmitTwoStepCode(_ context.Context, loginID, code string) (wrapper.LoginResult, error) {
	f.loginID, f.twoStepCode = loginID, code
	return f.verifyResult, f.verifyErr
}

func (f *fakeWrapperService) Logout(_ context.Context, username string) error {
	f.username = username
	return f.logoutErr
}

type fakeQualityService struct {
	result media.QualityResult
	err    error
	req    media.QualityRequest
}

func (f *fakeQualityService) QueryQuality(_ context.Context, req media.QualityRequest) (media.QualityResult, error) {
	f.req = req
	return f.result, f.err
}

func configureTestTools() config.ToolsConfig {
	return config.ToolsConfig{FFmpeg: "true", GPAC: "true", MP4Box: "true", MP4Extract: "true", MP4Edit: "true"}
}

func requestJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestHealthEndpoint(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/health.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	fake := &fakeWrapperService{statusErr: errors.New("should not be called")}
	server := &Server{store: store, wrapper: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/health", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.statusCalls != 0 {
		t.Fatalf("health called wrapper.Status %d times", fake.statusCalls)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestHealthEndpointWithoutStore(t *testing.T) {
	server := &Server{}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/health", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHealthEndpointDatabaseUnavailable(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/health-closed.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: store}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/health", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestWrapperStatusEndpoint(t *testing.T) {
	fake := &fakeWrapperService{statusResult: wrapper.Status{
		Ready: true, Status: true, Regions: []string{"us"}, ClientCount: 1,
		Accounts: []string{"user@example.com"}, AccountsSupported: true,
	}}
	server := &Server{wrapper: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/wrapper/status", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body wrapper.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ready || body.ClientCount != 1 || len(body.Regions) != 1 {
		t.Fatalf("unexpected body: %#v", body)
	}
	if !body.AccountsSupported || len(body.Accounts) != 1 || body.Accounts[0] != "user@example.com" {
		t.Fatalf("unexpected accounts support: %#v", body)
	}
}

func TestWrapperStatusEndpointReportsUnsupportedAccounts(t *testing.T) {
	fake := &fakeWrapperService{statusResult: wrapper.Status{Ready: true, Status: true, Regions: []string{"us"}, ClientCount: 1}}
	server := &Server{wrapper: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/wrapper/status", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body wrapper.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AccountsSupported {
		t.Fatalf("accounts_supported = true, want false: %#v", body)
	}
}

func TestWrapperStatusUnavailable(t *testing.T) {
	server := &Server{wrapper: &fakeWrapperService{statusErr: errors.New("offline")}}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/wrapper/status", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestWrapperLoginResponses(t *testing.T) {
	tests := []struct {
		name   string
		result wrapper.LoginResult
		err    error
		status int
	}{
		{name: "logged in", result: wrapper.LoginResult{Status: wrapper.LoginStatusLoggedIn}, status: http.StatusOK},
		{name: "needs two step", result: wrapper.LoginResult{Status: wrapper.LoginStatusNeedsTwoStep, LoginID: "login-1"}, status: http.StatusAccepted},
		{name: "authentication failed", err: wrapper.ErrAuthenticationFailed, status: http.StatusUnauthorized},
		{name: "already logged in", err: wrapper.ErrAlreadyLoggedIn, status: http.StatusConflict},
		{name: "timeout", err: wrapper.ErrLoginTimeout, status: http.StatusGatewayTimeout},
		{name: "upstream", err: errors.New("connection refused"), status: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeWrapperService{startResult: tt.result, startErr: tt.err}
			server := &Server{wrapper: fake}
			recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/wrapper/login", `{"username":" user ","password":" secret "}`)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tt.status, recorder.Body.String())
			}
			if fake.username != "user" || fake.password != "secret" {
				t.Fatalf("credentials not trimmed: %q %q", fake.username, fake.password)
			}
		})
	}
}

func TestWrapperLoginValidatesRequiredFields(t *testing.T) {
	server := &Server{wrapper: &fakeWrapperService{}}
	for _, body := range []string{`{"username":"","password":"secret"}`, `{"username":"user","password":" "}`, `{`} {
		recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/wrapper/login", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d", body, recorder.Code)
		}
	}
}

func TestWrapperTwoStepResponses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "failed", err: wrapper.ErrAuthenticationFailed, status: http.StatusUnauthorized},
		{name: "not found", err: wrapper.ErrLoginSessionNotFound, status: http.StatusNotFound},
		{name: "busy", err: wrapper.ErrLoginSessionBusy, status: http.StatusConflict},
		{name: "timeout", err: wrapper.ErrLoginTimeout, status: http.StatusGatewayTimeout},
		{name: "upstream", err: errors.New("stream failed"), status: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeWrapperService{verifyResult: wrapper.LoginResult{Status: wrapper.LoginStatusLoggedIn}, verifyErr: tt.err}
			server := &Server{wrapper: fake}
			recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/wrapper/login/login-1/2fa", `{"two_step_code":" 123456 "}`)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tt.status, recorder.Body.String())
			}
			if fake.loginID != "login-1" || fake.twoStepCode != "123456" {
				t.Fatalf("unexpected verification input: %q %q", fake.loginID, fake.twoStepCode)
			}
		})
	}
}

func TestWrapperTwoStepValidatesCode(t *testing.T) {
	server := &Server{wrapper: &fakeWrapperService{}}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/wrapper/login/login-1/2fa", `{"two_step_code":" "}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestWrapperLogoutResponses(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "not found", err: wrapper.ErrAccountNotFound, status: http.StatusNotFound},
		{name: "upstream", err: errors.New("unavailable"), status: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeWrapperService{logoutErr: tt.err}
			server := &Server{wrapper: fake}
			recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/wrapper/logout", `{"username":" user "}`)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.status)
			}
			if fake.username != "user" {
				t.Fatalf("username = %q", fake.username)
			}
		})
	}
}

func TestWrapperLogoutValidatesUsername(t *testing.T) {
	server := &Server{wrapper: &fakeWrapperService{}}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/wrapper/logout", `{"username":" "}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestQualityEndpointReturnsOptions(t *testing.T) {
	fake := &fakeQualityService{result: media.QualityResult{
		Input: "https://music.apple.com/cn/album/example/1?i=2", Storefront: "cn", Type: "song", AdamID: "2",
		Song:      media.QualitySong{ID: "2", Name: "One Last Kiss", Artist: "Hikaru Utada", Album: "One Last Kiss"},
		Qualities: []media.QualityOption{{ID: "alac", Label: "Lossless", Available: true, CodecID: "audio-alac-stereo-44100-16", BitDepth: 16, SampleRate: 44100}},
	}}
	server := &Server{quality: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", `{"url":" https://music.apple.com/cn/album/example/1?i=2 "}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.req.URL != "https://music.apple.com/cn/album/example/1?i=2" {
		t.Fatalf("quality request URL = %q", fake.req.URL)
	}
	var body media.QualityResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AdamID != "2" || len(body.Qualities) != 1 || body.Qualities[0].ID != "alac" {
		t.Fatalf("unexpected quality response: %#v", body)
	}
}

func TestQualityEndpointValidatesURL(t *testing.T) {
	server := &Server{quality: &fakeQualityService{}}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/quality", `{"url":" "}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestCapabilitiesAdvertisesArtistDownloads(t *testing.T) {
	server := &Server{tools: media.NewToolChecker(configureTestTools())}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/capabilities", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		SupportedInputs   []string `json:"supported_inputs"`
		UnsupportedInputs []string `json:"unsupported_inputs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !containsString(body.SupportedInputs, "artist_url") {
		t.Fatalf("supported_inputs = %#v, want artist_url", body.SupportedInputs)
	}
	if containsString(body.UnsupportedInputs, "artist") {
		t.Fatalf("unsupported_inputs = %#v, did not want artist", body.UnsupportedInputs)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSwaggerUI(t *testing.T) {
	server := &Server{}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/docs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"swagger-ui-dist@5.32.8", "/api/openapi.yaml", "SwaggerUIBundle"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Swagger UI does not contain %q", expected)
		}
	}
}

func TestOpenAPISpecification(t *testing.T) {
	server := &Server{}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/openapi.yaml", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/yaml; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(recorder.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid OpenAPI YAML: %v", err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q", spec.OpenAPI)
	}
	wantOperations := map[string][]string{
		"/api/v1/health":                       {"get"},
		"/api/v1/capabilities":                 {"get"},
		"/api/v1/wrapper/status":               {"get"},
		"/api/v1/wrapper/login":                {"post"},
		"/api/v1/wrapper/login/{login_id}/2fa": {"post"},
		"/api/v1/wrapper/logout":               {"post"},
		"/api/v1/quality":                      {"post"},
		"/api/v1/downloads":                    {"get", "post"},
		"/api/v1/downloads/{job_id}":           {"get", "delete"},
		"/api/v1/downloads/{job_id}/cancel":    {"post"},
		"/api/v1/downloads/{job_id}/events":    {"get"},
		"/api/v1/downloads/{job_id}/events/ws": {"get"},
	}
	for path, methods := range wantOperations {
		operations, ok := spec.Paths[path]
		if !ok {
			t.Errorf("OpenAPI path %q is missing", path)
			continue
		}
		for _, method := range methods {
			if _, ok := operations[method]; !ok {
				t.Errorf("OpenAPI operation %s %s is missing", method, path)
			}
		}
	}
}

// stubProcessor parses "type|storefront|id" test URLs into a ValidationResult
// so createDownload can be exercised against a real jobs.Manager without
// hitting Apple Music.
type stubProcessor struct{}

func (stubProcessor) ValidateRequest(_ context.Context, url string) (jobs.ValidationResult, error) {
	parts := strings.SplitN(url, "|", 3)
	if len(parts) != 3 {
		return jobs.ValidationResult{}, &jobs.RequestError{Code: "invalid_url", Message: "malformed test url"}
	}
	return jobs.ValidationResult{Type: parts[0], Storefront: parts[1], ID: parts[2]}, nil
}

func (stubProcessor) ProcessJob(context.Context, domain.Job, jobs.Reporter) error { return nil }

func newTestServerWithManager(t *testing.T) *Server {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	manager := jobs.NewManager(store, events.NewHub(), stubProcessor{}, 1, slog.Default())
	return &Server{store: store, manager: manager}
}

func TestCreateDownloadSplitsBatchAndDedupes(t *testing.T) {
	server := newTestServerWithManager(t)
	body := `{"urls":["song|us|1, song|us|2","song|us|1"]}`
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads", body)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp domain.BatchSubmitResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Accepted != 2 || resp.Rejected != 1 || len(resp.Results) != 3 {
		t.Fatalf("resp = %+v, want 2 accepted / 1 rejected across 3 results", resp)
	}
	if resp.Results[2].Status != domain.SubmitDuplicateInRequest {
		t.Fatalf("third result = %+v, want duplicate_in_request", resp.Results[2])
	}
}

func TestGetDownloadDerivesProgressFromItems(t *testing.T) {
	server := newTestServerWithManager(t)
	ctx := context.Background()
	job := domain.Job{ID: "job1", Input: "song|us|1", Type: "song", Status: domain.JobRunning, TotalItems: 4}
	if err := server.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	statuses := []domain.ItemStatus{domain.ItemCompleted, domain.ItemSkipped, domain.ItemFailed, domain.ItemDownloading}
	for i, st := range statuses {
		item := domain.JobItem{ID: "item" + strconv.Itoa(i), JobID: job.ID, Index: i, Status: st}
		if err := server.store.CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads/"+job.ID, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Job domain.Job `json:"job"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// completed + skipped = 2 done, 1 failed — must match the returned items even
	// though the persisted job row still has DoneItems=0 (never finalized).
	if resp.Job.DoneItems != 2 {
		t.Fatalf("done_items = %d, want 2", resp.Job.DoneItems)
	}
	if resp.Job.FailedItems != 1 {
		t.Fatalf("failed_items = %d, want 1", resp.Job.FailedItems)
	}
}

// TestGetDownloadReportsHookStates verifies the snapshot endpoint carries the
// same hook information the SSE/WS stream pushes as hook_* events — the two
// are access modes of one state, so a client that never subscribes must still
// see hook outcomes.
func TestGetDownloadReportsHookStates(t *testing.T) {
	server := newTestServerWithManager(t)
	ctx := context.Background()
	job := domain.Job{ID: "job1", Input: "song|us|1", Type: "song", Status: domain.JobCompleted}
	if err := server.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []domain.Event{
		{JobID: job.ID, Type: "hook_started", Phase: "emby-refresh"},
		{JobID: job.ID, Type: "hook_succeeded", Phase: "emby-refresh"},
		{JobID: job.ID, Type: "hook_started", Phase: "notify"},
		{JobID: job.ID, Type: "hook_failed", Phase: "notify", Message: "connect: refused"},
	} {
		if _, err := server.store.AddEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads/"+job.ID, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Hooks []domain.HookState `json:"hooks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := []domain.HookState{
		{Name: "emby-refresh", Status: "succeeded"},
		{Name: "notify", Status: "failed", Error: "connect: refused"},
	}
	if len(resp.Hooks) != len(want) {
		t.Fatalf("hooks = %+v, want %+v", resp.Hooks, want)
	}
	for i := range want {
		if resp.Hooks[i] != want[i] {
			t.Fatalf("hooks[%d] = %+v, want %+v", i, resp.Hooks[i], want[i])
		}
	}

	// A job with no hook events must report an empty array, not null.
	plain := domain.Job{ID: "job2", Input: "song|us|2", Type: "song", Status: domain.JobRunning}
	if err := server.store.CreateJob(ctx, plain); err != nil {
		t.Fatal(err)
	}
	recorder = requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads/"+plain.ID, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"hooks":[]`) {
		t.Fatalf("body = %s, want a \"hooks\":[] field", recorder.Body.String())
	}
}

func TestListDownloadsDerivesProgressFromItems(t *testing.T) {
	server := newTestServerWithManager(t)
	ctx := context.Background()
	job := domain.Job{ID: "job1", Input: "song|us|1", Type: "song", Status: domain.JobRunning, TotalItems: 4}
	if err := server.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	statuses := []domain.ItemStatus{domain.ItemCompleted, domain.ItemSkipped, domain.ItemFailed, domain.ItemDownloading}
	for i, st := range statuses {
		item := domain.JobItem{ID: "item" + strconv.Itoa(i), JobID: job.ID, Index: i, Status: st}
		if err := server.store.CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var jobs []domain.Job
	if err := json.Unmarshal(recorder.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	// completed + skipped = 2 done, 1 failed — the persisted job row still has
	// DoneItems=0 (never finalized), so the list must count from job_items.
	if jobs[0].DoneItems != 2 {
		t.Fatalf("done_items = %d, want 2", jobs[0].DoneItems)
	}
	if jobs[0].FailedItems != 1 {
		t.Fatalf("failed_items = %d, want 1", jobs[0].FailedItems)
	}
}

func TestDownloadResponsesIncludeArtworkURLTemplate(t *testing.T) {
	server := newTestServerWithManager(t)
	ctx := context.Background()
	jobArt := "https://is1-ssl.mzstatic.com/image/thumb/Music/album/{w}x{h}bb.jpg"
	itemArt := "https://is1-ssl.mzstatic.com/image/thumb/Music/track/{w}x{h}bb.jpg"
	job := domain.Job{ID: "job1", Input: "album|us|1", Type: "album", ArtworkURL: jobArt, Status: domain.JobRunning, TotalItems: 1}
	if err := server.store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	item := domain.JobItem{ID: "item1", JobID: job.ID, Index: 1, ArtworkURL: itemArt, Status: domain.ItemQueued}
	if err := server.store.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads/"+job.ID, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var detail struct {
		Job   domain.Job       `json:"job"`
		Items []domain.JobItem `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Job.ArtworkURL != jobArt {
		t.Fatalf("job artwork_url = %q, want %q", detail.Job.ArtworkURL, jobArt)
	}
	if len(detail.Items) != 1 || detail.Items[0].ArtworkURL != itemArt {
		t.Fatalf("items = %+v, want one item with artwork_url %q", detail.Items, itemArt)
	}

	recorder = requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var jobs []domain.Job
	if err := json.Unmarshal(recorder.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ArtworkURL != jobArt {
		t.Fatalf("listed jobs = %+v, want one job with artwork_url %q", jobs, jobArt)
	}
}

func TestDeleteDownload(t *testing.T) {
	server := newTestServerWithManager(t)
	ctx := context.Background()
	terminal := domain.Job{ID: "job_done", Input: "song|us|1", Type: "song", CanonicalKey: "song:1", Status: domain.JobCompleted}
	running := domain.Job{ID: "job_run", Input: "song|us|2", Type: "song", CanonicalKey: "song:2", Status: domain.JobRunning}
	for _, job := range []domain.Job{terminal, running} {
		if err := server.store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.store.CreateItem(ctx, domain.JobItem{ID: "item1", JobID: terminal.ID, Status: domain.ItemCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.AddEvent(ctx, domain.Event{JobID: terminal.ID, Type: "job_finished"}); err != nil {
		t.Fatal(err)
	}

	recorder := requestJSON(t, server.Routes(), http.MethodDelete, "/api/v1/downloads/"+terminal.ID, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete terminal: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := server.store.GetJob(ctx, terminal.ID); err == nil {
		t.Fatal("job row still exists after delete")
	}
	items, err := server.store.ListItems(ctx, terminal.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("items after delete = %v (err %v), want none", items, err)
	}
	events, err := server.store.ListEventsAfter(ctx, terminal.ID, 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after delete = %v (err %v), want none", events, err)
	}

	recorder = requestJSON(t, server.Routes(), http.MethodDelete, "/api/v1/downloads/"+running.ID, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("delete running: status = %d, want %d", recorder.Code, http.StatusConflict)
	}

	recorder = requestJSON(t, server.Routes(), http.MethodDelete, "/api/v1/downloads/missing", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("delete missing: status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestEventsWebSocketStreamsBacklogLiveAndResume(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	hub := events.NewHub()
	manager := jobs.NewManager(store, hub, stubProcessor{}, 1, slog.Default())
	server := &Server{store: store, hub: hub, manager: manager}

	ctx := context.Background()
	job := domain.Job{ID: "job1", Input: "song|us|1", Type: "song", Status: domain.JobRunning}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Event(ctx, domain.Event{JobID: job.ID, Type: "job_started", Message: "job started"}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.Routes())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/downloads/job1/events/ws"

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	readEvent := func(c *websocket.Conn) domain.Event {
		t.Helper()
		msgType, raw, err := c.Read(dialCtx)
		if err != nil {
			t.Fatal(err)
		}
		if msgType != websocket.MessageText {
			t.Fatalf("message type = %v, want text", msgType)
		}
		var ev domain.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("invalid event JSON %q: %v", raw, err)
		}
		return ev
	}

	// Backlog: the event persisted before the connection must be replayed.
	first := readEvent(conn)
	if first.Type != "job_started" || first.JobID != job.ID {
		t.Fatalf("first event = %+v, want job_started for job1", first)
	}

	// Live: a hub publish must wake the drain without waiting for the ticker.
	if err := manager.Event(ctx, domain.Event{JobID: job.ID, Type: "item_progress", Message: "halfway"}); err != nil {
		t.Fatal(err)
	}
	second := readEvent(conn)
	if second.Type != "item_progress" || second.ID <= first.ID {
		t.Fatalf("second event = %+v, want item_progress with id > %d", second, first.ID)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// Resume: reconnecting with last_event_id must skip already-seen events.
	resume, _, err := websocket.Dial(dialCtx, wsURL+"?last_event_id="+strconv.FormatInt(first.ID, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resume.CloseNow()
	got := readEvent(resume)
	if got.ID != second.ID {
		t.Fatalf("resumed event id = %d, want %d (events at or before last_event_id must be skipped)", got.ID, second.ID)
	}
	_ = resume.Close(websocket.StatusNormalClosure, "")
}

func TestEventsRejectsTerminalJob(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	hub := events.NewHub()
	manager := jobs.NewManager(store, hub, stubProcessor{}, 1, slog.Default())
	server := &Server{store: store, hub: hub, manager: manager}

	ctx := context.Background()
	job := domain.Job{ID: "done1", Input: "song|us|1", Type: "song", Status: domain.JobCompleted}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads/done1/events", "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("SSE subscribe to terminal job: status = %d, want %d", recorder.Code, http.StatusConflict)
	}

	ts := httptest.NewServer(server.Routes())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/downloads/done1/events/ws"
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(dialCtx, wsURL, nil)
	if err == nil {
		t.Fatal("WS subscribe to terminal job: dial succeeded, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		status := -1
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("WS subscribe to terminal job: status = %d, want %d", status, http.StatusConflict)
	}

	recorder = requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/downloads/missing/events", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("SSE subscribe to missing job: status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

// TestEventsReplaysBacklogForTerminalJobInsteadOfRejecting guards against the
// regression a PR reviewer caught: rejecting a terminal job's subscription
// before checking for undelivered backlog would let a client miss events it
// never saw (including the terminal event itself, in the narrow window where
// the status write and the terminal event write used to land as separate,
// non-atomic statements). A client that is not yet caught up must still get
// the backlog; only a client with nothing left to receive gets rejected.
func TestEventsReplaysBacklogForTerminalJobInsteadOfRejecting(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	hub := events.NewHub()
	manager := jobs.NewManager(store, hub, stubProcessor{}, 1, slog.Default())
	server := &Server{store: store, hub: hub, manager: manager}

	ctx := context.Background()
	job := domain.Job{ID: "done2", Input: "song|us|1", Type: "song", Status: domain.JobCompleted}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Event(ctx, domain.Event{JobID: job.ID, Type: "job_started", Message: "job started"}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.AddEvent(ctx, domain.Event{JobID: job.ID, Type: "job_finished", Message: "completed"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/done2/events", nil)
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("SSE with pending backlog on terminal job: status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "job_started") || !strings.Contains(recorder.Body.String(), "job_finished") {
		t.Fatalf("SSE body missing backlog events: %s", recorder.Body.String())
	}

	ts := httptest.NewServer(server.Routes())
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/downloads/done2/events/ws"
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("WS with pending backlog on terminal job: dial failed: %v", err)
	}
	defer conn.CloseNow()
	_, raw, err := conn.Read(dialCtx)
	if err != nil {
		t.Fatal(err)
	}
	var ev domain.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("invalid event JSON %q: %v", raw, err)
	}
	if ev.Type != "job_started" {
		t.Fatalf("first WS event = %+v, want job_started", ev)
	}

	// Fully caught up now: a fresh connection with last_event_id at the
	// terminal event must go back to being rejected outright.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/downloads/done2/events", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(stored.ID, 10))
	recorder = httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("SSE fully caught up on terminal job: status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

// TestEventsWaitsForPendingHookBeforeClosingTerminalJob guards against the
// gap a PR reviewer caught: a job's own terminal event is not the last event
// its stream will ever see, because post-download hook dispatch is
// fire-and-forget and can keep recording hook_started/hook_succeeded well
// after the job itself reached a terminal status. Connecting while a hook is
// still in flight must not be rejected — the stream must stay open and
// deliver the hook's own events. Once the hook finishes, a fresh connection
// (fully caught up) must go back to being rejected outright, proving
// eventsExhausted picks up the hook's completion rather than staying stuck
// on the stale "pending" state forever.
func TestEventsWaitsForPendingHookBeforeClosingTerminalJob(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	hub := events.NewHub()
	manager := jobs.NewManager(store, hub, stubProcessor{}, 1, slog.Default())

	release := make(chan struct{})
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer hookServer.Close()
	dispatcher := hooks.NewDispatcher(hooks.Config{Enabled: true, TimeoutSeconds: 5, Entries: []hooks.Entry{
		{Name: "notify", Type: "webhook", Events: []string{"job_finished"}, URL: hookServer.URL},
	}}, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)
	defer dispatcher.Shutdown(context.Background())

	server := &Server{store: store, hub: hub, manager: manager}

	ctx := context.Background()
	job := domain.Job{ID: "done3", Input: "song|us|1", Type: "song", Status: domain.JobCompleted}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Event(ctx, domain.Event{JobID: job.ID, Type: "job_finished", Message: "completed"}); err != nil {
		t.Fatal(err)
	}
	// Simulate the hook that finalizeJob would have fired for this terminal job.
	dispatcher.Dispatch("job_finished", job, nil)
	deadline := time.After(2 * time.Second)
	for !dispatcher.Pending(job.ID) {
		select {
		case <-deadline:
			t.Fatal("hook never became pending")
		case <-time.After(time.Millisecond):
		}
	}

	ts := httptest.NewServer(server.Routes())
	defer ts.Close()
	getCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(getCtx, http.MethodGet, ts.URL+"/api/v1/downloads/done3/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (a pending hook must not be rejected as if the job's stream were exhausted)", resp.StatusCode, http.StatusOK)
	}

	reader := bufio.NewReader(resp.Body)
	readEventType := func() string {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE stream: %v", err)
			}
			if after, ok := strings.CutPrefix(line, "event: "); ok {
				return strings.TrimSpace(after)
			}
		}
	}
	if got := readEventType(); got != "job_finished" {
		t.Fatalf("first event = %q, want job_finished", got)
	}
	if got := readEventType(); got != "hook_started" {
		t.Fatalf("second event = %q, want hook_started (hook is pending, stream must not have closed)", got)
	}

	close(release) // let the blocked webhook call, and hook_succeeded, proceed
	if got := readEventType(); got != "hook_succeeded" {
		t.Fatalf("third event = %q, want hook_succeeded", got)
	}
	resp.Body.Close()

	deadline = time.After(2 * time.Second)
	for dispatcher.Pending(job.ID) {
		select {
		case <-deadline:
			t.Fatal("hook never stopped being pending after completing")
		case <-time.After(time.Millisecond):
		}
	}

	// The hook is done and the job was already terminal: a fresh, fully
	// caught-up connection must now be rejected outright, same as any other
	// exhausted terminal job.
	latestID, err := store.LatestEventID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err = http.NewRequestWithContext(getCtx, http.MethodGet, ts.URL+"/api/v1/downloads/done3/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", strconv.FormatInt(latestID, 10))
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status after hook completion = %d, want %d (nothing left to deliver)", resp.StatusCode, http.StatusConflict)
	}
}

func TestCreateDownloadRejectsEmptyURLs(t *testing.T) {
	server := newTestServerWithManager(t)
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads", `{"urls":[" , ,"]}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestCreateDownloadRejectsTooManyURLs(t *testing.T) {
	server := newTestServerWithManager(t)
	urls := make([]string, maxBatchSubmitURLs+1)
	for i := range urls {
		urls[i] = "song|us|" + strings.Repeat("1", 1) + string(rune('a'+i%26))
	}
	body, err := json.Marshal(domain.DownloadRequest{URLs: urls})
	if err != nil {
		t.Fatal(err)
	}
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads", string(body))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestCreateDownloadAllRejectedReturns422(t *testing.T) {
	server := newTestServerWithManager(t)
	recorder := requestJSON(t, server.Routes(), http.MethodPost, "/api/v1/downloads", `{"urls":["bad-url"]}`)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

type fakeDevTokenService struct {
	token string
	err   error
	calls int
}

func (f *fakeDevTokenService) MintDeveloperToken() (string, error) {
	f.calls++
	return f.token, f.err
}

func TestDeveloperTokenLegacyModeConflict(t *testing.T) {
	server := &Server{cfg: config.Default(), devToken: &fakeDevTokenService{}}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/developer-token", "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var resp map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["error"] == "" {
		t.Fatal("expected error message in response body")
	}
}

func signingEnabledConfig() config.Config {
	cfg := config.Default()
	cfg.Catalog.AppleMusicPrivateKeyPath = "keys/AuthKey.p8"
	cfg.Catalog.AppleMusicKeyID = "88KBJL3CKU"
	cfg.Catalog.AppleMusicTeamID = "2VTXNMR2GL"
	return cfg
}

func TestDeveloperTokenSigningMode(t *testing.T) {
	fake := &fakeDevTokenService{token: "signed.jwt.value"}
	server := &Server{cfg: signingEnabledConfig(), devToken: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/developer-token", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["token"] != "signed.jwt.value" {
		t.Fatalf("token = %q", resp["token"])
	}
	if _, ok := resp["expires_at"]; ok {
		t.Fatal("expires_at should not be in the response; clients read exp from the JWT")
	}
	if fake.calls != 1 {
		t.Fatalf("mint calls = %d, want 1", fake.calls)
	}
}

func TestDeveloperTokenSigningError(t *testing.T) {
	fake := &fakeDevTokenService{err: errors.New("boom")}
	server := &Server{cfg: signingEnabledConfig(), devToken: fake}
	recorder := requestJSON(t, server.Routes(), http.MethodGet, "/api/v1/developer-token", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}
