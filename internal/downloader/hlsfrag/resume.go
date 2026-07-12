package hlsfrag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
)

// ResumeState tracks consecutive fragments written for an HLS media download.
// It is persisted as a JSON sidecar next to the .part destination.
type ResumeState struct {
	Version       int    `json:"version"`
	PlaylistURL   string `json:"playlist_url"`
	FragmentCount int    `json:"fragment_count"`
	// NextIndex is the first fragment index not yet fully written.
	NextIndex    int    `json:"next_index"`
	BytesWritten int64  `json:"bytes_written"`
	// Fingerprint is a hash of all fragment URLs (detects playlist rotation).
	Fingerprint string `json:"fingerprint"`
}

const resumeVersion = 1

func resumePath(dest string) string {
	return dest + ".hlsfrags"
}

func fragmentFingerprint(frags []Fragment) string {
	h := sha256.New()
	for i := range frags {
		_, _ = h.Write([]byte(frags[i].URL))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func loadResumeState(dest string) (*ResumeState, error) {
	data, err := os.ReadFile(resumePath(dest))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st ResumeState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveResumeState(dest string, st *ResumeState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	path := resumePath(dest)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeResumeState(dest string) {
	_ = os.Remove(resumePath(dest))
	_ = os.Remove(resumePath(dest) + ".tmp")
}

// validateResume checks whether a prior partial download can continue.
// The file may be longer than BytesWritten when a crash happened after writes
// but before the next sidecar flush; the caller truncates back to BytesWritten.
func validateResume(st *ResumeState, playlistURL string, frags []Fragment, dest string) bool {
	if st == nil || st.Version != resumeVersion {
		return false
	}
	if st.PlaylistURL != playlistURL {
		return false
	}
	if st.FragmentCount != len(frags) {
		return false
	}
	if st.Fingerprint != fragmentFingerprint(frags) {
		return false
	}
	if st.NextIndex < 0 || st.NextIndex > len(frags) {
		return false
	}
	if st.BytesWritten < 0 {
		return false
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return false
	}
	// Too short ⇒ torn/corrupt partial. Equal or longer is OK (truncate later).
	return fi.Size() >= st.BytesWritten
}
