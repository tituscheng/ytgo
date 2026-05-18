package innertube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Player fetches video metadata using the ANDROID_VR Innertube client.
// If the video is age-restricted, it falls back to WEB_EMBEDDED_PLAYER.
func (c *Client) Player(ctx context.Context, videoID string) (*PlayerResponse, error) {
	visitorID, err := c.getVisitorID()
	if err != nil {
		return nil, err
	}

	req := PlayerRequest{
		VideoID:         videoID,
		Context:         androidVRContext(visitorID),
		PlaybackContext: defaultPlaybackContext(),
		ContentCheckOK:  true,
		RacyCheckOk:     true,
	}

	body, err := c.postJSON(ctx, "player", req)
	if err != nil {
		return nil, err
	}

	var resp PlayerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal player response: %w", err)
	}

	// Handle playability status
	switch resp.PlayabilityStatus.Status {
	case "OK":
		return &resp, nil
	case "LOGIN_REQUIRED":
		if strings.HasPrefix(resp.PlayabilityStatus.Reason, "This video is private") {
			return nil, fmt.Errorf("video is private")
		}
		// Age-restricted: try embedded player fallback
		return c.playerEmbedded(ctx, videoID)
	case "UNPLAYABLE":
		return nil, fmt.Errorf("video unplayable: %s", resp.PlayabilityStatus.Reason)
	case "ERROR":
		return nil, fmt.Errorf("video error: %s", resp.PlayabilityStatus.Reason)
	default:
		return nil, fmt.Errorf("playability status %s: %s", resp.PlayabilityStatus.Status, resp.PlayabilityStatus.Reason)
	}
}

// playerEmbedded retries with the WEB_EMBEDDED_PLAYER client for age-restricted videos.
func (c *Client) playerEmbedded(ctx context.Context, videoID string) (*PlayerResponse, error) {
	visitorID, err := c.getVisitorID()
	if err != nil {
		return nil, err
	}

	req := PlayerRequest{
		VideoID:         videoID,
		Context:         embeddedPlayerContext(visitorID),
		PlaybackContext: defaultPlaybackContext(),
		ContentCheckOK:  true,
		RacyCheckOk:     true,
	}

	body, err := c.postJSON(ctx, "player", req)
	if err != nil {
		return nil, fmt.Errorf("embedded player fallback failed: %w", err)
	}

	var resp PlayerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal embedded player response: %w", err)
	}

	if resp.PlayabilityStatus.Status != "OK" {
		return nil, fmt.Errorf("can't bypass age restriction: %s", resp.PlayabilityStatus.Reason)
	}

	return &resp, nil
}
