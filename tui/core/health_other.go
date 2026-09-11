//go:build !unix

package core

func processAlive(int) bool { return false }
