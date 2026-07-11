package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"amdl/internal/applemusic"
)

var standaloneCoverMu sync.Mutex

func (d *Downloader) savePlaylistCover(ctx context.Context, artworkURL, playlistDir string) error {
	if artworkURL == "" {
		return nil
	}
	ext, err := standaloneCoverExt(d.cfg.Download.CoverFormat)
	if err != nil {
		return err
	}
	path := filepath.Join(playlistDir, "cover."+ext)
	if err := d.ensureStandaloneCover(ctx, path, func(context.Context) (string, error) {
		return artworkURL, nil
	}); err != nil {
		return fmt.Errorf("save playlist cover: %w", err)
	}
	return nil
}

func (d *Downloader) saveStandaloneCovers(ctx context.Context, song applemusic.Song, collectionType applemusic.URLType, storefront, albumDir, artistDir string) error {
	if collectionType == applemusic.TypePlaylist {
		return nil
	}

	ext, err := standaloneCoverExt(d.cfg.Download.CoverFormat)
	if err != nil {
		return err
	}
	var saveErrors []error
	if d.cfg.Download.SaveAlbumCover {
		artworkURL := firstNonEmpty(song.AlbumArtworkURL, song.ArtworkURL)
		if err := d.ensureStandaloneCover(ctx, filepath.Join(albumDir, "cover."+ext), func(context.Context) (string, error) {
			return artworkURL, nil
		}); err != nil {
			saveErrors = append(saveErrors, fmt.Errorf("save album cover: %w", err))
		}
	}
	if d.cfg.Download.SaveArtistCover && artistDir != "" {
		if err := d.ensureStandaloneCover(ctx, filepath.Join(artistDir, "artist."+ext), func(ctx context.Context) (string, error) {
			artistID := song.ArtistID
			artworkURL := song.ArtistArtworkURL
			if collectionType == applemusic.TypeAlbum {
				artistID = firstNonEmpty(song.AlbumArtistID, artistID)
				artworkURL = firstNonEmpty(song.AlbumArtistArtworkURL, artworkURL)
			}
			if artworkURL != "" {
				return artworkURL, nil
			}
			if artistID == "" {
				return "", nil
			}
			artist, err := d.catalog.Artist(ctx, storefront, artistID)
			return artist.ArtworkURL, err
		}); err != nil {
			saveErrors = append(saveErrors, fmt.Errorf("save artist cover: %w", err))
		}
	}
	return errors.Join(saveErrors...)
}

func (d *Downloader) ensureStandaloneCover(ctx context.Context, path string, resolveURL func(context.Context) (string, error)) error {
	standaloneCoverMu.Lock()
	defer standaloneCoverMu.Unlock()

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	artworkURL, err := resolveURL(ctx)
	if err != nil {
		return err
	}
	if artworkURL == "" {
		return nil
	}
	data, _, err := retryValue(ctx, d.cfg.Download.MaxAttempts, retryBackoff, func(int) ([]byte, error) {
		return d.catalog.FetchCover(ctx, []string{artworkURL}, d.cfg.Download.CoverFormat, d.cfg.Download.CoverSize)
	}, nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func standaloneCoverExt(format string) (string, error) {
	switch format {
	case "jpg", "jpeg":
		return "jpg", nil
	case "png":
		return "png", nil
	default:
		return "", fmt.Errorf("unsupported cover format %q", format)
	}
}
