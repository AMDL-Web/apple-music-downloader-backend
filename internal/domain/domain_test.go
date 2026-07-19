package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"amdl/internal/config"
)

func TestJobJSONRedactsMediaUserTokenOverride(t *testing.T) {
	token := "secret-token"
	embed := false
	raw, err := json.Marshal(Job{
		ID:        "job-1",
		Overrides: &config.DownloadOverrides{MediaUserToken: &token, EmbedCover: &embed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "media_user_token") {
		t.Fatalf("job JSON leaked media-user-token: %s", raw)
	}
	if !strings.Contains(string(raw), `"embed_cover":false`) {
		t.Fatalf("job JSON lost ordinary override: %s", raw)
	}

	raw, err = json.Marshal(Job{ID: "job-2", Overrides: &config.DownloadOverrides{MediaUserToken: &token}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "overrides") {
		t.Fatalf("token-only override should be omitted from job JSON: %s", raw)
	}
}

func TestMarshalEventPayloadKeepsSnapshotFieldsAndRedaction(t *testing.T) {
	token := "secret-token"
	embed := false
	payload := MarshalEventPayload(Job{
		ID: "job-1", Status: JobCompleted,
		Overrides: &config.DownloadOverrides{MediaUserToken: &token, EmbedCover: &embed},
	}, map[string]any{"message": "completed", "attempts": 2})
	if payload == "" {
		t.Fatal("MarshalEventPayload returned an empty payload")
	}
	if strings.Contains(payload, token) || strings.Contains(payload, "media_user_token") {
		t.Fatalf("event payload leaked media-user-token: %s", payload)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "job-1" || got["status"] != string(JobCompleted) || got["message"] != "completed" || got["attempts"] != float64(2) {
		t.Fatalf("event payload = %#v, want snapshot plus event fields", got)
	}
	if _, ok := got["overrides"]; !ok {
		t.Fatalf("event payload lost non-credential override: %#v", got)
	}
}

func TestSummarizeHooksKeepsLatestStatePerHook(t *testing.T) {
	events := []Event{
		{ID: 1, Type: "hook_started", Phase: "emby-refresh"},
		{ID: 2, Type: "hook_started", Phase: "notify"},
		{ID: 3, Type: "job_finished"}, // non-hook events must be ignored
		{ID: 4, Type: "hook_succeeded", Phase: "emby-refresh"},
		{ID: 5, Type: "hook_failed", Phase: "notify", Message: "connect: refused"},
	}
	got := SummarizeHooks(events, false)
	want := []HookState{
		{Name: "emby-refresh", Status: "succeeded"},
		{Name: "notify", Status: "failed", Error: "connect: refused"},
	}
	if len(got) != len(want) {
		t.Fatalf("SummarizeHooks = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SummarizeHooks[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSummarizeHooksStartedState(t *testing.T) {
	events := []Event{{ID: 1, Type: "hook_started", Phase: "notify"}}

	if got := SummarizeHooks(events, true); len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("SummarizeHooks(stillRunning=true) = %+v, want notify running", got)
	}
	// Started but nothing in flight anymore (e.g. restart mid-hook): its
	// terminal event will never arrive, so it must not read as running forever.
	if got := SummarizeHooks(events, false); len(got) != 1 || got[0].Status != "interrupted" {
		t.Fatalf("SummarizeHooks(stillRunning=false) = %+v, want notify interrupted", got)
	}
}

func TestSummarizeHooksEmpty(t *testing.T) {
	if got := SummarizeHooks(nil, false); len(got) != 0 {
		t.Fatalf("SummarizeHooks(nil) = %+v, want empty", got)
	}
}

func TestIsOverviewMilestone(t *testing.T) {
	milestones := []string{
		"job_queued", "job_recovered", "job_started", "resolved_input",
		"item_completed", "item_skipped", "item_failed",
		"job_finished", "job_failed", "job_cancelled", EventDeleted,
	}
	for _, ty := range milestones {
		if !IsOverviewMilestone(ty) {
			t.Errorf("IsOverviewMilestone(%q) = false, want true", ty)
		}
	}
	// Per-item detail / intermediate events must not wake the overview feed.
	for _, ty := range []string{"item_progress", "codec_selected", "codec_fallback", "codec_failed", "item_overwrite", "hook_started", ""} {
		if IsOverviewMilestone(ty) {
			t.Errorf("IsOverviewMilestone(%q) = true, want false", ty)
		}
	}
}
