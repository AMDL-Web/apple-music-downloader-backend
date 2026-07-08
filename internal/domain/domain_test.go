package domain

import "testing"

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
