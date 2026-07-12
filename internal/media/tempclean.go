package media

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// tempArtifactPrefixes are the basename prefixes of the scratch files and
// directories the downloader creates under Download.TempDir: the encrypted
// download (raw-), the streamed decrypt output (dec-), the flattened/tagged
// staging file (flat-), and the ffmpeg working dirs (fix-, check-). A crash
// mid-download can leave any of these behind.
var tempArtifactPrefixes = []string{"raw-", "dec-", "flat-", "fix-", "check-"}

// CleanupStaleTemp removes leftover downloader scratch files and directories
// from a previous run under dir. It only touches entries whose names match the
// downloader's own prefixes, so it is safe even if dir holds unrelated files.
//
// This assumes the temp dir has a single writer (one backend): at startup —
// before any job runs — anything matching is necessarily stale, never an
// in-flight download. Removal errors are logged, not fatal.
func CleanupStaleTemp(dir string, logger *slog.Logger) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir may not exist yet; nothing to clean
	}
	removed := 0
	for _, entry := range entries {
		if !hasTempArtifactPrefix(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			if logger != nil {
				logger.Warn("remove stale temp artifact", "path", path, "error", err)
			}
			continue
		}
		removed++
	}
	if removed > 0 && logger != nil {
		logger.Info("cleaned stale download temp artifacts", "dir", dir, "count", removed)
	}
}

func hasTempArtifactPrefix(name string) bool {
	for _, prefix := range tempArtifactPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
