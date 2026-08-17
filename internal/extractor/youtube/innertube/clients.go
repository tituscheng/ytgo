package innertube

// innertubeClient is one JS-less Innertube player profile.
type innertubeClient struct {
	name        string
	version     string
	userAgent   string
	headerName  string
	deviceMake  string
	deviceModel string
	osName      string
	osVersion   string
	androidSDK  int
}

// ANDROID_VR header stays "3" (ANDROID). Sending the documented "28" is more
// often bot-blocked; this matches the existing working ytgo / kkdai quirk.
var androidVRClient = innertubeClient{
	name:        "ANDROID_VR",
	version:     androidVRVer,
	userAgent:   androidVRAgent,
	headerName:  "3",
	deviceMake:  "Oculus",
	deviceModel: "Quest 3",
	osName:      "Android",
	osVersion:   "12L",
	androidSDK:  32,
}

// visionOSClient is yt-dlp nightly's JS-less quality client (no GVS PO token
// for HTTPS adaptive as of 2026.07, Innertube client name 101).
var visionOSClient = innertubeClient{
	name:        "VISIONOS",
	version:     "1.02",
	userAgent:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15",
	headerName:  "101",
	deviceMake:  "Apple",
	deviceModel: "RealityDevice17,1",
	osName:      "visionOS",
	osVersion:   "26.5.23O471",
}

// tvDowngradedClient is yt-dlp's kids / unplayable fallback (TVHTML5 5.x).
var tvDowngradedClient = innertubeClient{
	name:       "TVHTML5",
	version:    "5.20260707",
	userAgent:  "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/Version",
	headerName: "7",
}

func (cl innertubeClient) context(visitorID string) RequestContext {
	return RequestContext{
		Client: ClientInfo{
			HL:                "en",
			GL:                "US",
			ClientName:        cl.name,
			ClientVersion:     cl.version,
			UserAgent:         cl.userAgent,
			TimeZone:          "UTC",
			UTCOffset:         0,
			DeviceMake:        cl.deviceMake,
			DeviceModel:       cl.deviceModel,
			OSName:            cl.osName,
			OSVersion:         cl.osVersion,
			AndroidSDKVersion: cl.androidSDK,
			VisitorData:       visitorID,
		},
	}
}

func (cl innertubeClient) playerRequest(videoID, visitorID string) PlayerRequest {
	return PlayerRequest{
		VideoID:          videoID,
		Context:          cl.context(visitorID),
		PlaybackContext:  defaultPlaybackContext(),
		ContentCheckOK:   true,
		RacyCheckOk:      true,
		headerClientName: cl.headerName,
	}
}

func androidVRContext(visitorID string) RequestContext {
	return androidVRClient.context(visitorID)
}
