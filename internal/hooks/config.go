package hooks

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root of the standalone hooks.yaml file. It is loaded
// separately from configs/config.yaml so hook entries can be added, removed,
// or toggled without touching the main backend configuration.
type Config struct {
	Enabled        bool    `yaml:"enabled"`
	TimeoutSeconds int     `yaml:"timeout_seconds"`
	MaxConcurrent  int     `yaml:"max_concurrent"`
	Entries        []Entry `yaml:"entries"`
}

// Entry is one hook definition. Enabled and SendPayload use pointers so an
// omitted field defaults to true while an explicit `false` in the file is
// still honored.
type Entry struct {
	Name           string            `yaml:"name"`
	Enabled        *bool             `yaml:"enabled"`
	Type           string            `yaml:"type"`
	Events         []string          `yaml:"events"`
	JobTypes       []string          `yaml:"job_types"`
	URL            string            `yaml:"url"`
	Headers        map[string]string `yaml:"headers"`
	SendPayload    *bool             `yaml:"send_payload"`
	MaxAttempts    int               `yaml:"max_attempts"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	Command        string            `yaml:"command"`
	Workdir        string            `yaml:"workdir"`
}

func (e Entry) IsEnabled() bool {
	return e.Enabled == nil || *e.Enabled
}

func (e Entry) SendsPayload() bool {
	return e.SendPayload == nil || *e.SendPayload
}

func (e Entry) Timeout(fallback time.Duration) time.Duration {
	if e.TimeoutSeconds <= 0 {
		return fallback
	}
	return time.Duration(e.TimeoutSeconds) * time.Second
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
	return Config{Enabled: false, TimeoutSeconds: 30, MaxConcurrent: 2}
}

func (c Config) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

func (c Config) Concurrency() int {
	if c.MaxConcurrent <= 0 {
		return 2
	}
	return c.MaxConcurrent
}

// LoadConfig reads and validates the hooks config file at path. A missing
// file is not an error: it is treated the same as an empty, disabled config
// so the backend runs with hooks off until the operator opts in.
func LoadConfig(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
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

var allowedEvents = map[string]struct{}{
	"job_queued":    {},
	"job_finished":  {},
	"job_failed":    {},
	"job_cancelled": {},
}

var allowedJobTypes = map[string]struct{}{
	"song": {}, "album": {}, "playlist": {}, "artist": {},
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

		if _, ok := runners[e.Type]; !ok {
			return fmt.Errorf("%s: type must be one of %s", label, strings.Join(registeredTypes(), ", "))
		}
		if len(e.Events) == 0 {
			return fmt.Errorf("%s: events must list at least one of %s", label, strings.Join(sortedKeys(allowedEvents), ", "))
		}
		for _, ev := range e.Events {
			if _, ok := allowedEvents[ev]; !ok {
				return fmt.Errorf("%s: unsupported event %q, must be one of %s", label, ev, strings.Join(sortedKeys(allowedEvents), ", "))
			}
		}
		for _, jt := range e.JobTypes {
			if _, ok := allowedJobTypes[jt]; !ok {
				return fmt.Errorf("%s: unsupported job type %q, must be one of %s", label, jt, strings.Join(sortedKeys(allowedJobTypes), ", "))
			}
		}
		switch e.Type {
		case "webhook":
			if strings.TrimSpace(e.URL) == "" {
				return fmt.Errorf("%s: url is required for type webhook", label)
			}
		case "exec":
			if strings.TrimSpace(e.Command) == "" {
				return fmt.Errorf("%s: command is required for type exec", label)
			}
		}
		// max_attempts is not validated here: values <= 0 (including
		// negatives) behave as a single attempt, matching the documented
		// semantics and download.max_attempts.
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
