//go:build linux

package core

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// EnsureSnapDaemonGroup ensures that when running as root on Linux (such as
// inside an Ubuntu snap), the effective group is set to snap_daemon.
// This grants direct access to snap_daemon-owned data directories
// (like /var/snap/vybench/common/bench/sites) without triggering AppArmor DAC
// override capability denials.
func EnsureSnapDaemonGroup() {
	if os.Geteuid() != 0 {
		return
	}
	grp, err := user.LookupGroup("snap_daemon")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return
	}
	// Set real GID 0 (root) and effective GID to snap_daemon.
	_ = syscall.Setregid(0, gid)
	_ = syscall.Setgid(gid)
}
