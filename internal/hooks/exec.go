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
)

func init() {
	register("exec", &execRunner{})
}

type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, entry Entry, payload Payload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", entry.Command)
	cmd.Dir = entry.Workdir
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Env = append(os.Environ(), buildEnv(payload)...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}

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
		"AMDL_DONE_ITEMS=" + strconv.Itoa(payload.Job.DoneItems),
		"AMDL_FAILED_ITEMS=" + strconv.Itoa(payload.Job.FailedItems),
		"AMDL_OUTPUT_PATHS=" + strings.Join(outputPaths, "\n"),
	}
}
