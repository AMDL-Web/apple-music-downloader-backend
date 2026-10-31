package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"amdl/internal/sysproc"
)

// stderrTailBytes caps how much of a failed exec hook's stderr is kept. The
// text becomes the hook_failed event message, which is stored in SQLite and
// streamed to every SSE client, so a script that dumps megabytes must not land
// there whole. The end of the output is kept because that is where a script
// usually says why it failed.
const stderrTailBytes = 4 << 10

// backendEnvPrefix is the prefix of every environment variable the backend
// reads itself.
const backendEnvPrefix = "AMDL_"

type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, entry Entry, payload Payload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", entry.Command)
	cmd.Dir = entry.Workdir
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Env = hookEnv(os.Environ(), payload)
	// On cancel/timeout, force-close after this grace period so Wait can't
	// block indefinitely on grandchildren that inherited the pipes.
	cmd.WaitDelay = 5 * time.Second
	// Put the command in its own process group and kill the whole group on
	// timeout, so a shell script's child processes don't outlive the hook.
	setupProcessGroup(cmd)
	sysproc.NoConsole(cmd)

	stderr := &tailWriter{limit: stderrTailBytes}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := stderr.String(); tail != "" {
			return fmt.Errorf("%w: %s", err, tail)
		}
		return err
	}
	return nil
}

// hookEnv is the environment of an exec hook: the backend's own environment
// without any AMDL_* variable, plus the documented hook variables. The AMDL_*
// variables are the backend's own settings, and one left over from an older
// install may hold a secret, so none is passed on; PATH, HOME, TZ and the
// rest are.
func hookEnv(environ []string, payload Payload) []string {
	env := make([]string, 0, len(environ)+12)
	for _, kv := range environ {
		if strings.HasPrefix(kv, backendEnvPrefix) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, buildEnv(payload)...)
}

// buildEnv returns the documented hook variables (docs/automation.md, and the
// exec example in configs/hooks.example.yaml).
func buildEnv(payload Payload) []string {
	outputPaths := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		if item.OutputPath != "" {
			outputPaths = append(outputPaths, item.OutputPath)
		}
	}
	return []string{
		"AMDL_EVENT=" + payload.Event,
		"AMDL_JOB_ID=" + payload.Job.ID,
		"AMDL_JOB_TYPE=" + payload.Job.Type,
		"AMDL_JOB_STATUS=" + payload.Job.Status,
		"AMDL_JOB_INPUT=" + payload.Job.Input,
		"AMDL_STOREFRONT=" + payload.Job.Storefront,
		"AMDL_TOTAL_ITEMS=" + strconv.Itoa(payload.Job.TotalItems),
		"AMDL_COMPLETED_ITEMS=" + strconv.Itoa(payload.Job.CompletedItems),
		"AMDL_SKIPPED_ITEMS=" + strconv.Itoa(payload.Job.SkippedItems),
		"AMDL_FAILED_ITEMS=" + strconv.Itoa(payload.Job.FailedItems),
		"AMDL_CANCELLED_ITEMS=" + strconv.Itoa(payload.Job.CancelledItems),
		"AMDL_OUTPUT_PATHS=" + strings.Join(outputPaths, "\n"),
	}
}

// tailWriter keeps the last limit bytes written to it.
type tailWriter struct {
	limit     int
	buf       []byte
	truncated bool
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= w.limit {
		w.truncated = w.truncated || len(w.buf) > 0 || len(p) > w.limit
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return n, nil
	}
	if overflow := len(w.buf) + len(p) - w.limit; overflow > 0 {
		w.truncated = true
		w.buf = append(w.buf[:0], w.buf[overflow:]...)
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

// String returns the kept bytes, trimmed. A cut tail starts at the next whole
// UTF-8 character behind an ellipsis.
func (w *tailWriter) String() string {
	kept := w.buf
	if !w.truncated {
		return strings.TrimSpace(string(kept))
	}
	for len(kept) > 0 && !utf8.RuneStart(kept[0]) {
		kept = kept[1:]
	}
	return "…" + strings.TrimSpace(string(kept))
}
