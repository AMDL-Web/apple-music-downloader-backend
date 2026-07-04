//go:build unix

package hooks

import (
	"os/exec"
	"syscall"
)

// setupProcessGroup places the command in its own process group and, on
// context cancellation/timeout, kills the entire group. exec.CommandContext's
// default Cancel only signals the direct child (the `sh` process), so any
// grandchild processes the shell spawns would otherwise be orphaned and keep
// running — and on retry, stack up into multiple background processes.
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative PID targets the whole process group created by Setpgid.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// Fall back to killing just the direct child if the group is
			// already gone (e.g. it exited between the check and the signal).
			return cmd.Process.Kill()
		}
		return nil
	}
}
