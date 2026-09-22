//go:build !linux

package core

// EnsureSnapDaemonGroup is a no-op on non-Linux platforms.
func EnsureSnapDaemonGroup() {}
