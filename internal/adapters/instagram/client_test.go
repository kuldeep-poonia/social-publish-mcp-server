package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstagramClient_FullLifecycle(t *testing.T) {
	pollCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// 1. OAuth Short-Lived Exchange
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/access_token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "meta_short_lived_token_123",
				"token_type":   "bearer",
				"expires_in":   3600,
			})

		// 2. OAuth Long-Lived Token Upgrade
		case r.Method == http.MethodGet && r.URL.Path == "/oauth/access_token":
			grantType := r.URL.Query().Get("grant_type")
			if grantType == "fb_exchange_token" {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"access_token": "meta_long_lived_token_60_days",
					"token_type":   "bearer",
					"expires_in":   5184000, // 60 days
				})
			} else {
				http.Error(w, `{"error":{"message":"invalid grant_type"}}`, 400)
			}

		// 3. Facebook Page & Instagram Business Discovery
		case r.Method == http.MethodGet && r.URL.Path == "/v21.0/me/accounts":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id":           "page_id_1001",
						"name":         "Main Creator Page",
						"access_token": "page_token_abc",
						"instagram_business_account": map[string]interface{}{
							"id":       "1784140000099",
							"username": "creator_studio_brand",
							"name":     "Creator Brand",
						},
					},
				},
			})

		// 4. Container Creation (Step 1)
		case r.Method == http.MethodPost && r.URL.Path == "/v21.0/1784140000099/media":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "creation_container_777",
			})

		// 5. Container Poller
		case r.Method == http.MethodGet && r.URL.Path == "/v21.0/creation_container_777":
			pollCount++
			if pollCount == 1 {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":          "creation_container_777",
					"status_code": "IN_PROGRESS",
				})
			} else {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":          "creation_container_777",
					"status_code": "FINISHED",
				})
			}

		// 6. Media Publish (Step 2)
		case r.Method == http.MethodPost && r.URL.Path == "/v21.0/1784140000099/media_publish":
			creationID := r.FormValue("creation_id")
			if creationID != "creation_container_777" {
				http.Error(w, `{"error":{"message":"invalid creation_id"}}`, 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "published_ig_media_999",
			})

		// 7. Insights Retrieval
		case r.Method == http.MethodGet && r.URL.Path == "/v21.0/published_ig_media_999/insights":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"name": "impressions", "values": []map[string]interface{}{{"value": 15420}}},
					{"name": "reach", "values": []map[string]interface{}{{"value": 11200}}},
					{"name": "likes", "values": []map[string]interface{}{{"value": 850}}},
					{"name": "comments", "values": []map[string]interface{}{{"value": 42}}},
					{"name": "shares", "values": []map[string]interface{}{{"value": 19}}},
					{"name": "plays", "values": []map[string]interface{}{{"value": 18200}}},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient("mock_client_id", "mock_client_secret")
	client.apiBase = server.URL + "/v21.0"
	client.tokenURL = server.URL + "/oauth/access_token"

	ctx := context.Background()

	// Step 1: Short-Lived Token Exchange
	tok1, err := client.ExchangeShortLivedToken(ctx, "auth_code_xyz", "http://localhost:8080/auth/instagram/callback")
	if err != nil {
		t.Fatalf("short-lived exchange failed: %v", err)
	}
	if tok1.AccessToken != "meta_short_lived_token_123" {
		t.Errorf("unexpected short-lived token: %s", tok1.AccessToken)
	}

	// Step 2: Long-Lived Token Upgrade
	tok2, err := client.ExchangeLongLivedToken(ctx, tok1.AccessToken)
	if err != nil {
		t.Fatalf("long-lived upgrade failed: %v", err)
	}
	if tok2.AccessToken != "meta_long_lived_token_60_days" || tok2.ExpiresIn != 5184000 {
		t.Errorf("unexpected long-lived token response: %+v", tok2)
	}

	// Step 3: Discovery
	igAccount, _, err := client.GetInstagramBusinessAccount(ctx, tok2.AccessToken)
	if err != nil {
		t.Fatalf("business account discovery failed: %v", err)
	}
	if igAccount.ID != "1784140000099" || igAccount.Username != "creator_studio_brand" {
		t.Errorf("unexpected business account: %+v", igAccount)
	}

	// Step 4: Container Creation
	containerID, err := client.CreateMediaContainer(ctx, &CreateContainerRequest{
		IGUserID:    igAccount.ID,
		AccessToken: tok2.AccessToken,
		Caption:     "AI Generated Masterpiece #AI #Instagram",
		ImageURL:    "https://example.com/media/ephemeral/test.jpg",
	})
	if err != nil {
		t.Fatalf("container creation failed: %v", err)
	}
	if containerID != "creation_container_777" {
		t.Errorf("unexpected container ID: %s", containerID)
	}

	// Step 5: Bounded Poller
	statusResp, err := client.PollContainerStatus(ctx, containerID, tok2.AccessToken)
	if err != nil {
		t.Fatalf("container polling failed: %v", err)
	}
	if statusResp.StatusCode != ContainerStatusFinished {
		t.Errorf("expected status FINISHED, got: %s", statusResp.StatusCode)
	}

	// Step 6: Publish Media
	publishedID, err := client.PublishMedia(ctx, igAccount.ID, containerID, tok2.AccessToken)
	if err != nil {
		t.Fatalf("media publishing failed: %v", err)
	}
	if publishedID != "published_ig_media_999" {
		t.Errorf("unexpected published media ID: %s", publishedID)
	}

	// Step 7: Insights
	metrics, err := client.GetMediaInsights(ctx, publishedID, tok2.AccessToken)
	if err != nil {
		t.Fatalf("insights fetch failed: %v", err)
	}
	if metrics.Impressions != 15420 || metrics.Reach != 11200 || metrics.Likes != 850 || metrics.Plays != 18200 {
		t.Errorf("unexpected metrics values: %+v", metrics)
	}
}

func TestInstagramClient_PersonalAccountRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Simulate Facebook Pages without linked Instagram Business Account
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":                         "page_id_personal_only",
					"name":                       "Personal Hobby Page",
					"access_token":               "page_token_personal",
					"instagram_business_account": nil, // NULL
				},
			},
		})
	}))
	defer server.Close()

	client := NewClient("mock_client_id", "mock_client_secret")
	client.apiBase = server.URL
	ctx := context.Background()

	_, _, err := client.GetInstagramBusinessAccount(ctx, "valid_token")
	if err == nil || !errors.Is(err, ErrPersonalAccountNotSupported) {
		t.Fatalf("expected ErrPersonalAccountNotSupported for personal account, got: %v", err)
	}
}

func TestInstagramClient_ContainerPoller_TerminalStates(t *testing.T) {
	t.Run("Terminal ERROR State", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":            "container_err",
				"status_code":   "ERROR",
				"error_message": "Media format unsupported by transcoder",
			})
		}))
		defer server.Close()

		client := NewClient("mock_id", "mock_sec")
		client.apiBase = server.URL
		_, err := client.PollContainerStatus(context.Background(), "container_err", "tok")
		if err == nil || !errors.Is(err, ErrContainerProcessingFailed) {
			t.Fatalf("expected ErrContainerProcessingFailed, got: %v", err)
		}
	})

	t.Run("Terminal EXPIRED State", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":          "container_exp",
				"status_code": "EXPIRED",
			})
		}))
		defer server.Close()

		client := NewClient("mock_id", "mock_sec")
		client.apiBase = server.URL
		_, err := client.PollContainerStatus(context.Background(), "container_exp", "tok")
		if err == nil || !errors.Is(err, ErrContainerExpired) {
			t.Fatalf("expected ErrContainerExpired, got: %v", err)
		}
	})

	t.Run("Expired Token Error Code 190", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"message":       "Session has expired are you logged in?",
					"type":          "OAuthException",
					"code":          190,
					"error_subcode": 463,
				},
			})
		}))
		defer server.Close()

		client := NewClient("mock_id", "mock_sec")
		client.apiBase = server.URL
		_, err := client.PollContainerStatus(context.Background(), "container_any", "tok")
		if err == nil || !errors.Is(err, ErrReauthenticationRequired) {
			t.Fatalf("expected ErrReauthenticationRequired for code 190, got: %v", err)
		}
	})
}
