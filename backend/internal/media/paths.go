package media

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/config"
)

var invalidPathChars = regexp.MustCompile(`[\/\\<>:"|?*]`)

func safeName(v string) string {
	v = invalidPathChars.ReplaceAllString(v, "_")
	v = strings.TrimSpace(v)
	v = strings.TrimRight(v, ".")
	if v == "" {
		return "_"
	}
	return v
}

func outputPath(cfg config.Config, song applemusic.Song, collectionType applemusic.URLType, playlistIndex int, folderArtist, playlistName string) string {
	typeDir := filepath.Join(cfg.Download.DownloadsDir, downloadTypeFolder(cfg, collectionType))
	if collectionType == applemusic.TypePlaylist && playlistName != "" {
		folderPattern := cfg.Download.PlaylistFolderFormat
		if folderPattern == "" {
			folderPattern = "{PlaylistName}"
		}
		filePattern := cfg.Download.PlaylistSongFileFormat
		if filePattern == "" {
			filePattern = "{SongNumer:02d}. {SongName}"
		}
		folder := formatPattern(folderPattern, song, playlistIndex, "", playlistName)
		file := formatPattern(filePattern, song, playlistIndex, "", playlistName)
		return filepath.Join(typeDir, safeName(folder), safeName(file)+".m4a")
	}

	folderSong := song
	if folderArtist != "" {
		folderSong.ArtistName = folderArtist
	}
	artist := formatPattern(cfg.Download.ArtistFolderFormat, folderSong, playlistIndex, "", "")
	album := formatPattern(cfg.Download.AlbumFolderFormat, song, playlistIndex, "", "")
	file := formatPattern(cfg.Download.SongFileFormat, song, playlistIndex, "", "")
	return filepath.Join(typeDir, safeName(artist), safeName(album), safeName(file)+".m4a")
}

func downloadTypeFolder(cfg config.Config, collectionType applemusic.URLType) string {
	name := cfg.Download.SongsFolderName
	switch collectionType {
	case applemusic.TypeAlbum:
		name = cfg.Download.AlbumsFolderName
	case applemusic.TypePlaylist:
		name = cfg.Download.PlaylistsFolderName
	}
	if strings.TrimSpace(name) == "" {
		switch collectionType {
		case applemusic.TypeAlbum:
			name = "albums"
		case applemusic.TypePlaylist:
			name = "playlists"
		default:
			name = "songs"
		}
	}
	return safeName(name)
}

func formatPattern(pattern string, song applemusic.Song, playlistIndex int, codec, playlistName string) string {
	repl := map[string]string{
		"SongId":       song.ID,
		"ArtistName":   song.ArtistName,
		"AlbumName":    song.AlbumName,
		"SongName":     song.Name,
		"DiscNumber":   strconv.Itoa(max(1, song.DiscNumber)),
		"TrackNumber":  strconv.Itoa(max(1, song.TrackNumber)),
		"SongNumer":    strconv.Itoa(max(1, playlistIndex)),
		"Quality":      strings.ToUpper(codec),
		"Codec":        strings.ToUpper(codec),
		"Tag":          "",
		"PlaylistName": playlistName,
	}
	out := pattern
	for key, val := range repl {
		out = strings.ReplaceAll(out, "{"+key+"}", val)
		out = strings.ReplaceAll(out, "{"+key+":02d}", fmt.Sprintf("%02d", atoi(val)))
	}
	return out
}

func atoi(v string) int {
	i, _ := strconv.Atoi(v)
	return i
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
