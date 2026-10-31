package applemusic

import (
	"slices"
	"strings"
)

// EnrichWithAlbum fills song's album-level fields from album, the full album
// resource it belongs to: album artwork and palette, album artist (name, id
// and artwork), track and disc totals, and the artist page when the song's own
// artist relationship carried none. Track and disc totals come from album's
// entry for the same song when present, otherwise from the album's track list.
func EnrichWithAlbum(song Song, album Collection) Song {
	song.AlbumArtworkURL = album.ArtworkURL
	song.AlbumArtworkColors = album.ArtworkColors
	song.AlbumArtistID = album.ArtistID
	song.AlbumArtistArtworkURL = album.ArtistArtworkURL
	for _, track := range album.Tracks {
		if track.ID == song.ID {
			song.TrackCount = track.TrackCount
			song.DiscCount = track.DiscCount
			break
		}
	}
	if song.TrackCount == 0 {
		song.TrackCount = DiscTrackCount(album.Tracks, song.DiscNumber)
	}
	if song.DiscCount == 0 {
		song.DiscCount = maxDisc(album.Tracks)
	}
	song.AlbumArtist = album.Artist
	if song.ArtistURL == "" {
		song.ArtistURL = album.ArtistURL
	}
	return song
}

// PrimaryGenre picks the display genre from Apple's genreNames list: the
// first entry that is not the generic "Music" umbrella genre.
func PrimaryGenre(names []string) string {
	for _, name := range names {
		if name != "" && name != "Music" {
			return name
		}
	}
	return ""
}

// DisplayArtwork returns the artwork a song is shown with, its own or else its
// album's, together with the palette of that same image.
func (s Song) DisplayArtwork() (string, ArtworkColors) {
	if s.ArtworkURL != "" {
		return s.ArtworkURL, s.ArtworkColors
	}
	return s.AlbumArtworkURL, s.AlbumArtworkColors
}

func mapSong(raw catalogSongData) Song {
	s := Song{
		ID: raw.ID, Name: raw.Attributes.Name, ArtistName: raw.Attributes.ArtistName, AlbumName: raw.Attributes.AlbumName,
		ComposerName: raw.Attributes.ComposerName, GenreNames: raw.Attributes.GenreNames, ReleaseDate: raw.Attributes.ReleaseDate,
		TrackNumber: raw.Attributes.TrackNumber, DiscNumber: raw.Attributes.DiscNumber, ISRC: raw.Attributes.ISRC,
		DurationInMillis: raw.Attributes.DurationInMillis,
		ContentRating:    raw.Attributes.ContentRating, HasLyrics: raw.Attributes.HasLyrics || raw.Attributes.HasTimeSyncedLyrics,
		AppleDigitalMaster: raw.Attributes.IsAppleDigitalMaster,
		HiResLossless:      slices.Contains(raw.Attributes.AudioTraits, "hi-res-lossless"),
		AudioTraits:        raw.Attributes.AudioTraits, URL: raw.Attributes.URL,
		ArtworkURL: raw.Attributes.Artwork.URL, ArtworkColors: raw.Attributes.Artwork.colors(),
		EnhancedHLS: raw.Attributes.ExtendedAssetURLs.EnhancedHLS,
	}
	if len(raw.Relationships.Albums.Data) > 0 {
		album := raw.Relationships.Albums.Data[0]
		s.AlbumID = album.ID
		s.AlbumArtist = album.Attributes.ArtistName
		s.AlbumRelease = album.Attributes.ReleaseDate
		s.AlbumArtworkURL = album.Attributes.Artwork.URL
		s.AlbumArtworkColors = album.Attributes.Artwork.colors()
		s.Copyright = album.Attributes.Copyright
		s.RecordLabel = album.Attributes.RecordLabel
		s.UPC = album.Attributes.UPC
		s.TrackCount = album.Attributes.TrackCount
	}
	if len(raw.Relationships.Artists.Data) > 0 {
		artist := raw.Relationships.Artists.Data[0]
		s.ArtistID = artist.ID
		s.ArtistArtworkURL = artist.Attributes.Artwork.URL
		s.ArtistURL = artist.Attributes.URL
	}
	return s
}

func mapAlbumSummary(raw catalogAlbumData) Collection {
	var artistID, artistArtworkURL, artistURL string
	if len(raw.Relationships.Artists.Data) > 0 {
		artist := raw.Relationships.Artists.Data[0]
		artistID = artist.ID
		artistArtworkURL = artist.Attributes.Artwork.URL
		artistURL = artist.Attributes.URL
	}
	return Collection{
		ID: raw.ID, Type: TypeAlbum, Name: raw.Attributes.Name, Artist: raw.Attributes.ArtistName,
		ArtworkURL: raw.Attributes.Artwork.URL, ArtworkColors: raw.Attributes.Artwork.colors(),
		ArtistID: artistID, ArtistArtworkURL: artistArtworkURL, ArtistURL: artistURL,
		ReleaseDate: raw.Attributes.ReleaseDate, GenreNames: raw.Attributes.GenreNames,
		ContentRating: raw.Attributes.ContentRating, AppleDigitalMaster: raw.Attributes.IsMasteredForItunes,
		URL: raw.Attributes.URL, TrackCount: raw.Attributes.TrackCount, AudioTraits: raw.Attributes.AudioTraits,
	}
}

// mapPlaylistSummary maps a playlist resource without its tracks: a search hit
// or the head of Playlist. Artist is the curator.
func mapPlaylistSummary(raw catalogPlaylistData) Collection {
	return Collection{
		ID: raw.ID, Type: TypePlaylist, Name: raw.Attributes.Name,
		Artist:     firstNonEmpty(raw.Attributes.CuratorName, raw.Attributes.ArtistName),
		ArtworkURL: raw.Attributes.Artwork.URL, ArtworkColors: raw.Attributes.Artwork.colors(),
		URL: raw.Attributes.URL, AudioTraits: raw.Attributes.AudioTraits,
	}
}

func mapArtist(raw artistData) Artist {
	return Artist{
		ID: raw.ID, Name: raw.Attributes.Name, URL: raw.Attributes.URL, GenreNames: raw.Attributes.GenreNames,
		ArtworkURL: raw.Attributes.Artwork.URL, ArtworkColors: raw.Attributes.Artwork.colors(),
	}
}

// discTrackTotals derives each disc's track total from an album's track list.
// Counting entries alone underreports, because a storefront can withhold
// individual tracks from the relationship and music videos never become
// tracks: a disc returning track numbers 1 and 3 has at least three tracks,
// not two. Apple's own trackNumber is the better witness and can only fall
// short when the withheld track is the disc's last, so take whichever of the
// two is larger. A missing disc number folds into disc 1, matching how
// DiscNumber is rendered everywhere else.
func discTrackTotals(tracks []Song) map[int]int {
	totals := make(map[int]int)
	counts := make(map[int]int)
	for _, track := range tracks {
		disc := max(1, track.DiscNumber)
		counts[disc]++
		totals[disc] = max(totals[disc], track.TrackNumber)
	}
	for disc, count := range counts {
		totals[disc] = max(totals[disc], count)
	}
	return totals
}

// DiscTrackCount reports the track total of the disc holding discNumber, for
// callers with an album track list but no per-track total. Single-disc albums
// answer for the whole list, so a track whose disc number the catalog omitted
// still gets the album's length rather than a partial count.
func DiscTrackCount(tracks []Song, discNumber int) int {
	totals := discTrackTotals(tracks)
	if maxDisc(tracks) <= 1 {
		return max(totals[1], len(tracks))
	}
	if total := totals[max(1, discNumber)]; total > 0 {
		return total
	}
	return len(tracks)
}

// maxDisc is the highest disc number in tracks, or 1 when none carries one.
func maxDisc(tracks []Song) int {
	highest := 1
	for _, track := range tracks {
		highest = max(highest, track.DiscNumber)
	}
	return highest
}

func firstNonEmpty(vals ...string) string {
	for _, val := range vals {
		if strings.TrimSpace(val) != "" {
			return val
		}
	}
	return ""
}
