package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/wrapper"
)

// mediaBackend is every piece of I/O one track performs: network transfers,
// the wrapper, ffmpeg and the disk. The track pipeline (trackRun and the stage
// functions it calls) owns every status, message, progress band, permit, retry
// and event, and reaches the outside world only through this interface. So the
// real backend and the simulated one (simulate mode) walk the same lifecycle by
// construction.
type mediaBackend interface {
	// coverArt fetches the embedded cover, trying urls in order.
	coverArt(ctx context.Context, urls []string, format, size string) ([]byte, error)
	// lyrics fetches the raw TTML document for a song.
	lyrics(ctx context.Context, songID string, opts wrapper.LyricsRequestOptions) (string, error)
	// requireTools fails when an external tool the backend needs is missing.
	requireTools(ctx context.Context) error

	// resolveAACLC resolves a song's AAC-LC WebPlayback asset.
	resolveAACLC(ctx context.Context, songID string) (aacLCMedia, error)
	// fetch transfers one codec's still-encrypted media.
	fetch(ctx context.Context, req fetchRequest, progress transferProgress) (encryptedMedia, error)
	// discardCheckpoint drops a resumable transfer for outPath that is no
	// longer needed because the output already exists.
	discardCheckpoint(jobID, outPath string)
	// discardOutput removes outPath, its staged parts and its lyrics sidecars:
	// before a forced overwrite and after a failed attempt.
	discardOutput(outPath string)

	// stage creates the scratch file a codec attempt flattens, verifies and
	// tags before it is committed; cleanup removes whatever is left of it.
	stage(media encryptedMedia) (staged stagedFile, cleanup func(), err error)
	// decrypt decrypts req's input into the staged file, remuxing it into a
	// progressive MP4 on the way.
	decrypt(ctx context.Context, req decryptRequest, hooks decryptHooks) error
	// verify decodes the staged file completely; nil means it plays cleanly.
	verify(ctx context.Context, staged stagedFile) error
	// sampleRate reads the sample rate in Hz the staged file declares, for an
	// item whose manifest named none.
	sampleRate(staged stagedFile) (int, error)
	// repairALAC patches malformed ALAC packet terminators in the staged file
	// in place. patched is called with the count before the file is rewritten.
	repairALAC(ctx context.Context, staged stagedFile, patched func(int)) error
	mkdirAll(dir string) error
	// writeFile writes a sidecar or standalone cover atomically.
	writeFile(path string, data []byte) error
	// tag writes metadata, lyrics and cover art into the staged file.
	tag(ctx context.Context, staged stagedFile, song applemusic.Song, lyrics string, cover []byte) error
	// commit moves the staged file to outPath and returns its final size; see
	// finalizeToOutput for why this is the only way a track reaches outPath.
	commit(staged stagedFile, outPath string) (int64, error)
}

// backend picks the media backend for this Downloader's effective config.
func (d *Downloader) backend() mediaBackend {
	if d.cfg.Simulate.Enabled {
		return simulatedMedia{cfg: d.cfg}
	}
	return realMedia{d: d}
}

type fetchRequest struct {
	codec string
	uri   string
	// jobID and outPath own and key the low memory mode checkpoint.
	jobID   string
	outPath string
	// estimatedBytes is the size the manifest's bandwidth predicts for the
	// track. Only the simulated transfer uses it.
	estimatedBytes int64
}

// encryptedMedia is one codec attempt's still-encrypted track, held by the
// fetch phase for the decrypt phase so a decrypt retry never re-downloads.
type encryptedMedia struct {
	// raw holds the whole encrypted track in high memory mode and for AAC-LC.
	// One immutable copy allows decrypt retries without another CDN request.
	raw []byte
	// rawPath is the low memory mode checkpoint: the encrypted bytes stay on
	// disk so a whole Hi-Res track is never pinned in memory across the
	// decrypt phase, and a decrypt retry re-reads them without re-fetching.
	rawPath string
	// size is the byte count of a simulated transfer, which has no bytes.
	size int64
	// releaseInFlight owns the global media-backpressure permit acquired just
	// before the transfer. It stays held while the media waits for decryption
	// and is idempotent so cleanup paths can safely converge.
	releaseInFlight func()
}

// open returns a reader over the encrypted bytes and their size.
func (m encryptedMedia) open() (io.Reader, int64, func(), error) {
	if m.rawPath != "" {
		f, err := os.Open(m.rawPath)
		if err != nil {
			return nil, 0, nil, err
		}
		var size int64
		if fi, statErr := f.Stat(); statErr == nil {
			size = fi.Size()
		}
		return f, size, func() { _ = f.Close() }, nil
	}
	if m.raw != nil {
		return bytes.NewReader(m.raw), int64(len(m.raw)), func() {}, nil
	}
	return bytes.NewReader(nil), m.size, func() {}, nil
}

func (m encryptedMedia) release() {
	if m.releaseInFlight != nil {
		m.releaseInFlight()
	}
}

// estimatedMediaBytes is the size a stream of bandwidth bits per second has
// over the track's duration, with 256 kbps and 3.5 minutes standing in for
// unknown values.
func estimatedMediaBytes(durationMS, bandwidth int) int64 {
	seconds := float64(durationMS) / 1000
	if seconds <= 0 {
		seconds = 210
	}
	if bandwidth <= 0 {
		bandwidth = aacLCBandwidth
	}
	return int64(float64(bandwidth) / 8 * seconds)
}

// stagedFile is the flattened track on temp storage between decrypt and
// commit.
type stagedFile struct {
	path string
	// size is the simulated file size; the real backend stats the result.
	size int64
}

type decryptRequest struct {
	codec  string
	songID string
	input  io.Reader
	// size is the byte count of input, or 0 when unknown.
	size   int64
	staged stagedFile
	// keys are the enhanced codec's key URIs; aacLC carries AAC-LC's
	// Widevine key.
	keys  []string
	aacLC aacLCMedia
}

// decryptHooks let the pipeline publish the decrypt stage's transitions at
// the moments they really happen.
type decryptHooks struct {
	// ready runs once the key material is in hand: the decrypt session is open
	// (enhanced) or the license is parsed (AAC-LC).
	ready func()
	// progress reports the fraction of the encrypted input consumed. AAC-LC
	// decrypts in one opaque pass and never calls it.
	progress func(float64)
	// afterInput runs once the decrypted stream has been handed to the remuxer
	// in full, while the remuxer is still writing its trailer.
	afterInput func()
}

// realMedia is the production backend.
type realMedia struct{ d *Downloader }

func (m realMedia) cfg() config.Config { return m.d.cfg }

func (m realMedia) coverArt(ctx context.Context, urls []string, format, size string) ([]byte, error) {
	return m.d.fetchCover(ctx, urls, format, size)
}

func (m realMedia) lyrics(ctx context.Context, songID string, opts wrapper.LyricsRequestOptions) (string, error) {
	return m.d.wrapper.Lyrics(ctx, songID, opts)
}

func (m realMedia) requireTools(ctx context.Context) error {
	return m.d.tools.Require(ctx)
}

func (m realMedia) resolveAACLC(ctx context.Context, songID string) (aacLCMedia, error) {
	playlistURL, err := m.d.wrapper.WebPlayback(ctx, songID)
	if err != nil {
		return aacLCMedia{}, fmt.Errorf("request AAC-LC WebPlayback: %w", err)
	}
	media, err := extractAACLCMedia(ctx, m.d.http, playlistURL, m.d.requestGate)
	if err != nil {
		return aacLCMedia{}, fmt.Errorf("parse AAC-LC media playlist: %w", err)
	}
	return media, nil
}

func (m realMedia) fetch(ctx context.Context, req fetchRequest, progress transferProgress) (encryptedMedia, error) {
	switch {
	case req.codec == "aac-lc":
		raw, err := downloadBytes(ctx, m.d.http, req.uri, progress)
		return encryptedMedia{raw: raw}, err
	case m.cfg().Download.MemoryMode == config.MemoryModeHigh:
		// High memory mode keeps exactly one whole-track encrypted copy. The
		// fragment decrypt/remux stage remains streaming, so parsed, plaintext
		// and remuxed whole-track copies never accumulate beside it. A
		// validated Range reconnect may repair one interrupted response without
		// creating a persistent checkpoint.
		raw, err := downloadBytesWithRangeResume(ctx, m.d.http, req.uri, progress)
		return encryptedMedia{raw: raw}, err
	default:
		// Low memory mode streams the response to a resumable checkpoint and
		// reads it back fragment by fragment during decrypt.
		path, err := downloadToFile(ctx, m.d.http, req.uri, m.cfg().Download.TempDir, req.jobID, req.outPath, progress)
		return encryptedMedia{rawPath: path}, err
	}
}

func (m realMedia) discardCheckpoint(jobID, outPath string) {
	cleanupResumeForKey(m.cfg().Download.TempDir, jobID, outPath)
}

func (m realMedia) discardOutput(outPath string) { cleanupFailedOutput(outPath) }

func (m realMedia) stage(encryptedMedia) (stagedFile, func(), error) {
	f, err := os.CreateTemp(m.cfg().Download.TempDir, "flat-*.m4a")
	if err != nil {
		return stagedFile{}, nil, err
	}
	path := f.Name()
	_ = f.Close()
	return stagedFile{path: path}, func() { _ = os.Remove(path) }, nil
}

func (m realMedia) decrypt(ctx context.Context, req decryptRequest, hooks decryptHooks) error {
	if req.codec == "aac-lc" {
		return m.decryptAACLC(ctx, req, hooks)
	}
	session, err := m.d.wrapper.NewDecryptSession(ctx, req.songID)
	if err != nil {
		return fmt.Errorf("open decrypt session: %w", err)
	}
	hooks.ready()
	onProgress := func(consumed uint64) {
		if req.size > 0 && hooks.progress != nil {
			hooks.progress(float64(consumed) / float64(req.size))
		}
	}
	// mp4ff parses one fragment, the wrapper returns one plaintext fragment,
	// and DataParts writes those samples directly into ffmpeg's stdin without
	// another concatenation or a whole-track intermediate.
	streamErr := m.d.mp4.streamDecryptToFlatFile(ctx, req.input, req.staged.path, req.keys, session.DecryptFragment, onProgress, hooks.afterInput)
	closeErr := session.Close()
	if streamErr != nil {
		return fmt.Errorf("decrypt and flatten media: %w", streamErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close decrypt session: %w", closeErr)
	}
	return nil
}

func (m realMedia) decryptAACLC(ctx context.Context, req decryptRequest, hooks decryptHooks) error {
	challenge, parseLicense, err := newWidevineSession(req.aacLC.KID)
	if err != nil {
		return err
	}
	license, err := m.d.wrapper.License(ctx, req.songID, base64.StdEncoding.EncodeToString(challenge), req.aacLC.KeyURI)
	if err != nil {
		return fmt.Errorf("request AAC-LC license: %w", err)
	}
	keys, err := parseWidevineLicenseKeys(license, parseLicense)
	if err != nil {
		return err
	}
	hooks.ready()
	if err := m.d.mp4.decryptWidevineToFlatFile(ctx, req.input, keys, req.staged.path, hooks.afterInput); err != nil {
		return fmt.Errorf("normalize AAC-LC container: %w", err)
	}
	return nil
}

func (m realMedia) verify(ctx context.Context, staged stagedFile) error {
	return m.d.mp4.checkIntegrityFile(ctx, staged.path)
}

func (m realMedia) sampleRate(staged stagedFile) (int, error) {
	return readSampleRate(staged.path)
}

// repairALAC reads the file back, patches malformed ALAC packet terminators
// and writes the repaired bytes in place, keeping the whole-file read off the
// common (already-valid) path.
func (m realMedia) repairALAC(_ context.Context, staged stagedFile, patched func(int)) error {
	outBytes, err := os.ReadFile(staged.path)
	if err != nil {
		return fmt.Errorf("integrity check failed; read for alac repair: %w", err)
	}
	repaired, count, err := repairALACTerminators(outBytes)
	if err != nil {
		return fmt.Errorf("integrity check failed; alac repair failed: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("integrity check failed; alac repair found no malformed packets")
	}
	patched(count)
	if err := os.WriteFile(staged.path, repaired, 0o644); err != nil {
		return fmt.Errorf("integrity check failed; write repaired alac: %w", err)
	}
	return nil
}

func (m realMedia) mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

func (m realMedia) writeFile(path string, data []byte) error { return writeFileAtomic(path, data) }

func (m realMedia) tag(ctx context.Context, staged stagedFile, song applemusic.Song, lyrics string, cover []byte) error {
	return m.d.mp4.writeMetadata(ctx, staged.path, song, lyrics, cover)
}

func (m realMedia) commit(staged stagedFile, outPath string) (int64, error) {
	if err := finalizeToOutput(staged.path, outPath); err != nil {
		return 0, err
	}
	return outputFileSize(outPath), nil
}

// lyricsRequest builds the wrapper's lyrics options: the translation comes in
// the catalog language, and the romanization script stays wrapper-lite's
// default, which already picks the right one per lyrics language. wrapper-lite
// answers from its own account's storefront, whatever the job's.
func lyricsRequest(cfg config.Config, syllable bool) wrapper.LyricsRequestOptions {
	return wrapper.LyricsRequestOptions{Language: cfg.Catalog.Language, Syllable: syllable}
}
