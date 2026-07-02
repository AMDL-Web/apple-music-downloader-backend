# Classical Domain Support Design

## Goal

Accept Apple Music Classical links hosted at `classical.music.apple.com` while preserving the parser's current storefront, media type, and identifier semantics.

## Supported Behavior

The URL parser will accept the Classical host for these existing URL types:

- `album`
- `song`
- `playlist`
- `artist`

Album links containing a track query parameter (`?i=<song-id>`) will continue to follow `album_track_url_mode`: they resolve as a song in `song` mode and as the album in `album` mode.

Classical `music-video` links will remain unsupported. This matches Reference B (`explore/apple-music-downloader-main`), where Classical is included in the album, song, playlist, and artist patterns but excluded from the music-video pattern.

Existing support for `music.apple.com` and `beta.music.apple.com` remains unchanged. Unknown hosts remain rejected.

## Implementation

Keep the change inside the root Go module. Update `internal/applemusic/url.go` so host validation distinguishes the Classical host and rejects unsupported URL types for that host. Do not modify Reference B.

No catalog or download transport changes are needed: parsed Classical links use the same storefront, media IDs, and `amp-api.music.apple.com` catalog flow as regular Apple Music links.

## Error Handling

Malformed URLs, invalid storefronts, unknown types, and unknown hosts retain their existing errors. A Classical `music-video` URL returns an unsupported-type error and does not proceed to catalog lookup.

## Tests

Add parser tests before production changes to verify:

- Classical album parsing.
- Classical album-track parsing in song mode.
- Classical song, playlist, and artist parsing.
- Rejection of Classical music-video links.
- Continued rejection of unknown Apple Music-like hosts.

Run the focused `internal/applemusic` tests, then the complete Go test suite.

## Migration Impact

There are no configuration, API, or database schema changes. Existing jobs and stored data require no migration.
