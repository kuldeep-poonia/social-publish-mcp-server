package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/mcp"
)

func (s *HTTPServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://social-mcp.duckdns.org"
	}

	openAPISpec := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":       "Social Publisher & Analytics AI API",
			"description": "Multi-platform Social Media Publishing, Account Insights, and AI SEO Optimization API for Instagram, YouTube, and Twitter/X.",
			"version":     "1.0.0",
		},
		"servers": []map[string]string{
			{"url": baseURL},
		},
		"paths": map[string]interface{}{
			"/api/v1/publish": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "publishPost",
					"summary":     "Publish post to social media",
					"description": "Publishes an image, video, Reel, or text post to Instagram, YouTube, or Twitter with idempotency protection. NOTE FOR AI: When publishing an image generated with DALL-E or code interpreter in this chat, pass the generated image URL into media_urls.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"platform": map[string]interface{}{
											"type":        "string",
											"enum":        []string{"instagram", "youtube", "twitter"},
											"description": "Target social media platform",
										},
										"content": map[string]interface{}{
											"type":        "string",
											"description": "Caption, title, or tweet text",
										},
										"image_prompt": map[string]interface{}{
											"type":        "string",
											"description": "Optional prompt to auto-generate a stunning AI image visual on-the-fly when publishing without external URL or Base64 (solves ChatGPT sandbox limitations)",
										},
										"media_urls": map[string]interface{}{
											"type": "array",
											"items": map[string]string{
												"type": "string",
											},
											"description": "URLs of public media attachments",
										},
										"media_data": map[string]interface{}{
											"type":        "string",
											"description": "Optional Base64-encoded binary string of any AI-generated or local image/video to auto-stage and publish",
										},
										"media_type": map[string]interface{}{
											"type":        "string",
											"description": "Optional media type ('IMAGE' or 'REELS' for Instagram)",
										},
										"idempotency_key": map[string]interface{}{
											"type":        "string",
											"description": "Unique idempotency key to prevent duplicate publishing",
										},
									},
									"required": []string{"platform", "content"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Post published successfully or returned action_required connection link",
						},
						"401": map[string]interface{}{
							"description": "Unauthorized - valid Bearer token or OAuth session required",
						},
					},
				},
			},
			"/api/v1/upload": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "uploadMedia",
					"summary":     "Stage AI-generated media to public URL",
					"description": "Uploads Base64 binary image/video data and returns a public HTTPS URL accessible by Instagram, YouTube, and Twitter crawlers.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"file_name": map[string]interface{}{
											"type":        "string",
											"description": "Optional filename with extension (e.g. image.jpg, video.mp4)",
										},
										"media_data": map[string]interface{}{
											"type":        "string",
											"description": "Base64-encoded binary string of the image or video",
										},
									},
									"required": []string{"media_data"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Media staged successfully"},
					},
				},
			},
			"/api/v1/insights": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "getAccountInsights",
					"summary":     "Get account-level insights and reach diagnosis",
					"description": "Fetches 28-day reach, profile views, follower metrics, recent posts performance comparison, and engagement diagnostics for an account.",
					"parameters": []map[string]interface{}{
						{
							"name":        "platform",
							"in":          "query",
							"required":    true,
							"schema":      map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
							"description": "Platform to fetch account insights for",
						},
						{
							"name":        "time_period",
							"in":          "query",
							"required":    false,
							"schema":      map[string]interface{}{"type": "string", "enum": []string{"day", "week", "days_28", "lifetime"}, "default": "days_28"},
							"description": "Time window for metrics",
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Account insights retrieved successfully"},
					},
				},
			},
			"/api/v1/analytics": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "getPostAnalytics",
					"summary":     "Get post-level analytics",
					"description": "Retrieves likes, comments, views, and reach for a specific published post ID.",
					"parameters": []map[string]interface{}{
						{
							"name":     "platform",
							"in":       "query",
							"required": true,
							"schema":   map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
						},
						{
							"name":     "post_id",
							"in":       "query",
							"required": true,
							"schema":   map[string]interface{}{"type": "string"},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Post analytics retrieved successfully"},
					},
				},
			},
			"/api/v1/optimize": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "optimizeContentSEO",
					"summary":     "Optimize content with viral hooks and SEO",
					"description": "Generates 3 viral hooks, high-search keyword tags, niche hashtags, and can optionally update live post/video metadata.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"platform":        map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
										"target_type":     map[string]interface{}{"type": "string", "enum": []string{"post", "account_profile", "video"}},
										"topic_or_draft":  map[string]interface{}{"type": "string"},
										"niche":           map[string]interface{}{"type": "string"},
										"target_audience": map[string]interface{}{"type": "string"},
										"post_id":         map[string]interface{}{"type": "string"},
										"apply_update":    map[string]interface{}{"type": "boolean"},
									},
									"required": []string{"platform", "target_type", "topic_or_draft"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "SEO suggestions generated or applied"},
					},
				},
			},
			"/api/v1/schedule": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "schedulePost",
					"summary":     "Schedule post for future publication",
					"description": "Schedules a social media post, Reel, Video, or Tweet for autonomous future publication.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"platform": map[string]interface{}{
											"type": "string",
											"enum": []string{"instagram", "youtube", "twitter"},
										},
										"scheduled_time": map[string]interface{}{
											"type":        "string",
											"description": "ISO 8601 timestamp (e.g. 2026-08-30T18:00:00Z)",
										},
										"content": map[string]interface{}{
											"type": "string",
										},
										"image_prompt": map[string]interface{}{
											"type": "string",
										},
										"media_urls": map[string]interface{}{
											"type":  "array",
											"items": map[string]string{"type": "string"},
										},
										"media_path": map[string]interface{}{
											"type": "string",
										},
										"media_type": map[string]interface{}{
											"type": "string",
											"enum": []string{"IMAGE", "VIDEO", "REELS", "SHORTS"},
										},
									},
									"required": []string{"platform", "scheduled_time"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Post scheduled successfully"},
					},
				},
				"get": map[string]interface{}{
					"operationId": "listScheduledPosts",
					"summary":     "List upcoming scheduled posts",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "List of scheduled posts"},
					},
				},
			},
			"/api/v1/scout": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "scoutTrendingTopics",
					"summary":     "Scan trending topics and generate drafts",
					"description": "Scans real-time live discussions from Reddit & Hacker News, calculates virality velocity scores, and auto-generates platform drafts.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"niche": map[string]interface{}{
											"type": "string",
											"enum": []string{"ai_tech", "tech", "crypto", "business_startups", "programming", "fitness", "gaming", "finance"},
										},
										"platform": map[string]interface{}{
											"type": "string",
											"enum": []string{"all", "twitter", "instagram", "youtube"},
										},
										"limit": map[string]interface{}{
											"type": "integer",
										},
										"auto_draft": map[string]interface{}{
											"type": "boolean",
										},
										"save_drafts": map[string]interface{}{
											"type": "boolean",
										},
									},
									"required": []string{"niche"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Trending topics and generated drafts report"},
					},
				},
			},
			"/api/v1/posts/metadata": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "updatePostMetadata",
					"summary":     "Update and optimize post title, description, and tags for CTR",
					"description": "CTR-based title, description, and keyword tags optimization engine. Generates high-converting curiosity hooks and applies live updates.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"post_id": map[string]interface{}{
											"type": "string",
										},
										"platform": map[string]interface{}{
											"type": "string",
											"enum": []string{"youtube", "instagram", "twitter"},
										},
										"objective": map[string]interface{}{
											"type": "string",
											"enum": []string{"ctr_boost", "seo_search", "viral_rehook", "retention"},
										},
										"niche": map[string]interface{}{
											"type": "string",
										},
										"target_audience": map[string]interface{}{
											"type": "string",
										},
										"custom_title": map[string]interface{}{
											"type": "string",
										},
										"custom_description": map[string]interface{}{
											"type": "string",
										},
										"custom_tags": map[string]interface{}{
											"type":  "array",
											"items": map[string]string{"type": "string"},
										},
										"auto_optimize_ai": map[string]interface{}{
											"type": "boolean",
										},
										"apply_live": map[string]interface{}{
											"type": "boolean",
										},
									},
									"required": []string{"post_id"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Metadata optimization report and live update confirmation"},
					},
				},
			},
			"/api/v1/connect": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "connectPlatform",
					"summary":     "Get platform connection URL",
					"description": "Returns the OAuth connect URL to link user's social media account.",
					"parameters": []map[string]interface{}{
						{
							"name":     "platform",
							"in":       "query",
							"required": true,
							"schema":   map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Connection URL generated"},
					},
				},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{},
			"securitySchemes": map[string]interface{}{
				"OAuth2": map[string]interface{}{
					"type": "oauth2",
					"flows": map[string]interface{}{
						"authorizationCode": map[string]interface{}{
							"authorizationUrl": baseURL + "/oauth/authorize",
							"tokenUrl":         baseURL + "/oauth/token",
							"scopes": map[string]string{
								"read":    "Read analytics and insights",
								"write":   "Publish social media posts",
								"publish": "Publish social media content",
							},
						},
					},
				},
			},
		},
		"security": []map[string][]string{
			{"OAuth2": {"read", "write", "publish"}},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(openAPISpec)
}

func (s *HTTPServer) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Privacy Policy - Social Publisher MCP</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; line-height: 1.6; color: #333;">
  <h1>Privacy Policy</h1>
  <p><strong>Effective Date:</strong> January 1, 2026</p>
  <p>Social Publisher MCP Server ("the Service") allows users to publish content, view analytics, and optimize social media workflows across Instagram, YouTube, and Twitter/X.</p>
  <h2>Data Security & Encryption</h2>
  <p>We do not store passwords or plaintext access credentials. All platform access and refresh tokens are encrypted at rest using industry-standard AES-256-GCM encryption in an isolated vault.</p>
  <h2>Data Sharing</h2>
  <p>Your data is used solely to execute user-initiated publishing and analytics actions. We never sell, rent, or distribute personal data to third parties.</p>
</body>
</html>`))
}

func (s *HTTPServer) handleTerms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Terms of Service - Social Publisher MCP</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; line-height: 1.6; color: #333;">
  <h1>Terms of Service</h1>
  <p><strong>Effective Date:</strong> January 1, 2026</p>
  <p>By using the Social Publisher MCP Server, you agree to comply with platform developer policies of Meta, Google YouTube, and X/Twitter.</p>
</body>
</html>`))
}

func (s *HTTPServer) handleRESTPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	actor := database.GetActor(r.Context())
	log.Printf("[REST /api/v1/publish] START: ActorID=%s, Payload=%+v", actor.ActorID, reqBody)

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "publish_post",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		log.Printf("[REST /api/v1/publish] ERROR: %v", resp.Error)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}

	respJSON, _ := json.Marshal(resp.Result)
	log.Printf("[REST /api/v1/publish] RESPONSE: %s", string(respJSON))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respJSON)
}

func (s *HTTPServer) handleRESTUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "upload_media",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTAnalytics(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	postID := r.URL.Query().Get("post_id")
	if platform == "" || postID == "" {
		http.Error(w, "Query parameters 'platform' and 'post_id' are required", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name": "get_analytics",
		"arguments": map[string]interface{}{
			"platform": platform,
			"post_id":  postID,
		},
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTInsights(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	period := r.URL.Query().Get("time_period")
	if platform == "" {
		http.Error(w, "Query parameter 'platform' is required", http.StatusBadRequest)
		return
	}
	if period == "" {
		period = "days_28"
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name": "get_account_insights",
		"arguments": map[string]interface{}{
			"platform":    platform,
			"time_period": period,
		},
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "optimize_content_seo",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTConnect(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		http.Error(w, "Query parameter 'platform' is required", http.StatusBadRequest)
		return
	}

	actor := database.GetActor(r.Context())
	userID := actor.ActorID
	if userID == "" || userID == "anonymous" {
		userID = "default_user"
	}

	baseURL := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d", s.cfg.ServerPort)
	}
	connectURL := fmt.Sprintf("%s/auth/%s/connect?user_id=%s", baseURL, platform, userID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"platform":    platform,
		"connect_url": connectURL,
		"status":      "action_required",
		"instruction": "Open connect_url in your web browser to authenticate and link account",
	})
}

func (s *HTTPServer) handleRESTSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodPost {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		callReq := mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/call",
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{
			"name":      "schedule_post",
			"arguments": reqBody,
		})
		callReq.Params = paramsJSON

		reqBytes, _ := json.Marshal(callReq)
		resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

		if resp != nil && resp.Error != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(resp.Error)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp.Result)
		return
	}

	if r.Method == http.MethodGet {
		limit := 20
		args := map[string]interface{}{"limit": limit}

		callReq := mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/call",
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{
			"name":      "list_scheduled_posts",
			"arguments": args,
		})
		callReq.Params = paramsJSON

		reqBytes, _ := json.Marshal(callReq)
		resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

		if resp != nil && resp.Error != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(resp.Error)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp.Result)
		return
	}

	http.Error(w, "Method Not Allowed, use POST or GET", http.StatusMethodNotAllowed)
}

func (s *HTTPServer) handleRESTScheduleByID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodDelete {
		http.Error(w, "Method Not Allowed, use DELETE", http.StatusMethodNotAllowed)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[3] == "" {
		http.Error(w, "Missing post ID in path (/api/v1/schedule/{id})", http.StatusBadRequest)
		return
	}
	postID := pathParts[3]

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name": "cancel_scheduled_post",
		"arguments": map[string]interface{}{
			"post_id": postID,
		},
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleCronExecuteScheduled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.schedulerService == nil {
		http.Error(w, "Scheduler service is not initialized", http.StatusServiceUnavailable)
		return
	}

	report, err := s.schedulerService.ExecuteDuePosts(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "success",
		"report": report,
	})
}

func (s *HTTPServer) handleRESTScout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && err != io.EOF {
		http.Error(w, fmt.Sprintf("Invalid JSON payload: %v", err), http.StatusBadRequest)
		return
	}
	if reqBody == nil {
		reqBody = make(map[string]interface{})
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "scout_trending_topics",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTUpdateMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "Method Not Allowed, use POST or PUT", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && err != io.EOF {
		http.Error(w, fmt.Sprintf("Invalid JSON payload: %v", err), http.StatusBadRequest)
		return
	}
	if reqBody == nil {
		reqBody = make(map[string]interface{})
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "update_post_metadata",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTPostRouting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expected /api/v1/posts/{id}/metadata
	if len(pathParts) >= 5 && pathParts[4] == "metadata" {
		postID := pathParts[3]
		var reqBody map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		if reqBody == nil {
			reqBody = make(map[string]interface{})
		}
		reqBody["post_id"] = postID

		callReq := mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/call",
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{
			"name":      "update_post_metadata",
			"arguments": reqBody,
		})
		callReq.Params = paramsJSON

		reqBytes, _ := json.Marshal(callReq)
		resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

		if resp != nil && resp.Error != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(resp.Error)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp.Result)
		return
	}

	http.Error(w, "Not Found", http.StatusNotFound)
}
