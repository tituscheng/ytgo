package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"ytgo/internal/config"
	"ytgo/internal/core"
	"ytgo/internal/extractor/youtube"
	"ytgo/pkg/ytgo"
)

var rootCmd = &cobra.Command{
	Use:   "ytgo [URL]",
	Short: "A YouTube downloader written in Go",
	Long: `ytgo is a yt-dlp-inspired downloader focused on YouTube support.
It can be used as a CLI tool or imported as a Go library.`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return run(cmd, args)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		color.Red("Error: %v", err)
		os.Exit(1)
	}
}

func init() {
	cfg := config.DefaultOptions()

	rootCmd.PersistentFlags().String("config", "", "Config file path")
	viper.SetConfigName("ytgo")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.config/ytgo")
	viper.AddConfigPath("$HOME/.ytgo")

	// Format selection
	rootCmd.Flags().StringP("format", "f", cfg.Format, "Video format code or selector")
	rootCmd.Flags().BoolP("list-formats", "F", false, "List available formats")
	rootCmd.Flags().String("prefer-vcodec", "", "Prefer formats with this video codec prefix (e.g. avc1)")
	rootCmd.Flags().String("prefer-acodec", "", "Prefer formats with this audio codec prefix (e.g. mp4a)")
	rootCmd.Flags().String("prefer-ext", "", "Prefer formats with this container extension (e.g. mp4)")

	// Output
	rootCmd.Flags().StringP("output", "o", cfg.OutputTemplate, "Output filename template")
	rootCmd.Flags().StringP("paths", "P", "", "Output path for all files")

	// Subtitles
	rootCmd.Flags().Bool("write-subs", false, "Write subtitle file")
	rootCmd.Flags().Bool("write-auto-subs", false, "Write automatically generated subtitles")
	rootCmd.Flags().Bool("embed-subs", false, "Embed subtitles in the video")
	rootCmd.Flags().StringSlice("sub-langs", nil, "Subtitle languages to download")
	rootCmd.Flags().String("sub-format", "", "Subtitle format (srt, vtt, etc.)")

	// Metadata
	rootCmd.Flags().Bool("write-info-json", false, "Write video metadata to .info.json")
	rootCmd.Flags().Bool("write-description", false, "Write video description to .description")
	rootCmd.Flags().Bool("embed-metadata", false, "Embed metadata to media file")
	rootCmd.Flags().Bool("embed-thumbnail", false, "Embed thumbnail in media file")
	rootCmd.Flags().Bool("write-thumbnail", false, "Write thumbnail to disk")
	rootCmd.Flags().Bool("embed-chapters", false, "Embed chapters in media file")

	// Download behaviour
	rootCmd.Flags().Bool("skip-download", false, "Skip downloading video")
	rootCmd.Flags().Bool("simulate", false, "Simulate download (do not write files)")
	rootCmd.Flags().String("download-archive", "", "File to record downloaded IDs")
	rootCmd.Flags().Bool("no-overwrites", false, "Do not overwrite files")
	rootCmd.Flags().Bool("no-continue", false, "Do not resume partial downloads")

	// Audio extraction
	rootCmd.Flags().BoolP("extract-audio", "x", false, "Convert to audio only")
	rootCmd.Flags().String("audio-format", cfg.AudioFormat, "Audio format (mp3, m4a, opus, wav, flac, best)")
	rootCmd.Flags().String("audio-quality", cfg.AudioQuality, "Audio quality (0-9)")
	rootCmd.Flags().Bool("keep-video", false, "Keep video file after audio extraction")

	// Merge / remux
	rootCmd.Flags().String("merge-output-format", "", "Merge output format (mp4, mkv, etc.)")

	// Playlist
	rootCmd.Flags().Bool("yes-playlist", true, "Download playlist")
	rootCmd.Flags().Bool("no-playlist", false, "Download single video only")
	rootCmd.Flags().Int("playlist-start", cfg.PlaylistStart, "Playlist start index")
	rootCmd.Flags().Int("playlist-end", 0, "Playlist end index")

	// Network
	rootCmd.Flags().Duration("socket-timeout", cfg.SocketTimeout, "Network timeout")

	// Fragment download
	rootCmd.Flags().IntP("concurrent-fragments", "N", cfg.ConcurrentFragments, "Number of fragment download threads")

	// Concurrency limits
	rootCmd.Flags().Int("max-downloads", cfg.MaxDownloads, "Maximum concurrent video downloads")
	rootCmd.Flags().Int("max-postprocessors", cfg.MaxPostProcessors, "Maximum concurrent post-processing jobs")
	rootCmd.Flags().Int64("limit-rate", cfg.LimitRate, "Maximum download rate in bytes per second")

	// Post-processing
	rootCmd.Flags().String("ffmpeg-location", cfg.FFmpegLocation, "Path to ffmpeg binary")

	// Metadata enrichment
	rootCmd.Flags().Bool("enrich-metadata", false, "Fetch additional metadata (likes) — slower")

	// Error reporting
	rootCmd.Flags().String("write-error-log", "", "Write download failures to a JSON file")

	// Verbosity
	rootCmd.Flags().BoolP("quiet", "q", false, "Quiet mode")
	rootCmd.Flags().Bool("no-warnings", false, "Suppress warnings")
	rootCmd.Flags().BoolP("verbose", "v", false, "Verbose output")
	rootCmd.Flags().Bool("print-json", false, "Print JSON info")
	rootCmd.Flags().Bool("no-progress", false, "Do not show progress")

	// Bind all flags to viper
	_ = viper.BindPFlags(rootCmd.Flags())
	viper.SetEnvPrefix("YTGO")
	viper.AutomaticEnv()
}

func run(cmd *cobra.Command, args []string) error {
	cfg := config.DefaultOptions()

	// Read config file
	if configFile, _ := cmd.Flags().GetString("config"); configFile != "" {
		viper.SetConfigFile(configFile)
	}
	if err := viper.ReadInConfig(); err != nil {
		// Ignore not found and parse errors; use defaults
		_ = err
	}
	_ = viper.Unmarshal(&cfg)

	// Handle the one inverted flag that viper cannot express directly
	if noContinue, _ := cmd.Flags().GetBool("no-continue"); noContinue {
		cfg.ContinuePartial = false
	}

	// Validate subtitle format at the CLI boundary so we fail fast with a
	// clear message instead of partway through a download.
	switch cfg.SubFormat {
	case "", "srt", "vtt":
		// ok
	default:
		return fmt.Errorf("invalid --sub-format %q (allowed: srt, vtt)", cfg.SubFormat)
	}

	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "Options: %+v\n", cfg)
	}

	// Setup context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Accumulate failures for optional error log
	var failures []ytgo.DownloadFailure
	if cfg.WriteErrorLog != "" {
		cfg.OnError = func(f ytgo.DownloadFailure) {
			failures = append(failures, f)
		}
	}

	engine := core.NewEngine(cfg)
	ext := youtube.NewExtractor(cfg.SocketTimeout)
	ext.Enrich = cfg.EnrichMetadata
	engine.Register(ext)

	report, runErr := engine.Run(ctx, args[0])

	if cfg.WriteErrorLog != "" && len(failures) > 0 {
		data, _ := json.MarshalIndent(failures, "", "  ")
		if werr := os.WriteFile(cfg.WriteErrorLog, data, 0644); werr != nil && runErr == nil {
			runErr = fmt.Errorf("write error log: %w", werr)
		}
	}

	if cfg.Verbose && report != nil {
		fmt.Fprintf(os.Stderr, "Playlist complete: %d total, %d succeeded, %d skipped, %d failed\n",
			report.Total, report.Succeeded, report.Skipped, len(report.Failed))
	}

	return runErr
}
