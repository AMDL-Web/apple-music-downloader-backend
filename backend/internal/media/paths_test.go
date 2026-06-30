package media

import (
	"path/filepath"
	"testing"

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/config"
)

func TestOutputPathUsesAlbumFolderArtistWithoutChangingTrackMetadata(t *testing.T) {
	cfg := config.Default()
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

	got := outputPath(cfg, song, applemusic.TypeAlbum, 2, song.AlbumArtist, "")
	want := filepath.Join("downloads", "albums", "Primary Artist", "Shared Album", "02. Guest Track.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
	if song.ArtistName != "Guest Artist" {
		t.Fatalf("track artist was modified: %q", song.ArtistName)
	}
}

func TestOutputPathKeepsTrackArtistWhenNoAlbumFolderArtist(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.ArtistFolderFormat = "{ArtistName}"
	cfg.Download.AlbumFolderFormat = "{AlbumName}"
	cfg.Download.SongFileFormat = "{SongName}"

	song := applemusic.Song{ArtistName: "Track Artist", AlbumName: "Album", Name: "Song"}
	got := outputPath(cfg, song, applemusic.TypeSong, 1, "", "")
	want := filepath.Join("downloads", "songs", "Track Artist", "Album", "Song.m4a")
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

func TestOutputPathPlaylistUsesFlatFolder(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.PlaylistFolderFormat = "{PlaylistName}"
	cfg.Download.PlaylistSongFileFormat = "{SongNumer:02d}. {SongName}"

	song := applemusic.Song{ArtistName: "Artist A", AlbumName: "Album X", Name: "Track One"}
	got := outputPath(cfg, song, applemusic.TypePlaylist, 3, "", "My Playlist")
	want := filepath.Join("downloads", "playlists", "My Playlist", "03. Track One.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
}

func TestCollectionFolderArtistFallsBackToFirstTrack(t *testing.T) {
	tracks := []applemusic.Song{{ArtistName: "First Artist"}, {ArtistName: "Second Artist"}}
	if got := collectionFolderArtist(applemusic.TypeAlbum, tracks); got != "First Artist" {
		t.Fatalf("album folder artist = %q, want %q", got, "First Artist")
	}
}

func TestOutputPathUsesConfiguredTypeFolderNames(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.SongsFolderName = "single tracks"
	cfg.Download.AlbumsFolderName = "records"
	cfg.Download.PlaylistsFolderName = "lists"
	cfg.Download.ArtistFolderFormat = "{ArtistName}"
	cfg.Download.AlbumFolderFormat = "{AlbumName}"
	cfg.Download.SongFileFormat = "{SongName}"
	cfg.Download.PlaylistFolderFormat = "{PlaylistName}"
	cfg.Download.PlaylistSongFileFormat = "{SongName}"
	song := applemusic.Song{ArtistName: "Artist", AlbumName: "Album", Name: "Song"}

	tests := []struct {
		kind     applemusic.URLType
		playlist string
		want     string
	}{
		{applemusic.TypeSong, "", filepath.Join("downloads", "single tracks", "Artist", "Album", "Song.m4a")},
		{applemusic.TypeAlbum, "", filepath.Join("downloads", "records", "Artist", "Album", "Song.m4a")},
		{applemusic.TypePlaylist, "List", filepath.Join("downloads", "lists", "List", "Song.m4a")},
	}
	for _, tt := range tests {
		if got := outputPath(cfg, song, tt.kind, 1, "", tt.playlist); got != tt.want {
			t.Errorf("outputPath(%s) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}
