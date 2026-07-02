# Classical Domain Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept `classical.music.apple.com` links for albums, songs, playlists, and artists while continuing to reject Classical music-video links.

**Architecture:** Extend the existing parser's explicit host allowlist, then apply a host-specific media-type restriction before returning a parsed result. Keep catalog and downloader behavior unchanged because accepted links already reduce to the existing storefront, URL type, and catalog ID model.

**Tech Stack:** Go standard library (`net/url`, `testing`) and the existing `internal/applemusic` parser.

## Global Constraints

- Modify production code only in the root Go module; `explore/apple-music-downloader-main` remains read-only.
- Classical supports only `album`, `song`, `playlist`, and `artist` URL types.
- Classical `music-video` URLs remain unsupported.
- Existing `music.apple.com` and `beta.music.apple.com` behavior must remain unchanged.
- No configuration, API, database, or migration changes.

---

## File Structure

- Modify `internal/applemusic/url.go`: extend explicit host validation and enforce the Classical media-type boundary.
- Modify `internal/applemusic/url_test.go`: specify accepted Classical parsing behavior and rejected-host/type behavior.

### Task 1: Parse Supported Classical URLs

**Files:**
- Modify: `internal/applemusic/url.go:28-66`
- Test: `internal/applemusic/url_test.go`

**Interfaces:**
- Consumes: `ParseWithAlbumTrackMode(raw string, albumTrackURLMode string) (ParsedURL, error)` and the existing `URLType` constants.
- Produces: unchanged parser interface with support for `classical.music.apple.com` on album, song, playlist, and artist URLs.

- [ ] **Step 1: Add failing Classical parser tests**

Append these tests to `internal/applemusic/url_test.go`:

```go
func TestParseClassicalSupportedURLs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		mode string
		want ParsedURL
	}{
		{name: "album", raw: "https://classical.music.apple.com/us/album/foo/123456789", mode: "song", want: ParsedURL{Storefront: "us", Type: TypeAlbum, ID: "123456789"}},
		{name: "album track as song", raw: "https://classical.music.apple.com/us/album/foo/123456789?i=987654321", mode: "song", want: ParsedURL{Storefront: "us", Type: TypeSong, ID: "987654321"}},
		{name: "album track as album", raw: "https://classical.music.apple.com/us/album/foo/123456789?i=987654321", mode: "album", want: ParsedURL{Storefront: "us", Type: TypeAlbum, ID: "123456789"}},
		{name: "song", raw: "https://classical.music.apple.com/gb/song/foo/987654321", mode: "song", want: ParsedURL{Storefront: "gb", Type: TypeSong, ID: "987654321"}},
		{name: "playlist", raw: "https://classical.music.apple.com/jp/playlist/foo/pl.abcdef", mode: "song", want: ParsedURL{Storefront: "jp", Type: TypePlaylist, ID: "pl.abcdef"}},
		{name: "artist", raw: "https://classical.music.apple.com/de/artist/foo/123456789", mode: "song", want: ParsedURL{Storefront: "de", Type: TypeArtist, ID: "123456789"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWithAlbumTrackMode(tt.raw, tt.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got.Storefront != tt.want.Storefront || got.Type != tt.want.Type || got.ID != tt.want.ID {
				t.Fatalf("unexpected parse result: %+v", got)
			}
		})
	}
}

func TestParseClassicalRejectsMusicVideo(t *testing.T) {
	_, err := ParseWithAlbumTrackMode("https://classical.music.apple.com/us/music-video/foo/123456789", "song")
	if err == nil {
		t.Fatal("expected Classical music-video URL to be rejected")
	}
}

func TestParseRejectsUnknownAppleMusicHost(t *testing.T) {
	_, err := ParseWithAlbumTrackMode("https://unknown.music.apple.com/us/album/foo/123456789", "song")
	if err == nil {
		t.Fatal("expected unknown host to be rejected")
	}
}
```

- [ ] **Step 2: Run focused tests and verify the acceptance test fails**

Run:

```bash
go test ./internal/applemusic -run 'TestParseClassical|TestParseRejectsUnknownAppleMusicHost' -v
```

Expected: `TestParseClassicalSupportedURLs` fails with `unsupported host "classical.music.apple.com"`; both rejection tests pass.

- [ ] **Step 3: Implement minimal host and type validation**

In `internal/applemusic/url.go`, add below `storefrontPattern`:

```go
const (
	appleMusicHost          = "music.apple.com"
	betaAppleMusicHost      = "beta.music.apple.com"
	classicalAppleMusicHost = "classical.music.apple.com"
)
```

Replace the existing host condition with:

```go
	if u.Host != appleMusicHost && u.Host != betaAppleMusicHost && u.Host != classicalAppleMusicHost {
		return ParsedURL{}, fmt.Errorf("unsupported host %q", u.Host)
	}
```

After `kind := URLType(parts[1])`, add:

```go
	if u.Host == classicalAppleMusicHost && kind == TypeVideo {
		return ParsedURL{}, fmt.Errorf("unsupported Apple Music Classical URL type %q", kind)
	}
```

- [ ] **Step 4: Run parser tests and verify they pass**

Run:

```bash
go test ./internal/applemusic -v
```

Expected: PASS, including all Classical cases and all pre-existing parser cases.

- [ ] **Step 5: Format and run the complete Go test suite**

Run:

```bash
gofmt -w internal/applemusic/url.go internal/applemusic/url_test.go
go test ./...
```

Expected: formatting produces no unintended changes and every package reports PASS or `[no test files]` with no failures.

- [ ] **Step 6: Review and commit the implementation**

Run:

```bash
git diff --check
git diff -- internal/applemusic/url.go internal/applemusic/url_test.go
git add internal/applemusic/url.go internal/applemusic/url_test.go
git commit -m "feat: support Apple Music Classical URLs"
```

Expected: the diff contains only the parser and parser-test changes described above, and the commit succeeds.
