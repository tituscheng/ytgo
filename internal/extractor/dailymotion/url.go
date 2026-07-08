package dailymotion

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	dailyHostPattern = regexp.MustCompile(`(?i)^(?:www|touch|geo)\.dailymotion\.[a-z]{2,3}$`)
	dailyVideoPath   = regexp.MustCompile(`(?i)/(?:embed/|crawler/|swf/)?video/([^/?_&#]+)`)
	dailySwfPath     = regexp.MustCompile(`(?i)/swf/([^/?_&#]+)`)
	dailyShortHost   = regexp.MustCompile(`(?i)^dai\.ly$`)
)

func isDailymotionHost(host string) bool {
	host = strings.ToLower(host)
	if dailyShortHost.MatchString(host) {
		return true
	}
	return dailyHostPattern.MatchString(host)
}

func isExcludedPath(path, rawQuery string) bool {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "/playlist/") {
		return true
	}
	if strings.Contains(lower, "/search/") {
		return true
	}
	if strings.HasPrefix(lower, "/user/") || strings.HasPrefix(lower, "/old/user/") {
		return true
	}
	q, err := url.ParseQuery(rawQuery)
	if err == nil && q.Get("playlist") != "" {
		return true
	}
	return false
}

func parseVideoID(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	host := strings.ToLower(u.Hostname())
	if !isDailymotionHost(host) {
		return "", fmt.Errorf("unsupported Dailymotion URL: %s", rawURL)
	}
	if isExcludedPath(u.Path, u.RawQuery) {
		return "", fmt.Errorf("unsupported Dailymotion URL: %s", rawURL)
	}

	if dailyShortHost.MatchString(host) {
		id := strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
		if id == "" {
			return "", fmt.Errorf("missing Dailymotion video ID in URL: %s", rawURL)
		}
		return id, nil
	}

	if strings.EqualFold(host, "geo.dailymotion.com") {
		if videoID := u.Query().Get("video"); videoID != "" {
			return videoID, nil
		}
		return "", fmt.Errorf("missing video query parameter in geo player URL: %s", rawURL)
	}

	if m := dailyVideoPath.FindStringSubmatch(u.Path); len(m) >= 2 {
		return m[1], nil
	}
	if m := dailySwfPath.FindStringSubmatch(u.Path); len(m) >= 2 {
		return m[1], nil
	}

	return "", fmt.Errorf("could not parse Dailymotion video ID from URL: %s", rawURL)
}

func isSuitableURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if !isDailymotionHost(u.Hostname()) {
		return false
	}
	if isExcludedPath(u.Path, u.RawQuery) {
		return false
	}

	if dailyShortHost.MatchString(strings.ToLower(u.Hostname())) {
		id := strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
		return id != ""
	}

	if strings.EqualFold(u.Hostname(), "geo.dailymotion.com") {
		return u.Query().Get("video") != "" && u.Query().Get("playlist") == ""
	}

	if dailyVideoPath.MatchString(u.Path) || dailySwfPath.MatchString(u.Path) {
		return true
	}
	return false
}
