package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"amdl/internal/client"
)

// ServerEvery is how often the server's state is read while a view shows
// it.
const ServerEvery = 10 * time.Second

// StatsDays is the window of the server's download statistics.
const StatsDays = 14

// activeSamples is how many readings of the busy worker count the system
// screen charts.
const activeSamples = 12

// Server is what the backend reports about itself. A part the backend did
// not answer for stays nil, with its error beside it when it failed.
type Server struct {
	Health  *client.Health
	System  *client.SystemInfo
	Wrapper *client.WrapperStatus
	Stats   *client.Stats
	Sync    *client.LibrarySync
	Hooks   *client.Hooks
	// HookPresets are the services a webhook can be built for, read once
	// per connection: they change only with the backend's version.
	HookPresets []client.HookPreset
	// Startup is what the backend read only when it started, from /system.
	Startup *Startup
	// Errors are the error logs of the last day, or nil.
	Errors    *ErrorLogs
	HealthErr string
	// WrapperErr says why the wrapper's status could not be read.
	WrapperErr string
	// Read is when the last read finished.
	Read time.Time
	// Active are the last readings of System.Jobs.Active, oldest first: the
	// API keeps no history of it.
	Active []int

	reading bool
	// stale asks for a read before ServerEvery is up, after a change the
	// state shows; the last read stays on screen meanwhile.
	stale bool
}

// refreshServer reads the server's state again on the next WatchServer,
// keeping the last read on screen until the new one lands.
func (s *Store) refreshServer() { s.Server.stale = true }

// WatchServer reads the server's state when the last read is older than
// ServerEvery. Views that show it call it every frame and redraw after
// ServerEvery, so it reads only while one of them is on screen.
func (s *Store) WatchServer() { s.WatchServerWithin(ServerEvery) }

// WatchServerWithin is WatchServer for a view that follows a change, such as
// a booting wrapper, and reads when the last read is older than every.
func (s *Store) WatchServerWithin(every time.Duration) {
	sv := &s.Server
	if s.client == nil || sv.reading || (!sv.stale && s.now().Sub(sv.Read) < every) {
		return
	}
	sv.reading, sv.stale = true, false
	c, ctx := s.client, s.ctx
	presets := sv.HookPresets
	go func() {
		next := Server{HookPresets: presets}
		var wg sync.WaitGroup
		read := func(fn func(context.Context)) {
			wg.Add(1)
			go func() { defer wg.Done(); fn(ctx) }()
		}
		read(func(ctx context.Context) {
			if h, err := c.Health(ctx); err == nil {
				next.Health = &h
			} else {
				next.HealthErr = err.Error()
			}
		})
		read(func(ctx context.Context) {
			var v struct {
				client.SystemInfo
				Startup *Startup `json:"startup"`
			}
			if raw, err := c.Call(ctx, http.MethodGet, "/api/v1/system", nil, nil); err == nil && json.Unmarshal(raw, &v) == nil {
				next.System, next.Startup = &v.SystemInfo, v.Startup
			}
		})
		read(func(ctx context.Context) { next.Errors = readErrors(ctx, c, s.now()) })
		read(func(ctx context.Context) {
			if v, err := c.WrapperStatus(ctx); err == nil {
				next.Wrapper = &v
			} else {
				next.WrapperErr = err.Error()
			}
		})
		// Backends older than these endpoints answer 404; their cards then
		// say nothing rather than fail the screen.
		read(func(ctx context.Context) {
			if v, err := c.Stats(ctx, StatsDays); err == nil {
				next.Stats = &v
			}
		})
		read(func(ctx context.Context) {
			if v, err := c.LibrarySync(ctx); err == nil {
				next.Sync = &v
			}
		})
		read(func(ctx context.Context) {
			if v, err := c.Hooks(ctx); err == nil {
				next.Hooks = &v
			}
		})
		if presets == nil {
			read(func(ctx context.Context) {
				if v, err := c.HookPresets(ctx); err == nil {
					next.HookPresets = v
				}
			})
		}
		wg.Wait()
		s.postIf(ctx, func() {
			// A change made while this read ran asks for one more.
			next.Read, next.Active, next.stale = s.now(), sv.Active, sv.stale
			if next.System != nil {
				next.Active = append(next.Active, next.System.Jobs.Active)
				if len(next.Active) > activeSamples {
					next.Active = next.Active[len(next.Active)-activeSamples:]
				}
			}
			*sv = next
		})
	}()
}

// Startup is /system's startup block: configuration the backend reads only
// when it starts, which Settings shows as facts.
type Startup struct {
	WrapperAddress      string `json:"wrapper_address"`
	Listen              string `json:"listen"`
	DatabasePath        string `json:"database_path"`
	LogBufferSize       int    `json:"log_buffer_size"`
	CatalogLanguage     string `json:"catalog_language"`
	MaxParallelRequests int    `json:"max_parallel_requests"`
}

// ErrorLogs are the backend's error logs of the last day.
type ErrorLogs struct {
	// Times are when each error was logged, oldest first; Full means there
	// were more than errorLimit.
	Times []time.Time
	Full  bool
	// Last is the newest one's message.
	Last string
	// Off means the backend keeps no log history (logging.buffer_size 0).
	Off bool
}

// errorLimit bounds the errors one read lists.
const errorLimit = 200

func readErrors(ctx context.Context, c *client.Client, now time.Time) *ErrorLogs {
	q := url.Values{"level": {"error"}, "since": {now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)}, "limit": {strconv.Itoa(errorLimit)}}
	raw, err := c.Call(ctx, http.MethodGet, "/api/v1/logs", q, nil)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "feature_unavailable" {
		return &ErrorLogs{Off: true}
	}
	var v struct {
		Logs []struct {
			Time    time.Time `json:"time"`
			Message string    `json:"message"`
		} `json:"logs"`
	}
	if err != nil || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	e := &ErrorLogs{Full: len(v.Logs) >= errorLimit}
	for _, l := range v.Logs {
		e.Times = append(e.Times, l.Time)
		e.Last = l.Message
	}
	return e
}

// DownloadsStorage is the free space of the backend's downloads folder,
// or nil.
func (sv *Server) DownloadsStorage() *client.Storage { return sv.storage("downloads_dir") }

// TempStorage is the free space of the backend's temporary folder, or nil.
func (sv *Server) TempStorage() *client.Storage { return sv.storage("temp_dir") }

func (sv *Server) storage(name string) *client.Storage {
	if sv.System == nil {
		return nil
	}
	for i := range sv.System.Storage {
		if sv.System.Storage[i].Name == name {
			return &sv.System.Storage[i]
		}
	}
	return nil
}
