package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"amdl/backend/internal/jobs"
)

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
