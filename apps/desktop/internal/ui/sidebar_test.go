package ui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/internal/client"
	"amdl/internal/domain"
)

func TestTheConnectionCardSlidesUpAsADownloadStarts(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	a.Screen = ScreenQueue
	tt := ui.NewTester(a.View, 1280, 800)
	card := func() float32 {
		r, ok := tt.Find("Connection settings")
		if !ok {
			t.Fatalf("no connection card in %q", tt.Texts())
		}
		return r.Y
	}
	before := card()
	now := time.Now()
	a.Store.ApplySnapshot(client.DownloadList{
		StatusCounts: domain.JobStatusCounts{Running: 1, Total: 1},
		Downloads:    []domain.Job{{ID: "r", Type: "album", Status: domain.JobRunning, Title: "Running Album", TotalItems: 5, CreatedAt: now, StartedAt: &now}},
	})
	tt.Frame()
	if y := card(); y != before {
		t.Errorf("the card jumped from %v to %v the frame the download started", before, y)
	}
	tt.Advance(sidebarSlide)
	tt.Frame()
	if y, want := card(), before-(nowHeight+nowGap); y != want {
		t.Errorf("the card ends at %v, want %v: above the now card", y, want)
	}
	if !tt.HasText("Running Album") {
		t.Errorf("no now card in %q", tt.Texts())
	}
}
