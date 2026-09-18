//go:build !windows

package service

// Elevated reports whether this process runs with administrator rights. Only
// Windows asks: the Unix services here are per-user and need none.
func Elevated() bool { return true }
