package innertube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiBaseURL     = "https://www.youtube.com/youtubei/v1"
	androidVRKey   = "AIzaSyA8eiZmM1FaDVjRy-df2KTyQ_vz_yYM39w"
	androidVRVer   = "1.65.10"
	androidVRAgent = "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip"
)

var contentPlaybackNonceAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// Client makes requests to YouTube's Innertube API.
type Client struct {
	HTTPClient *http.Client
	visitorID  string
	consentID  string
	visitorUpdated time.Time
}

// NewClient creates a Client with sensible defaults (plain transport).
// Tests and simple usage use this. Production paths should prefer
// NewClientWithTransport so that connection pooling, keep-alives, and
// HTTP/2 are shared with the rest of the application.
func NewClient(timeout time.Duration) *Client {
	return NewClientWithTransport(nil, timeout)
}

// NewClientWithTransport creates a Client using the provided RoundTripper
// (typically a *http.Transport from transport.NewTunedTransport).
// If rt is nil, a default transport is used.
func NewClientWithTransport(rt http.RoundTripper, timeout time.Duration) *Client {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Client{
		HTTPClient: &http.Client{Transport: rt, Timeout: timeout},
		consentID:  strconv.Itoa(rand.Intn(899) + 100),
	}
}

// androidVRContext returns the ANDROID_VR client context.
func androidVRContext(visitorID string) RequestContext {
	return RequestContext{
		Client: ClientInfo{
			HL:                "en",
			GL:                "US",
			ClientName:        "ANDROID_VR",
			ClientVersion:     androidVRVer,
			UserAgent:         androidVRAgent,
			TimeZone:          "UTC",
			UTCOffset:         0,
			VisitorData:       visitorID,
		},
	}
}

// webContext returns the WEB client context for enrichment calls.
func webContext(visitorID string) RequestContext {
	return RequestContext{
		Client: ClientInfo{
			HL:            "en",
			GL:            "US",
			ClientName:    "WEB",
			ClientVersion: "2.20250101",
			UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			TimeZone:      "UTC",
			UTCOffset:     0,
			VisitorData:   visitorID,
		},
	}
}

// embeddedPlayerContext returns the WEB_EMBEDDED_PLAYER fallback context.
func embeddedPlayerContext(visitorID string) RequestContext {
	return RequestContext{
		Client: ClientInfo{
			HL:            "en",
			GL:            "US",
			ClientName:    "WEB_EMBEDDED_PLAYER",
			ClientVersion: "1.19700101",
			UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
			TimeZone:      "UTC",
			UTCOffset:     0,
			VisitorData:   visitorID,
		},
	}
}

func defaultPlaybackContext() *PlaybackContext {
	return &PlaybackContext{
		ContentPlaybackContext: ContentPlaybackContext{
			HTML5Preference: "HTML5_PREF_WANTS",
		},
	}
}

// postJSON sends a POST request to the given Innertube endpoint and unmarshals the response.
func (c *Client) postJSON(ctx context.Context, endpoint string, body any) ([]byte, error) {
	// Match kkdai: use empty key for ANDROID_VR to avoid bot detection.
	url := fmt.Sprintf("%s/%s?key=", apiBaseURL, endpoint)

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://youtube.com")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	if rc, ok := body.(PlayerRequest); ok {
		req.Header.Set("User-Agent", rc.Context.Client.UserAgent)
		// kkdai hardcodes X-Youtube-Client-Name to "3" (ANDROID) regardless of actual client.
		// This inconsistency appears to help bypass bot detection.
		req.Header.Set("X-Youtube-Client-Name", "3")
		req.Header.Set("X-Youtube-Client-Version", rc.Context.Client.ClientVersion)
		req.Header.Set("x-goog-visitor-id", c.visitorID)
	}

	req.AddCookie(&http.Cookie{Name: "CONSENT", Value: "YES+cb.20210328-17-p0.en+FX+" + c.consentID})

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return io.ReadAll(resp.Body)
}

// getVisitorID returns a valid visitor ID, fetching one from YouTube if needed.
func (c *Client) getVisitorID(ctx context.Context) (string, error) {
	if c.visitorID == "" || time.Since(c.visitorUpdated) > 10*time.Hour {
		if err := c.refreshVisitorID(ctx); err != nil {
			return "", err
		}
	}
	return c.visitorID, nil
}

// refreshVisitorID fetches a real visitorData token from YouTube's homepage.
// It retries up to 3 times with exponential backoff before falling back to
// synthetic visitor data.
func (c *Client) refreshVisitorID(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		if err := c.tryRefreshVisitorID(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	// Final fallback: use synthetic data
	_ = lastErr
	c.visitorID = randomVisitorData("US")
	c.visitorUpdated = time.Now()
	return nil
}

func (c *Client) tryRefreshVisitorID(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.youtube.com", nil)
	if err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	const sep = "\nytcfg.set("
	_, after, found := strings.Cut(string(data), sep)
	if !found {
		return fmt.Errorf("ytcfg.set not found")
	}

	var value struct {
		InnertubeContext struct {
			Client struct {
				VisitorData string `json:"visitorData"`
			} `json:"client"`
		} `json:"INNERTUBE_CONTEXT"`
	}
	if err := json.NewDecoder(strings.NewReader(after)).Decode(&value); err != nil {
		return fmt.Errorf("decode ytcfg: %w", err)
	}

	vd, err := url.PathUnescape(value.InnertubeContext.Client.VisitorData)
	if err != nil {
		return fmt.Errorf("unescape visitorData: %w", err)
	}

	c.visitorID = vd
	c.visitorUpdated = time.Now()
	return nil
}

// protoBuilder is a minimal protobuf encoder for generating visitorData.
type protoBuilder struct {
	buf bytes.Buffer
}

func (pb *protoBuilder) writeVarint(val int64) error {
	if val == 0 {
		_, err := pb.buf.Write([]byte{0})
		return err
	}
	for {
		b := byte(val & 0x7F)
		val >>= 7
		if val != 0 {
			b |= 0x80
		}
		_, err := pb.buf.Write([]byte{b})
		if err != nil {
			return err
		}
		if val == 0 {
			break
		}
	}
	return nil
}

func (pb *protoBuilder) field(field int, wireType byte) error {
	val := int64(field<<3) | int64(wireType&0x07)
	return pb.writeVarint(val)
}

func (pb *protoBuilder) varint(field int, val int64) error {
	if err := pb.field(field, 0); err != nil {
		return err
	}
	return pb.writeVarint(val)
}

func (pb *protoBuilder) bytes(field int, data []byte) error {
	if err := pb.field(field, 2); err != nil {
		return err
	}
	if err := pb.writeVarint(int64(len(data))); err != nil {
		return err
	}
	_, err := pb.buf.Write(data)
	return err
}

func (pb *protoBuilder) stringField(field int, s string) error {
	return pb.bytes(field, []byte(s))
}

func (pb *protoBuilder) toBytes() []byte {
	return pb.buf.Bytes()
}

func (pb *protoBuilder) toURLEncodedBase64() string {
	b64 := base64.URLEncoding.EncodeToString(pb.toBytes())
	return url.QueryEscape(b64)
}

// randomVisitorData generates a YouTube-compatible visitorData string.
func randomVisitorData(countryCode string) string {
	var inner protoBuilder
	inner.stringField(2, "")
	inner.varint(4, int64(rand.Intn(255)+1))

	var mid protoBuilder
	mid.stringField(1, countryCode)
	mid.bytes(2, inner.toBytes())

	var outer protoBuilder
	outer.stringField(1, randString(contentPlaybackNonceAlphabet, 11))
	outer.varint(5, time.Now().Unix()-int64(rand.Intn(600000)))
	outer.bytes(6, mid.toBytes())

	return outer.toURLEncodedBase64()
}

func randString(alphabet string, sz int) string {
	var b strings.Builder
	b.Grow(sz)
	for i := 0; i < sz; i++ {
		b.WriteByte(alphabet[rand.Intn(len(alphabet))])
	}
	return b.String()
}
