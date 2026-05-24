package postprocessor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tituscheng/ytgo/internal/extractor"
)

func TestFindFFmpeg(t *testing.T) {
	// Test with empty preferred path
	path := findFFmpeg("")
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		assert.NotEmpty(t, path)
	} else {
		assert.Empty(t, path)
	}

	// Test with preferred path that doesn't exist and no system ffmpeg
	t.Setenv("PATH", "")
	assert.Empty(t, findFFmpeg("/nonexistent/ffmpeg"))
}

func TestMergerNoFFmpeg(t *testing.T) {
	t.Setenv("PATH", "")
	m := NewMerger("/nonexistent/ffmpeg")
	_, err := m.Run(context.Background(), []string{"a.mp4", "b.m4a"}, "out.mp4", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ffmpeg not found")
}

func TestMergerSingleInput(t *testing.T) {
	// If only one input, return it directly without calling ffmpeg
	m := NewMerger("ffmpeg")
	out, err := m.Run(context.Background(), []string{"only.mp4"}, "out.mp4", "")
	require.NoError(t, err)
	assert.Equal(t, "only.mp4", out)
}

func TestFFmpegBaseArgsQuiet(t *testing.T) {
	quiet := ffmpegBaseArgs(true)
	assert.Contains(t, quiet, "-nostats")
	assert.Contains(t, quiet, "error")

	verbose := ffmpegBaseArgs(false)
	assert.Contains(t, verbose, "-stats")
	assert.Contains(t, verbose, "warning")
}

func TestConverterNoFFmpeg(t *testing.T) {
	t.Setenv("PATH", "")
	c := NewConverter("/nonexistent/ffmpeg")
	_, err := c.ExtractAudio(context.Background(), "in.mp4", "mp3", "5")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ffmpeg not found")
}

func TestEmbedderNoFFmpeg(t *testing.T) {
	t.Setenv("PATH", "")
	e := NewEmbedder("/nonexistent/ffmpeg")
	err := e.Run(context.Background(), "in.mp4", &extractor.VideoInfo{}, EmbedOptions{Metadata: true})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ffmpeg not found")
}

func TestWriteChaptersMetadata(t *testing.T) {
	chapters := []extractor.Chapter{
		{Title: "Intro", StartTime: 0},
		{Title: "Main", StartTime: 60000000000}, // 1 minute in nanoseconds
	}
	path, err := writeChaptersMetadata(chapters)
	require.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, ";FFMETADATA1")
	assert.Contains(t, content, "[CHAPTER]")
	assert.Contains(t, content, "title=Intro")
	assert.Contains(t, content, "title=Main")
}

func TestMergerWithMockFFmpeg(t *testing.T) {
	// Create a fake ffmpeg script that just copies the first input to output
	tmpDir := t.TempDir()
	ffmpegScript := filepath.Join(tmpDir, "ffmpeg")
	script := `#!/bin/sh
# Fake ffmpeg: just copy last argument from first -i argument
input=""
output=""
next=0
for arg in "$@"; do
    if [ "$arg" = "-i" ]; then
        next=1
        continue
    fi
    if [ "$next" = "1" ]; then
        input="$arg"
        next=0
    fi
    output="$arg"
done
cp "$input" "$output"
`
	require.NoError(t, os.WriteFile(ffmpegScript, []byte(script), 0755))

	m := NewMerger(ffmpegScript)

	// Create dummy input files
	input1 := filepath.Join(tmpDir, "video.mp4")
	input2 := filepath.Join(tmpDir, "audio.m4a")
	require.NoError(t, os.WriteFile(input1, []byte("video"), 0644))
	require.NoError(t, os.WriteFile(input2, []byte("audio"), 0644))

	output := filepath.Join(tmpDir, "merged.mp4")
	got, err := m.Run(context.Background(), []string{input1, input2}, output, "")
	require.NoError(t, err)
	assert.Equal(t, output, got)
	assert.FileExists(t, output)
}

func TestConverterWithMockFFmpeg(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpegScript := filepath.Join(tmpDir, "ffmpeg")
	script := `#!/bin/sh
# Fake ffmpeg: just touch the output file
output=""
for arg in "$@"; do
    output="$arg"
done
touch "$output"
`
	require.NoError(t, os.WriteFile(ffmpegScript, []byte(script), 0755))

	c := NewConverter(ffmpegScript)
	input := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("video"), 0644))

	output, err := c.ExtractAudio(context.Background(), input, "mp3", "5")
	require.NoError(t, err)
	assert.FileExists(t, output)
	assert.Equal(t, ".mp3", filepath.Ext(output))
}

func TestEmbedderWithMockFFmpeg(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpegScript := filepath.Join(tmpDir, "ffmpeg")
	script := `#!/bin/sh
# Fake ffmpeg: find the last argument that doesn't start with - and touch/copy it
output=""
for arg in "$@"; do
    case "$arg" in
        -*) ;;
        *) output="$arg" ;;
    esac
done
# Copy first input file to output
input=""
for arg in "$@"; do
    if [ "$arg" = "-i" ]; then
        next=1
        continue
    fi
    if [ "$next" = "1" ]; then
        input="$arg"
        next=0
    fi
done
if [ -n "$input" ] && [ -n "$output" ]; then
    cp "$input" "$output"
fi
`
	require.NoError(t, os.WriteFile(ffmpegScript, []byte(script), 0755))

	e := NewEmbedder(ffmpegScript)
	input := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("video"), 0644))

	info := &extractor.VideoInfo{
		Title:       "Test",
		Uploader:    "Uploader",
		Description: "Desc",
	}
	err := e.Run(context.Background(), input, info, EmbedOptions{Metadata: true})
	require.NoError(t, err)
}

// mockFFmpeg returns a fake ffmpeg script path that records its arguments to
// argsFile and then copies the first -i input to the output.
func mockFFmpeg(t *testing.T, tmpDir string) (ffmpegPath, argsFile string) {
	ffmpegPath = filepath.Join(tmpDir, "ffmpeg")
	argsFile = filepath.Join(tmpDir, "ffmpeg.args")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > \"" + argsFile + "\"\n" +
		"input=\"\"\n" +
		"output=\"\"\n" +
		"next=0\n" +
		"for arg in \"$@\"; do\n" +
		"    if [ \"$arg\" = \"-i\" ]; then\n" +
		"        next=1\n" +
		"        continue\n" +
		"    fi\n" +
		"    if [ \"$next\" = \"1\" ]; then\n" +
		"        input=\"$arg\"\n" +
		"        next=0\n" +
		"    fi\n" +
		"    case \"$arg\" in\n" +
		"        -*) ;;\n" +
		"        *) output=\"$arg\" ;;\n" +
		"    esac\n" +
		"done\n" +
		"if [ -n \"$input\" ] && [ -n \"$output\" ]; then\n" +
		"    cp \"$input\" \"$output\"\n" +
		"fi\n"
	require.NoError(t, os.WriteFile(ffmpegPath, []byte(script), 0755))
	return ffmpegPath, argsFile
}

func readArgs(t *testing.T, argsFile string) string {
	data, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	return string(data)
}

func TestMergerQuietUsesNoStats(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpeg, argsFile := mockFFmpeg(t, tmpDir)

	input1 := filepath.Join(tmpDir, "video.mp4")
	input2 := filepath.Join(tmpDir, "audio.m4a")
	require.NoError(t, os.WriteFile(input1, []byte("video"), 0644))
	require.NoError(t, os.WriteFile(input2, []byte("audio"), 0644))

	m := NewMerger(ffmpeg)
	m.Quiet = true
	output := filepath.Join(tmpDir, "merged.mp4")
	_, err := m.Run(context.Background(), []string{input1, input2}, output, "")
	require.NoError(t, err)

	args := readArgs(t, argsFile)
	assert.Contains(t, args, "-nostats")
	assert.Contains(t, args, "error")
	assert.NotContains(t, args, "-stats")
}

func TestMergerAutoFastStart_MP4(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpeg, argsFile := mockFFmpeg(t, tmpDir)

	input1 := filepath.Join(tmpDir, "video.mp4")
	input2 := filepath.Join(tmpDir, "audio.m4a")
	require.NoError(t, os.WriteFile(input1, []byte("video"), 0644))
	require.NoError(t, os.WriteFile(input2, []byte("audio"), 0644))

	m := NewMerger(ffmpeg)
	output := filepath.Join(tmpDir, "merged.mp4")
	_, err := m.Run(context.Background(), []string{input1, input2}, output, "")
	require.NoError(t, err)

	args := readArgs(t, argsFile)
	assert.Contains(t, args, "-movflags")
	assert.Contains(t, args, "+faststart")
}

func TestMergerAutoFastStart_WEBM(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpeg, argsFile := mockFFmpeg(t, tmpDir)

	input1 := filepath.Join(tmpDir, "video.webm")
	input2 := filepath.Join(tmpDir, "audio.webm")
	require.NoError(t, os.WriteFile(input1, []byte("video"), 0644))
	require.NoError(t, os.WriteFile(input2, []byte("audio"), 0644))

	m := NewMerger(ffmpeg)
	output := filepath.Join(tmpDir, "merged.webm")
	_, err := m.Run(context.Background(), []string{input1, input2}, output, "")
	require.NoError(t, err)

	args := readArgs(t, argsFile)
	assert.NotContains(t, args, "-movflags")
}

func TestConverterAutoFastStart_M4A(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpeg, argsFile := mockFFmpeg(t, tmpDir)

	input := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("video"), 0644))

	c := NewConverter(ffmpeg)
	_, err := c.ExtractAudio(context.Background(), input, "m4a", "5")
	require.NoError(t, err)

	args := readArgs(t, argsFile)
	assert.Contains(t, args, "-movflags")
	assert.Contains(t, args, "+faststart")
}

func TestConverterAutoFastStart_MP3(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpeg, argsFile := mockFFmpeg(t, tmpDir)

	input := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("video"), 0644))

	c := NewConverter(ffmpeg)
	_, err := c.ExtractAudio(context.Background(), input, "mp3", "5")
	require.NoError(t, err)

	args := readArgs(t, argsFile)
	assert.NotContains(t, args, "-movflags")
}

func TestEmbedderAutoFastStart_MP4(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpeg, argsFile := mockFFmpeg(t, tmpDir)

	input := filepath.Join(tmpDir, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("video"), 0644))

	e := NewEmbedder(ffmpeg)
	info := &extractor.VideoInfo{Title: "Test"}
	err := e.Run(context.Background(), input, info, EmbedOptions{Metadata: true})
	require.NoError(t, err)

	args := readArgs(t, argsFile)
	assert.Contains(t, args, "-movflags")
	assert.Contains(t, args, "+faststart")
}
