package rumble

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	embedURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?rumble\.com/embed/(?:[0-9a-z]+\.)?(?P<id>[0-9a-z]+)`)
	pageURLPattern  = regexp.MustCompile(`(?i)^https?://(?:www\.)?rumble\.com/(?P<slug>v[\w.-]+)(?:\?[^#]*)?(?:#.*)?$`)
	videoIDPattern  = regexp.MustCompile(`(?i)(?:embed/|video["']?\s*:\s*["'])(?P<id>[0-9a-z]+)`)
)

func parseEmbedVideoID(rawURL string) (string, bool) {
	m := embedURLPattern.FindStringSubmatch(strings.TrimSpace(rawURL))
	if m == nil {
		return "", false
	}
	return m[1], true
}

func isPageURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "rumble.com" && !strings.HasSuffix(host, ".rumble.com") {
		return false
	}
	if !pageURLPattern.MatchString(rawURL) {
		return false
	}
	m := pageURLPattern.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return false
	}
	slug := strings.ToLower(m[1])
	return !strings.HasPrefix(slug, "videos")
}

func resolveVideoID(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	if id, ok := parseEmbedVideoID(rawURL); ok {
		return id, nil
	}
	if !isPageURL(rawURL) {
		return "", fmt.Errorf("unsupported Rumble URL: %s", rawURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch Rumble page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch Rumble page: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}

	m := videoIDPattern.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("could not find Rumble video ID in page: %s", rawURL)
	}
	return string(m[1]), nil
}

func videoIDFromPageHTML(html []byte) (string, bool) {
	m := videoIDPattern.FindSubmatch(html)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}
