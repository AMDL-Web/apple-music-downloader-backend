package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"amdl/internal/wrapper"
)

// maxJSONBodyBytes bounds every JSON request body handled by this API. The
// largest legitimate payload is a runtime-config patch or a batch of 100
// URLs; 1 MiB leaves ample room for both while preventing unbounded reads.
const maxJSONBodyBytes int64 = 1 << 20

// decodeJSONBody applies the same resource and framing rules to every JSON
// endpoint: at most maxJSONBodyBytes, exactly one JSON value, and optionally
// no unknown object fields. It writes the request error response itself so a
// MaxBytesError can consistently map to 413 instead of being flattened into
// a malformed-JSON 400.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, disallowUnknownFields bool) bool {
	if r.ContentLength > maxJSONBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("JSON request body exceeds %d bytes", maxJSONBodyBytes))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if disallowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(dst); err != nil {
		writeJSONBodyError(w, err)
		return false
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("request body must contain exactly one JSON value")
		}
		writeJSONBodyError(w, err)
		return false
	}
	return true
}

func writeJSONBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("JSON request body exceeds %d bytes", tooLarge.Limit))
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeWrapperError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, wrapper.ErrAuthenticationFailed):
		status = http.StatusUnauthorized
	case errors.Is(err, wrapper.ErrAlreadyLoggedIn), errors.Is(err, wrapper.ErrLoginSessionBusy):
		status = http.StatusConflict
	case errors.Is(err, wrapper.ErrLoginSessionNotFound), errors.Is(err, wrapper.ErrAccountNotFound):
		status = http.StatusNotFound
	case errors.Is(err, wrapper.ErrLoginTimeout):
		status = http.StatusGatewayTimeout
	}
	writeError(w, status, err)
}
