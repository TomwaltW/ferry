//go:build !windows

package main

// consoleInput has nothing to change off Windows: a pseudo terminal's line
// discipline already hands escape sequences through as bytes.
func consoleInput() func() { return func() {} }
