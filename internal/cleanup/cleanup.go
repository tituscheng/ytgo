// Package cleanup provides a stack-based temporary file tracker that guarantees
// removal of registered paths when Cleanup() is called (typically deferred).
package cleanup

import "os"

// Stack tracks temporary file paths for guaranteed cleanup.
type Stack []string

// Push registers a path for cleanup.
func (s *Stack) Push(path string) {
	*s = append(*s, path)
}

// Pop removes the most recently registered path from the cleanup list.
// Call this after a successful operation that makes the path permanent.
func (s *Stack) Pop() {
	if len(*s) == 0 {
		return
	}
	*s = (*s)[:len(*s)-1]
}

// Cleanup removes all registered paths in reverse order (most recent first).
// It silently ignores files that no longer exist.
func (s *Stack) Cleanup() {
	for i := len(*s) - 1; i >= 0; i-- {
		_ = os.Remove((*s)[i])
	}
	*s = nil
}
