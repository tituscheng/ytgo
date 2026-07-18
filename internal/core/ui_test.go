package core

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tituscheng/ytgo/internal/extractor"
)

func TestFormatStreamLabel(t *testing.T) {
	assert.Equal(t, "video (1080p)", formatStreamLabel(extractor.Format{
		FormatID: "137", HasVideo: true, Height: 1080,
	}))
	assert.Equal(t, "audio", formatStreamLabel(extractor.Format{
		FormatID: "140", HasAudio: true,
	}))
	assert.Equal(t, "audio (~128 kbps)", formatStreamLabel(extractor.Format{
		FormatID: "140", HasAudio: true, ABR: 128,
	}))
}

// Ensure failure helper stays available for concurrent download reporting.
// (printDownloadFailed is side-effectful; just smoke-test it doesn't panic.)
func TestPrintDownloadFailed_NoPanic(t *testing.T) {
	printDownloadFailed("audio (~128 kbps)", assert.AnError)
}
