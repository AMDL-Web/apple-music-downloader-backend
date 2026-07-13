package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeToOutputSameFilesystemRenames(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "flat-123.m4a")
	dst := filepath.Join(dir, "sub", "Song.m4a")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("finished tagged audio")
	// Staging uses os.CreateTemp (0600); write src with that mode so the test
	// proves finalizeToOutput upgrades the finished file to world-readable 0644.
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := finalizeToOutput(src, dst); err != nil {
		t.Fatalf("finalizeToOutput: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("dst content = %q, want %q", got, want)
	}
	// A same-filesystem finalize is a rename, so the staged source is consumed.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("src still present after rename (err=%v)", err)
	}
	assertMode0644(t, dst)
}

func TestCopyIntoPlaceLeavesCompleteFileAndKeepsSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "flat-abc.m4a")
	dst := filepath.Join(dir, "out", "Track.m4a")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("cross-device payload")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}

	// copyIntoPlace is the cross-filesystem branch of finalizeToOutput; exercise
	// it directly (a real EXDEV needs two filesystems).
	if err := copyIntoPlace(src, dst); err != nil {
		t.Fatalf("copyIntoPlace: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("dst content = %q, want %q", got, want)
	}
	// The intermediate .part must be gone (renamed into place), not orphaned.
	if _, err := os.Stat(dst + partSuffix); !os.IsNotExist(err) {
		t.Fatalf("dst .part still present after copyIntoPlace (err=%v)", err)
	}
	// The copy path leaves the source for the caller's temp cleanup to remove.
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("src should remain after copy, got err=%v", err)
	}
}

func assertMode0644(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("%s mode = %o, want 0644", path, perm)
	}
}

func TestCopyFilePreservesContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.bin")
	dst := filepath.Join(dir, "out.bin")
	want := make([]byte, 200000)
	for i := range want {
		want[i] = byte(i * 7)
	}
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("copied %d bytes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %d, want %d", i, got[i], want[i])
		}
	}
}
