package config

import (
	"encoding/json"
	"slices"
)

// MutableView returns only the runtime-changeable part of cfg — the shape
// GET/PUT /api/v1/config exchange with clients, which have no use for the
// startup-bound fields the update endpoint refuses to change anyway. The
// download section is derived from its json tags so a field added to
// DownloadConfig shows up here automatically; max_running_jobs is dropped to
// stay in sync with RuntimeLockedChanges below.
func MutableView(cfg Config) map[string]any {
	raw, _ := json.Marshal(cfg.Download)
	download := map[string]any{}
	_ = json.Unmarshal(raw, &download)
	delete(download, "max_running_jobs")
	return map[string]any{
		"catalog":  map[string]any{"album_track_url_mode": cfg.Catalog.AlbumTrackURLMode},
		"download": download,
		"simulate": cfg.Simulate,
	}
}

// RuntimeLockedChanges returns the dotted keys of fields that differ between
// old and updated but are consumed only at process startup (listen address,
// database path, wrapper connection, catalog client/token signing, worker
// pool size, tool paths). Changing them through the runtime config API would
// silently do nothing, so PUT /api/v1/config rejects an update whenever this
// returns a non-empty list. Everything not listed here — the download section
// (minus max_running_jobs), the simulate section, and
// catalog.album_track_url_mode — takes effect immediately for new requests
// and newly started jobs.
func RuntimeLockedChanges(old, updated Config) []string {
	var changed []string
	lock := func(key string, differs bool) {
		if differs {
			changed = append(changed, key)
		}
	}
	lock("server.listen", old.Server.Listen != updated.Server.Listen)
	lock("database.path", old.Database.Path != updated.Database.Path)
	lock("wrapper.address", old.Wrapper.Address != updated.Wrapper.Address)
	lock("wrapper.insecure", old.Wrapper.Insecure != updated.Wrapper.Insecure)
	lock("wrapper.timeout_seconds", old.Wrapper.TimeoutSeconds != updated.Wrapper.TimeoutSeconds)
	lock("wrapper.login_timeout_seconds", old.Wrapper.LoginTimeoutSeconds != updated.Wrapper.LoginTimeoutSeconds)
	lock("catalog.default_storefront", old.Catalog.DefaultStorefront != updated.Catalog.DefaultStorefront)
	lock("catalog.language", old.Catalog.Language != updated.Catalog.Language)
	lock("catalog.apple_music_private_key_path", old.Catalog.AppleMusicPrivateKeyPath != updated.Catalog.AppleMusicPrivateKeyPath)
	lock("catalog.apple_music_key_id", old.Catalog.AppleMusicKeyID != updated.Catalog.AppleMusicKeyID)
	lock("catalog.apple_music_team_id", old.Catalog.AppleMusicTeamID != updated.Catalog.AppleMusicTeamID)
	lock("catalog.developer_token_ttl_hours", old.Catalog.DeveloperTokenTTLHours != updated.Catalog.DeveloperTokenTTLHours)
	lock("catalog.allowed_origins", !slices.Equal(old.Catalog.AllowedOrigins, updated.Catalog.AllowedOrigins))
	lock("catalog.token_cache_ttl_hours", old.Catalog.TokenCacheTTLHours != updated.Catalog.TokenCacheTTLHours)
	lock("download.max_running_jobs", old.Download.MaxRunningJobs != updated.Download.MaxRunningJobs)
	lock("tools.ffmpeg", old.Tools.FFmpeg != updated.Tools.FFmpeg)
	return changed
}

// preserveRuntimeLocked returns loaded with every startup-bound field forced
// back to current's values, leaving only the runtime-mutable part (download
// minus max_running_jobs, simulate, catalog.album_track_url_mode) from
// loaded. Store.Reload uses it so a manual file edit to a locked field never
// half-applies to a running process. Must cover the same field set as
// RuntimeLockedChanges above.
func preserveRuntimeLocked(loaded, current Config) Config {
	albumTrackURLMode := loaded.Catalog.AlbumTrackURLMode
	loaded.Server = current.Server
	loaded.Database = current.Database
	loaded.Wrapper = current.Wrapper
	loaded.Tools = current.Tools
	loaded.Catalog = current.Catalog
	loaded.Catalog.AlbumTrackURLMode = albumTrackURLMode
	loaded.Download.MaxRunningJobs = current.Download.MaxRunningJobs
	return loaded
}
