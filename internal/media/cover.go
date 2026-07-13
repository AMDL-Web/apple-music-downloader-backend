package media

import (
	"container/list"
	"context"
	"fmt"
	"sync"

	"amdl/internal/applemusic"
)

type coverCacheKey struct {
	url    string
	format string
	size   string
}

type coverCacheResult struct {
	key      coverCacheKey
	done     chan struct{}
	data     []byte
	err      error
	complete bool
	element  *list.Element
}

type coverFetcher interface {
	FetchCover(context.Context, []string, string, string) ([]byte, error)
}

const defaultCoverCacheBytes int64 = 64 << 20

// coverCache is scoped to one download job. Successful artwork bytes remain
// immutable and are shared by embedded and standalone cover consumers;
// failures are removed so the normal retry envelope can make another request.
type coverCache struct {
	fetcher coverFetcher

	mu         sync.Mutex
	results    map[coverCacheKey]*coverCacheResult
	lru        list.List
	totalBytes int64
	maxBytes   int64
}

func newCoverCache(fetcher coverFetcher) *coverCache {
	return newCoverCacheWithLimit(fetcher, defaultCoverCacheBytes)
}

func newCoverCacheWithLimit(fetcher coverFetcher, maxBytes int64) *coverCache {
	return &coverCache{fetcher: fetcher, results: make(map[coverCacheKey]*coverCacheResult), maxBytes: maxBytes}
}

func (d *Downloader) fetchCover(ctx context.Context, artworkURLs []string, format, size string) ([]byte, error) {
	if d.covers == nil {
		return d.catalog.FetchCover(ctx, artworkURLs, format, size)
	}
	return d.covers.fetch(ctx, artworkURLs, format, size)
}

func (c *coverCache) fetch(ctx context.Context, artworkURLs []string, format, size string) ([]byte, error) {
	var lastErr error
	tried := false
	for _, artworkURL := range artworkURLs {
		if artworkURL == "" {
			continue
		}
		tried = true
		data, err := c.fetchOne(ctx, coverCacheKey{url: artworkURL, format: format, size: size})
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	if !tried {
		return nil, fmt.Errorf("no artwork url available")
	}
	return nil, lastErr
}

func (c *coverCache) fetchOne(ctx context.Context, key coverCacheKey) ([]byte, error) {
	c.mu.Lock()
	if pending, ok := c.results[key]; ok {
		if pending.complete && pending.element != nil {
			c.lru.MoveToBack(pending.element)
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.done:
			return pending.data, pending.err
		}
	}
	pending := &coverCacheResult{key: key, done: make(chan struct{})}
	c.results[key] = pending
	c.mu.Unlock()

	data, err := c.fetcher.FetchCover(ctx, []string{key.url}, key.format, key.size)
	c.mu.Lock()
	pending.data = data
	pending.err = err
	pending.complete = true
	if err != nil {
		if c.results[key] == pending {
			delete(c.results, key)
		}
	} else {
		pending.element = c.lru.PushBack(pending)
		c.totalBytes += int64(len(data))
		c.evict()
	}
	close(pending.done)
	c.mu.Unlock()
	return data, err
}

func (c *coverCache) evict() {
	for c.totalBytes > c.maxBytes {
		oldest := c.lru.Front()
		if oldest == nil {
			return
		}
		result := oldest.Value.(*coverCacheResult)
		c.lru.Remove(oldest)
		result.element = nil
		c.totalBytes -= int64(len(result.data))
		if c.results[result.key] == result {
			delete(c.results, result.key)
		}
	}
}

// trackCoverURLs returns artwork URL candidates for a track.
// Playlist downloads try the song cover first, then the album cover.
func trackCoverURLs(song applemusic.Song, collectionType applemusic.URLType) []string {
	if collectionType == applemusic.TypePlaylist {
		urls := make([]string, 0, 2)
		if song.ArtworkURL != "" {
			urls = append(urls, song.ArtworkURL)
		}
		if song.AlbumArtworkURL != "" && song.AlbumArtworkURL != song.ArtworkURL {
			urls = append(urls, song.AlbumArtworkURL)
		}
		return urls
	}
	if song.ArtworkURL != "" {
		return []string{song.ArtworkURL}
	}
	if song.AlbumArtworkURL != "" {
		return []string{song.AlbumArtworkURL}
	}
	return nil
}
