package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"amdl/internal/config"
)

// A job's canonical key is "type:storefront:adamID:destination". It is the
// dedup identity enforced by the partial unique index on jobs(canonical_key)
// over the queued/running statuses, and it is also the record of what was
// parsed at submission time, replayed by the download worker instead of
// re-parsing the raw input.
//
// The destination segment exists because two jobs writing to different
// directories are not duplicates of each other: without it, downloading an
// album to /music and then submitting the same album to /backup while the
// first is still running is rejected as a duplicate, which is wrong. Dedup
// means "don't download the same thing to the same place twice", so the key
// has to carry the place.
//
// Build and parse live together in this file on purpose. They are the two
// halves of one format, they sit in different packages at their call sites
// (internal/jobs writes the key, internal/media reads it back), and a change
// to one without the other is silent: an extra segment would simply shift
// which substring the worker believes is the adam id, and it would download
// the wrong thing without erroring.

// destinationSegmentLen is the width of the destination segment, in hex
// digits of the SHA-256 of the canonicalized effective downloads directory.
// The path is hashed rather than embedded so the key stays short, so a
// filesystem path stays out of a database index, and so a ':' in a directory
// name cannot reach the parser. 12 digits is 48 bits, which is enormous
// margin against the handful of destinations an install actually uses; a
// collision would only ever cost an over-eager dedup, never a wrong download.
const destinationSegmentLen = 12

// destinationSegment hashes the canonicalized directory the submission would
// write to: the downloads_dir override when the request sets one, else the
// runtime config's download.downloads_dir. Apply is the single definition of
// "effective", so this cannot drift from what the job actually runs under.
//
// Canonicalizing before hashing matters: "<root>/./users/a/" and
// "<root>/users/a" are the same directory and both are accepted, so hashing
// the raw string would let a caller defeat their own dedup by respelling the
// path.
func destinationSegment(base config.Config, overrides *config.DownloadOverrides) string {
	// Apply is nil-safe, so a submission with no overrides at all resolves to
	// the config default and keys identically to one that names it explicitly.
	dir := overrides.Apply(base).Download.DownloadsDir
	canonical, err := config.CanonicalPath(dir)
	if err != nil {
		// Never fail a submit over a key detail. Canonicalization touches the
		// filesystem, so it can fail on something unrelated to this request
		// (an unreadable directory partway up the tree); the lexical clean is
		// still deterministic for the same spelling, which keeps the key
		// stable across this job's retries and post-restart recovery.
		canonical = filepath.Clean(dir)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:destinationSegmentLen]
}

// canonicalKey joins the four segments. The first three stay verbatim and
// human-readable so existing log lines and operator habits still work.
func canonicalKey(jobType, storefront, id, destination string) string {
	return jobType + ":" + storefront + ":" + id + ":" + destination
}

// ParseCanonicalKey recovers the submission-time parse result from a stored
// key. It reports false for a key it cannot make sense of, leaving the caller
// to fall back to re-parsing the raw input.
//
// It accepts three-segment keys written before destinations joined the key,
// so no data migration is needed: rows already in the jobs table keep
// resolving to the same type/storefront/adam id they always did. Such a job
// simply carries no destination in its key, which can only cost a missed
// dedup against a newly submitted job, never a wrong download.
func ParseCanonicalKey(key string) (jobType, storefront, id string, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) < 3 {
		return "", "", "", false
	}
	idParts := parts[2:]
	// Drop the trailing destination segment, but only when something is left
	// for the id. The length guard is what keeps an old three-segment key
	// whose adam id happens to be 12 hex-looking digits from being read as a
	// destination with an empty id.
	if len(idParts) > 1 && isDestinationSegment(idParts[len(idParts)-1]) {
		idParts = idParts[:len(idParts)-1]
	}
	// Re-join instead of taking idParts[0]. An adam id is the last path
	// segment of a submitted URL, ':' is legal there and nothing rejects it,
	// so an id containing one has to survive the round trip intact — this is
	// the case a plain fixed-count split would silently truncate.
	id = strings.Join(idParts, ":")
	jobType, storefront = parts[0], parts[1]
	if jobType == "" || storefront == "" || id == "" {
		return "", "", "", false
	}
	return jobType, storefront, id, true
}

func isDestinationSegment(s string) bool {
	if len(s) != destinationSegmentLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
