package applemusic

type Song struct {
	ID                    string
	Name                  string
	ArtistName            string
	AlbumName             string
	ComposerName          string
	GenreNames            []string
	ReleaseDate           string
	TrackNumber           int
	DiscNumber            int
	TrackCount            int
	DiscCount             int
	ISRC                  string
	ContentRating         string
	HasLyrics             bool
	ArtworkURL            string
	AlbumArtworkURL       string
	ArtistArtworkURL      string
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
	ArtistID         string
	ArtistArtworkURL string
	Tracks           []Song
}

type Artist struct {
	ID         string
	Name       string
	ArtworkURL string
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
	URL string `json:"url"`
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
}

type relationshipSongs struct {
	Data []catalogSongData `json:"data"`
	Next string            `json:"next"`
}

type relationshipAlbums struct {
	Data []catalogAlbumData `json:"data"`
}

type relationshipArtists struct {
	Data []artistData `json:"data"`
}

type artistData struct {
	ID         string           `json:"id"`
	Attributes artistAttributes `json:"attributes"`
}

type artistAttributes struct {
	Name    string  `json:"name"`
	Artwork artwork `json:"artwork"`
}
