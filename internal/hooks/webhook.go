package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func init() {
	register("webhook", &webhookRunner{client: &http.Client{}})
}

type webhookRunner struct {
	client *http.Client
}

func (r *webhookRunner) Run(ctx context.Context, entry Entry, payload Payload) error {
	var body bytes.Buffer
	if entry.SendsPayload() {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return fmt.Errorf("encode payload: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL, &body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if entry.SendsPayload() {
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
