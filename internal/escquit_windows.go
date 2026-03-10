//go:build windows

package internal

// WatchEscQuit is a no-op on Windows. Use Ctrl+C instead.
func WatchEscQuit(onQuit func()) {}
