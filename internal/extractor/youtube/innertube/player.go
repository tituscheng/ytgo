package innertube

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Player fetches video metadata from JS-less Innertube clients in parallel.
// VISIONOS supplies adaptive HTTPS (no GVS PO token). ANDROID_VR supplies
// muxed itag 18. Age-restricted videos fall back to WEB_EMBEDDED_PLAYER;
// kids / unplayable videos try TVHTML5 downgraded.
func (c *Client) Player(ctx context.Context, videoID string) (*PlayerResponse, error) {
	visitorID, err := c.getVisitorID(ctx)
	if err != nil {
		return nil, err
	}

	vr, vis, vrErr, visErr := c.fetchPrimaryPlayers(ctx, videoID, visitorID)
	vrOK := isPlayable(vr)
	visOK := isPlayable(vis)

	if !vrOK && !visOK {
		if isPrivate(vr) || isPrivate(vis) {
			return nil, fmt.Errorf("video is private")
		}
		if isAgeRestricted(vr) || isAgeRestricted(vis) {
			return c.playerEmbedded(ctx, videoID)
		}
		if isUnplayable(vr) || isUnplayable(vis) {
			tv, tvErr := c.playerNamed(ctx, videoID, visitorID, tvDowngradedClient)
			if tvErr == nil && isPlayable(tv) {
				return attachMerged(tv, nil, tv), nil
			}
		}
		if vr == nil && vis == nil {
			if vrErr != nil {
				return nil, vrErr
			}
			if visErr != nil {
				return nil, visErr
			}
		}
		return nil, playabilityError(firstResponse(vr, vis))
	}

	meta := vr
	if !vrOK {
		meta = vis
	}
	var quality *PlayerResponse
	if visOK {
		quality = vis
	}
	var vrPlayable *PlayerResponse
	if vrOK {
		vrPlayable = vr
	}
	return attachMerged(meta, vrPlayable, quality), nil
}

func (c *Client) fetchPrimaryPlayers(ctx context.Context, videoID, visitorID string) (vr, vis *PlayerResponse, vrErr, visErr error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		vr, vrErr = c.playerNamed(ctx, videoID, visitorID, androidVRClient)
	}()
	go func() {
		defer wg.Done()
		vis, visErr = c.playerNamed(ctx, videoID, visitorID, visionOSClient)
	}()
	wg.Wait()
	return vr, vis, vrErr, visErr
}

func (c *Client) playerNamed(ctx context.Context, videoID, visitorID string, cl innertubeClient) (*PlayerResponse, error) {
	return c.playerWithContext(ctx, videoID, cl.playerRequest(videoID, visitorID))
}

func attachMerged(meta, vr, quality *PlayerResponse) *PlayerResponse {
	out := *meta
	var vrData, qualityData *StreamingData
	if vr != nil {
		vrData = &vr.StreamingData
	}
	if quality != nil {
		qualityData = &quality.StreamingData
	}
	out.StreamingData = mergeStreaming(vrData, qualityData)
	return &out
}

func firstResponse(resps ...*PlayerResponse) *PlayerResponse {
	for _, r := range resps {
		if r != nil {
			return r
		}
	}
	return nil
}

func playabilityError(resp *PlayerResponse) error {
	if resp == nil {
		return fmt.Errorf("no playable innertube response")
	}
	switch resp.PlayabilityStatus.Status {
	case "UNPLAYABLE":
		return fmt.Errorf("video unplayable: %s", resp.PlayabilityStatus.Reason)
	case "ERROR":
		return fmt.Errorf("video error: %s", resp.PlayabilityStatus.Reason)
	case "LOGIN_REQUIRED":
		if isPrivate(resp) {
			return fmt.Errorf("video is private")
		}
		return fmt.Errorf("playability status %s: %s", resp.PlayabilityStatus.Status, resp.PlayabilityStatus.Reason)
	default:
		return fmt.Errorf("playability status %s: %s", resp.PlayabilityStatus.Status, resp.PlayabilityStatus.Reason)
	}
}

// PlayerWithEnrichment calls Player and then makes a secondary WEB client
// call to fetch additional metadata (e.g. likeCount) if it was missing.
func (c *Client) PlayerWithEnrichment(ctx context.Context, videoID string) (*PlayerResponse, error) {
	resp, err := c.Player(ctx, videoID)
	if err != nil {
		return nil, err
	}

	// If we already have a non-zero like count, skip the secondary call.
	// ANDROID_VR sometimes returns "0" as a placeholder — verify with WEB.
	if resp.VideoDetails.LikeCount != "" && resp.VideoDetails.LikeCount != "0" {
		return resp, nil
	}

	visitorID, err := c.getVisitorID(ctx)
	if err != nil {
		return resp, nil // don't fail the whole extraction over enrichment
	}

	req := PlayerRequest{
		VideoID:          videoID,
		Context:          webContext(visitorID),
		PlaybackContext:  defaultPlaybackContext(),
		ContentCheckOK:   true,
		RacyCheckOk:      true,
		headerClientName: "1",
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
	visitorID, err := c.getVisitorID(ctx)
	if err != nil {
		return nil, err
	}

	req := PlayerRequest{
		VideoID:          videoID,
		Context:          embeddedPlayerContext(visitorID),
		PlaybackContext:  defaultPlaybackContext(),
		ContentCheckOK:   true,
		RacyCheckOk:      true,
		headerClientName: "56",
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
