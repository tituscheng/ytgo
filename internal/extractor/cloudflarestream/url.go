package cloudflarestream

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	domainPattern    = `(?:cloudflarestream\.com|(?:videodelivery|bytehighway)\.net)`
	subdomainPattern = `(?:(?:watch|iframe|customer-[a-z0-9]+)\.)?`
	idPattern        = `[0-9a-f]{32}|eyJ[\w%-]+\.[\w%-]+\.[\w%-]+`
)

var (
	embedJSPattern = regexp.MustCompile(
		`(?i)^https?://(?:embed\.|(?:` + subdomainPattern + `))` + domainPattern +
			`/embed/[^/?#]+\.js\?(?:[^#]*&)?video=(?P<id>` + idPattern + `)`,
	)
	directURLPattern = regexp.MustCompile(
		`(?i)^https?://(?:` + subdomainPattern + `)(` + domainPattern + `)/(?P<id>` + idPattern + `)`,
	)
	manifestURLPattern = regexp.MustCompile(
		`(?i)^https?://(?:` + subdomainPattern + `)` + domainPattern +
			`/(?P<id>` + idPattern + `)/manifest/video\.(?:m3u8|mpd)`,
	)
)

type parsedVideo struct {
	rawID        string
	displayID    string
	manifestHost string
}

func parseVideoURL(rawURL string) (parsedVideo, error) {
	normalized, err := normalizeURL(rawURL)
	if err != nil {
		return parsedVideo{}, fmt.Errorf("could not parse Cloudflare Stream URL: %s", rawURL)
	}

	if m := embedJSPattern.FindStringSubmatch(normalized); m != nil {
		id := decodeComponent(m[1])
		return parsedVideo{
			rawID:        id,
			displayID:    displayID(id),
			manifestHost: manifestHost(normalized),
		}, nil
	}

	if m := manifestURLPattern.FindStringSubmatch(normalized); m != nil {
		id := decodeComponent(m[1])
		return parsedVideo{
			rawID:        id,
			displayID:    displayID(id),
			manifestHost: manifestHost(normalized),
		}, nil
	}

	if m := directURLPattern.FindStringSubmatch(normalized); m != nil {
		id := decodeComponent(m[2])
		return parsedVideo{
			rawID:        id,
			displayID:    displayID(id),
			manifestHost: manifestHost(normalized),
		}, nil
	}

	return parsedVideo{}, fmt.Errorf("could not parse Cloudflare Stream URL: %s", rawURL)
}

func normalizeURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if u.Path != "" {
		if path, err := url.PathUnescape(u.Path); err == nil {
			u.Path = path
		}
	}
	return u.String(), nil
}

func decodeComponent(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

func displayID(rawID string) string {
	if !strings.Contains(rawID, ".") {
		return rawID
	}
	sub, err := jwtSubject(rawID)
	if err != nil || sub == "" {
		return rawID
	}
	return sub
}

func jwtSubject(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1] + "===")
		if err != nil {
			return "", err
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	return claims.Sub, nil
}

func manifestHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "cloudflarestream.com"
	}

	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(host, "customer-") {
		return host
	}
	if strings.Contains(host, "bytehighway.net") {
		return "bytehighway.net"
	}
	return "cloudflarestream.com"
}

func (p parsedVideo) baseURL() string {
	return fmt.Sprintf("https://%s/%s/", p.manifestHost, p.rawID)
}

func (p parsedVideo) thumbnailURL() string {
	return p.baseURL() + "thumbnails/thumbnail.jpg"
}

func (p parsedVideo) masterManifestURL() string {
	return p.baseURL() + "manifest/video.m3u8"
}

func (p parsedVideo) dashManifestURL() string {
	return p.baseURL() + "manifest/video.mpd"
}

func (p parsedVideo) directDownloadURL() string {
	return p.baseURL() + "downloads/default.mp4"
}
