//go:build unix

package player

import (
	"errors"
	"os/exec"
	"syscall"
)

// detach starts the player in its own session so closing the terminal or
// exiting sel-ani does not take it down.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminate signals the player's whole process group (it is a session
// leader, so pgid == pid): flatpak and wrapper scripts fork children.
func terminate(cmd *exec.Cmd, kill bool) error {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil // already gone
	}
	return err
}
