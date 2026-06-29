package media

import (
	"path/filepath"
	"testing"

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/config"
)

func TestOutputPathUsesAlbumFolderArtistWithoutChangingTrackMetadata(t *testing.T) {
	cfg := config.Config{}
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.ArtistFolderFormat = "{ArtistName}"
	cfg.Download.AlbumFolderFormat = "{AlbumName}"
	cfg.Download.SongFileFormat = "{TrackNumber:02d}. {SongName}"

	song := applemusic.Song{
		ArtistName:  "Guest Artist",
		AlbumArtist: "Primary Artist",
		AlbumName:   "Shared Album",
		Name:        "Guest Track",
		TrackNumber: 2,
	}

	got := outputPath(cfg, song, 2, song.AlbumArtist)
	want := filepath.Join("downloads", "Primary Artist", "Shared Album", "02. Guest Track.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
	if song.ArtistName != "Guest Artist" {
		t.Fatalf("track artist was modified: %q", song.ArtistName)
	}
}

func TestOutputPathKeepsTrackArtistWhenNoAlbumFolderArtist(t *testing.T) {
	cfg := config.Config{}
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.ArtistFolderFormat = "{ArtistName}"
	cfg.Download.AlbumFolderFormat = "{AlbumName}"
	cfg.Download.SongFileFormat = "{SongName}"

	song := applemusic.Song{ArtistName: "Track Artist", AlbumName: "Album", Name: "Song"}
	got := outputPath(cfg, song, 1, "")
	want := filepath.Join("downloads", "Track Artist", "Album", "Song.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
}

func TestCollectionFolderArtistOnlyGroupsAlbums(t *testing.T) {
	tracks := []applemusic.Song{
		{ArtistName: "First Track Artist", AlbumArtist: "Album Artist"},
		{ArtistName: "Second Track Artist", AlbumArtist: "Album Artist"},
	}
	if got := collectionFolderArtist(applemusic.TypeAlbum, tracks); got != "Album Artist" {
		t.Fatalf("album folder artist = %q, want %q", got, "Album Artist")
	}
	if got := collectionFolderArtist(applemusic.TypePlaylist, tracks); got != "" {
		t.Fatalf("playlist folder artist = %q, want empty", got)
	}
}

func TestCollectionFolderArtistFallsBackToFirstTrack(t *testing.T) {
	tracks := []applemusic.Song{{ArtistName: "First Artist"}, {ArtistName: "Second Artist"}}
	if got := collectionFolderArtist(applemusic.TypeAlbum, tracks); got != "First Artist" {
		t.Fatalf("album folder artist = %q, want %q", got, "First Artist")
	}
}
