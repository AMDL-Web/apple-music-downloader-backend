package media

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"amdl/internal/applemusic"
	"amdl/internal/config"
)

var invalidPathChars = regexp.MustCompile(`[\/\\<>:"|?*]`)
var templateVariablePattern = regexp.MustCompile(`\{([A-Za-z]+)(?::(02d))?\}`)

type pathTemplateContext struct {
	song          applemusic.Song
	playlistIndex int
	playlistName  string
	playlistID    string
	codec         string
	quality       string
}

type selectedMediaInfo struct {
	MediaURI   string
	Keys       []string
	CodecID    string
	BitDepth   int
	SampleRate int
	Bandwidth  int
}

func safeName(v string) string {
	v = invalidPathChars.ReplaceAllString(v, "_")
	v = strings.TrimSpace(v)
	v = strings.TrimRight(v, ".")
	if v == "" {
		return "_"
	}
	return v
}

func outputPath(cfg config.Config, song applemusic.Song, collectionType applemusic.URLType, playlistIndex int, folderArtist, playlistName, playlistID, codec, quality string) string {
	typeDir := filepath.Join(cfg.Download.DownloadsDir, downloadTypeFolder(cfg, collectionType))
	ctx := pathTemplateContext{
		song:          song,
		playlistIndex: playlistIndex,
		playlistName:  playlistName,
		playlistID:    playlistID,
		codec:         codec,
		quality:       quality,
	}
	if collectionType == applemusic.TypePlaylist && playlistName != "" {
		folderPattern := cfg.Download.PlaylistFolderFormat
		filePattern := cfg.Download.PlaylistSongFileFormat
		folder := formatPattern(folderPattern, ctx)
		file := formatPattern(filePattern, ctx)
		return filepath.Join(typeDir, safeName(folder), safeName(file)+".m4a")
	}

	folderSong := song
	if folderArtist != "" {
		folderSong.ArtistName = folderArtist
	}
	artistCtx := ctx
	artistCtx.song = folderSong
	artist := formatPattern(cfg.Download.ArtistFolderFormat, artistCtx)
	album := formatPattern(cfg.Download.AlbumFolderFormat, ctx)
	file := formatPattern(cfg.Download.SongFileFormat, ctx)
	return filepath.Join(typeDir, safeName(artist), safeName(album), safeName(file)+".m4a")
}

func playlistFolderPath(cfg config.Config, song applemusic.Song, playlistName, playlistID string) string {
	typeDir := filepath.Join(cfg.Download.DownloadsDir, downloadTypeFolder(cfg, applemusic.TypePlaylist))
	folder := formatPattern(cfg.Download.PlaylistFolderFormat, pathTemplateContext{
		song:          song,
		playlistIndex: 1,
		playlistName:  playlistName,
		playlistID:    playlistID,
	})
	return filepath.Join(typeDir, safeName(folder))
}

func downloadTypeFolder(cfg config.Config, collectionType applemusic.URLType) string {
	name := cfg.Download.SongsFolderName
	switch collectionType {
	case applemusic.TypeAlbum:
		name = cfg.Download.AlbumsFolderName
	case applemusic.TypePlaylist:
		name = cfg.Download.PlaylistsFolderName
	}
	return safeName(name)
}

func formatPattern(pattern string, ctx pathTemplateContext) string {
	return templateVariablePattern.ReplaceAllStringFunc(pattern, func(match string) string {
		parts := templateVariablePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		value, ok := templateValue(parts[1], ctx)
		if !ok {
			return match
		}
		if parts[2] == "02d" {
			return fmt.Sprintf("%02d", atoi(value))
		}
		return value
	})
}

func templateValue(key string, ctx pathTemplateContext) (string, bool) {
	song := ctx.song
	switch key {
	case "SongId":
		return song.ID, true
	case "SongNumer", "SongNumber":
		return strconv.Itoa(max(1, ctx.playlistIndex)), true
	case "SongName":
		return song.Name, true
	case "ArtistName", "UrlArtistName":
		return song.ArtistName, true
	case "ArtistId":
		return song.ArtistID, true
	case "AlbumName":
		return song.AlbumName, true
	case "AlbumId":
		return song.AlbumID, true
	case "AlbumArtist":
		return firstNonEmpty(song.AlbumArtist, song.ArtistName), true
	case "ReleaseDate":
		return firstNonEmpty(song.AlbumRelease, song.ReleaseDate), true
	case "ReleaseYear":
		release := firstNonEmpty(song.AlbumRelease, song.ReleaseDate)
		if len(release) >= 4 {
			return release[:4], true
		}
		return "", true
	case "UPC":
		return song.UPC, true
	case "Copyright":
		return song.Copyright, true
	case "RecordLabel":
		return song.RecordLabel, true
	case "DiscNumber":
		return strconv.Itoa(max(1, song.DiscNumber)), true
	case "DiscCount":
		return strconv.Itoa(max(1, song.DiscCount)), true
	case "TrackNumber":
		return strconv.Itoa(max(1, song.TrackNumber)), true
	case "TrackCount":
		return strconv.Itoa(max(1, song.TrackCount)), true
	case "PlaylistName":
		return ctx.playlistName, true
	case "PlaylistId":
		return ctx.playlistID, true
	case "Quality":
		return ctx.quality, true
	case "Codec":
		return strings.ToUpper(ctx.codec), true
	case "Tag":
		return "", true
	default:
		return "", false
	}
}

func qualityLabel(info selectedMediaInfo) string {
	if info.BitDepth > 0 && info.SampleRate > 0 {
		return fmt.Sprintf("%d-bit/%d kHz", info.BitDepth, info.SampleRate/1000)
	}
	if info.Bandwidth > 0 {
		return fmt.Sprintf("%dKbps", info.Bandwidth/1000)
	}
	return ""
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
