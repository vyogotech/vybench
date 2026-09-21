//go:build !unix

package core

import "os"

// fileOwnerUID has no answer off unix, so ownership never blocks a path there.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }
