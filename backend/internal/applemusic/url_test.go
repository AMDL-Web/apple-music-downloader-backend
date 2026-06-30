package applemusic

import "testing"

func TestParseAlbumTrackURLAsSong(t *testing.T) {
	got, err := Parse("https://music.apple.com/us/album/foo/123456789?i=987654321")
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
	got, err := Parse("https://music.apple.com/jp/playlist/foo/pl.abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypePlaylist || got.ID != "pl.abcdef" || got.Storefront != "jp" {
		t.Fatalf("unexpected parse result: %+v", got)
	}
}
