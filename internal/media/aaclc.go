package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"amdl/internal/applemusic"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/limits"

	widevine "github.com/iyear/gowidevine"
	wvpb "github.com/iyear/gowidevine/widevinepb"
	"google.golang.org/protobuf/proto"
)

type aacLCMedia struct {
	MediaURI string
	KeyURI   string
	KID      string
}

func extractAACLCMedia(ctx context.Context, client *http.Client, playlistURL string, gates ...*limits.RequestGate) (aacLCMedia, error) {
	body, err := downloadText(ctx, client, playlistURL, gates...)
	if err != nil {
		return aacLCMedia{}, err
	}
	var media aacLCMedia
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			keyURI := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))["URI"]
			if keyURI != "" {
				media.KeyURI = keyURI
				parts := strings.SplitN(keyURI, ",", 2)
				if len(parts) == 2 {
					media.KID = parts[1]
				}
			}
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			mapURI := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))["URI"]
			if mapURI != "" {
				media.MediaURI = absURL(playlistURL, mapURI)
			}
		}
	}
	if media.MediaURI == "" {
		return aacLCMedia{}, fmt.Errorf("aac-lc playlist has no EXT-X-MAP media URI")
	}
	if media.KeyURI == "" || media.KID == "" {
		return aacLCMedia{}, fmt.Errorf("aac-lc playlist has no Widevine key URI")
	}
	if _, err := base64.StdEncoding.DecodeString(media.KID); err != nil {
		return aacLCMedia{}, fmt.Errorf("decode aac-lc KID: %w", err)
	}
	return media, nil
}

func newWidevineSession(kidBase64 string) ([]byte, func([]byte) ([]*widevine.Key, error), error) {
	deviceRaw, err := base64.StdEncoding.DecodeString(widevineDeviceBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("decode embedded Widevine device: %w", err)
	}
	device, err := widevine.NewDevice(widevine.FromWVD(bytes.NewReader(deviceRaw)))
	if err != nil {
		return nil, nil, fmt.Errorf("load Widevine device: %w", err)
	}
	pssh, err := makeWidevinePSSH(kidBase64)
	if err != nil {
		return nil, nil, err
	}
	challenge, parseLicense, err := widevine.NewCDM(device).GetLicenseChallenge(pssh, wvpb.LicenseType_AUTOMATIC, false)
	if err != nil {
		return nil, nil, fmt.Errorf("generate Widevine license challenge: %w", err)
	}
	return challenge, parseLicense, nil
}

func makeWidevinePSSH(kidBase64 string) (*widevine.PSSH, error) {
	kid, err := base64.StdEncoding.DecodeString(kidBase64)
	if err != nil {
		return nil, fmt.Errorf("decode Widevine KID: %w", err)
	}
	data, err := proto.Marshal(&wvpb.WidevinePsshData{KeyIds: [][]byte{kid}})
	if err != nil {
		return nil, fmt.Errorf("marshal Widevine PSSH data: %w", err)
	}
	systemID := []byte{0xed, 0xef, 0x8b, 0xa9, 0x79, 0xd6, 0x4a, 0xce, 0xa3, 0xc8, 0x27, 0xdc, 0xd5, 0x1d, 0x21, 0xed}
	var box bytes.Buffer
	_ = binary.Write(&box, binary.BigEndian, uint32(32+len(data)))
	box.WriteString("pssh")
	_ = binary.Write(&box, binary.BigEndian, uint32(0))
	box.Write(systemID)
	_ = binary.Write(&box, binary.BigEndian, uint32(len(data)))
	box.Write(data)
	pssh, err := widevine.NewPSSH(box.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parse generated Widevine PSSH: %w", err)
	}
	return pssh, nil
}

func decodeWidevineLicense(value string) ([]byte, error) {
	license, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode Widevine license: %w", err)
	}
	return license, nil
}

func parseWidevineLicenseKeys(licenseValue string, parseLicense func([]byte) ([]*widevine.Key, error)) ([]*widevine.Key, error) {
	license, err := decodeWidevineLicense(licenseValue)
	if err != nil {
		return nil, err
	}
	keys, err := parseLicense(license)
	if err != nil {
		return nil, fmt.Errorf("parse Widevine license: %w", err)
	}
	return keys, nil
}

// decryptWidevineMP4ToWriter writes gowidevine's decrypted fragmented MP4
// directly to its consumer, which in production is ffmpeg's stdin, so no
// whole-track plaintext []byte or intermediate file exists beside the
// still-encrypted retry buffer. Gowidevine v0.1.3 still decodes the input into
// an in-memory MP4 box tree before it writes it.
func decryptWidevineMP4ToWriter(raw io.Reader, keys []*widevine.Key, dst io.Writer) error {
	if err := widevine.DecryptMP4Auto(raw, keys, dst); err != nil {
		return fmt.Errorf("decrypt AAC-LC MP4: %w", err)
	}
	return nil
}

func (p *MP4Processor) decryptWidevineToFlatFile(
	ctx context.Context,
	raw io.Reader,
	keys []*widevine.Key,
	outPath string,
	afterInput func(),
) error {
	return p.produceToFlatFile(ctx, outPath, func(dst io.Writer) error {
		return decryptWidevineMP4ToWriter(raw, keys, dst)
	}, afterInput)
}

// aacLCBandwidth is the bitrate of every AAC-LC WebPlayback asset, whose media
// playlist declares none: it sizes the simulated transfer and is the item's
// reported bitrate.
const aacLCBandwidth = 256000

// aacLCDownload is the AAC-LC fetch phase's result: the key to decrypt with and
// the still-encrypted track.
type aacLCDownload struct {
	media aacLCMedia
	encryptedMedia
}

// fetchAACLCMedia resolves the AAC-LC media playlist and downloads the
// still-encrypted stream into memory. Kept separate from decryptAACLC so a
// decrypt-phase retry can reuse these bytes instead of re-hitting the CDN. The
// in-flight permit is returned held, in releaseInFlight.
func (d *Downloader) fetchAACLCMedia(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, reporter jobs.Reporter, set publishStage) (aacLCDownload, error) {
	backend := d.backend()
	set(domain.ItemResolving, "requesting AAC-LC WebPlayback asset", nil)
	media, err := backend.resolveAACLC(ctx, song.ID)
	if err != nil {
		return aacLCDownload{}, err
	}
	d.setItemQuality(ctx, reporter, item, 0, 0, aacLCBandwidth)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: domain.EventCodecSelected, Phase: "aac-lc", Payload: domain.MarshalEventPayload(*item, map[string]any{
		"codec_id": "aac-lc", "bitrate": aacLCBandwidth, "attempt": item.Attempt, "max_attempts": item.MaxAttempts,
	})})

	set(domain.ItemWaitingDownload, "waiting for download slot", nil)
	releaseInFlight, err := d.inFlightLimit.Acquire(ctx)
	if err != nil {
		return aacLCDownload{}, err
	}
	releaseDownload, err := d.downloadLimit.Acquire(ctx)
	if err != nil {
		releaseInFlight()
		return aacLCDownload{}, err
	}
	set(domain.ItemDownloading, "downloading encrypted AAC-LC media", startDownload)
	encrypted, err := func() (encryptedMedia, error) {
		defer releaseDownload()
		return backend.fetch(ctx, fetchRequest{
			codec: "aac-lc", uri: media.MediaURI,
			estimatedBytes: estimatedMediaBytes(song.DurationInMillis, aacLCBandwidth),
		}, func(done, total int64) {
			if total > 0 {
				p := clampFrac(float64(done) / float64(total))
				set(domain.ItemDownloading, fmt.Sprintf("downloading %.0f%%", p*100), downloadProgress(done, p))
			}
		})
	}()
	if err != nil {
		releaseInFlight()
		return aacLCDownload{}, fmt.Errorf("download encrypted AAC-LC media: %w", err)
	}
	encrypted.releaseInFlight = releaseInFlight
	return aacLCDownload{media: media, encryptedMedia: encrypted}, nil
}

// decryptAACLC takes the already-downloaded encrypted bytes from
// fetchAACLCMedia, acquires the license, decrypts straight into ffmpeg and
// finalizes the file. On retry this re-runs without downloading media again.
// Gowidevine still retains the decoded media boxes until it has emitted the
// track.
func (d *Downloader) decryptAACLC(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, download aacLCDownload, lyrics trackLyrics, cover []byte, outPath string, reporter jobs.Reporter, set publishStage) error {
	backend := d.backend()
	// Flatten, verify, and tag on temp storage, then move the finished file to
	// its final path, same as downloadEnhancedCodec (see finalizeToOutput).
	staged, cleanupStaged, err := backend.stage(download.encryptedMedia)
	if err != nil {
		return fmt.Errorf("create flatten output: %w", err)
	}
	defer cleanupStaged()
	input, inputSize, closeInput, err := download.open()
	if err != nil {
		return fmt.Errorf("open downloaded media: %w", err)
	}
	defer closeInput()

	set(domain.ItemWaitingDecrypt, "waiting for decrypt slot", nil)
	releaseDecrypt, err := d.decryptLimit.Acquire(ctx)
	if err != nil {
		return err
	}
	set(domain.ItemDecrypting, "acquiring Widevine license", startDecrypt)
	// afterInput is the successful producer boundary, while this defer covers
	// license, producer, stdin, ffmpeg-start, and cancellation failures. Keep a
	// local once even though Semaphore's release is itself idempotent: ownership
	// is explicit here and future limiter implementations cannot over-release.
	var releaseDecryptOnce sync.Once
	releaseDecryptPermit := func() { releaseDecryptOnce.Do(releaseDecrypt) }
	defer releaseDecryptPermit()
	err = backend.decrypt(ctx, decryptRequest{
		codec: "aac-lc", songID: song.ID, input: input, size: inputSize, staged: staged, aacLC: download.media,
	}, decryptHooks{
		// AAC-LC decrypts in one gowidevine pass with no intermediate byte count
		// to report, so the decrypt meter jumps straight from 0 to 1 at the
		// remux boundary below rather than sweeping.
		ready: func() { set(domain.ItemDecrypting, "decrypting AAC-LC", nil) },
		afterInput: func() {
			// Decryption has consumed the immutable retry buffer: release both
			// permits before the container's final flush and publish remuxing
			// until ffmpeg exits.
			releaseDecryptPermit()
			download.release()
			set(domain.ItemRemuxing, "remuxing AAC-LC", decryptFrac(1))
		},
	})
	if err != nil {
		return err
	}
	set(domain.ItemRemuxing, "AAC-LC remuxed", markRemuxed)

	// The ffmpeg copy pass also normalises the M4A brand and creates the
	// moov.udta.meta.ilst structure that go-mp4tag writes into. The decrypted
	// WebPlayback asset has no udta box, so tagging would otherwise fail.
	return d.finalizeTrackOutput(ctx, backend, trackOutput{
		job: job, item: item, reporter: reporter, song: song, codec: "aac-lc",
		lyrics: lyrics, cover: cover, staged: staged, outPath: outPath,
		labels: aacLCOutputLabels,
	}, set)
}
