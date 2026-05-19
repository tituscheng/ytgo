// Package archive handles the download archive file.
//
// Thread-safety: Archive is safe for concurrent use. All methods acquire an
// internal mutex, so multiple goroutines may call Has/Add simultaneously.
//
// Crash-recovery consistency: Add writes the ID to disk first. Only after a
// successful append do we update the in-memory map. This guarantees that any
// crash leaves the on-disk archive as the source of truth.
package archive

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Archive tracks which videos have already been downloaded.
type Archive struct {
	mu      sync.Mutex
	path    string
	entries map[string]bool
}

// Open reads an existing archive file or creates a new one.
func Open(path string) (*Archive, error) {
	a := &Archive{
		path:    path,
		entries: make(map[string]bool),
	}
	if path == "" {
		return a, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			a.entries[line] = true
		}
	}
	return a, scanner.Err()
}

// Has checks whether the given ID is already in the archive.
func (a *Archive) Has(id string) bool {
	if a == nil || a.path == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.entries[id]
}

// Add records an ID as downloaded and appends it to the file.
func (a *Archive) Add(id string) error {
	if a == nil || a.path == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries[id] {
		return nil
	}
	// Write to disk first. Only mark in-memory on success so that a crash
	// cannot leave the map ahead of the file.
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, id); err != nil {
		return err
	}
	a.entries[id] = true
	return nil
}
