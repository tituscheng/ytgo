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

	resp, err := c.playerWithContext(ctx, videoID, req)
	if err != nil {
		return nil, err
	}

	// Handle playability status
	switch resp.PlayabilityStatus.Status {
	case "OK":
		return resp, nil
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

// PlayerWithEnrichment calls Player and then makes a secondary WEB client
// call to fetch additional metadata (e.g. likeCount) if it was missing.
func (c *Client) PlayerWithEnrichment(ctx context.Context, videoID string) (*PlayerResponse, error) {
	resp, err := c.Player(ctx, videoID)
	if err != nil {
		return nil, err
	}

	// If we already have likes, skip the secondary call
	if resp.VideoDetails.LikeCount != "" {
		return resp, nil
	}

	visitorID, err := c.getVisitorID()
	if err != nil {
		return resp, nil // don't fail the whole extraction over enrichment
	}

	req := PlayerRequest{
		VideoID:         videoID,
		Context:         webContext(visitorID),
		PlaybackContext: defaultPlaybackContext(),
		ContentCheckOK:  true,
		RacyCheckOk:     true,
	}

	webResp, err := c.playerWithContext(ctx, videoID, req)
	if err != nil {
		return resp, nil // enrichment is best-effort
	}

	if webResp.VideoDetails.LikeCount != "" {
		resp.VideoDetails.LikeCount = webResp.VideoDetails.LikeCount
	}
	return resp, nil
}

// playerWithContext makes a player request with the given context and returns
// the raw response without playability-status handling.
func (c *Client) playerWithContext(ctx context.Context, videoID string, req PlayerRequest) (*PlayerResponse, error) {
	body, err := c.postJSON(ctx, "player", req)
	if err != nil {
		return nil, err
	}

	var resp PlayerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal player response: %w", err)
	}
	return &resp, nil
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

	resp, err := c.playerWithContext(ctx, videoID, req)
	if err != nil {
		return nil, fmt.Errorf("embedded player fallback failed: %w", err)
	}

	if resp.PlayabilityStatus.Status != "OK" {
		return nil, fmt.Errorf("can't bypass age restriction: %s", resp.PlayabilityStatus.Reason)
	}

	return resp, nil
}
