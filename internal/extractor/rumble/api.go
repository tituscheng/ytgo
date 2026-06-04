package rumble

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

const embedJSURL = "https://rumble.com/embedJS/u3/"

type streamEntry struct {
	URL  string `json:"url"`
	Meta struct {
		Bitrate int   `json:"bitrate"`
		Size    int64 `json:"size"`
		W       int   `json:"w"`
		H       int   `json:"h"`
		Live    bool  `json:"live"`
	} `json:"meta"`
}

type embedResponse struct {
	Title    string  `json:"title"`
	Duration int     `json:"duration"`
	PubDate  string  `json:"pubDate"`
	FPS      float64 `json:"fps"`
	I        string  `json:"i"`
	Author   struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"author"`
	CC json.RawMessage `json:"cc"`
	T []struct {
		I string `json:"i"`
		W int    `json:"w"`
		H int    `json:"h"`
	} `json:"t"`
	UA map[string]json.RawMessage `json:"ua"`
	U  map[string]json.RawMessage `json:"u"`
}

func fetchEmbedJSON(ctx context.Context, client *http.Client, apiBase, videoID string) (*embedResponse, error) {
	base := apiBase
	if base == "" {
		base = embedJSURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("request", "video")
	q.Set("ver", "2")
	q.Set("v", videoID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Rumble embedJS: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(body)) == "false" {
		return nil, fmt.Errorf("Rumble video not found: %s", videoID)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch Rumble embedJS: HTTP %d", resp.StatusCode)
	}

	var video embedResponse
	if err := json.Unmarshal(body, &video); err != nil {
		return nil, fmt.Errorf("parse Rumble embedJS: %w", err)
	}
	if video.Title == "" && len(video.UA) == 0 && len(video.U) == 0 {
		return nil, fmt.Errorf("empty Rumble video response: %s", videoID)
	}
	return &video, nil
}

func parseUploadDate(pubDate string) string {
	if pubDate == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, pubDate)
	if err != nil {
		return ""
	}
	return t.Format("20060102")
}
