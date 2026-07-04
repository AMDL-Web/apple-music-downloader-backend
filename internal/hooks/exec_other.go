//go:build !unix

package hooks

import "os/exec"

// setupProcessGroup is a no-op on platforms without POSIX process groups;
// exec.CommandContext's default cancellation (kill the direct child) applies.
func setupProcessGroup(cmd *exec.Cmd) {}
