//go:build unix

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExecHookTimeoutKillsProcessGroup verifies that when an exec hook times
// out, not just the direct `sh` process but the whole process group it spawned
// is killed — so a grandchild the script backgrounded does not outlive the
// hook. Without Setpgid + group kill, exec.CommandContext would leave the
// grandchild running.
func TestExecHookTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Background a long-lived grandchild, record its PID, then keep the shell
	// itself alive long enough to be killed by the timeout.
	command := "sleep 30 & echo $! > " + pidFile + "; sleep 30"
	entry := Entry{Name: "slow", Type: "exec", Events: []string{"job_finished"}, Command: command}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := (&execRunner{}).Run(ctx, entry, Payload{Event: "job_finished"})
	if err == nil {
		t.Fatal("Run returned nil, want an error from the timed-out exec hook")
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("Run took %s; WaitDelay should bound the post-cancel wait", elapsed)
	}

	pid := readPIDFile(t, pidFile)
	if pid <= 0 {
		t.Fatalf("read grandchild pid = %d, want a positive pid", pid)
	}

	// The grandchild must die once the group is killed. Poll briefly because
	// reparenting to init and reaping take a moment.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return // gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Best-effort cleanup if the test is about to fail.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	t.Fatalf("grandchild pid %d still alive after hook timeout; process group was not killed", pid)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if s := strings.TrimSpace(string(raw)); s != "" {
				pid, convErr := strconv.Atoi(s)
				if convErr == nil {
					return pid
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s not written in time", path)
	return 0
}
