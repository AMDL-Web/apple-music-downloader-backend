package ui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/internal/client"
	"amdl/internal/domain"
)

func TestTheConnectionCardStaysAtTheFootAsADownloadStarts(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	a.Screen = ScreenQueue
	tt := ui.NewTester(a.View, 1280, 800)
	find := func(label string) ui.Rect {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("no %q in %q", label, tt.Texts())
		}
		return r
	}
	card, settings := find("Connection settings"), find("Settings")
	now := time.Now()
	a.Store.ApplySnapshot(client.DownloadList{
		StatusCounts: domain.JobStatusCounts{Running: 1, Total: 1},
		Downloads:    []domain.Job{{ID: "r", Type: "album", Status: domain.JobRunning, Title: "Running Album", TotalItems: 5, CreatedAt: now, StartedAt: &now}},
	})
	tt.Frame()
	tt.Advance(sidebarSlide)
	tt.Frame()
	if got := find("Connection settings"); got != card {
		t.Errorf("the connection card moved from %v to %v", card, got)
	}
	if got := find("Settings"); got != settings {
		t.Errorf("the sections moved from %v to %v", settings, got)
	}
	if nowCard := find("Downloading now"); nowCard.Y+nowCard.H+nowGap != card.Y {
		t.Errorf("the now card at %v does not sit right over the connection card at %v", nowCard, card)
	}
}
