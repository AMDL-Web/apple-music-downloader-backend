package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
)

// Identity is the authenticated caller, resolved from the trusted proxy
// headers and injected into the request context by Middleware.
type Identity struct {
	UserID   string
	Username string
	Role     domain.Role
}

func (id Identity) IsAdmin() bool { return id.Role == domain.RoleAdmin }

type contextKey struct{}

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// publicPath reports whether the request may skip authentication.
func publicPath(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/health", "/docs", "/api/openapi.yaml":
		return true
	}
	return false
}

// Middleware resolves X-User / X-Email (injected by the fronting proxy)
// against the users table and stores the Identity in the request context.
// With cfg.Enabled false every request acts as a built-in admin, keeping
// single-user deployments working unchanged.
func Middleware(store *db.Store, cfg config.AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicPath(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !cfg.Enabled {
				next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), Identity{Role: domain.RoleAdmin})))
				return
			}
			// Fail closed: an enabled auth mode with no configured secret means
			// the trust boundary is forgeable, so refuse every request rather
			// than fall through to header-derived identity. Config validation
			// already rejects this at startup; this guards against any path
			// that constructs the middleware without validation.
			if cfg.InternalSecret == "" {
				writeAuthError(w, http.StatusInternalServerError, "auth is enabled but no internal secret is configured")
				return
			}
			secret := r.Header.Get("X-Internal-Secret")
			if subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.InternalSecret)) != 1 {
				writeAuthError(w, http.StatusUnauthorized, "invalid internal secret")
				return
			}
			xUser := strings.TrimSpace(r.Header.Get("X-User"))
			xEmail := strings.TrimSpace(r.Header.Get("X-Email"))
			if xUser == "" && xEmail == "" {
				writeAuthError(w, http.StatusForbidden, "missing identity headers")
				return
			}
			user, err := store.ResolveIdentity(r.Context(), xUser, xEmail)
			if err == sql.ErrNoRows {
				writeAuthError(w, http.StatusForbidden, "user is not registered; contact an administrator")
				return
			}
			if err != nil {
				writeAuthError(w, http.StatusInternalServerError, "identity lookup failed")
				return
			}
			if !user.Enabled {
				writeAuthError(w, http.StatusForbidden, "user is disabled")
				return
			}
			identity := Identity{UserID: user.ID, Username: user.Username, Role: user.Role}
			next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
		})
	}
}

// RequireAdmin rejects non-admin callers with 403.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := FromContext(r.Context())
		if !ok || !identity.IsAdmin() {
			writeAuthError(w, http.StatusForbidden, "administrator role required")
			return
		}
		next(w, r)
	}
}

func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
