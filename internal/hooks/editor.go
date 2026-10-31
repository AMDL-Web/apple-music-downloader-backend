package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"amdl/internal/domain"
)

// Editor changes hooks.yaml from the API: the master switch and the webhook
// entries, one at a time. Command (exec) entries are only read: adding,
// changing, removing or running one through the API is refused (ErrReadOnly),
// so a command runs only where someone wrote it into the file. Each write
// re-reads the file, edits its YAML tree in place so the other entries and
// the comments stay, validates the result as a startup would, writes it and
// hands it to the dispatcher, so it applies to the next event.
type Editor struct {
	path string
	d    *Dispatcher
	mu   sync.Mutex
}

// NewEditor returns an Editor of the hooks.yaml at path that applies each
// write to d.
func NewEditor(path string, d *Dispatcher) *Editor {
	return &Editor{path: path, d: d}
}

var (
	// ErrNotFound is a name no entry has.
	ErrNotFound = errors.New("no hook has this name")
	// ErrReadOnly is a write or test of a command hook.
	ErrReadOnly = errors.New("command hooks are edited in hooks.yaml itself")
)

// FileError is a hooks.yaml on disk that does not load; nothing is written
// until it is fixed.
type FileError struct{ Err error }

func (e *FileError) Error() string { return "hooks.yaml does not load: " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// InvalidError is a write whose result would not load.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// WebhookPatch is a write to one webhook entry. A nil field keeps the
// entry's value; a new entry starts from none.
type WebhookPatch struct {
	Enabled     *bool
	Events      *[]string
	JobTypes    *[]string
	MaxAttempts *int
	Method      *string
	URL         *string
	Headers     *map[string]string
	Body        *string
}

func (p WebhookPatch) apply(e *Entry) {
	if p.Enabled != nil {
		on := *p.Enabled
		e.Enabled = &on
	}
	if p.Events != nil {
		e.Events = append([]string{}, (*p.Events)...)
	}
	if p.JobTypes != nil {
		e.JobTypes = append([]string{}, (*p.JobTypes)...)
	}
	if p.MaxAttempts != nil {
		e.MaxAttempts = *p.MaxAttempts
	}
	if p.Method != nil {
		e.Method = *p.Method
	}
	if p.URL != nil {
		e.URL = *p.URL
	}
	if p.Headers != nil {
		e.Headers = map[string]string{}
		for k, v := range *p.Headers {
			e.Headers[k] = v
		}
	}
	if p.Body != nil {
		e.Body = *p.Body
	}
}

// SetEnabled sets the master switch.
func (ed *Editor) SetEnabled(on bool) (Listing, error) {
	ed.mu.Lock()
	defer ed.mu.Unlock()
	f, err := ed.open()
	if err != nil {
		return Listing{}, err
	}
	value := "false"
	if on {
		value = "true"
	}
	f.set("enabled", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: value})
	cfg, err := ed.save(f)
	if err != nil {
		return Listing{}, err
	}
	return listingOf(cfg), nil
}

// PutWebhook writes the webhook entry name, created when no entry has the
// name.
func (ed *Editor) PutWebhook(name string, p WebhookPatch) (ListedEntry, error) {
	ed.mu.Lock()
	defer ed.mu.Unlock()
	f, err := ed.open()
	if err != nil {
		return ListedEntry{}, err
	}
	entries := f.entries()
	i, item := find(entries, name)
	entry := Entry{Name: name, Type: typeWebhook}
	if item != nil {
		if err := item.Decode(&entry); err != nil {
			return ListedEntry{}, &FileError{Err: err}
		}
		if entry.Type != typeWebhook {
			return ListedEntry{}, ErrReadOnly
		}
	}
	p.apply(&entry)
	var node yaml.Node
	if err := node.Encode(entry); err != nil {
		return ListedEntry{}, err
	}
	if item != nil {
		node.HeadComment, node.LineComment, node.FootComment = item.HeadComment, item.LineComment, item.FootComment
		entries.Content[i] = &node
	} else {
		entries.Content = append(entries.Content, &node)
	}
	cfg, err := ed.save(f)
	if err != nil {
		return ListedEntry{}, err
	}
	_, written := cfg.entry(name)
	return listedEntry(written), nil
}

// Delete removes the webhook entry name.
func (ed *Editor) Delete(name string) error {
	ed.mu.Lock()
	defer ed.mu.Unlock()
	f, err := ed.open()
	if err != nil {
		return err
	}
	entries := f.entries()
	i, item := find(entries, name)
	if item == nil {
		return ErrNotFound
	}
	if typeOf(item) != typeWebhook {
		return ErrReadOnly
	}
	entries.Content = append(entries.Content[:i], entries.Content[i+1:]...)
	_, err = ed.save(f)
	return err
}

// Test sends the webhook entry name once, with a sample payload of its first
// event, and returns the error a dispatched run would record. Nothing is
// recorded and the master switch does not matter.
func (ed *Editor) Test(ctx context.Context, name string) error {
	ed.mu.Lock()
	cfg, err := LoadConfig(ed.path)
	ed.mu.Unlock()
	if err != nil {
		return &FileError{Err: err}
	}
	ok, entry := cfg.entry(name)
	if !ok {
		return ErrNotFound
	}
	if entry.Type != typeWebhook {
		return ErrReadOnly
	}
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	return ed.d.runnerFor(entry).Run(ctx, entry, samplePayload(entry))
}

// samplePayload is what a test of entry sends: a finished one-track album
// named as a test, under entry's first event.
func samplePayload(entry Entry) Payload {
	now := time.Now().UTC()
	event := string(domain.EventJobFinished)
	if len(entry.Events) > 0 {
		event = entry.Events[0]
	}
	payload := Payload{Event: event, Timestamp: now.Format(time.RFC3339), Job: JobPayload{
		ID: "job_test", Input: "https://music.apple.com/", Type: "album",
		Title: "Test from amdl", ArtistName: "amdl", Status: "completed",
		TotalItems: 1, CompletedItems: 1, StartedAt: &now, FinishedAt: &now,
	}}
	if event == string(domain.EventJobQueued) {
		payload.Job.Title, payload.Job.ArtistName, payload.Job.Status = "", "", "queued"
		payload.Job.TotalItems, payload.Job.CompletedItems, payload.Job.StartedAt, payload.Job.FinishedAt = 0, 0, nil, nil
		return payload
	}
	payload.Items = []ItemPayload{{ID: "item_test", Title: "Test track", Artist: "amdl", Album: "Test from amdl", Status: "completed", Codec: "alac"}}
	return payload
}

func (c Config) entry(name string) (bool, Entry) {
	for _, e := range c.Entries {
		if e.Name == name {
			return true, e
		}
	}
	return false, Entry{}
}

// hooksFile is a hooks.yaml as a YAML tree, and its file mode.
type hooksFile struct {
	doc  yaml.Node
	mode os.FileMode
}

// open reads the file as a tree, a missing or empty one as an empty
// mapping. A file that does not load is a FileError: editing one could only
// replace what someone meant with a guess.
func (ed *Editor) open() (*hooksFile, error) {
	f := &hooksFile{mode: 0o644}
	raw, err := os.ReadFile(ed.path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, err
	default:
		if info, err := os.Stat(ed.path); err == nil {
			f.mode = info.Mode().Perm()
		}
		if _, err := parseConfig(raw); err != nil {
			return nil, &FileError{Err: err}
		}
		if err := yaml.Unmarshal(raw, &f.doc); err != nil {
			return nil, &FileError{Err: err}
		}
	}
	if f.doc.Kind != yaml.DocumentNode || len(f.doc.Content) == 0 {
		f.doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if root := f.doc.Content[0]; root.Kind != yaml.MappingNode {
		// A comment-only file decodes to a null scalar; the comments go
		// over the mapping that replaces it.
		f.doc.Content[0] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: root.HeadComment, FootComment: root.FootComment}
	}
	return f, nil
}

// set puts value under key in the root mapping.
func (f *hooksFile) set(key string, value *yaml.Node) {
	root := f.doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			old := root.Content[i+1]
			value.LineComment, value.FootComment = old.LineComment, old.FootComment
			root.Content[i+1] = value
			return
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// entries is the root's entries sequence, made one when the key is missing
// or empty, as in the shipped file, whose examples are comments under it.
func (f *hooksFile) entries() *yaml.Node {
	root := f.doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "entries" {
			continue
		}
		if v := root.Content[i+1]; v.Kind == yaml.SequenceNode {
			return v
		}
		old := root.Content[i+1]
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", HeadComment: old.HeadComment, LineComment: old.LineComment, FootComment: old.FootComment}
		root.Content[i+1] = seq
		return seq
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "entries"}, seq)
	return seq
}

// find is the index and node of the entry named name in entries.
func find(entries *yaml.Node, name string) (int, *yaml.Node) {
	for i, item := range entries.Content {
		if field(item, "name") == name {
			return i, item
		}
	}
	return -1, nil
}

func typeOf(item *yaml.Node) string { return field(item, "type") }

func field(item *yaml.Node, key string) string {
	if item.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == key {
			return item.Content[i+1].Value
		}
	}
	return ""
}

// save validates the tree as a startup would, writes it and applies it.
func (ed *Editor) save(f *hooksFile) (Config, error) {
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&f.doc); err != nil {
		return Config{}, err
	}
	if err := enc.Close(); err != nil {
		return Config{}, err
	}
	cfg, err := parseConfig(out.Bytes())
	if err != nil {
		return Config{}, &InvalidError{Err: err}
	}
	if err := os.MkdirAll(filepath.Dir(ed.path), 0o755); err != nil {
		return Config{}, err
	}
	if err := writeFileAtomic(ed.path, out.Bytes(), f.mode); err != nil {
		return Config{}, fmt.Errorf("write hooks.yaml: %w", err)
	}
	ed.d.SetConfig(cfg)
	return cfg, nil
}

// writeFileAtomic writes data through a random temporary file and a rename,
// so a crash cannot leave a truncated file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
