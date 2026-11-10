//go:build !unix

package player

import (
	"errors"
	"os"
	"os/exec"
)

func detach(*exec.Cmd) {}

// terminate has no process groups or SIGTERM here: kill outright.
func terminate(cmd *exec.Cmd, _ bool) error {
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
