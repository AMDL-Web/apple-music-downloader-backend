package demo

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"

	"amdl/internal/client"
)

// The catalog is made up: every artist, album and song below is invented.

type artist struct {
	id, name string
	genre    string
}

type album struct {
	id, title string
	artist    *artist
	year      int
	tracks    []string
	// bg and fg are the cover's two colors, hex without "#".
	bg, fg string
}

type song struct {
	id    string
	album *album
	index int // 0-based
}

var artists = []*artist{
	{id: "910001", name: "Mira Solenne", genre: "Pop"},
	{id: "910002", name: "Nightjar Coast", genre: "Alternative"},
	{id: "910003", name: "The Velvet Orchard", genre: "Jazz"},
	{id: "910004", name: "青柠电台", genre: "Mandopop"},
}

var albums = []*album{
	{id: "920001", title: "Glass Weather", artist: artists[0], year: 2025, bg: "1f3b5c", fg: "f2c14e", tracks: []string{
		"Glass Weather", "Paper Satellites", "Slow Lightning", "Northbound", "Halo Static", "Small Hours",
		"Weathervane", "Blue Hour Again", "Signal Fires", "Undertow", "Night Market", "Afterglow (Reprise)"}},
	{id: "920002", title: "Tidal Rooms", artist: artists[0], year: 2022, bg: "0f5257", fg: "a8e6cf", tracks: []string{
		"Tidal Rooms", "Seaglass", "Low Ceilings", "Hollow Bells", "Lighthouse Keeper", "Drift",
		"Open Water", "Moon Tide", "Coastline", "Harbor Song"}},
	{id: "920003", title: "Low Tide Signals", artist: artists[1], year: 2026, bg: "2b2d42", fg: "ef8354", tracks: []string{
		"Low Tide Signals", "Radio Silence", "Static Bloom", "Dune Lines", "Faultline", "Kestrel",
		"Cold Open", "Saltwater Wires", "Night Ferry", "Long Exposure", "Breakwater"}},
	{id: "920004", title: "Salt & Static", artist: artists[1], year: 2023, bg: "5c2a3e", fg: "f4a259", tracks: []string{
		"Salt & Static", "Harbor Lights", "Sodium Glow", "Wreckage", "Steady Hands", "Gull Cry",
		"Tin Roof", "Ember Coast", "Last Ferry"}},
	{id: "920005", title: "Orchard Lights", artist: artists[2], year: 2024, bg: "3d2c1e", fg: "e9c46a", tracks: []string{
		"Orchard Lights", "Velvet Hour", "Brass Moon", "Late Bloom", "Copper Rain", "Quiet Waltz",
		"Amber Room", "Last Call at Nine"}},
	{id: "920006", title: "Late Bloom", artist: artists[2], year: 2026, bg: "264653", fg: "f4d35e", tracks: []string{
		"Late Bloom", "Garden Steps", "Under Lanterns", "Fig Tree", "Evening Rounds"}},
	{id: "920007", title: "夏夜信号", artist: artists[3], year: 2025, bg: "14213d", fg: "fca311", tracks: []string{
		"夏夜信号", "便利店灯光", "天台", "末班车", "汽水", "晚风收音机", "霓虹", "海报", "慢慢走", "再见夏天"}},
	{id: "920008", title: "海边的耳机", artist: artists[3], year: 2023, bg: "1b4332", fg: "95d5b2", tracks: []string{
		"海边的耳机", "潮汐表", "白色衬衫", "灯塔", "冰柠檬", "防波堤", "落日", "空房间", "回程"}},
}

// playlist is a made-up playlist, its cover an album's.
type playlist struct {
	id, name, curator, cover string
}

var playlists = []*playlist{
	{id: "pl.demo0001", name: "Glass and Neon", curator: "AMDL Editors", cover: "920001"},
	{id: "pl.demo0002", name: "Coastal Nights", curator: "Nightjar Coast", cover: "920003"},
	{id: "pl.demo0003", name: "Orchard Sessions", curator: "The Velvet Orchard", cover: "920005"},
	{id: "pl.demo0004", name: "夏天的电台", curator: "青柠电台", cover: "920007"},
	{id: "pl.demo0005", name: "Signals After Dark", curator: "AMDL Editors", cover: "920002"},
	{id: "pl.demo0006", name: "Slow Lightning Radio", curator: "Mira Solenne", cover: "920004"},
}

func (p *playlist) url() string {
	return "https://music.apple.com/us/playlist/" + slug(p.name) + "/" + p.id
}

func (p *playlist) hit() client.SearchHit {
	a := findAlbum(p.cover)
	return client.SearchHit{
		ID: p.id, Type: "playlist", Name: p.name, URL: p.url(), ArtworkURL: a.artwork(),
		ArtworkBgColor: a.bg, ArtworkTextColor1: a.fg, ArtworkTextColor2: "ffffff",
		ArtistName: p.curator, AudioTraits: []string{"lossless"},
	}
}

// traits are the album's audio traits: two albums come in Dolby Atmos too,
// so a download of them asks which to take.
func (a *album) traits() []string {
	if a.id == "920001" || a.id == "920003" {
		return []string{"atmos", "lossless", "lossy-stereo", "spatial"}
	}
	return []string{"lossless", "lossy-stereo"}
}

// songID is a song's catalog id: its album's and its position.
func songID(a *album, i int) string { return fmt.Sprintf("%s%02d", a.id, i+1) }

func findArtist(id string) *artist {
	for _, a := range artists {
		if a.id == id {
			return a
		}
	}
	return nil
}

func findAlbum(id string) *album {
	for _, a := range albums {
		if a.id == id {
			return a
		}
	}
	return nil
}

func findSong(id string) (song, bool) {
	for _, a := range albums {
		for i := range a.tracks {
			if songID(a, i) == id {
				return song{id: id, album: a, index: i}, true
			}
		}
	}
	return song{}, false
}

func (a *album) url() string { return "https://music.apple.com/us/album/" + slug(a.title) + "/" + a.id }
func (a *artist) url() string {
	return "https://music.apple.com/us/artist/" + slug(a.name) + "/" + a.id
}
func (s song) url() string { return s.album.url() + "?i=" + s.id }

func (a *album) releaseDate() string { return fmt.Sprintf("%d-%02d-14", a.year, 3+len(a.tracks)%9) }

// artwork is the cover's URL template, served by this package (cover).
func (a *album) artwork() string { return Address + "/art/" + a.id + "/{w}x{h}bb.{f}" }

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "album"
	}
	return b.String()
}

// durationMS is a song's length: three to five minutes, fixed per song.
func durationMS(id string) int { return 180_000 + int(hash(id)%120_000) }

func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

func (a *album) hit() client.SearchHit {
	return client.SearchHit{
		ID: a.id, Type: "album", Name: a.title, URL: a.url(), ArtworkURL: a.artwork(),
		ArtworkBgColor: a.bg, ArtworkTextColor1: a.fg, ArtworkTextColor2: "ffffff",
		ArtistName: a.artist.name, ReleaseDate: a.releaseDate(), Genre: a.artist.genre,
		TrackCount: len(a.tracks), AudioTraits: a.traits(),
	}
}

func (s song) hit() client.SearchHit {
	a := s.album
	return client.SearchHit{
		ID: s.id, Type: "song", Name: a.tracks[s.index], URL: s.url(), ArtworkURL: a.artwork(),
		ArtworkBgColor: a.bg, ArtworkTextColor1: a.fg, ArtworkTextColor2: "ffffff",
		ArtistName: a.artist.name, AlbumName: a.title, ReleaseDate: a.releaseDate(), Genre: a.artist.genre,
		DurationMS: durationMS(s.id), AudioTraits: a.traits(),
	}
}

func (a *artist) hit() client.SearchHit {
	cover := a.albums()[0]
	return client.SearchHit{
		ID: a.id, Type: "artist", Name: a.name, URL: a.url(), ArtworkURL: cover.artwork(),
		ArtworkBgColor: cover.bg, ArtworkTextColor1: cover.fg, Genre: a.genre,
	}
}

func (a *artist) albums() []*album {
	var out []*album
	for _, al := range albums {
		if al.artist == a {
			out = append(out, al)
		}
	}
	return out
}

func (a *album) detail() client.Album {
	out := client.Album{
		Storefront: "us", ID: a.id, URL: a.url(), Title: a.title, ArtistName: a.artist.name, ArtistURL: a.artist.url(),
		ArtworkURL: a.artwork(), ArtworkBgColor: a.bg, ArtworkTextColor1: a.fg, ArtworkTextColor2: "ffffff",
		ReleaseDate: a.releaseDate(), Genre: a.artist.genre, TrackCount: len(a.tracks), DiscCount: 1,
		AudioTraits: a.traits(),
	}
	for i, name := range a.tracks {
		id := songID(a, i)
		out.Tracks = append(out.Tracks, client.AlbumTrack{
			ID: id, URL: song{id: id, album: a, index: i}.url(), DiscNumber: 1, TrackNumber: i + 1,
			Name: name, ArtistName: a.artist.name, DurationMS: durationMS(id), AudioTraits: a.traits(),
		})
	}
	return out
}

// coverMax bounds the generated covers' side; the views scale larger ones
// down anyway.
const coverMax = 600

// cover draws an album's cover at px pixels: its two colors in a diagonal
// gradient, with a disc of the second whose place follows the album, as PNG.
func cover(a *album, px int) []byte {
	px = min(max(px, 16), coverMax)
	bg, fg := hexColor(a.bg), hexColor(a.fg)
	h := hash(a.id)
	cx, cy := 0.3+float64(h%40)/100, 0.3+float64(h/40%40)/100
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	for y := range px {
		for x := range px {
			u, v := float64(x)/float64(px), float64(y)/float64(px)
			t := (u + v) / 2
			c := mix(bg, darken(bg, 0.55), t)
			d := math.Hypot(u-cx, v-cy)
			// A two-percent edge keeps the disc's rim smooth at every size.
			if in := 1 - smooth(0.21, 0.23, d); in > 0 {
				c = mix(c, fg, in)
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	// Encoding an image of this package's own making cannot fail.
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func hexColor(s string) color.NRGBA {
	var r, g, b uint8
	// The catalog's colors are six hex digits.
	_, _ = fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b)
	return color.NRGBA{r, g, b, 255}
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

func darken(c color.NRGBA, f float64) color.NRGBA {
	return color.NRGBA{uint8(float64(c.R) * f), uint8(float64(c.G) * f), uint8(float64(c.B) * f), 255}
}

func smooth(lo, hi, x float64) float64 {
	t := min(max((x-lo)/(hi-lo), 0), 1)
	return t * t * (3 - 2*t)
}
