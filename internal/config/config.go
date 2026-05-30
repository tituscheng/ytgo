// Package config holds the user-configurable download options.
package config

import (
	"log/slog"
	"time"

	"github.com/tituscheng/ytgo/pkg/ytgo"
)

// DownloadOptions collects every flag that influences extraction / download / post-processing.
type DownloadOptions struct {
	// Format selection
	Format string `mapstructure:"format"`

	// FormatPreferences boosts scores for formats matching these preferences.
	// They are NOT hard filters — formats that don't match are still considered,
	// but matching formats get a large score bonus.
	PreferVideoCodec string `mapstructure:"prefer-vcodec"`
	PreferAudioCodec string `mapstructure:"prefer-acodec"`
	PreferContainer  string `mapstructure:"prefer-ext"`

	// FormatFilter is an optional pre-filter applied before scoring.
	// Only formats passing this filter are considered.
	FormatFilter ytgo.FormatFilter `mapstructure:"-"`

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
	SkipDownload    bool   `mapstructure:"skip-download"`
	DownloadArchive string `mapstructure:"download-archive"`
	NoOverwrites    bool   `mapstructure:"no-overwrites"`
	SkipExisting    bool   `mapstructure:"skip-existing"`
	ContinuePartial bool   `mapstructure:"continue"`

	// Audio extraction
	ExtractAudio bool   `mapstructure:"extract-audio"`
	AudioFormat  string `mapstructure:"audio-format"`
	AudioQuality string `mapstructure:"audio-quality"`
	KeepVideo    bool   `mapstructure:"keep-video"`

	// Merging / remux
	MergeOutputFormat string `mapstructure:"merge-output-format"`

	// Playlist
	YesPlaylist   bool `mapstructure:"yes-playlist"`
	NoPlaylist    bool `mapstructure:"no-playlist"`
	PlaylistStart int  `mapstructure:"playlist-start"`
	PlaylistEnd   int  `mapstructure:"playlist-end"`

	// Network
	SocketTimeout time.Duration `mapstructure:"socket-timeout"`

	// Fragment download
	ConcurrentFragments int `mapstructure:"concurrent-fragments"`

	// Concurrency limits
	MaxDownloads      int   `mapstructure:"max-downloads"`
	MaxPostProcessors int   `mapstructure:"max-postprocessors"`
	LimitRate         int64 `mapstructure:"limit-rate"` // bytes/sec

	// Post-processing
	FFmpegLocation string `mapstructure:"ffmpeg-location"`

	// Metadata enrichment (slower — makes secondary API calls)
	EnrichMetadata bool `mapstructure:"enrich-metadata"`

	// OnProgress receives structured progress events for every phase (download,
	// merge, audio extraction). Library use only — not settable via CLI/config
	// file. The engine serializes calls, so the callback need not be safe for
	// concurrent use.
	OnProgress ytgo.ProgressFunc `mapstructure:"-"`

	// OnError is called for every video that fails during processing.
	// Library/CLI code sets it directly; not configurable via config file.
	// Calls are serialized by the engine, so user code does not need its
	// own mutex when mutating shared state from this callback.
	OnError func(ytgo.DownloadFailure) `mapstructure:"-"`

	// Logger is an optional structured logger for library use.
	// When nil, no structured logging is emitted.
	Logger *slog.Logger `mapstructure:"-"`

	// WriteErrorLog is a file path to write structured failure JSON.
	// Set via --write-error-log CLI flag.
	WriteErrorLog string `mapstructure:"write-error-log"`

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
		SkipExisting:        true,
		ContinuePartial:     true,
		PlaylistStart:       1,
		ConcurrentFragments: 1,
		SocketTimeout:       30 * time.Second,
		MaxDownloads:      3,
		MaxPostProcessors: 2,
		AudioFormat:         "best",
		AudioQuality:        "5",
		FFmpegLocation:      "ffmpeg",
	}
}
