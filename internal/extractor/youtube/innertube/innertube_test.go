package innertube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildPlayerResponse creates a player response JSON as a map.
func buildPlayerResponse(status, reason string) map[string]any {
	return map[string]any{
		"playabilityStatus": map[string]any{
			"status": status,
			"reason": reason,
		},
		"videoDetails": map[string]any{
			"videoId":          "dQw4w9WgXcQ",
			"title":            "Test Video",
			"lengthSeconds":    "213",
			"channelId":        "UCtest",
			"shortDescription": "A test video",
			"viewCount":        "1000",
			"author":           "Test Author",
			"thumbnail": map[string]any{
				"thumbnails": []map[string]any{
					{"url": "https://example.com/thumb.jpg", "width": 1280, "height": 720},
				},
			},
		},
		"streamingData": map[string]any{
			"formats": []map[string]any{
				{
					"itag":        18,
					"url":         "https://example.com/video.mp4",
					"mimeType":    "video/mp4; codecs=\"avc1.42001E, mp4a.40.2\"",
					"bitrate":     500000,
					"width":       640,
					"height":      360,
					"fps":         30,
					"quality":     "medium",
					"audioChannels": 2,
				},
			},
		},
		"microformat": map[string]any{
			"playerMicroformatRenderer": map[string]any{
				"lengthSeconds": "213",
				"publishDate":   "2009-10-25T00:00:00Z",
			},
		},
	}
}

func TestPlayer_Success(t *testing.T) {
	respData := buildPlayerResponse("OK", "")
	body, _ := json.Marshal(respData)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/youtubei/v1/player", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer server.Close()

	client := NewClient(10 * time.Second)
	client.HTTPClient.Transport = &testTransport{server: server}

	resp, err := client.Player(context.Background(), "dQw4w9WgXcQ")
	require.NoError(t, err)
	assert.Equal(t, "Test Video", resp.VideoDetails.Title)
	assert.Equal(t, "Test Author", resp.VideoDetails.Author)
	assert.Len(t, resp.StreamingData.Formats, 1)
	assert.Equal(t, 18, resp.StreamingData.Formats[0].ItagNo)
}

func TestPlayer_AgeRestrictedFallback(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req PlayerRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Context.Client.ClientName == "ANDROID_VR" {
			resp := buildPlayerResponse("LOGIN_REQUIRED", "Sign in to confirm your age")
			body, _ := json.Marshal(resp)
			w.Write(body)
			return
		}

		resp := buildPlayerResponse("OK", "")
		body, _ := json.Marshal(resp)
		w.Write(body)
	}))
	defer server.Close()

	client := NewClient(10 * time.Second)
	client.HTTPClient.Transport = &testTransport{server: server}

	resp, err := client.Player(context.Background(), "dQw4w9WgXcQ")
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
	assert.Equal(t, "Test Video", resp.VideoDetails.Title)
}

func TestPlayer_PrivateVideo(t *testing.T) {
	resp := buildPlayerResponse("LOGIN_REQUIRED", "This video is private")
	body, _ := json.Marshal(resp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer server.Close()

	client := NewClient(10 * time.Second)
	client.HTTPClient.Transport = &testTransport{server: server}

	_, err := client.Player(context.Background(), "private123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private")
}

func TestPlayer_Unplayable(t *testing.T) {
	resp := buildPlayerResponse("UNPLAYABLE", "Video unavailable")
	body, _ := json.Marshal(resp)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer server.Close()

	client := NewClient(10 * time.Second)
	client.HTTPClient.Transport = &testTransport{server: server}

	_, err := client.Player(context.Background(), "unavail123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unplayable")
}

func TestPlaylist_SingleColumn(t *testing.T) {
	playlistResp := map[string]any{
		"header": map[string]any{
			"playlistHeaderRenderer": map[string]any{
				"title": map[string]any{
					"runs": []map[string]any{{"text": "Test Playlist"}},
				},
				"descriptionText": map[string]any{
					"runs": []map[string]any{{"text": "A playlist for testing"}},
				},
				"ownerText": map[string]any{
					"runs": []map[string]any{{"text": "Test Channel"}},
				},
			},
		},
		"contents": map[string]any{
			"singleColumnBrowseResultsRenderer": map[string]any{
				"tabs": []map[string]any{
					{
						"tabRenderer": map[string]any{
							"content": map[string]any{
								"sectionListRenderer": map[string]any{
									"contents": []map[string]any{
										{
											"playlistVideoListRenderer": map[string]any{
												"contents": []map[string]any{
													{
														"playlistVideoRenderer": map[string]any{
															"videoId":       "vid1",
															"title":         map[string]any{"runs": []map[string]any{{"text": "Video 1"}}},
															"shortBylineText": map[string]any{"runs": []map[string]any{{"text": "Author 1"}}},
															"lengthSeconds": "120",
														},
													},
													{
														"playlistVideoRenderer": map[string]any{
															"videoId":       "vid2",
															"title":         map[string]any{"runs": []map[string]any{{"text": "Video 2"}}},
															"shortBylineText": map[string]any{"runs": []map[string]any{{"text": "Author 2"}}},
															"lengthSeconds": "240",
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(playlistResp)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/youtubei/v1/browse", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer server.Close()

	client := NewClient(10 * time.Second)
	client.HTTPClient.Transport = &testTransport{server: server}

	pl, err := client.Playlist(context.Background(), "PLtest123")
	require.NoError(t, err)
	assert.Equal(t, "Test Playlist", pl.Title)
	assert.Equal(t, "A playlist for testing", pl.Description)
	assert.Equal(t, "Test Channel", pl.Author)
	require.Len(t, pl.Entries, 2)
	assert.Equal(t, "vid1", pl.Entries[0].ID)
	assert.Equal(t, "Video 1", pl.Entries[0].Title)
	assert.Equal(t, 2*time.Minute, pl.Entries[0].Duration)
	assert.Equal(t, "vid2", pl.Entries[1].ID)
	assert.Equal(t, 4*time.Minute, pl.Entries[1].Duration)
}

func TestPlaylist_WithContinuation(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req PlayerRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Continuation == "" {
			playlistResp := map[string]any{
				"header": map[string]any{
					"playlistHeaderRenderer": map[string]any{
						"title": map[string]any{"simpleText": "Continued Playlist"},
					},
				},
				"contents": map[string]any{
					"singleColumnBrowseResultsRenderer": map[string]any{
						"tabs": []map[string]any{
							{
								"tabRenderer": map[string]any{
									"content": map[string]any{
										"sectionListRenderer": map[string]any{
											"contents": []map[string]any{
												{
													"playlistVideoListRenderer": map[string]any{
														"contents": []map[string]any{
															{
																"playlistVideoRenderer": map[string]any{
																	"videoId":       "vid1",
																	"title":         map[string]any{"simpleText": "Video 1"},
																	"lengthSeconds": "60",
																},
															},
															{
																"continuationItemRenderer": map[string]any{
																	"continuationEndpoint": map[string]any{
																		"continuationCommand": map[string]any{
																			"token": "CONT_TOKEN_1",
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}
			body, _ := json.Marshal(playlistResp)
			w.Write(body)
			return
		}

		// Continuation request
		contResp := map[string]any{
			"onResponseReceivedActions": []map[string]any{
				{
					"appendContinuationItemsAction": map[string]any{
						"continuationItems": []map[string]any{
							{
								"playlistVideoRenderer": map[string]any{
									"videoId":       "vid2",
									"title":         map[string]any{"simpleText": "Video 2"},
									"lengthSeconds": "90",
								},
							},
						},
					},
				},
			},
		}
		body, _ := json.Marshal(contResp)
		w.Write(body)
	}))
	defer server.Close()

	client := NewClient(10 * time.Second)
	client.HTTPClient.Transport = &testTransport{server: server}

	pl, err := client.Playlist(context.Background(), "PLcont123")
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
	assert.Equal(t, "Continued Playlist", pl.Title)
	require.Len(t, pl.Entries, 2)
	assert.Equal(t, "vid1", pl.Entries[0].ID)
	assert.Equal(t, "vid2", pl.Entries[1].ID)
	assert.Equal(t, 90*time.Second, pl.Entries[1].Duration)
}

// testTransport redirects requests to the test server while preserving the path.
// It also handles the initial visitor ID refresh GET request.
type testTransport struct {
	server *httptest.Server
	base   http.RoundTripper
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "youtube.com") {
		if req.URL.Path == "" || req.URL.Path == "/" {
			// Visitor ID refresh request - return fake HTML with ytcfg
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`<script>var ytcfg={d:function(){return window.ytconfig.data_||ytcfg.data_}};ytcfg.set({"INNERTUBE_CONTEXT":{"client":{"visitorData":"CgttdXJwaHlsYWIxMCh4q4DQBjIKCgJVUxIEGgAgKw%3D%3D"}}});</script>`)),
				Header:     http.Header{"Content-Type": []string{"text/html"}},
			}, nil
		}
		url := fmt.Sprintf("%s%s", t.server.URL, req.URL.Path)
		newReq, err := http.NewRequestWithContext(req.Context(), req.Method, url, req.Body)
		if err != nil {
			return nil, err
		}
		newReq.Header = req.Header
		return t.server.Client().Transport.RoundTrip(newReq)
	}
	if t.base != nil {
		return t.base.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}
