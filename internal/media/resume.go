package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const resumeMetadataVersion = 1

type resumeMetadata struct {
	Version      int    `json:"version"`
	SourceHash   string `json:"source_hash"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Total        int64  `json:"total,omitempty"`
	Complete     bool   `json:"complete,omitempty"`
}

func resumableDownloadPaths(dir, owner, resumeKey string) (string, string) {
	sum := sha256.Sum256([]byte(resumeKey))
	name := "resume-" + hex.EncodeToString(sum[:]) + ".mp4"
	path := filepath.Join(resumeOwnerDir(dir, owner), name)
	return path, path + ".json"
}

func resumeOwnerDir(dir, owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return filepath.Join(dir, "resume-job-"+hex.EncodeToString(sum[:]))
}

func loadResumeCheckpoint(path, metadataPath, uri string) (int64, resumeMetadata) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	var metadata resumeMetadata
	if json.Unmarshal(raw, &metadata) != nil || metadata.Version != resumeMetadataVersion {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	// The resume key is the output path, so a retry or codec fallback that
	// resolves the same output to a different media object would otherwise pick
	// up the previous object's partial bytes. If-Range alone cannot catch that
	// when validators collide across variants, so bind the checkpoint to its
	// source and restart cleanly on any mismatch.
	if metadata.SourceHash != sourceFingerprint(uri) {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	// Without a strong ETag or Last-Modified, a same-URL/same-size object can
	// change between requests and a naked Range would silently splice two
	// representations. Restart from zero rather than trade integrity for reuse.
	if resumeValidator(metadata) == "" {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	if metadata.Total > 0 && info.Size() > metadata.Total {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	// Total is written before the body so progress survives interruption. Only
	// the complete marker is committed after the media file has been synced and
	// closed; a crash in that final window must restart rather than trust length
	// alone (a sparse/page-cache tail can have the right stat size).
	if metadata.Total > 0 && info.Size() == metadata.Total && !metadata.Complete {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	return info.Size(), metadata
}

// sourceFingerprint identifies the object a checkpoint belongs to. CDN URLs
// for the same media commonly rotate query-string signatures between fetches,
// so only the scheme, host, and path participate; a different variant or codec
// lands on a different path and invalidates the checkpoint.
func sourceFingerprint(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Host != "" {
		uri = u.Scheme + "://" + u.Host + u.Path
	}
	sum := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(sum[:])
}

func writeResumeMetadata(path string, metadata resumeMetadata) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func resumeValidator(metadata resumeMetadata) string {
	if metadata.ETag != "" && !strings.HasPrefix(strings.TrimSpace(metadata.ETag), "W/") {
		return metadata.ETag
	}
	return metadata.LastModified
}

func resumeObjectChanged(metadata resumeMetadata, header http.Header) bool {
	if metadata.ETag != "" && header.Get("ETag") != "" && metadata.ETag != header.Get("ETag") {
		return true
	}
	return metadata.LastModified != "" && header.Get("Last-Modified") != "" && metadata.LastModified != header.Get("Last-Modified")
}

// cleanupResumableDownload removes one checkpoint and its metadata. It leaves
// the job's directory, even empty: a sibling track may have just created it
// and be about to open its own checkpoint there. cleanupResumeOwner removes
// the directory once the job is done with it.
func cleanupResumableDownload(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(path + ".json")
	_ = os.Remove(path + ".json.tmp")
}

func cleanupResumeForKey(dir, owner, resumeKey string) {
	path, _ := resumableDownloadPaths(dir, owner, resumeKey)
	cleanupResumableDownload(path)
}

func cleanupResumeOwner(dir, owner string) {
	if dir == "" || owner == "" {
		return
	}
	_ = os.RemoveAll(resumeOwnerDir(dir, owner))
}
