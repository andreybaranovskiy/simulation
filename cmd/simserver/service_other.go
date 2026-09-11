//go:build !windows

package main

// runAsService is a no-op off Windows: there is no service control manager to
// hand off to, so the server always runs in the foreground. It exists so main
// stays identical across platforms.
func runAsService() bool { return false }
