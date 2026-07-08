package dailymotion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const metadataURL = "https://www.dailymotion.com/player/metadata/video/"

type metadataResponse struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Duration    int    `json:"duration"`
	CreatedTime int64  `json:"created_time"`
	Explicit    bool   `json:"explicit"`
	Owner       struct {
		ID         string `json:"id"`
		Screenname string `json:"screenname"`
	} `json:"owner"`
	Posters    map[string]string `json:"posters"`
	Thumbnails map[string]string `json:"thumbnails"`
	Qualities  map[string][]qualityEntry `json:"qualities"`
	Subtitles  struct {
		Data json.RawMessage `json:"data"`
	} `json:"subtitles"`
	Error *metadataError `json:"error"`
}

type qualityEntry struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

type subtitleEntry struct {
	URLs     []string `json:"urls"`
	Language string   `json:"language"`
}

type metadataError struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	RawMessage string `json:"raw_message"`
}

func fetchMetadata(ctx context.Context, client *http.Client, metadataBase, videoID string) (*metadataResponse, error) {
	base := metadataBase
	if base == "" {
		base = metadataURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Path == "" || strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + videoID
	} else {
		u.Path = u.Path + "/" + videoID
	}
	q := u.Query()
	if q.Get("app") == "" {
		q.Set("app", "com.dailymotion.neon")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", "https://www.dailymotion.com")
	req.Header.Set("Cookie", "ff=off")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Dailymotion metadata: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch Dailymotion metadata: HTTP %d", resp.StatusCode)
	}

	var metadata metadataResponse
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("parse Dailymotion metadata: %w", err)
	}
	if metadata.Error != nil {
		return nil, metadataErrorToErr(videoID, metadata.Error)
	}
	if metadata.Title == "" && len(metadata.Qualities) == 0 {
		return nil, fmt.Errorf("empty Dailymotion metadata response: %s", videoID)
	}
	return &metadata, nil
}

func metadataErrorToErr(videoID string, errInfo *metadataError) error {
	msg := errInfo.Title
	if msg == "" {
		msg = errInfo.RawMessage
	}
	if msg == "" {
		msg = "unknown error"
	}
	if errInfo.Code == "DM007" {
		return fmt.Errorf("dailymotion video %s is geo-restricted: %s", videoID, msg)
	}
	return fmt.Errorf("dailymotion said: %s", msg)
}

func parseUploadDate(createdTime int64) string {
	if createdTime <= 0 {
		return ""
	}
	return time.Unix(createdTime, 0).UTC().Format("20060102")
}
