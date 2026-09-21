//go:build unix

package core

import (
	"os/exec"
	"syscall"
	"time"
)

// detach starts cmd in its own session. With no controlling terminal a child
// that prompts (getpass opens /dev/tty) fails immediately instead of hanging
// or fighting the TUI for keystrokes, and the whole process tree can be
// signalled as one group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminate sends SIGTERM to cmd's process group, then SIGKILL if it is still
// around a few seconds later.
func terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	time.AfterFunc(5*time.Second, func() { _ = syscall.Kill(pgid, syscall.SIGKILL) })
}
