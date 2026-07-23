package applemusic

// ArtworkColors is the color palette Apple attaches to attributes.artwork
// next to url/width/height: a background color plus four text colors, all
// hex strings without a leading "#". Fields are empty when the catalog
// omits the palette.
type ArtworkColors struct {
	BgColor    string
	TextColor1 string
	TextColor2 string
	TextColor3 string
	TextColor4 string
}

type Song struct {
	ID               string
	Name             string
	ArtistName       string
	AlbumName        string
	ComposerName     string
	GenreNames       []string
	ReleaseDate      string
	TrackNumber      int
	DiscNumber       int
	TrackCount       int
	DiscCount        int
	DurationInMillis int
	ISRC             string
	ContentRating    string
	HasLyrics        bool
	ArtworkURL       string
	AlbumArtworkURL  string
	ArtistArtworkURL string
	// ArtworkColors/AlbumArtworkColors are the palettes of the artwork behind
	// ArtworkURL and AlbumArtworkURL respectively.
	ArtworkColors         ArtworkColors
	AlbumArtworkColors    ArtworkColors
	EnhancedHLS           string
	AlbumID               string
	AlbumArtist           string
	AlbumArtistID         string
	AlbumArtistArtworkURL string
	AlbumRelease          string
	Copyright             string
	RecordLabel           string
	UPC                   string
	ArtistID              string
}

type Collection struct {
	ID               string
	Type             URLType
	Name             string
	Artist           string
	ArtworkURL       string
	ArtworkColors    ArtworkColors
	ArtistID         string
	ArtistArtworkURL string
	// ReleaseDate/GenreNames carry the album-level attributes (YYYY-MM-DD and
	// Apple's genreNames list); only populated for album collections — the
	// catalog does not expose them on playlists or stations.
	ReleaseDate string
	GenreNames  []string
	Tracks      []Song
}

type Artist struct {
	ID            string
	Name          string
	ArtworkURL    string
	ArtworkColors ArtworkColors
}

// StationInfo is the catalog metadata for an Apple Music radio station. Format
// mirrors attributes.playParams.format: "tracks" for a personalized/curated
// station that resolves to a finite next-tracks list (downloadable here), or
// "stream" for a live broadcast (not downloadable — no static track list).
type StationInfo struct {
	ID            string
	Name          string
	ArtworkURL    string
	ArtworkColors ArtworkColors
	Format        string
	IsLive        bool
}

type ArtistAlbums struct {
	Artist
	Albums []Collection
}

type catalogSongResponse struct {
	Data []catalogSongData `json:"data"`
}

type catalogAlbumResponse struct {
	Data []catalogAlbumData `json:"data"`
}

type catalogPlaylistResponse struct {
	Data []catalogPlaylistData `json:"data"`
}

type catalogArtistResponse struct {
	Data []artistData `json:"data"`
}

type catalogStationResponse struct {
	Data []catalogStationData `json:"data"`
}

type catalogStationData struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Attributes stationAttributes `json:"attributes"`
}

type stationAttributes struct {
	Name       string  `json:"name"`
	IsLive     bool    `json:"isLive"`
	Artwork    artwork `json:"artwork"`
	PlayParams struct {
		Format string `json:"format"`
	} `json:"playParams"`
}

// stationTracksResponse is the shape of POST /v1/me/stations/next-tracks/{id}:
// a page of catalog songs, decoded with the same catalogSongData used for
// album/playlist tracks so mapSong applies uniformly.
type stationTracksResponse struct {
	Data []catalogSongData `json:"data"`
}

type catalogSongData struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Href          string            `json:"href"`
	Attributes    songAttributes    `json:"attributes"`
	Relationships songRelationships `json:"relationships"`
}

type catalogAlbumData struct {
	ID            string             `json:"id"`
	Type          string             `json:"type"`
	Attributes    albumAttributes    `json:"attributes"`
	Relationships albumRelationships `json:"relationships"`
}

type catalogPlaylistData struct {
	ID            string                `json:"id"`
	Type          string                `json:"type"`
	Attributes    playlistAttributes    `json:"attributes"`
	Relationships playlistRelationships `json:"relationships"`
}

type songAttributes struct {
	Name                string            `json:"name"`
	ArtistName          string            `json:"artistName"`
	AlbumName           string            `json:"albumName"`
	ComposerName        string            `json:"composerName"`
	GenreNames          []string          `json:"genreNames"`
	ReleaseDate         string            `json:"releaseDate"`
	TrackNumber         int               `json:"trackNumber"`
	DiscNumber          int               `json:"discNumber"`
	DurationInMillis    int               `json:"durationInMillis"`
	ISRC                string            `json:"isrc"`
	ContentRating       string            `json:"contentRating"`
	HasTimeSyncedLyrics bool              `json:"hasTimeSyncedLyrics"`
	HasLyrics           bool              `json:"hasLyrics"`
	Artwork             artwork           `json:"artwork"`
	ExtendedAssetURLs   extendedAssetURLs `json:"extendedAssetUrls"`
}

type albumAttributes struct {
	Name        string   `json:"name"`
	ArtistName  string   `json:"artistName"`
	GenreNames  []string `json:"genreNames"`
	ReleaseDate string   `json:"releaseDate"`
	TrackCount  int      `json:"trackCount"`
	Copyright   string   `json:"copyright"`
	RecordLabel string   `json:"recordLabel"`
	UPC         string   `json:"upc"`
	Artwork     artwork  `json:"artwork"`
}

type playlistAttributes struct {
	Name        string  `json:"name"`
	CuratorName string  `json:"curatorName"`
	ArtistName  string  `json:"artistName"`
	Artwork     artwork `json:"artwork"`
}

type artwork struct {
	URL        string `json:"url"`
	BgColor    string `json:"bgColor"`
	TextColor1 string `json:"textColor1"`
	TextColor2 string `json:"textColor2"`
	TextColor3 string `json:"textColor3"`
	TextColor4 string `json:"textColor4"`
}

// colors lifts the wire palette into the exported ArtworkColors value.
func (a artwork) colors() ArtworkColors {
	return ArtworkColors{
		BgColor:    a.BgColor,
		TextColor1: a.TextColor1,
		TextColor2: a.TextColor2,
		TextColor3: a.TextColor3,
		TextColor4: a.TextColor4,
	}
}

type extendedAssetURLs struct {
	EnhancedHLS string `json:"enhancedHls"`
}

type songRelationships struct {
	Albums  relationshipAlbums  `json:"albums"`
	Artists relationshipArtists `json:"artists"`
}

type albumRelationships struct {
	Tracks  relationshipSongs   `json:"tracks"`
	Artists relationshipArtists `json:"artists"`
}

type playlistRelationships struct {
	Tracks relationshipSongs `json:"tracks"`
	// Library is populated only when the request carries a media-user-token
	// and include=library: for a private (user-shared) playlist it exposes the
	// owner's library copy, whose attributes carry the user-uploaded artwork
	// that the public catalog attributes omit.
	Library relationshipLibraryPlaylists `json:"library"`
}

type relationshipLibraryPlaylists struct {
	Data []libraryPlaylistData `json:"data"`
}

type libraryPlaylistData struct {
	ID         string `json:"id"`
	Attributes struct {
		Name    string  `json:"name"`
		Artwork artwork `json:"artwork"`
	} `json:"attributes"`
}

type relationshipSongs struct {
	Data []catalogSongData `json:"data"`
	Next string            `json:"next"`
}

type relationshipAlbums struct {
	Data []catalogAlbumData `json:"data"`
	Next string             `json:"next"`
}

type relationshipArtists struct {
	Data []artistData `json:"data"`
}

type artistData struct {
	ID            string              `json:"id"`
	Attributes    artistAttributes    `json:"attributes"`
	Relationships artistRelationships `json:"relationships"`
}

type artistAttributes struct {
	Name    string  `json:"name"`
	Artwork artwork `json:"artwork"`
}

type artistRelationships struct {
	Albums relationshipAlbums `json:"albums"`
}
