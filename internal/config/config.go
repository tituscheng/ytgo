// Package config holds the user-configurable download options.
package config

import (
	"time"
)

// DownloadOptions collects every flag that influences extraction / download / post-processing.
type DownloadOptions struct {
	// Format selection
	Format string `mapstructure:"format"`

	// Output
	OutputTemplate string `mapstructure:"output"`
	Paths          string `mapstructure:"paths"`

	// Subtitles
	WriteSubs     bool     `mapstructure:"write-subs"`
	WriteAutoSubs bool     `mapstructure:"write-auto-subs"`
	EmbedSubs     bool     `mapstructure:"embed-subs"`
	SubLangs      []string `mapstructure:"sub-langs"`
	SubFormat     string   `mapstructure:"sub-format"`

	// Metadata / thumbnails / chapters
	WriteInfoJSON    bool `mapstructure:"write-info-json"`
	WriteDescription bool `mapstructure:"write-description"`
	EmbedMetadata    bool `mapstructure:"embed-metadata"`
	EmbedThumbnail   bool `mapstructure:"embed-thumbnail"`
	WriteThumbnail   bool `mapstructure:"write-thumbnail"`
	EmbedChapters    bool `mapstructure:"embed-chapters"`

	// Download behaviour
	SkipDownload      bool   `mapstructure:"skip-download"`
	DownloadArchive   string `mapstructure:"download-archive"`
	ForceWriteArchive bool   `mapstructure:"force-write-archive"`
	NoOverwrites      bool   `mapstructure:"no-overwrites"`
	ContinuePartial   bool   `mapstructure:"continue"`
	NoContinue        bool   `mapstructure:"no-continue"`

	// Audio extraction
	ExtractAudio bool   `mapstructure:"extract-audio"`
	AudioFormat  string `mapstructure:"audio-format"`
	AudioQuality string `mapstructure:"audio-quality"`
	KeepVideo    bool   `mapstructure:"keep-video"`

	// Merging / remux
	MergeOutputFormat string `mapstructure:"merge-output-format"`
	RemuxVideo        string `mapstructure:"remux-video"`
	RecodeVideo       string `mapstructure:"recode-video"`

	// Playlist
	YesPlaylist   bool   `mapstructure:"yes-playlist"`
	NoPlaylist    bool   `mapstructure:"no-playlist"`
	PlaylistStart int    `mapstructure:"playlist-start"`
	PlaylistEnd   int    `mapstructure:"playlist-end"`
	PlaylistItems string `mapstructure:"playlist-items"`

	// Network / auth
	CookiesFromBrowser string        `mapstructure:"cookies-from-browser"`
	Cookies            string        `mapstructure:"cookies"`
	UserAgent          string        `mapstructure:"user-agent"`
	Referer            string        `mapstructure:"referer"`
	Proxy              string        `mapstructure:"proxy"`
	SocketTimeout      time.Duration `mapstructure:"socket-timeout"`

	// Fragment download
	ConcurrentFragments int    `mapstructure:"concurrent-fragments"`
	BufferSize          string `mapstructure:"buffer-size"`

	// Post-processing
	FFmpegLocation string `mapstructure:"ffmpeg-location"`

	// Verbosity
	Quiet       bool `mapstructure:"quiet"`
	NoWarnings  bool `mapstructure:"no-warnings"`
	Verbose     bool `mapstructure:"verbose"`
	PrintJSON   bool `mapstructure:"print-json"`
	ListFormats bool `mapstructure:"list-formats"`
	Simulate    bool `mapstructure:"simulate"`
	NoProgress  bool `mapstructure:"no-progress"`
}

// DefaultOptions returns a DownloadOptions pre-filled with sensible defaults.
func DefaultOptions() DownloadOptions {
	return DownloadOptions{
		Format:              "bv*+ba/best",
		OutputTemplate:      "%(title)s [%(id)s].%(ext)s",
		ContinuePartial:     true,
		PlaylistStart:       1,
		ConcurrentFragments: 1,
		SocketTimeout:       30 * time.Second,
		AudioFormat:         "best",
		AudioQuality:        "5",
		FFmpegLocation:      "ffmpeg",
	}
}
