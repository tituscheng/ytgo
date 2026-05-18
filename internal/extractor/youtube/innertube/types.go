// Package innertube provides a minimal, zero-dependency client for YouTube's
// private Innertube API. It targets the ANDROID_VR client which returns
// pre-decrypted URLs without requiring JavaScript execution.
package innertube

// PlayerRequest is the JSON body sent to /youtubei/v1/player.
type PlayerRequest struct {
	VideoID         string           `json:"videoId,omitempty"`
	BrowseID        string           `json:"browseId,omitempty"`
	Continuation    string           `json:"continuation,omitempty"`
	Context         RequestContext   `json:"context"`
	PlaybackContext *PlaybackContext `json:"playbackContext,omitempty"`
	ContentCheckOK  bool             `json:"contentCheckOk,omitempty"`
	RacyCheckOk     bool             `json:"racyCheckOk,omitempty"`
	Params          string           `json:"params"`
}

// RequestContext wraps the client metadata.
type RequestContext struct {
	Client ClientInfo `json:"client"`
}

// ClientInfo describes the Innertube client.
type ClientInfo struct {
	HL                string `json:"hl"`
	GL                string `json:"gl"`
	ClientName        string `json:"clientName"`
	ClientVersion     string `json:"clientVersion"`
	AndroidSDKVersion int    `json:"androidSDKVersion,omitempty"`
	UserAgent         string `json:"userAgent,omitempty"`
	TimeZone          string `json:"timeZone"`
	UTCOffset         int    `json:"utcOffsetMinutes"`
	DeviceModel       string `json:"deviceModel,omitempty"`
	VisitorData       string `json:"visitorData,omitempty"`
}

// PlaybackContext is sent with player requests.
type PlaybackContext struct {
	ContentPlaybackContext ContentPlaybackContext `json:"contentPlaybackContext"`
}

// ContentPlaybackContext holds player preferences.
type ContentPlaybackContext struct {
	HTML5Preference string `json:"html5Preference"`
}

// PlayerResponse is the root JSON returned by the player endpoint.
type PlayerResponse struct {
	ResponseContext   ResponseContext   `json:"responseContext"`
	PlayabilityStatus PlayabilityStatus `json:"playabilityStatus"`
	StreamingData     StreamingData     `json:"streamingData"`
	VideoDetails      VideoDetails      `json:"videoDetails"`
	Microformat       Microformat       `json:"microformat"`
	Captions          *Captions         `json:"captions,omitempty"`
}

// ResponseContext contains debugging metadata.
type ResponseContext struct {
	VisitorData string `json:"visitorData,omitempty"`
}

// PlayabilityStatus indicates whether the video is playable.
type PlayabilityStatus struct {
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	PlayableInEmbed bool   `json:"playableInEmbed,omitempty"`
}

// StreamingData contains the actual media URLs.
type StreamingData struct {
	ExpiresInSeconds string   `json:"expiresInSeconds"`
	Formats          []Format `json:"formats"`
	AdaptiveFormats  []Format `json:"adaptiveFormats"`
	DashManifestURL  string   `json:"dashManifestUrl,omitempty"`
	HlsManifestURL   string   `json:"hlsManifestUrl,omitempty"`
}

// Format represents a single video or audio stream.
type Format struct {
	ItagNo           int    `json:"itag"`
	URL              string `json:"url,omitempty"`
	Cipher           string `json:"signatureCipher,omitempty"`
	MimeType         string `json:"mimeType"`
	Bitrate          int    `json:"bitrate"`
	FPS              int    `json:"fps,omitempty"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	LastModified     string `json:"lastModified,omitempty"`
	ContentLength    int64  `json:"contentLength,string,omitempty"`
	QualityLabel     string `json:"qualityLabel,omitempty"`
	ProjectionType   string `json:"projectionType,omitempty"`
	AverageBitrate   int    `json:"averageBitrate,omitempty"`
	AudioQuality     string `json:"audioQuality,omitempty"`
	ApproxDurationMs string `json:"approxDurationMs,omitempty"`
	AudioSampleRate  string `json:"audioSampleRate,omitempty"`
	AudioChannels    int    `json:"audioChannels,omitempty"`
	Quality          string `json:"quality,omitempty"`

	InitRange *Range `json:"initRange,omitempty"`
	IndexRange *Range `json:"indexRange,omitempty"`

	AudioTrack *AudioTrack `json:"audioTrack,omitempty"`
}

// Range describes byte offsets for adaptive formats.
type Range struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// AudioTrack holds multi-language audio metadata.
type AudioTrack struct {
	DisplayName    string `json:"displayName"`
	ID             string `json:"id"`
	AudioIsDefault bool   `json:"audioIsDefault"`
}

// VideoDetails contains high-level video metadata.
type VideoDetails struct {
	VideoID          string   `json:"videoId"`
	Title            string   `json:"title"`
	LengthSeconds    string   `json:"lengthSeconds"`
	Keywords         []string `json:"keywords,omitempty"`
	ChannelID        string   `json:"channelId"`
	ShortDescription string   `json:"shortDescription"`
	ViewCount        string   `json:"viewCount"`
	Author           string   `json:"author"`
	IsPrivate        bool     `json:"isPrivate"`
	IsLiveContent    bool     `json:"isLiveContent"`
	Thumbnail        struct {
		Thumbnails []Thumbnail `json:"thumbnails"`
	} `json:"thumbnail"`
}

// Thumbnail represents a thumbnail image.
type Thumbnail struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Microformat contains structured metadata from the page.
type Microformat struct {
	PlayerMicroformatRenderer struct {
		Thumbnail struct {
			Thumbnails []Thumbnail `json:"thumbnails"`
		} `json:"thumbnail"`
		Title              string   `json:"title,omitempty"`
		Description        string   `json:"description,omitempty"`
		LengthSeconds      string   `json:"lengthSeconds"`
		OwnerProfileURL    string   `json:"ownerProfileUrl,omitempty"`
		ExternalChannelID  string   `json:"externalChannelId,omitempty"`
		IsFamilySafe       bool     `json:"isFamilySafe"`
		AvailableCountries []string `json:"availableCountries,omitempty"`
		Category           string   `json:"category,omitempty"`
		PublishDate        string   `json:"publishDate,omitempty"`
		UploadDate         string   `json:"uploadDate,omitempty"`
		OwnerChannelName   string   `json:"ownerChannelName,omitempty"`
	} `json:"playerMicroformatRenderer"`
}

// Captions holds subtitle / caption tracks.
type Captions struct {
	PlayerCaptionsTracklistRenderer struct {
		CaptionTracks []CaptionTrack `json:"captionTracks,omitempty"`
	} `json:"playerCaptionsTracklistRenderer"`
}

// CaptionTrack represents a single subtitle track.
type CaptionTrack struct {
	BaseURL      string `json:"baseUrl"`
	Name         Text   `json:"name"`
	LanguageCode string `json:"languageCode"`
	Kind         string `json:"kind,omitempty"`
	IsTranslatable bool `json:"isTranslatable,omitempty"`
}

// Text is a common Innertube wrapper for simple text or runs.
type Text struct {
	SimpleText string `json:"simpleText,omitempty"`
	Runs       []Run  `json:"runs,omitempty"`
}

// Run is a segment of text with optional styling.
type Run struct {
	Text string `json:"text"`
}

// String returns the text content, preferring SimpleText.
func (t Text) String() string {
	if t.SimpleText != "" {
		return t.SimpleText
	}
	if len(t.Runs) > 0 {
		return t.Runs[0].Text
	}
	return ""
}

// PlaylistResponse is the root JSON returned by the browse endpoint for playlists.
type PlaylistResponse struct {
	Header   PlaylistHeader   `json:"header"`
	Sidebar  PlaylistSidebar  `json:"sidebar"`
	Contents BrowseContents   `json:"contents"`
}

// PlaylistHeader contains the playlist title and description.
type PlaylistHeader struct {
	PlaylistHeaderRenderer struct {
		Title           Text `json:"title"`
		Description     Text `json:"description,omitempty"`
		DescriptionText Text `json:"descriptionText,omitempty"`
		OwnerText       Text `json:"ownerText,omitempty"`
	} `json:"playlistHeaderRenderer"`
}

// PlaylistSidebar contains author info.
type PlaylistSidebar struct {
	PlaylistSidebarRenderer struct {
		Items []PlaylistSidebarItem `json:"items"`
	} `json:"playlistSidebarRenderer"`
}

// PlaylistSidebarItem is a generic sidebar item.
type PlaylistSidebarItem struct {
	PlaylistSidebarPrimaryInfoRenderer struct {
		Title Text `json:"title"`
	} `json:"playlistSidebarPrimaryInfoRenderer,omitempty"`
	PlaylistSidebarSecondaryInfoRenderer struct {
		VideoOwner struct {
			VideoOwnerRenderer struct {
				Title Text `json:"title"`
			} `json:"videoOwnerRenderer"`
		} `json:"videoOwner"`
	} `json:"playlistSidebarSecondaryInfoRenderer,omitempty"`
}

// BrowseContents wraps the browse renderer (supports both single and two column layouts).
type BrowseContents struct {
	TwoColumnBrowseResultsRenderer struct {
		Tabs []BrowseTab `json:"tabs"`
	} `json:"twoColumnBrowseResultsRenderer,omitempty"`
	SingleColumnBrowseResultsRenderer struct {
		Tabs []BrowseTab `json:"tabs"`
	} `json:"singleColumnBrowseResultsRenderer,omitempty"`
}

// BrowseTab is a generic tab structure.
type BrowseTab struct {
	TabRenderer struct {
		Content struct {
			SectionListRenderer struct {
				Contents []struct {
					ItemSectionRenderer struct {
						Contents []struct {
							PlaylistVideoListRenderer struct {
								Contents []PlaylistVideoItem `json:"contents"`
							} `json:"playlistVideoListRenderer,omitempty"`
						} `json:"contents"`
					} `json:"itemSectionRenderer,omitempty"`
					PlaylistVideoListRenderer struct {
						Contents []PlaylistVideoItem `json:"contents"`
					} `json:"playlistVideoListRenderer,omitempty"`
				} `json:"contents"`
			} `json:"sectionListRenderer"`
		} `json:"content"`
	} `json:"tabRenderer"`
}

// PlaylistVideoItem is either a video entry or a continuation token.
type PlaylistVideoItem struct {
	PlaylistVideoRenderer struct {
		VideoID   string      `json:"videoId"`
		Title     Text        `json:"title"`
		Author    Text        `json:"shortBylineText"`
		Duration  string      `json:"lengthSeconds"`
		Thumbnail struct {
			Thumbnails []Thumbnail `json:"thumbnails"`
		} `json:"thumbnail"`
	} `json:"playlistVideoRenderer,omitempty"`
	ContinuationItemRenderer struct {
		ContinuationEndpoint struct {
			ContinuationCommand struct {
				Token string `json:"token"`
			} `json:"continuationCommand"`
		} `json:"continuationEndpoint"`
	} `json:"continuationItemRenderer,omitempty"`
}

// ContinuationResponse is returned when fetching continuation tokens.
type ContinuationResponse struct {
	OnResponseReceivedActions []struct {
		AppendContinuationItemsAction struct {
			ContinuationItems []PlaylistVideoItem `json:"continuationItems"`
		} `json:"appendContinuationItemsAction,omitempty"`
	} `json:"onResponseReceivedActions,omitempty"`
	ContinuationContents struct {
		PlaylistVideoListContinuation struct {
			Contents      []PlaylistVideoItem `json:"contents,omitempty"`
			Continuations []struct {
				NextContinuationData struct {
					Continuation string `json:"continuation"`
				} `json:"nextContinuationData,omitempty"`
			} `json:"continuations,omitempty"`
		} `json:"playlistVideoListContinuation,omitempty"`
	} `json:"continuationContents,omitempty"`
}
