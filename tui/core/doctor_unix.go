//go:build unix

package core

import (
	"os"
	"syscall"
)

// fileOwnerUID reports the uid that owns fi.
func fileOwnerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
