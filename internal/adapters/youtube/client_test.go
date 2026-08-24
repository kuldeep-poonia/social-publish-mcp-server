package youtube

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestYouTubeClient_FullLifecycle(t *testing.T) {
	// Mock Google OAuth & YouTube Data API Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		bodyStr := string(bodyBytes)

		switch {
		case strings.Contains(r.URL.Path, "/token"):
			if strings.Contains(bodyStr, "grant_type=authorization_code") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"access_token": "google_access_token_initial",
					"refresh_token": "google_refresh_token_valid",
					"expires_in": 3600,
					"token_type": "Bearer",
					"scope": "https://www.googleapis.com/auth/youtube.upload"
				}`))
				return
			}
			if strings.Contains(bodyStr, "grant_type=refresh_token") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"access_token": "google_access_token_refreshed",
					"expires_in": 3600,
					"token_type": "Bearer"
				}`))
				return
			}

		case strings.Contains(r.URL.Path, "/upload/youtube/v3/videos"):
			if r.Method == http.MethodPost {
				// Resumable upload init
				uploadSessionURL := fmt.Sprintf("http://%s/upload/session/12345", r.Host)
				w.Header().Set("Location", uploadSessionURL)
				w.WriteHeader(http.StatusOK)
				return
			}

		case strings.Contains(r.URL.Path, "/upload/session/12345"):
			if r.Method == http.MethodPut {
				cr := r.Header.Get("Content-Range")
				if cr == "bytes */1024" {
					// Query offset
					w.Header().Set("Range", "bytes=0-511")
					w.WriteHeader(308)
					return
				}
				// Final chunk
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"id": "yt_video_lifecycle_987",
					"snippet": {"title": "Lifecycle Test Video"}
				}`))
				return
			}

		case strings.Contains(r.URL.Path, "/youtube/v3/videos"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"items": [
						{
							"id": "yt_video_lifecycle_987",
							"snippet": {"title": "Lifecycle Test Video"},
							"status": {"privacyStatus": "public"},
							"statistics": {
								"viewCount": "48291",
								"likeCount": "1932",
								"commentCount": "215"
							}
						}
					]
				}`))
				return
			}

		case strings.Contains(r.URL.Path, "/revoke"):
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewClient("mock_yt_client_id", "mock_yt_client_secret")
	client.tokenURL = server.URL + "/token"
	client.revokeURL = server.URL + "/revoke"
	client.uploadBase = server.URL + "/upload/youtube/v3/videos"
	client.apiBase = server.URL + "/youtube/v3/videos"

	ctx := context.Background()

	// 1. OAuth 2.0 PKCE Code Exchange
	tokenResp, err := client.ExchangeOAuthToken(ctx, "auth_code_xyz", "verifier_abc", "http://localhost:8080/callback")
	if err != nil {
		t.Fatalf("ExchangeOAuthToken failed: %v", err)
	}
	if tokenResp.AccessToken != "google_access_token_initial" {
		t.Fatalf("expected initial access token, got %s", tokenResp.AccessToken)
	}

	// 2. Token Refresh
	refreshResp, err := client.RefreshToken(ctx, "google_refresh_token_valid")
	if err != nil {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	if refreshResp.AccessToken != "google_access_token_refreshed" {
		t.Fatalf("expected refreshed token, got %s", refreshResp.AccessToken)
	}

	// 3. Initiate Resumable Upload
	snippet := &VideoSnippet{Title: "Lifecycle Test Video", Description: "Test Description"}
	status := &VideoStatus{PrivacyStatus: "public"}
	sessionURI, err := client.InitiateResumableUpload(ctx, refreshResp.AccessToken, snippet, status, "video/mp4", 1024)
	if err != nil {
		t.Fatalf("InitiateResumableUpload failed: %v", err)
	}
	if !strings.Contains(sessionURI, "/upload/session/12345") {
		t.Fatalf("expected session URI with session id, got %s", sessionURI)
	}

	// 4. Query Resumable Offset
	offset, err := client.QueryResumableOffset(ctx, sessionURI, 1024)
	if err != nil {
		t.Fatalf("QueryResumableOffset failed: %v", err)
	}
	if offset != 512 {
		t.Fatalf("expected offset 512, got %d", offset)
	}

	// 5. Upload Final Chunk
	chunkBuf := bytes.NewReader(make([]byte, 512))
	_, videoID, isComplete, err := client.UploadChunk(ctx, sessionURI, chunkBuf, 512, 1023, 1024, "video/mp4")
	if err != nil {
		t.Fatalf("UploadChunk failed: %v", err)
	}
	if !isComplete || videoID != "yt_video_lifecycle_987" {
		t.Fatalf("expected complete with videoID 'yt_video_lifecycle_987', got %s (complete: %t)", videoID, isComplete)
	}

	// 6. Get Video Analytics
	analytics, err := client.GetVideoAnalytics(ctx, refreshResp.AccessToken, "yt_video_lifecycle_987")
	if err != nil {
		t.Fatalf("GetVideoAnalytics failed: %v", err)
	}
	if analytics.ViewCount != 48291 || analytics.LikeCount != 1932 || analytics.CommentCount != 215 {
		t.Fatalf("unexpected analytics metrics: %+v", analytics)
	}

	// 7. Revoke Token
	if err := client.RevokeToken(ctx, refreshResp.AccessToken); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}

	t.Logf("================================================================================")
	t.Logf("     YOUTUBE CLIENT FULL LIFECYCLE TEST SUITE: 100%% PASS                        ")
	t.Logf("================================================================================")
	t.Logf("[1/7] OAuth 2.0 PKCE Exchange: PASS (Token: %s)", tokenResp.AccessToken)
	t.Logf("[2/7] Token Refresh:          PASS (Refreshed: %s)", refreshResp.AccessToken)
	t.Logf("[3/7] Resumable Init:         PASS (Session: %s)", sessionURI)
	t.Logf("[4/7] Offset Query:           PASS (Offset: %d)", offset)
	t.Logf("[5/7] Chunk Upload:           PASS (Video ID: %s)", videoID)
	t.Logf("[6/7] Analytics Retrieval:    PASS (Views: %d, Likes: %d, Comments: %d)", analytics.ViewCount, analytics.LikeCount, analytics.CommentCount)
	t.Logf("[7/7] Token Revocation:       PASS (Revoked: true)")
	t.Logf("================================================================================")
}
