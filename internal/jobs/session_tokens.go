package jobs

import (
	"context"
	"strings"
	"sync"
)

// needsMediaUserToken reports whether a job of this type/id ever reads the
// batch's media-user-token: stations always (the next-tracks endpoint requires
// it) and private playlists (pl.u- ids, whose cover lives on the owner's
// library copy). Every other job type never reads it, so no credential is
// retained for those jobs at all.
func needsMediaUserToken(jobType, id string) bool {
	return jobType == "station" || (jobType == "playlist" && strings.HasPrefix(id, "pl.u-"))
}

// SessionTokenStore holds per-job media-user-tokens (Apple subscription
// tokens) selected for a download batch. The per-job copy is deliberately kept
// in memory only and never persisted with database jobs: it authorizes a single
// submission's user-scoped resolution and nothing more. A process restart drops
// in-memory request tokens; configured catalog.media_user_token values can be
// selected again for newly submitted jobs.
type SessionTokenStore struct {
	mu     sync.Mutex
	tokens map[string]string
}

func NewSessionTokenStore() *SessionTokenStore {
	return &SessionTokenStore{tokens: map[string]string{}}
}

// Set records token for jobID. An empty token is not stored, so Get stays
// consistent with "no token supplied".
func (s *SessionTokenStore) Set(jobID, token string) {
	if s == nil || token == "" {
		return
	}
	s.mu.Lock()
	s.tokens[jobID] = token
	s.mu.Unlock()
}

// Get returns the token for jobID, or "" if none was supplied (or it was
// already dropped, e.g. after a restart).
func (s *SessionTokenStore) Get(jobID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[jobID]
}

// Delete forgets jobID's token. Called when a job is deleted so its credential
// does not linger in memory.
func (s *SessionTokenStore) Delete(jobID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.tokens, jobID)
	s.mu.Unlock()
}

type ctxKey int

const mediaUserTokenKey ctxKey = iota

// WithMediaUserToken attaches a media-user-token to ctx so the download
// pipeline can read it during station resolution without threading it through
// every call. An empty token leaves ctx unchanged.
func WithMediaUserToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, mediaUserTokenKey, token)
}

// MediaUserTokenFromContext returns the media-user-token attached to ctx, or ""
// if none is present.
func MediaUserTokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(mediaUserTokenKey).(string)
	return token
}
