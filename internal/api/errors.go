package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"amdl/internal/db"
)

// ErrorCode is the stable, machine-readable code of an error response. Each
// constant below is a value of the ErrorCode enum in openapi.yaml, and the two
// sets are kept equal by TestErrorCodesMatchSpec. Clients switch on the code;
// the message is for humans and may change.
type ErrorCode string

// Error codes, grouped by the HTTP status they are sent with.
const (
	// 400
	CodeInvalidRequest ErrorCode = "invalid_request"
	CodeInvalidJSON    ErrorCode = "invalid_json"
	// 401
	CodeAuthenticationFailed ErrorCode = "authentication_failed"
	// 404
	CodeRouteNotFound   ErrorCode = "route_not_found"
	CodeJobNotFound     ErrorCode = "job_not_found"
	CodeLoginNotFound   ErrorCode = "login_not_found"
	CodeAccountNotFound ErrorCode = "account_not_found"
	CodeCatalogNotFound ErrorCode = "catalog_not_found"
	CodeHookNotFound    ErrorCode = "hook_not_found"
	// 405
	CodeMethodNotAllowed ErrorCode = "method_not_allowed"
	// 409
	CodeJobActive         ErrorCode = "job_active"
	CodeJobNotRetryable   ErrorCode = "job_not_retryable"
	CodeJobNotCancellable ErrorCode = "job_not_cancellable"
	CodeJobFinalizing     ErrorCode = "job_finalizing"
	CodeDuplicateActive   ErrorCode = "duplicate_active"
	CodeAlreadyLoggedIn   ErrorCode = "already_logged_in"
	CodeLoginPending      ErrorCode = "login_pending"
	CodeHookReadOnly      ErrorCode = "hook_read_only"
	// 413
	CodeRequestTooLarge ErrorCode = "request_too_large"
	// 422
	CodeInvalidOverrides    ErrorCode = "invalid_overrides"
	CodeConfigKeyNotMutable ErrorCode = "config_key_not_mutable"
	CodeInvalidConfig       ErrorCode = "invalid_config"
	CodeInvalidHook         ErrorCode = "invalid_hook"
	CodeInvalidHooksFile    ErrorCode = "invalid_hooks_file"
	CodeNoURLsAccepted      ErrorCode = "no_urls_accepted"
	// 500
	CodeInternalError ErrorCode = "internal_error"
	CodeStoreError    ErrorCode = "store_error"
	// 501
	CodeLoginUnsupported ErrorCode = "login_unsupported"
	// 502
	CodeUpstreamError ErrorCode = "upstream_error"
	// 503
	CodeQueueFull          ErrorCode = "queue_full"
	CodeWrapperUnavailable ErrorCode = "wrapper_unavailable"
	CodeFeatureUnavailable ErrorCode = "feature_unavailable"
	// 504
	CodeUpstreamTimeout ErrorCode = "upstream_timeout"
)

// errorResponse is the body of every non-2xx JSON response (the ErrorResponse
// schema), except GET /api/v1/health, whose 503 keeps the HealthResponse body.
type errorResponse struct {
	Error errorObject `json:"error"`
}

type errorObject struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Details any       `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	writeErrorDetails(w, status, code, message, nil)
}

// writeErrorDetails is writeError with a code-specific details object.
func writeErrorDetails(w http.ResponseWriter, status int, code ErrorCode, message string, details any) {
	writeJSON(w, status, errorResponse{Error: errorObject{Code: code, Message: message, Details: details}})
}

// writeStoreError answers a failed store read or write: 500 store_error, which
// tells the client the request may succeed if retried.
func writeStoreError(w http.ResponseWriter, op string, err error) {
	writeError(w, http.StatusInternalServerError, CodeStoreError, fmt.Sprintf("%s: %v", op, err))
}

// writeJobLookupError answers a failed job lookup. Only a missing row is a
// 404: a transient store error must not tell a client to drop a job that
// still exists.
func writeJobLookupError(w http.ResponseWriter, jobID string, err error) {
	if isJobNotFound(err) {
		writeError(w, http.StatusNotFound, CodeJobNotFound, fmt.Sprintf("job %s not found", jobID))
		return
	}
	writeStoreError(w, "read job "+jobID, err)
}

// isJobNotFound reports a missing job: the store answers sql.ErrNoRows, the
// job manager db.ErrJobNotFound.
func isJobNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, db.ErrJobNotFound)
}
