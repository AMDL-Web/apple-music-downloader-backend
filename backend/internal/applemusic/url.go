package applemusic

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type URLType string

const (
	TypeSong     URLType = "song"
	TypeAlbum    URLType = "album"
	TypePlaylist URLType = "playlist"
	TypeArtist   URLType = "artist"
	TypeVideo    URLType = "music-video"
)

type ParsedURL struct {
	Raw        string  `json:"raw"`
	Storefront string  `json:"storefront"`
	Type       URLType `json:"type"`
	ID         string  `json:"id"`
}

var storefrontPattern = regexp.MustCompile(`^[a-z]{2}$`)

func ParseWithAlbumTrackMode(raw, albumTrackURLMode string) (ParsedURL, error) {
	if albumTrackURLMode != "song" && albumTrackURLMode != "album" {
		return ParsedURL{}, fmt.Errorf("invalid album_track_url_mode %q (expected song or album)", albumTrackURLMode)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ParsedURL{}, err
	}
	if u.Host != "music.apple.com" && u.Host != "beta.music.apple.com" {
		return ParsedURL{}, fmt.Errorf("unsupported host %q", u.Host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 {
		return ParsedURL{}, fmt.Errorf("invalid Apple Music URL")
	}
	storefront := strings.ToLower(parts[0])
	if !storefrontPattern.MatchString(storefront) {
		return ParsedURL{}, fmt.Errorf("invalid storefront %q", storefront)
	}
	kind := URLType(parts[1])
	id := parts[len(parts)-1]
	switch kind {
	case TypeAlbum:
		if songID := u.Query().Get("i"); songID != "" && albumTrackURLMode == "song" {
			return ParsedURL{Raw: raw, Storefront: storefront, Type: TypeSong, ID: songID}, nil
		}
		return ParsedURL{Raw: raw, Storefront: storefront, Type: TypeAlbum, ID: id}, nil
	case TypeSong:
		return ParsedURL{Raw: raw, Storefront: storefront, Type: TypeSong, ID: id}, nil
	case TypePlaylist:
		return ParsedURL{Raw: raw, Storefront: storefront, Type: TypePlaylist, ID: id}, nil
	case TypeArtist:
		return ParsedURL{Raw: raw, Storefront: storefront, Type: TypeArtist, ID: id}, nil
	case TypeVideo:
		return ParsedURL{Raw: raw, Storefront: storefront, Type: TypeVideo, ID: id}, nil
	default:
		return ParsedURL{}, fmt.Errorf("unsupported Apple Music URL type %q", kind)
	}
}
