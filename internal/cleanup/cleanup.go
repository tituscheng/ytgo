// Package cleanup provides a stack-based temporary file tracker that guarantees
// removal of registered paths when Cleanup() is called (typically deferred).
package cleanup

import (
	"os"
	"sync"
)

// Stack tracks temporary file paths for guaranteed cleanup.
// It is safe for concurrent use (e.g. multi-format stdout downloads).
type Stack struct {
	mu    sync.Mutex
	paths []string
}

// Push registers a path for cleanup.
func (s *Stack) Push(path string) {
	s.mu.Lock()
	s.paths = append(s.paths, path)
	s.mu.Unlock()
}

// Pop removes the most recently registered path from the cleanup list.
// Call this after a successful operation that makes the path permanent.
func (s *Stack) Pop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.paths) == 0 {
		return
	}
	s.paths = s.paths[:len(s.paths)-1]
}

// Cleanup removes all registered paths in reverse order (most recent first).
// It silently ignores files that no longer exist.
func (s *Stack) Cleanup() {
	s.mu.Lock()
	paths := s.paths
	s.paths = nil
	s.mu.Unlock()

	for i := len(paths) - 1; i >= 0; i-- {
		_ = os.Remove(paths[i])
	}
}
