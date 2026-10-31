package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

var webhookClient = &http.Client{}

type webhookRunner struct {
	client *http.Client
}

// Run sends entry's request: its method, POST when unset, with its body
// template rendered over the payload, or the JSON payload itself when it has
// none; a GET carries no body. Content-Type is application/json, unless the
// entry's headers name another.
func (r *webhookRunner) Run(ctx context.Context, entry Entry, payload Payload) error {
	method := entry.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	if method != http.MethodGet {
		var raw []byte
		var err error
		if entry.Body != "" {
			raw, err = renderBody(entry.Body, payload)
		} else {
			raw, err = json.Marshal(payload)
		}
		if err != nil {
			return fmt.Errorf("build body: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, entry.URL, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range entry.Headers {
		req.Header.Set(k, v)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
