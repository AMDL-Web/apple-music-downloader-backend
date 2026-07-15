package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOutputLockSerializesNormalizedPathsAndReclaimsKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "album", "song.m4a")
	alias := filepath.Join(dir, "album", "unused", "..", "song.m4a")
	table := &outputLockTable{}

	unlockFirst, err := table.acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		unlock, acquireErr := table.acquire(alias)
		if acquireErr != nil {
			acquired <- nil
			return
		}
		acquired <- unlock
	}()

	select {
	case unlock := <-acquired:
		if unlock != nil {
			unlock()
		}
		t.Fatal("normalized alias acquired while the first writer still held the lock")
	case <-time.After(50 * time.Millisecond):
	}

	unlockFirst()
	var unlockSecond func()
	select {
	case unlockSecond = <-acquired:
		if unlockSecond == nil {
			t.Fatal("second lock acquisition failed")
		}
	case <-time.After(time.Second):
		t.Fatal("second writer did not acquire after release")
	}
	unlockSecond()

	table.mu.Lock()
	entries := len(table.entries)
	table.mu.Unlock()
	if entries != 0 {
		t.Fatalf("lock table retained %d unused keys, want 0", entries)
	}
}

func TestOutputLockSerializesSymlinkAliases(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	table := &outputLockTable{}
	unlock, err := table.acquire(filepath.Join(realDir, "song.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		second, _ := table.acquire(filepath.Join(linkDir, "song.m4a"))
		acquired <- second
	}()
	select {
	case second := <-acquired:
		second()
		t.Fatal("symlink alias bypassed output lock")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case second := <-acquired:
		second()
	case <-time.After(time.Second):
		t.Fatal("symlink alias did not acquire after release")
	}
}

func TestCanonicalOutputPathDoesNotChangeWhenFileSymlinkIsRemoved(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.m4a")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "song.m4a")
	if err := os.Symlink(target, outPath); err != nil {
		t.Fatal(err)
	}
	before, err := canonicalOutputPath(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(outPath); err != nil {
		t.Fatal(err)
	}
	after, err := canonicalOutputPath(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("output lock key changed after removing final symlink: %q -> %q", before, after)
	}
}
