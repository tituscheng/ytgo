package youtube

import (
	"github.com/tituscheng/ytgo/internal/extractor"
	"github.com/tituscheng/ytgo/internal/extractor/youtube/innertube"
)

// appendManifestFormats adds HLS/DASH manifest formats and live metadata from
// the innertube player response.
func appendManifestFormats(info *extractor.VideoInfo, resp *innertube.PlayerResponse) {
	info.IsLiveContent = resp.VideoDetails.IsLiveContent

	if resp.StreamingData.HlsManifestURL != "" {
		url := resp.StreamingData.HlsManifestURL
		info.Formats = append(info.Formats, extractor.Format{
			FormatID:     "hls",
			URL:          url,
			ManifestURL:  url,
			Ext:          "mp4",
			VideoCodec:   "avc1",
			AudioCodec:   "aac",
			QualityLabel: "HLS",
			HasVideo:     true,
			HasAudio:     true,
		})
		return
	}

	if resp.StreamingData.DashManifestURL != "" {
		url := resp.StreamingData.DashManifestURL
		info.Formats = append(info.Formats, extractor.Format{
			FormatID:     "dash",
			URL:          url,
			ManifestURL:  url,
			Ext:          "mp4",
			QualityLabel: "DASH",
			HasVideo:     true,
			HasAudio:     true,
		})
	}
}
