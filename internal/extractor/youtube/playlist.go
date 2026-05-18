package youtube

import (
	"context"
	"fmt"

	"ytgo/internal/extractor"
)

func (e *Extractor) extractPlaylist(ctx context.Context, playlistID, rawURL string) (*extractor.VideoInfo, error) {
	playlist, err := e.client.GetPlaylistContext(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("get playlist failed: %w", err)
	}

	info := &extractor.VideoInfo{
		ID:            playlistID,
		PlaylistID:    playlistID,
		PlaylistTitle: playlist.Title,
		Title:         playlist.Title,
		OriginalURL:   rawURL,
		WebpageURL:    fmt.Sprintf("https://www.youtube.com/playlist?list=%s", playlistID),
	}

	for _, entry := range playlist.Videos {
		entryInfo := &extractor.VideoInfo{
			ID:          entry.ID,
			Title:       entry.Title,
			Duration:    entry.Duration,
			Playlist:    playlistID,
			PlaylistID:  playlistID,
			OriginalURL: fmt.Sprintf("https://www.youtube.com/watch?v=%s", entry.ID),
			WebpageURL:  fmt.Sprintf("https://www.youtube.com/watch?v=%s", entry.ID),
		}
		info.Entries = append(info.Entries, entryInfo)
	}

	return info, nil
}
