package cmd

import (
	"context"
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
)

var rootCmd = &cobra.Command{
	Use:   "ytgo [URL]",
	Short: "A YouTube downloader written in Go",
	Long: `ytgo is a yt-dlp-inspired downloader focused on YouTube support.
It can be used as a CLI tool or imported as a Go library.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          run,
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
	rootCmd.Flags().String("remux-video", "", "Remux video to another container")
	rootCmd.Flags().String("recode-video", "", "Recode video to another format")

	// Playlist
	rootCmd.Flags().Bool("yes-playlist", true, "Download playlist")
	rootCmd.Flags().Bool("no-playlist", false, "Download single video only")
	rootCmd.Flags().Int("playlist-start", cfg.PlaylistStart, "Playlist start index")
	rootCmd.Flags().Int("playlist-end", 0, "Playlist end index")
	rootCmd.Flags().String("playlist-items", "", "Playlist items to download")

	// Network / auth
	rootCmd.Flags().String("cookies-from-browser", "", "Browser to extract cookies from")
	rootCmd.Flags().String("cookies", "", "Cookie file path")
	rootCmd.Flags().String("user-agent", "", "User agent string")
	rootCmd.Flags().String("proxy", "", "HTTP proxy URL")
	rootCmd.Flags().Duration("socket-timeout", cfg.SocketTimeout, "Network timeout")

	// Fragment download
	rootCmd.Flags().IntP("concurrent-fragments", "N", cfg.ConcurrentFragments, "Number of fragment download threads")

	// Post-processing
	rootCmd.Flags().String("ffmpeg-location", cfg.FFmpegLocation, "Path to ffmpeg binary")

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

	// Override from flags
	cfg.Format, _ = cmd.Flags().GetString("format")
	cfg.OutputTemplate, _ = cmd.Flags().GetString("output")
	cfg.Paths, _ = cmd.Flags().GetString("paths")
	cfg.ListFormats, _ = cmd.Flags().GetBool("list-formats")
	cfg.WriteSubs, _ = cmd.Flags().GetBool("write-subs")
	cfg.WriteAutoSubs, _ = cmd.Flags().GetBool("write-auto-subs")
	cfg.EmbedSubs, _ = cmd.Flags().GetBool("embed-subs")
	cfg.SubLangs, _ = cmd.Flags().GetStringSlice("sub-langs")
	cfg.SubFormat, _ = cmd.Flags().GetString("sub-format")
	cfg.WriteInfoJSON, _ = cmd.Flags().GetBool("write-info-json")
	cfg.WriteDescription, _ = cmd.Flags().GetBool("write-description")
	cfg.EmbedMetadata, _ = cmd.Flags().GetBool("embed-metadata")
	cfg.EmbedThumbnail, _ = cmd.Flags().GetBool("embed-thumbnail")
	cfg.WriteThumbnail, _ = cmd.Flags().GetBool("write-thumbnail")
	cfg.EmbedChapters, _ = cmd.Flags().GetBool("embed-chapters")
	cfg.SkipDownload, _ = cmd.Flags().GetBool("skip-download")
	cfg.Simulate, _ = cmd.Flags().GetBool("simulate")
	cfg.DownloadArchive, _ = cmd.Flags().GetString("download-archive")
	cfg.NoOverwrites, _ = cmd.Flags().GetBool("no-overwrites")
	noContinue, _ := cmd.Flags().GetBool("no-continue")
	cfg.ContinuePartial = !noContinue
	cfg.ExtractAudio, _ = cmd.Flags().GetBool("extract-audio")
	cfg.AudioFormat, _ = cmd.Flags().GetString("audio-format")
	cfg.AudioQuality, _ = cmd.Flags().GetString("audio-quality")
	cfg.KeepVideo, _ = cmd.Flags().GetBool("keep-video")
	cfg.MergeOutputFormat, _ = cmd.Flags().GetString("merge-output-format")
	cfg.RemuxVideo, _ = cmd.Flags().GetString("remux-video")
	cfg.RecodeVideo, _ = cmd.Flags().GetString("recode-video")
	cfg.YesPlaylist, _ = cmd.Flags().GetBool("yes-playlist")
	cfg.NoPlaylist, _ = cmd.Flags().GetBool("no-playlist")
	cfg.PlaylistStart, _ = cmd.Flags().GetInt("playlist-start")
	cfg.PlaylistEnd, _ = cmd.Flags().GetInt("playlist-end")
	cfg.PlaylistItems, _ = cmd.Flags().GetString("playlist-items")
	cfg.CookiesFromBrowser, _ = cmd.Flags().GetString("cookies-from-browser")
	cfg.Cookies, _ = cmd.Flags().GetString("cookies")
	cfg.UserAgent, _ = cmd.Flags().GetString("user-agent")
	cfg.Proxy, _ = cmd.Flags().GetString("proxy")
	cfg.SocketTimeout, _ = cmd.Flags().GetDuration("socket-timeout")
	cfg.ConcurrentFragments, _ = cmd.Flags().GetInt("concurrent-fragments")
	cfg.FFmpegLocation, _ = cmd.Flags().GetString("ffmpeg-location")
	cfg.Quiet, _ = cmd.Flags().GetBool("quiet")
	cfg.NoWarnings, _ = cmd.Flags().GetBool("no-warnings")
	cfg.Verbose, _ = cmd.Flags().GetBool("verbose")
	cfg.PrintJSON, _ = cmd.Flags().GetBool("print-json")
	cfg.NoProgress, _ = cmd.Flags().GetBool("no-progress")

	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "Options: %+v\n", cfg)
	}

	// Setup context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	engine := core.NewEngine(cfg)
	engine.Register(youtube.NewExtractor(cfg.SocketTimeout))

	return engine.Run(ctx, args[0])
}
