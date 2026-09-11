//go:build !unix

package core

import "os/exec"

func detach(cmd *exec.Cmd) {}

func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
