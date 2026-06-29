package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/config"
	"amdl/backend/internal/domain"
	"amdl/backend/internal/jobs"
	"amdl/backend/internal/storage"
	"amdl/backend/internal/wrapper"
)

type Downloader struct {
	cfg     config.Config
	catalog *applemusic.CatalogClient
	wrapper *wrapper.Client
	tools   *ToolChecker
	http    *http.Client
	mp4     *MP4Processor
	logger  *slog.Logger
}

func NewDownloader(cfg config.Config, catalog *applemusic.CatalogClient, wrapperClient *wrapper.Client, tools *ToolChecker, logger *slog.Logger) *Downloader {
	return &Downloader{cfg: cfg, catalog: catalog, wrapper: wrapperClient, tools: tools, http: newHTTPClient(), mp4: newMP4Processor(cfg), logger: logger}
}

func (d *Downloader) ProcessJob(ctx context.Context, job domain.Job, reporter jobs.Reporter) error {
	parsed, err := applemusic.Parse(job.Input)
	if err != nil {
		return err
	}
	if parsed.Type == applemusic.TypeArtist || parsed.Type == applemusic.TypeVideo {
		return fmt.Errorf("%s download is not implemented in core phase", parsed.Type)
	}
	job.Type = string(parsed.Type)
	job.Storefront = parsed.Storefront
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}
	if err := reporter.Event(ctx, domain.Event{JobID: job.ID, Type: "resolved_input", Message: string(parsed.Type)}); err != nil {
		return err
	}

	tracks, err := d.resolveTracks(ctx, parsed)
	if err != nil {
		return err
	}
	job.TotalItems = len(tracks)
	if err := reporter.SetJob(ctx, job); err != nil {
		return err
	}
	if len(tracks) == 0 {
		return fmt.Errorf("no downloadable songs found")
	}

	items := make([]domain.JobItem, len(tracks))
	for i, track := range tracks {
		items[i] = domain.JobItem{
			ID: storage.NewID("item"), JobID: job.ID, AdamID: track.ID, Kind: "song", Index: i + 1,
			Title: track.Name, Artist: track.ArtistName, Album: track.AlbumName, Status: domain.ItemQueued,
		}
		if err := reporter.AddItem(ctx, items[i]); err != nil {
			return err
		}
	}

	parallel := d.cfg.Download.MaxParallelTracks
	if parallel <= 0 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := range tracks {
		i := i
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := d.processTrack(ctx, job, items[i], tracks[i], parsed.Storefront, i+1, reporter); err != nil {
				d.logger.Error("track failed", "adam_id", tracks[i].ID, "error", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func (d *Downloader) resolveTracks(ctx context.Context, parsed applemusic.ParsedURL) ([]applemusic.Song, error) {
	switch parsed.Type {
	case applemusic.TypeSong:
		song, err := d.catalog.Song(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return nil, err
		}
		return []applemusic.Song{song}, nil
	case applemusic.TypeAlbum:
		album, err := d.catalog.Album(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return nil, err
		}
		return album.Tracks, nil
	case applemusic.TypePlaylist:
		playlist, err := d.catalog.Playlist(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return nil, err
		}
		return playlist.Tracks, nil
	default:
		return nil, fmt.Errorf("unsupported input type %s", parsed.Type)
	}
}

func (d *Downloader) processTrack(ctx context.Context, job domain.Job, item domain.JobItem, initial applemusic.Song, storefront string, playlistIndex int, reporter jobs.Reporter) error {
	set := func(status domain.ItemStatus, progress float64, message string) {
		item.Status = status
		item.Progress = progress
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_progress", Phase: string(status), Message: message})
	}
	set(domain.ItemResolving, 0.02, "resolving metadata")

	song, err := d.catalog.Song(ctx, storefront, initial.ID)
	if err != nil {
		item.Status = domain.ItemFailed
		item.Error = err.Error()
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_failed", Message: err.Error()})
		return err
	}
	item.Title = song.Name
	item.Artist = song.ArtistName
	item.Album = song.AlbumName
	_ = reporter.UpdateItem(ctx, item)

	outPath := outputPath(d.cfg, song, playlistIndex)
	if _, err := os.Stat(outPath); err == nil {
		item.Status = domain.ItemSkipped
		item.Progress = 1
		item.OutputPath = outPath
		_ = reporter.UpdateItem(ctx, item)
		_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_skipped", Message: "already exists"})
		return nil
	}

	var cover []byte
	if d.cfg.Download.EmbedCover {
		cover, _ = d.catalog.Cover(ctx, song.ArtworkURL, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
	}
	lyrics := ""
	if d.cfg.Download.EmbedLyrics || d.cfg.Download.SaveLyricsFile {
		if raw, err := d.wrapper.Lyrics(ctx, song.ID, storefront, d.cfg.Catalog.Language); err == nil {
			lyrics = convertLyrics(raw, d.cfg.Download.LyricsFormat)
		}
	}

	if err := d.tools.Require(ctx); err != nil {
		return d.failItem(ctx, reporter, job, item, err)
	}
	var lastErr error
	for _, codec := range d.cfg.Download.QualityPriority {
		item.Codec = codec
		if err := d.downloadCodec(ctx, job, &item, song, storefront, codec, lyrics, cover, outPath, reporter, set); err == nil {
			return nil
		} else {
			lastErr = err
			_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_failed", Phase: codec, Message: err.Error()})
			if !d.cfg.Download.CodecAlternative {
				break
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no configured codec succeeded")
	}
	return d.failItem(ctx, reporter, job, item, lastErr)
}

func (d *Downloader) downloadCodec(ctx context.Context, job domain.Job, item *domain.JobItem, song applemusic.Song, storefront, codec, lyrics string, cover []byte, outPath string, reporter jobs.Reporter, set func(domain.ItemStatus, float64, string)) error {
	set(domain.ItemDownloading, 0.12, "selecting manifest")
	master := song.EnhancedHLS
	if codec == "alac" {
		m3u8, err := d.wrapper.M3U8(ctx, song.ID)
		if err != nil {
			return err
		}
		master = m3u8
	}
	if master == "" {
		return fmt.Errorf("no enhanced hls manifest")
	}
	info, err := extractMedia(ctx, d.http, master, codec, d.cfg.Download.ALACMaxSampleRate, d.cfg.Download.ALACMaxBitDepth)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"codec_id": info.CodecID, "bit_depth": info.BitDepth, "sample_rate": info.SampleRate})
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "codec_selected", Phase: codec, Payload: string(payload)})

	set(domain.ItemDownloading, 0.25, "downloading encrypted media")
	raw, err := downloadBytes(ctx, d.http, info.MediaURI)
	if err != nil {
		return err
	}
	set(domain.ItemDecrypting, 0.45, "extracting samples")
	extracted, err := d.mp4.extractSong(ctx, raw, codec)
	if err != nil {
		return err
	}
	samples := make([]wrapper.DecryptSample, 0, len(extracted.Samples))
	for i, sample := range extracted.Samples {
		keyIndex := sample.DescIndex
		if keyIndex < 0 || keyIndex >= len(info.Keys) {
			keyIndex = 0
		}
		samples = append(samples, wrapper.DecryptSample{Key: info.Keys[keyIndex], Index: i, Data: sample.Data})
	}
	decryptedSamples, err := d.wrapper.Decrypt(ctx, song.ID, samples)
	if err != nil {
		return err
	}
	var mediaBytes []byte
	for _, sample := range decryptedSamples {
		mediaBytes = append(mediaBytes, sample...)
	}
	set(domain.ItemRemuxing, 0.7, "remuxing")
	outBytes, err := d.mp4.encapsulate(ctx, extracted, mediaBytes)
	if err != nil {
		return err
	}
	if codec != "ec3" && codec != "ac3" {
		if fixed, err := d.mp4.fixEncapsulate(ctx, outBytes); err == nil {
			outBytes = fixed
		}
	}
	if codec == "aac" || codec == "aac-downmix" || codec == "aac-binaural" {
		if fixed, err := d.mp4.fixESDS(ctx, raw, outBytes); err == nil {
			outBytes = fixed
		}
	}
	if d.cfg.Download.CheckIntegrity && !d.mp4.checkIntegrity(ctx, outBytes) {
		return fmt.Errorf("integrity check failed")
	}
	set(domain.ItemSaving, 0.86, "saving")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(outPath, outBytes, 0o644); err != nil {
		return err
	}
	if d.cfg.Download.SaveLyricsFile && lyrics != "" {
		ext := ".lrc"
		if d.cfg.Download.LyricsFormat == "ttml" {
			ext = ".ttml"
		}
		_ = os.WriteFile(stringsTrimSuffix(outPath, ".m4a")+ext, []byte(lyrics), 0o644)
	}
	set(domain.ItemTagging, 0.93, "writing metadata")
	if err := d.mp4.writeMetadata(ctx, outPath, song, lyrics, cover, extracted); err != nil {
		return err
	}
	item.Status = domain.ItemCompleted
	item.Progress = 1
	item.OutputPath = outPath
	item.Codec = codec
	_ = reporter.UpdateItem(ctx, *item)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_completed", Message: outPath})
	return nil
}

func (d *Downloader) failItem(ctx context.Context, reporter jobs.Reporter, job domain.Job, item domain.JobItem, err error) error {
	item.Status = domain.ItemFailed
	item.Error = err.Error()
	_ = reporter.UpdateItem(ctx, item)
	_ = reporter.Event(ctx, domain.Event{JobID: job.ID, ItemID: item.ID, Type: "item_failed", Message: err.Error()})
	return err
}

func stringsTrimSuffix(v, suffix string) string {
	if len(v) >= len(suffix) && v[len(v)-len(suffix):] == suffix {
		return v[:len(v)-len(suffix)]
	}
	return v
}
