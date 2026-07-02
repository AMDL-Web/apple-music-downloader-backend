package applemusic

import "testing"

func TestParseAlbumTrackURLAsSong(t *testing.T) {
	got, err := ParseWithAlbumTrackMode("https://music.apple.com/us/album/foo/123456789?i=987654321", "song")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeSong || got.ID != "987654321" || got.Storefront != "us" {
		t.Fatalf("unexpected parse result: %+v", got)
	}
}

func TestParseAlbumTrackURLAsAlbum(t *testing.T) {
	got, err := ParseWithAlbumTrackMode("https://music.apple.com/us/album/foo/123456789?i=987654321", "album")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeAlbum || got.ID != "123456789" || got.Storefront != "us" {
		t.Fatalf("unexpected parse result: %+v", got)
	}
}

func TestParseAlbumTrackURLRejectsInvalidMode(t *testing.T) {
	if _, err := ParseWithAlbumTrackMode("https://music.apple.com/us/album/foo/123456789?i=987654321", "invalid"); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestParsePlaylistURL(t *testing.T) {
	got, err := ParseWithAlbumTrackMode("https://music.apple.com/jp/playlist/foo/pl.abcdef", "song")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypePlaylist || got.ID != "pl.abcdef" || got.Storefront != "jp" {
		t.Fatalf("unexpected parse result: %+v", got)
	}
}

func TestParseClassicalSupportedURLs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		mode string
		want ParsedURL
	}{
		{name: "album", raw: "https://classical.music.apple.com/cn/album/89286124?l=zh-Hans-CN", mode: "song", want: ParsedURL{Storefront: "cn", Type: TypeAlbum, ID: "89286124"}},
		{name: "album track as song", raw: "https://classical.music.apple.com/us/album/foo/123456789?i=987654321", mode: "song", want: ParsedURL{Storefront: "us", Type: TypeSong, ID: "987654321"}},
		{name: "album track as album", raw: "https://classical.music.apple.com/us/album/foo/123456789?i=987654321", mode: "album", want: ParsedURL{Storefront: "us", Type: TypeAlbum, ID: "123456789"}},
		{name: "song", raw: "https://classical.music.apple.com/gb/song/foo/987654321", mode: "song", want: ParsedURL{Storefront: "gb", Type: TypeSong, ID: "987654321"}},
		{name: "playlist", raw: "https://classical.music.apple.com/jp/playlist/foo/pl.abcdef", mode: "song", want: ParsedURL{Storefront: "jp", Type: TypePlaylist, ID: "pl.abcdef"}},
		{name: "artist", raw: "https://classical.music.apple.com/de/artist/foo/123456789", mode: "song", want: ParsedURL{Storefront: "de", Type: TypeArtist, ID: "123456789"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWithAlbumTrackMode(tt.raw, tt.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got.Storefront != tt.want.Storefront || got.Type != tt.want.Type || got.ID != tt.want.ID {
				t.Fatalf("unexpected parse result: %+v", got)
			}
		})
	}
}

func TestParseClassicalRejectsMusicVideo(t *testing.T) {
	_, err := ParseWithAlbumTrackMode("https://classical.music.apple.com/us/music-video/foo/123456789", "song")
	if err == nil {
		t.Fatal("expected Classical music-video URL to be rejected")
	}
}

func TestParseRejectsUnknownAppleMusicHost(t *testing.T) {
	_, err := ParseWithAlbumTrackMode("https://unknown.music.apple.com/us/album/foo/123456789", "song")
	if err == nil {
		t.Fatal("expected unknown host to be rejected")
	}
}
