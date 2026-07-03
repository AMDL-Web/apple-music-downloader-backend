package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
)

// authTestSecret is the internal secret used by tests that exercise identity
// resolution behind an enabled auth mode, which now requires a configured
// secret and a matching X-Internal-Secret header.
const authTestSecret = "s3cret"

func openStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func identityEcho(t *testing.T, captured *Identity) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := FromContext(r.Context())
		if !ok {
			t.Fatal("identity missing from context")
		}
		*captured = id
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareDisabledActsAsAdmin(t *testing.T) {
	var got Identity
	handler := Middleware(nil, config.AuthConfig{Enabled: false})(identityEcho(t, &got))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !got.IsAdmin() || got.UserID != "" {
		t.Fatalf("identity = %+v, want built-in admin", got)
	}
}

func TestMiddlewarePublicPathsSkipAuth(t *testing.T) {
	handler := Middleware(openStore(t), config.AuthConfig{Enabled: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/api/v1/health", "/docs", "/api/openapi.yaml"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("path %s status = %d, want 200", path, recorder.Code)
		}
	}
}

func TestMiddlewareResolvesUserAliasAndEmail(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, domain.User{
		Username: "lyjw", Role: domain.RoleAdmin, Enabled: true,
		Aliases: []string{"Liang"}, Emails: []string{"lyjw@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		xUser  string
		xEmail string
	}{
		{name: "username", xUser: "lyjw"},
		{name: "username case-insensitive", xUser: "LYJW"},
		{name: "alias", xUser: "liang"},
		{name: "email", xUser: "unknown", xEmail: "LYJW@example.com"},
		{name: "email only", xEmail: "lyjw@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Identity
			handler := Middleware(store, config.AuthConfig{Enabled: true, InternalSecret: authTestSecret})(identityEcho(t, &got))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
			req.Header.Set("X-Internal-Secret", authTestSecret)
			if tt.xUser != "" {
				req.Header.Set("X-User", tt.xUser)
			}
			if tt.xEmail != "" {
				req.Header.Set("X-Email", tt.xEmail)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if got.UserID != user.ID || got.Username != "lyjw" || !got.IsAdmin() {
				t.Fatalf("identity = %+v", got)
			}
		})
	}
}

func TestMiddlewareRejectsUnknownMissingAndDisabled(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, domain.User{Username: "gone", Role: domain.RoleUser, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	handler := Middleware(store, config.AuthConfig{Enabled: true, InternalSecret: authTestSecret})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not be reached")
	}))

	tests := []struct {
		name  string
		xUser string
	}{
		{name: "missing headers"},
		{name: "unknown user", xUser: "stranger"},
		{name: "disabled user", xUser: "gone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
			req.Header.Set("X-Internal-Secret", authTestSecret)
			if tt.xUser != "" {
				req.Header.Set("X-User", tt.xUser)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", recorder.Code)
			}
		})
	}
}

func TestMiddlewareInternalSecret(t *testing.T) {
	store := openStore(t)
	if _, err := store.CreateUser(context.Background(), domain.User{Username: "lyjw", Role: domain.RoleAdmin, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := Middleware(store, config.AuthConfig{Enabled: true, InternalSecret: "s3cret"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
	req.Header.Set("X-User", "lyjw")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing secret status = %d, want 401", recorder.Code)
	}

	req.Header.Set("X-Internal-Secret", "wrong")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret status = %d, want 401", recorder.Code)
	}

	req.Header.Set("X-Internal-Secret", "s3cret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("correct secret status = %d, want 200", recorder.Code)
	}
}

func TestMiddlewareEnabledWithoutSecretFailsClosed(t *testing.T) {
	store := openStore(t)
	if _, err := store.CreateUser(context.Background(), domain.User{Username: "boss", Role: domain.RoleAdmin, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler := Middleware(store, config.AuthConfig{Enabled: true})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not be reached when no secret is configured")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil)
	req.Header.Set("X-User", "boss")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (fail closed), body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireAdmin(t *testing.T) {
	called := false
	handler := RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wrapper/status", nil)
	handler(recorder, req.WithContext(withIdentity(req.Context(), Identity{UserID: "u1", Role: domain.RoleUser})))
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("non-admin: status = %d, called = %v", recorder.Code, called)
	}

	recorder = httptest.NewRecorder()
	handler(recorder, req.WithContext(withIdentity(req.Context(), Identity{UserID: "u2", Role: domain.RoleAdmin})))
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("admin: status = %d, called = %v", recorder.Code, called)
	}

	recorder = httptest.NewRecorder()
	handler(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("no identity: status = %d, want 403", recorder.Code)
	}
}
