package media

import (
	"path/filepath"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
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

	got := outputPath(cfg, song, applemusic.TypeAlbum, 2, song.AlbumArtist, "", "", "", "")
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
	got := outputPath(cfg, song, applemusic.TypeSong, 1, "", "", "", "", "")
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
	if got := collectionFolderArtist(applemusic.TypeArtist, tracks); got != "Album Artist" {
		t.Fatalf("artist folder artist = %q, want %q", got, "Album Artist")
	}
	if got := collectionFolderArtist(applemusic.TypePlaylist, tracks); got != "" {
		t.Fatalf("playlist folder artist = %q, want empty", got)
	}
}

func TestOutputPathPlaylistUsesFlatFolder(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.PlaylistFolderFormat = "{PlaylistName}"
	cfg.Download.PlaylistSongFileFormat = "{SongNumber:02d}. {SongName}"

	song := applemusic.Song{ArtistName: "Artist A", AlbumName: "Album X", Name: "Track One"}
	got := outputPath(cfg, song, applemusic.TypePlaylist, 3, "", "My Playlist", "", "", "")
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
	cfg.Download.ArtistsFolderName = "artists"
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
		{applemusic.TypeArtist, "", filepath.Join("downloads", "artists", "Artist", "Album", "Song.m4a")},
		{applemusic.TypePlaylist, "List", filepath.Join("downloads", "lists", "List", "Song.m4a")},
	}
	for _, tt := range tests {
		if got := outputPath(cfg, song, tt.kind, 1, "", tt.playlist, "", "", ""); got != tt.want {
			t.Errorf("outputPath(%s) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestOutputPathExpandsCompatibilityMetadataVariables(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.ArtistFolderFormat = "{UrlArtistName} [{ArtistId}]"
	cfg.Download.AlbumFolderFormat = "{ReleaseYear} - {AlbumName} ({AlbumId}) [{UPC}] {RecordLabel} {Copyright}"
	cfg.Download.SongFileFormat = "{DiscNumber:02d}-{TrackNumber:02d} of {DiscCount}-{TrackCount} {AlbumArtist} - {SongName} [{Codec}] [{Quality}] {UnknownVar}"

	song := applemusic.Song{
		ID:           "song-123",
		Name:         "Track One",
		ArtistName:   "Track Artist",
		ArtistID:     "artist-123",
		AlbumName:    "Album X",
		AlbumID:      "album-123",
		AlbumArtist:  "Album Artist",
		AlbumRelease: "2024-05-17",
		ReleaseDate:  "2024-05-18",
		Copyright:    "2024 Label",
		RecordLabel:  "Label Co",
		UPC:          "012345678901",
		DiscNumber:   1,
		DiscCount:    2,
		TrackNumber:  3,
		TrackCount:   12,
	}

	got := outputPath(cfg, song, applemusic.TypeAlbum, 1, song.AlbumArtist, "", "", "alac", "24-bit/96 kHz")
	want := filepath.Join("downloads", "albums", "Album Artist [artist-123]", "2024 - Album X (album-123) [012345678901] Label Co 2024 Label", "01-03 of 2-12 Album Artist - Track One [ALAC] [24-bit_96 kHz] {UnknownVar}.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
}

func TestOutputPathExpandsPlaylistIdAndSongNumber(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.PlaylistFolderFormat = "{PlaylistName} ({PlaylistId})"
	cfg.Download.PlaylistSongFileFormat = "{SongNumber:02d}. {ArtistName} - {SongName}"

	song := applemusic.Song{ArtistName: "Artist A", AlbumName: "Album X", Name: "Track One"}
	got := outputPath(cfg, song, applemusic.TypePlaylist, 7, "", "My Playlist", "pl.123", "", "")
	want := filepath.Join("downloads", "playlists", "My Playlist (pl.123)", "07. Artist A - Track One.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
}

func TestOutputPathDoesNotExpandRemovedSongNumerMisspelling(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.PlaylistFolderFormat = "{PlaylistName}"
	cfg.Download.PlaylistSongFileFormat = "{SongNumer:02d}. {SongName}"

	song := applemusic.Song{ArtistName: "Artist A", AlbumName: "Album X", Name: "Track One"}
	got := outputPath(cfg, song, applemusic.TypePlaylist, 7, "", "My Playlist", "", "", "")
	want := filepath.Join("downloads", "playlists", "My Playlist", "{SongNumer_02d}. Track One.m4a")
	if got != want {
		t.Fatalf("outputPath() = %q, want %q", got, want)
	}
}

func TestOutputPathUsesCodecQualityPerAttempt(t *testing.T) {
	cfg := config.Default()
	cfg.Download.DownloadsDir = "downloads"
	cfg.Download.SongFileFormat = "{SongName} [{Codec}] [{Quality}]"

	song := applemusic.Song{ArtistName: "Artist", AlbumName: "Album", Name: "Song"}
	alac := outputPath(cfg, song, applemusic.TypeSong, 1, "", "", "", "alac", "24-bit/96 kHz")
	aac := outputPath(cfg, song, applemusic.TypeSong, 1, "", "", "", "aac", "256Kbps")

	if alac == aac {
		t.Fatalf("codec-specific output paths matched: %q", alac)
	}
	if want := filepath.Join("downloads", "songs", "Artist", "Album", "Song [ALAC] [24-bit_96 kHz].m4a"); alac != want {
		t.Fatalf("ALAC outputPath() = %q, want %q", alac, want)
	}
	if want := filepath.Join("downloads", "songs", "Artist", "Album", "Song [AAC] [256Kbps].m4a"); aac != want {
		t.Fatalf("AAC outputPath() = %q, want %q", aac, want)
	}
}

func TestQualityLabelFormatsSelectedMedia(t *testing.T) {
	tests := []struct {
		name string
		info selectedMediaInfo
		want string
	}{
		{name: "lossless", info: selectedMediaInfo{BitDepth: 24, SampleRate: 96000, Bandwidth: 3900000}, want: "24-bit/96 kHz"},
		{name: "bitrate", info: selectedMediaInfo{Bandwidth: 256000}, want: "256Kbps"},
		{name: "unknown", info: selectedMediaInfo{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qualityLabel(tt.info); got != tt.want {
				t.Fatalf("qualityLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}
