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

func outputPath(cfg config.Config, song applemusic.Song, playlistIndex int) string {
	artist := formatPattern(cfg.Download.ArtistFolderFormat, song, playlistIndex, "")
	album := formatPattern(cfg.Download.AlbumFolderFormat, song, playlistIndex, "")
	namePattern := cfg.Download.SongFileFormat
	if playlistIndex > 0 && cfg.Download.PlaylistSongFileFormat != "" {
		namePattern = cfg.Download.PlaylistSongFileFormat
	}
	file := formatPattern(namePattern, song, playlistIndex, "")
	return filepath.Join(cfg.Download.DownloadsDir, safeName(artist), safeName(album), safeName(file)+".m4a")
}

func formatPattern(pattern string, song applemusic.Song, playlistIndex int, codec string) string {
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
		"PlaylistName": song.AlbumName,
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
