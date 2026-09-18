//go:build windows

package service

import "golang.org/x/sys/windows"

// Elevated reports whether this process runs with administrator rights,
// which installing or removing a Windows service needs.
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
