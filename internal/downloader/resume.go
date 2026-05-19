package downloader

import (
	"encoding/json"
	"os"
	"strconv"
)

// ResumeState tracks which byte ranges have been successfully downloaded
// for a segmented download. It is persisted as a JSON sidecar file.
type ResumeState struct {
	URL           string      `json:"url"`            // current URL (ephemeral)
	DestPath      string      `json:"dest_path"`
	FileSize      int64       `json:"file_size"`
	VideoID       string      `json:"video_id"`       // durable identity
	FormatID      string      `json:"format_id"`
	ContentLength int64       `json:"content_length"` // from clen= query param
	Completed     []ByteRange `json:"completed"`
}

// DownloadIdentity scopes resume state to a specific video + format.
type DownloadIdentity struct {
	VideoID       string
	FormatID      string
	ContentLength int64 // expected from clen=; 0 means unknown
}

// Validate checks whether the stored resume state matches the requested identity.
// A mismatch means the state is stale (e.g. user changed --format between runs).
// ContentLength is only checked when both stored and expected values are non-zero.
func (rs *ResumeState) Validate(id DownloadIdentity, url string, fileSize int64) bool {
	if rs.VideoID != "" && rs.VideoID != id.VideoID {
		return false
	}
	if rs.FormatID != "" && rs.FormatID != id.FormatID {
		return false
	}
	if rs.ContentLength > 0 && id.ContentLength > 0 && rs.ContentLength != id.ContentLength {
		return false
	}
	// FileSize mismatch is a strong signal of stale state
	if rs.FileSize > 0 && fileSize > 0 && rs.FileSize != fileSize {
		return false
	}
	return true
}

// ParseContentLengthFromURL extracts the clen= value from a googlevideo.com URL.
func ParseContentLengthFromURL(rawURL string) int64 {
	// Simple scan for clen= followed by digits
	const prefix = "clen="
	for i := 0; i < len(rawURL); i++ {
		if i+len(prefix) <= len(rawURL) && rawURL[i:i+len(prefix)] == prefix {
			start := i + len(prefix)
			end := start
			for end < len(rawURL) && rawURL[end] >= '0' && rawURL[end] <= '9' {
				end++
			}
			if end > start {
				if n, err := strconv.ParseInt(rawURL[start:end], 10, 64); err == nil {
					return n
				}
			}
		}
	}
	return 0
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
