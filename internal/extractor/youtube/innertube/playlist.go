package innertube

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// PlaylistEntry holds basic metadata for a video in a playlist.
type PlaylistEntry struct {
	ID         string
	Title      string
	Author     string
	Duration   time.Duration
	Thumbnails []Thumbnail
}

// PlaylistInfo holds metadata and entries for a YouTube playlist.
type PlaylistInfo struct {
	ID          string
	Title       string
	Description string
	Author      string
	Entries     []PlaylistEntry
}

// Playlist fetches playlist metadata and all video entries.
func (c *Client) Playlist(ctx context.Context, playlistID string) (*PlaylistInfo, error) {
	visitorID, err := c.getVisitorID(ctx)
	if err != nil {
		return nil, err
	}

	req := PlayerRequest{
		BrowseID:     "VL" + playlistID,
		Context:      androidVRContext(visitorID),
		ContentCheckOK: true,
		RacyCheckOk:    true,
	}

	body, err := c.postJSON(ctx, "browse", req)
	if err != nil {
		return nil, err
	}

	var resp PlaylistResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal playlist response: %w", err)
	}

	info := &PlaylistInfo{
		ID: playlistID,
	}

	// Extract title / description / author from header
	hdr := resp.Header.PlaylistHeaderRenderer
	info.Title = hdr.Title.String()
	info.Description = hdr.Description.String()
	if info.Description == "" {
		info.Description = hdr.DescriptionText.String()
	}
	info.Author = hdr.OwnerText.String()

	// Fallback author from sidebar
	if info.Author == "" {
		for _, item := range resp.Sidebar.PlaylistSidebarRenderer.Items {
			if t := item.PlaylistSidebarSecondaryInfoRenderer.VideoOwner.VideoOwnerRenderer.Title.String(); t != "" {
				info.Author = t
				break
			}
		}
	}

	// Extract videos
	entries, continuation, err := extractPlaylistVideos(resp)
	if err != nil {
		return nil, err
	}
	info.Entries = entries

	// Follow continuation tokens
	for continuation != "" {
		visitorID, err := c.getVisitorID(ctx)
		if err != nil {
			return nil, err
		}
		contReq := PlayerRequest{
			Continuation: continuation,
			Context:      androidVRContext(visitorID),
			ContentCheckOK: true,
			RacyCheckOk:    true,
		}

		contBody, err := c.postJSON(ctx, "browse", contReq)
		if err != nil {
			return nil, err
		}

		var contResp ContinuationResponse
		if err := json.Unmarshal(contBody, &contResp); err != nil {
			return nil, fmt.Errorf("unmarshal continuation response: %w", err)
		}

		var items []PlaylistVideoItem
		// Try new format first
		if len(contResp.OnResponseReceivedActions) > 0 {
			items = contResp.OnResponseReceivedActions[0].AppendContinuationItemsAction.ContinuationItems
		} else {
			// Fallback to older format
			items = contResp.ContinuationContents.PlaylistVideoListContinuation.Contents
		}

		var nextCont string
		for _, item := range items {
			if item.PlaylistVideoRenderer.VideoID != "" {
				info.Entries = append(info.Entries, toPlaylistEntry(item))
			}
			if tok := item.ContinuationItemRenderer.ContinuationEndpoint.ContinuationCommand.Token; tok != "" {
				nextCont = tok
			}
		}

		// Alternative continuation location
		if nextCont == "" && len(contResp.ContinuationContents.PlaylistVideoListContinuation.Continuations) > 0 {
			nextCont = contResp.ContinuationContents.PlaylistVideoListContinuation.Continuations[0].NextContinuationData.Continuation
		}

		continuation = nextCont
	}

	return info, nil
}

func extractPlaylistVideos(resp PlaylistResponse) ([]PlaylistEntry, string, error) {
	var tabs []BrowseTab
	if len(resp.Contents.SingleColumnBrowseResultsRenderer.Tabs) > 0 {
		tabs = resp.Contents.SingleColumnBrowseResultsRenderer.Tabs
	} else if len(resp.Contents.TwoColumnBrowseResultsRenderer.Tabs) > 0 {
		tabs = resp.Contents.TwoColumnBrowseResultsRenderer.Tabs
	}
	if len(tabs) == 0 {
		return nil, "", fmt.Errorf("no tabs in playlist response")
	}

	sections := tabs[0].TabRenderer.Content.SectionListRenderer.Contents
	if len(sections) == 0 {
		return nil, "", fmt.Errorf("no sections in playlist response")
	}

	// The item section may be wrapped depending on client
	var videoItems []PlaylistVideoItem
	firstSection := sections[0]

	// Check for itemSectionRenderer wrapper (two-column layout)
	if len(firstSection.ItemSectionRenderer.Contents) > 0 {
		inner := firstSection.ItemSectionRenderer.Contents[0]
		videoItems = inner.PlaylistVideoListRenderer.Contents
	} else if len(firstSection.PlaylistVideoListRenderer.Contents) > 0 {
		// Direct playlistVideoListRenderer (single-column layout)
		videoItems = firstSection.PlaylistVideoListRenderer.Contents
	} else {
		return nil, "", fmt.Errorf("no playlist video list found")
	}

	var entries []PlaylistEntry
	var continuation string
	for _, item := range videoItems {
		if item.PlaylistVideoRenderer.VideoID != "" {
			entries = append(entries, toPlaylistEntry(item))
		}
		if tok := item.ContinuationItemRenderer.ContinuationEndpoint.ContinuationCommand.Token; tok != "" {
			continuation = tok
		}
	}

	return entries, continuation, nil
}

func toPlaylistEntry(item PlaylistVideoItem) PlaylistEntry {
	r := item.PlaylistVideoRenderer
	var d time.Duration
	if sec, err := strconv.Atoi(r.Duration); err == nil {
		d = time.Duration(sec) * time.Second
	}
	return PlaylistEntry{
		ID:         r.VideoID,
		Title:      r.Title.String(),
		Author:     r.Author.String(),
		Duration:   d,
		Thumbnails: r.Thumbnail.Thumbnails,
	}
}
