package hooks

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"amdl/internal/config"
	"amdl/internal/domain"

	"gopkg.in/yaml.v3"
)

// Config is the root of the standalone hooks.yaml file. It is loaded
// separately from configs/config.yaml so hook entries can be added, removed,
// or toggled without touching the main backend configuration.
type Config struct {
	Enabled bool    `yaml:"enabled"`
	Entries []Entry `yaml:"entries"`
}

// hookTimeout bounds every attempt of every hook, and maxConcurrent bounds the
// hook executions running at once across all jobs and entries. They are fixed
// rather than configurable: one install serves one user.
const (
	hookTimeout   = 30 * time.Second
	maxConcurrent = 2
)

// Hook entry types. validate and Dispatcher.runnerFor switch over the same two.
const (
	typeWebhook = "webhook"
	typeExec    = "exec"
)

// Entry is one hook definition. Enabled uses a pointer so an omitted field
// defaults to true while an explicit `false` in the file is still honored.
// Method and Body shape a webhook's request (webhookRunner): POST with the
// JSON payload unless they say otherwise. omitempty keeps the entries Editor
// writes to the keys they set.
type Entry struct {
	Name        string            `yaml:"name"`
	Enabled     *bool             `yaml:"enabled,omitempty"`
	Type        string            `yaml:"type"`
	Events      []string          `yaml:"events,flow"`
	JobTypes    []string          `yaml:"job_types,omitempty,flow"`
	URL         string            `yaml:"url,omitempty"`
	Method      string            `yaml:"method,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Body        string            `yaml:"body,omitempty"`
	MaxAttempts int               `yaml:"max_attempts,omitempty"`
	Command     string            `yaml:"command,omitempty"`
	Workdir     string            `yaml:"workdir,omitempty"`
}

func (e Entry) IsEnabled() bool {
	return e.Enabled == nil || *e.Enabled
}

func (e Entry) MatchesEvent(event string) bool {
	for _, ev := range e.Events {
		if ev == event {
			return true
		}
	}
	return false
}

func (e Entry) MatchesJobType(jobType string) bool {
	if len(e.JobTypes) == 0 {
		return true
	}
	for _, t := range e.JobTypes {
		if t == jobType {
			return true
		}
	}
	return false
}

func Default() Config {
	return Config{}
}

// LoadConfig reads and validates the hooks config file at path. A missing
// file is not an error: it is treated the same as an empty, disabled config
// so the backend runs with hooks off until the operator opts in.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Default(), err
	}
	return parseConfig(raw)
}

// parseConfig decodes and validates the contents of a hooks.yaml.
func parseConfig(raw []byte) (Config, error) {
	cfg := Default()
	if removed := removedKeys(raw); len(removed) > 0 {
		return cfg, fmt.Errorf("removed hooks key(s) %s; every hook now gets the payload and runs with a fixed %s timeout and at most %d hooks at once, see %s",
			strings.Join(removed, ", "), hookTimeout, maxConcurrent, config.UpgradeGuide)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// removedKeys lists the keys an older hooks.yaml may hold that no longer exist,
// so the startup error names them and points to the upgrade guide instead of
// the decoder's bare "field not found". Entries are named by hook name, or by
// position when the name is missing. A document that does not parse yields nil
// and the typed decoder reports it.
func removedKeys(raw []byte) []string {
	var doc struct {
		Entries []map[string]any `yaml:"entries"`
	}
	var top map[string]any
	if yaml.Unmarshal(raw, &top) != nil || yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var removed []string
	for _, key := range []string{"timeout_seconds", "max_concurrent"} {
		if _, ok := top[key]; ok {
			removed = append(removed, key)
		}
	}
	for i, entry := range doc.Entries {
		label := fmt.Sprintf("entries[%d]", i)
		if name, ok := entry["name"].(string); ok && name != "" {
			label = fmt.Sprintf("entries[%q]", name)
		}
		for _, key := range []string{"send_payload", "timeout_seconds"} {
			if _, ok := entry[key]; ok {
				removed = append(removed, label+"."+key)
			}
		}
	}
	return removed
}

// allowedEvents are the hook event names, which are the event types of the
// job lifecycle events that trigger them.
var allowedEvents = map[string]struct{}{
	string(domain.EventJobQueued):    {},
	string(domain.EventJobFinished):  {},
	string(domain.EventJobFailed):    {},
	string(domain.EventJobCancelled): {},
}

var allowedJobTypes = map[string]struct{}{
	"song": {}, "album": {}, "playlist": {}, "artist": {}, "station": {},
}

func (c Config) validate() error {
	seenNames := map[string]bool{}
	for i, e := range c.Entries {
		label := fmt.Sprintf("hooks.entries[%d]", i)
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("%s: name cannot be empty", label)
		}
		label = fmt.Sprintf("hooks.entries[%q]", e.Name)
		if seenNames[e.Name] {
			return fmt.Errorf("%s: duplicate hook name", label)
		}
		seenNames[e.Name] = true

		if e.Type != typeWebhook && e.Type != typeExec {
			return fmt.Errorf("%s: type must be one of %s, %s", label, typeWebhook, typeExec)
		}
		if len(e.Events) == 0 {
			return fmt.Errorf("%s: events must list at least one of %s", label, strings.Join(sortedKeys(allowedEvents), ", "))
		}
		hasCreation, hasTerminal := false, false
		for _, ev := range e.Events {
			if _, ok := allowedEvents[ev]; !ok {
				return fmt.Errorf("%s: unsupported event %q, must be one of %s", label, ev, strings.Join(sortedKeys(allowedEvents), ", "))
			}
			if ev == string(domain.EventJobQueued) {
				hasCreation = true
			} else {
				hasTerminal = true
			}
		}
		// A single entry firing on both creation and a terminal event would run
		// twice for the same job, but the hook lifecycle events (and the
		// GET /downloads/{id} snapshot's SummarizeHooks) are keyed by hook name
		// alone, so the second run's result would overwrite the first's. Keep
		// one execution per entry per job: split creation and terminal hooks
		// into separate entries (they may share a URL/command).
		if hasCreation && hasTerminal {
			return fmt.Errorf("%s: job_queued (creation) cannot be combined with terminal events in one hook; define a separate entry for each", label)
		}
		for _, jt := range e.JobTypes {
			if _, ok := allowedJobTypes[jt]; !ok {
				return fmt.Errorf("%s: unsupported job type %q, must be one of %s", label, jt, strings.Join(sortedKeys(allowedJobTypes), ", "))
			}
		}
		switch e.Type {
		case typeWebhook:
			if strings.TrimSpace(e.URL) == "" {
				return fmt.Errorf("%s: url is required for type webhook", label)
			}
			if err := validateWebhookURL(e.URL); err != nil {
				return fmt.Errorf("%s: url: %w", label, err)
			}
			switch e.Method {
			case "", http.MethodGet, http.MethodPost, http.MethodPut:
			default:
				return fmt.Errorf("%s: method must be one of GET, POST, PUT", label)
			}
			if e.Method == http.MethodGet && e.Body != "" {
				return fmt.Errorf("%s: a GET request has no body", label)
			}
			if _, err := bodyTemplate(e.Body); err != nil {
				return fmt.Errorf("%s: body: %w", label, err)
			}
		case typeExec:
			if strings.TrimSpace(e.Command) == "" {
				return fmt.Errorf("%s: command is required for type exec", label)
			}
			if e.Method != "" || e.Body != "" {
				return fmt.Errorf("%s: method and body are for type webhook", label)
			}
		}
		// max_attempts is not validated here: values <= 0 (including
		// negatives) behave as a single attempt, matching the documented
		// semantics and download.max_attempts.
	}
	return nil
}

// validateWebhookURL accepts only an absolute http or https URL with a host,
// so a typo fails startup instead of every run of the hook.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q must be an absolute http or https URL", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
