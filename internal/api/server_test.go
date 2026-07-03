package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/media"
	"amdl/internal/wrapper"
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

func TestWriteSubmitErrorUnsupportedStorefront(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSubmitError(recorder, &jobs.RequestError{
		Code: "unsupported_storefront", Message: "unsupported", Storefront: "us",
		SupportedStorefronts: []string{"cn"},
	})

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "unsupported_storefront" || body["storefront"] != "us" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestWriteSubmitErrorDecryptorUnavailable(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSubmitError(recorder, &jobs.RequestError{Code: "decryptor_unavailable", Message: "offline"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
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
		"/api/v1/me":                           {"get"},
		"/api/v1/users":                        {"get", "post"},
		"/api/v1/users/{user_id}":              {"get", "patch", "delete"},
		"/api/v1/wrapper/status":               {"get"},
		"/api/v1/wrapper/login":                {"post"},
		"/api/v1/wrapper/login/{login_id}/2fa": {"post"},
		"/api/v1/wrapper/logout":               {"post"},
		"/api/v1/quality":                      {"post"},
		"/api/v1/downloads":                    {"get", "post"},
		"/api/v1/downloads/{job_id}":           {"get"},
		"/api/v1/downloads/{job_id}/cancel":    {"post"},
		"/api/v1/downloads/{job_id}/events":    {"get"},
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
