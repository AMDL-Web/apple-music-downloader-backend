package media

import (
	"testing"

	"amdl/backend/internal/applemusic"
)

func TestTrackCoverURLsPlaylistPrefersSongThenAlbum(t *testing.T) {
	song := applemusic.Song{
		ArtworkURL:      "https://example.test/song.jpg",
		AlbumArtworkURL: "https://example.test/album.jpg",
	}
	got := trackCoverURLs(song, applemusic.TypePlaylist)
	if len(got) != 2 || got[0] != song.ArtworkURL || got[1] != song.AlbumArtworkURL {
		t.Fatalf("trackCoverURLs() = %#v", got)
	}
}

func TestTrackCoverURLsPlaylistDedupesSameAlbumArt(t *testing.T) {
	url := "https://example.test/same.jpg"
	song := applemusic.Song{ArtworkURL: url, AlbumArtworkURL: url}
	got := trackCoverURLs(song, applemusic.TypePlaylist)
	if len(got) != 1 || got[0] != url {
		t.Fatalf("trackCoverURLs() = %#v", got)
	}
}

func TestTrackCoverURLsAlbumUsesSongOnly(t *testing.T) {
	song := applemusic.Song{
		ArtworkURL:      "https://example.test/song.jpg",
		AlbumArtworkURL: "https://example.test/album.jpg",
	}
	got := trackCoverURLs(song, applemusic.TypeAlbum)
	if len(got) != 1 || got[0] != song.ArtworkURL {
		t.Fatalf("trackCoverURLs() = %#v", got)
	}
}
