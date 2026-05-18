package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	assert.Equal(t, "bv*+ba/best", opts.Format)
	assert.Equal(t, "%(title)s [%(id)s].%(ext)s", opts.OutputTemplate)
	assert.True(t, opts.ContinuePartial)
}
