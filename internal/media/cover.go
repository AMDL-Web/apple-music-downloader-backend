package media

import (
	"amdl/internal/applemusic"
)

// trackCoverURLs returns artwork URL candidates for a track.
// Playlist and station downloads try the song cover first, then the album
// cover, because their tracks come from different albums.
func trackCoverURLs(song applemusic.Song, collectionType applemusic.URLType) []string {
	if collectionType == applemusic.TypePlaylist || collectionType == applemusic.TypeStation {
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
