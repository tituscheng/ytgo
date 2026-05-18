package downloader

import (
	"encoding/json"
	"os"
)

// ResumeState tracks which byte ranges have been successfully downloaded
// for a segmented download. It is persisted as a JSON sidecar file.
type ResumeState struct {
	URL       string      `json:"url"`
	DestPath  string      `json:"dest_path"`
	FileSize  int64       `json:"file_size"`
	Completed []ByteRange `json:"completed"`
}

// resumePath returns the sidecar file path for a given destination.
func resumePath(dest string) string {
	return dest + ".segments"
}

// LoadResumeState reads a previous resume state if one exists.
func LoadResumeState(dest string) (*ResumeState, error) {
	path := resumePath(dest)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rs ResumeState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, err
	}
	return &rs, nil
}

// Save persists the resume state to disk.
func (rs *ResumeState) Save() error {
	path := resumePath(rs.DestPath)
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Remove deletes the resume sidecar file.
func (rs *ResumeState) Remove() error {
	return os.Remove(resumePath(rs.DestPath))
}

// MissingRanges returns the byte ranges that still need to be downloaded.
func (rs *ResumeState) MissingRanges(planned []ByteRange) []ByteRange {
	if len(rs.Completed) == 0 {
		return planned
	}
	completed := make(map[int]bool)
	for _, c := range rs.Completed {
		completed[c.Index] = true
	}
	var missing []ByteRange
	for _, p := range planned {
		if !completed[p.Index] {
			missing = append(missing, p)
		}
	}
	return missing
}

// IsComplete reports whether all planned segments are downloaded.
func (rs *ResumeState) IsComplete(planned []ByteRange) bool {
	return len(rs.MissingRanges(planned)) == 0
}

// preallocate creates the destination file and extends it to the expected size
// so that pwrite can be used at arbitrary offsets.
func preallocate(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

// fileSize returns the current size of a file, or 0 if it doesn't exist.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
