package media

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
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
	// The intermediate staging file must be gone (renamed into place), not
	// orphaned. Its name carries a per-call uniquifier, so scan for the suffix
	// rather than stat'ing one fixed name.
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), partSuffix) {
			t.Fatalf("staging file %s still present after copyIntoPlace", entry.Name())
		}
	}
	// The copy path leaves the source for the caller's temp cleanup to remove.
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("src should remain after copy, got err=%v", err)
	}
}

// TestCopyIntoPlaceConcurrentWritersToSameDestination is the regression test for
// the shared staging file. It drives copyIntoPlace directly rather than through
// ProcessJob: the collision needs two writers inside the EXDEV branch aimed at
// one destination, and reaching that through the public path would mean two
// filesystems plus defeating processOutputLocks, which serialises same-output
// writers in-process. copyIntoPlace is the unit that owns the staging name, so
// that is where the invariant is tested.
//
// With the staging name fixed at dst+".part" every writer truncates the same
// file: the finished dst ends up a mix of two payloads, or the second rename
// fails with ENOENT because the first already moved the shared file away.
func TestCopyIntoPlaceConcurrentWritersToSameDestination(t *testing.T) {
	const (
		writers = 6
		rounds  = 3
		// Large enough that the copies genuinely overlap rather than each
		// finishing inside the goroutine-start jitter.
		payloadSize = 2 << 20
	)
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('A' + i)}, payloadSize)
	}

	for round := range rounds {
		dir := t.TempDir()
		dst := filepath.Join(dir, "out", "Track.m4a")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		srcs := make([]string, writers)
		for i := range srcs {
			srcs[i] = filepath.Join(dir, fmt.Sprintf("flat-%d.m4a", i))
			if err := os.WriteFile(srcs[i], payloads[i], 0o644); err != nil {
				t.Fatal(err)
			}
		}

		errs := make([]error, writers)
		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(writers)
		for i := range writers {
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = copyIntoPlace(srcs[i], dst)
			}()
		}
		start.Done()
		done.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: writer %d: copyIntoPlace: %v", round, i, err)
			}
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("round %d: read dst: %v", round, err)
		}
		// Whichever writer renamed last wins, but the winner's bytes must be all
		// that is there — no truncation, no interleaving.
		winner := -1
		for i, want := range payloads {
			if bytes.Equal(got, want) {
				winner = i
				break
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: dst is not any single writer's payload: %d bytes, first byte %q, distinct bytes %q",
				round, len(got), firstByte(got), distinctBytes(got))
		}
		// Every staging file must have been renamed away or removed; none may be
		// left behind in the destination directory.
		entries, err := os.ReadDir(filepath.Dir(dst))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), partSuffix) {
				t.Fatalf("round %d: staging file %s left behind", round, entry.Name())
			}
		}
	}
}

func firstByte(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return string(b[:1])
}

func distinctBytes(b []byte) string {
	var seen [256]bool
	out := make([]byte, 0, 8)
	for _, c := range b {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
			if len(out) == cap(out) {
				break
			}
		}
	}
	return string(out)
}

// A per-call uniquifier makes the staging name longer than the output name, so
// it must not push a name that only just fits past the filesystem's per-component
// limit — that would fail the download after everything else had succeeded.
func TestStagingNameFitsFilesystemLimit(t *testing.T) {
	for _, tc := range []struct{ name, base string }{
		{"ascii", strings.Repeat("x", 400) + ".m4a"},
		// Multi-byte, to prove the trim lands on a rune boundary: APFS rejects
		// filenames that are not valid UTF-8.
		{"multibyte", strings.Repeat("é", 200) + ".m4a"},
		{"short", "Track.m4a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := stagingPrefix(filepath.Join("/downloads", "album", tc.base))
			// stagingPattern's "*" is replaced by os.CreateTemp with 10 digits.
			longest := len(prefix) + 10 + len(partSuffix)
			if longest > maxStagingNameLen {
				t.Fatalf("staging name can reach %d bytes, limit is %d", longest, maxStagingNameLen)
			}
			if !utf8.ValidString(prefix) {
				t.Fatalf("staging prefix %q is not valid UTF-8", prefix)
			}
			if !strings.HasSuffix(stagingPattern(filepath.Join("/x", tc.base)), "*"+partSuffix) {
				t.Fatal("staging pattern lost its uniquifier or suffix")
			}
		})
	}
}

// The deterministic half of the same regression: the staging name must not be
// derived from the destination alone. A file already sitting at dst+".part" —
// another writer's in-flight staging file — must be neither truncated nor
// renamed away by this copy. Single-goroutine, so it pins the naming rule
// without depending on scheduling.
func TestCopyIntoPlaceDoesNotClaimTheDestinationDerivedName(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "flat.m4a")
	if err := os.WriteFile(src, []byte("this writer's payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "Track.m4a")
	inFlight := dst + partSuffix
	const otherBytes = "another writer's bytes"
	if err := os.WriteFile(inFlight, []byte(otherBytes), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyIntoPlace(src, dst); err != nil {
		t.Fatalf("copyIntoPlace: %v", err)
	}

	got, err := os.ReadFile(inFlight)
	if err != nil {
		t.Fatalf("copyIntoPlace consumed the destination-derived staging name: %v", err)
	}
	if string(got) != otherBytes {
		t.Fatalf("copyIntoPlace wrote through the destination-derived staging name: %q", got)
	}
	if body, err := os.ReadFile(dst); err != nil || string(body) != "this writer's payload" {
		t.Fatalf("dst = %q (err=%v), want this writer's payload", body, err)
	}
}

func TestCleanupFailedOutputSweepsStagedParts(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "song.m4a")
	removed := []string{
		outPath,
		// Orphan from a build predating the uniquifier.
		outPath + partSuffix,
		outPath + ".1234567890" + partSuffix,
		outPath + ".987654321" + partSuffix,
		filepath.Join(dir, "song.lrc"),
		filepath.Join(dir, "song.ttml"),
	}
	kept := []string{
		// A different track downloading concurrently into the same album folder.
		filepath.Join(dir, "other.m4a.1234567890"+partSuffix),
		filepath.Join(dir, "song.m4a.other.m4a"),
		// Not a staging name copyIntoPlace can produce: the uniquifier is the
		// digit run os.CreateTemp writes, so anything else is left alone.
		outPath + ".notauniquifier" + partSuffix,
	}
	for _, path := range append(append([]string{}, removed...), kept...) {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	cleanupFailedOutput(outPath)

	for _, path := range removed {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after cleanupFailedOutput", filepath.Base(path))
		}
	}
	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should have survived cleanupFailedOutput: %v", filepath.Base(path), err)
		}
	}
}

// safeName leaves "[" and "]" in place, so a bracketed title is a real output
// name — and one filepath.Glob would read as a character class.
func TestCleanupFailedOutputHandlesGlobMetacharactersInTitle(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "Song [Live].m4a")
	staged := outPath + ".1234567890" + partSuffix
	// A glob built from the bracketed name would match this instead.
	bystander := filepath.Join(dir, "Song L.m4a.1234567890"+partSuffix)
	for _, path := range []string{staged, bystander} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cleanupFailedOutput(outPath)

	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("own staging file survived (err=%v)", err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("another track's staging file was removed: %v", err)
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
