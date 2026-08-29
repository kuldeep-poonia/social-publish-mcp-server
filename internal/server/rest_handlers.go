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
			"/api/v1/persona": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "getBrandPersona",
					"summary":     "Get active brand persona",
					"description": "Retrieves the currently locked tone, voice rules, visual aesthetic, and forbidden buzzwords for content generation.",
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Active brand persona configuration"},
					},
				},
				"post": map[string]interface{}{
					"operationId": "setBrandPersona",
					"summary":     "Lock brand persona and aesthetic",
					"description": "Configures brand personality, tone of voice, visual aesthetic, color palette, and prohibited words.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"brand_name": map[string]interface{}{
											"type": "string",
										},
										"tone": map[string]interface{}{
											"type": "string",
											"enum": []string{"sarcastic", "witty", "authoritative", "casual_chill", "bold_provocative", "academic", "inspirational"},
										},
										"visual_style": map[string]interface{}{
											"type": "string",
										},
										"color_palette": map[string]interface{}{
											"type": "string",
										},
										"voice_guidelines": map[string]interface{}{
											"type": "string",
										},
										"forbidden_words": map[string]interface{}{
											"type":  "array",
											"items": map[string]string{"type": "string"},
										},
										"target_audience": map[string]interface{}{
											"type": "string",
										},
									},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Brand persona locked successfully"},
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Privacy Policy - Social Publishing & Analytics MCP Server</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --border: #334155;
      --text: #f8fafc;
      --text-muted: #94a3b8;
      --primary: #38bdf8;
      --primary-glow: rgba(56, 189, 248, 0.15);
      --accent: #818cf8;
      --success: #34d399;
      --warning: #fbbf24;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      line-height: 1.7;
      padding: 40px 20px;
    }
    .container {
      max-width: 880px;
      margin: 0 auto;
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 16px;
      padding: 48px;
      box-shadow: 0 20px 40px rgba(0,0,0,0.4);
    }
    .header {
      border-bottom: 1px solid var(--border);
      padding-bottom: 24px;
      margin-bottom: 32px;
    }
    .badge {
      display: inline-block;
      padding: 4px 12px;
      border-radius: 9999px;
      font-size: 0.8rem;
      font-weight: 600;
      background: var(--primary-glow);
      color: var(--primary);
      border: 1px solid rgba(56, 189, 248, 0.3);
      margin-bottom: 12px;
    }
    h1 { font-size: 2.2rem; font-weight: 800; color: #fff; margin-bottom: 8px; letter-spacing: -0.02em; }
    .meta { font-size: 0.9rem; color: var(--text-muted); }
    h2 { font-size: 1.35rem; font-weight: 700; color: var(--primary); margin: 32px 0 12px; border-left: 4px solid var(--primary); padding-left: 12px; }
    p, li { color: #cbd5e1; font-size: 1rem; margin-bottom: 14px; }
    ul { padding-left: 24px; margin-bottom: 18px; }
    li { margin-bottom: 8px; }
    strong { color: #fff; font-weight: 600; }
    .callout {
      background: rgba(56, 189, 248, 0.08);
      border: 1px solid rgba(56, 189, 248, 0.2);
      border-radius: 12px;
      padding: 20px;
      margin: 20px 0;
    }
    .callout-title { font-weight: 700; color: var(--primary); margin-bottom: 6px; }
    .table-wrapper { overflow-x: auto; margin: 20px 0; }
    table { width: 100%; border-collapse: collapse; text-align: left; font-size: 0.95rem; }
    th, td { padding: 12px 16px; border: 1px solid var(--border); }
    th { background: rgba(15, 23, 42, 0.6); color: var(--primary); font-weight: 600; }
    td { background: rgba(30, 41, 59, 0.5); }
    a { color: var(--primary); text-decoration: none; transition: color 0.2s; }
    a:hover { color: #7dd3fc; text-decoration: underline; }
    .footer {
      border-top: 1px solid var(--border);
      margin-top: 40px;
      padding-top: 24px;
      text-align: center;
      font-size: 0.85rem;
      color: var(--text-muted);
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <span class="badge">Privacy Policy & Data Protection</span>
      <h1>Privacy Policy</h1>
      <div class="meta">
        <strong>Application:</strong> Social Publishing & Analytics MCP Server &bull; 
        <strong>Effective Date:</strong> August 2026 &bull; 
        <strong>Contact:</strong> <a href="mailto:kuldeeppoonia20298@gmail.com">kuldeeppoonia20298@gmail.com</a>
      </div>
    </div>

    <h2>1. Introduction & Purpose</h2>
    <p>The <strong>Social Publishing & Analytics MCP Server</strong> ("we", "our", or "the Service") provides an open-standard Model Context Protocol (MCP) server enabling users and authorized AI assistant clients (such as Claude Desktop, Cursor, and ChatGPT) to publish multimedia posts, schedule queues, optimize post metadata, and analyze engagement metrics across <strong>Twitter/X</strong>, <strong>Instagram (Meta)</strong>, and <strong>YouTube (Google)</strong>.</p>
    <p>We are firmly committed to user privacy. This policy details how our system collects, encrypts, processes, and protects your data.</p>

    <h2>2. Data We Collect & Process</h2>
    <p>We collect only the technical data strictly necessary to execute your requested social media actions:</p>
    <ul>
      <li><strong>OAuth Credentials:</strong> Platform User IDs (Twitter user ID, YouTube channel ID, Instagram business ID) and OAuth 2.0 / 2.1 access and refresh tokens granted during account authorization.</li>
      <li><strong>Post Content & Metadata:</strong> Text, captions, hashtags, video titles, descriptions, and media URLs provided for publishing or scheduling.</li>
      <li><strong>Brand Persona Guidelines:</strong> User-defined brand voice, tone parameters, visual aesthetic notes, and forbidden buzzwords.</li>
      <li><strong>Engagement Metrics:</strong> Post-level impressions, likes, retweets, views, comments, and follower growth retrieved via official platform APIs.</li>
      <li><strong>Security Telemetry:</strong> Anonymized request IPs and timestamps for rate limiting, DDoS defense, and security auditing.</li>
    </ul>

    <div class="callout">
      <div class="callout-title">🔒 What We Do NOT Collect</div>
      <p style="margin-bottom:0;">We <strong>never</strong> collect, access, or store personal account passwords, credit card or billing details, private Direct Messages (DMs), or unauthenticated browsing history.</p>
    </div>

    <h2>3. Cryptographic Storage & Security Vault</h2>
    <p>We enforce industry-leading security practices across our entire infrastructure:</p>
    <ul>
      <li><strong>AES-256-GCM Token Encryption:</strong> All OAuth access and refresh tokens are encrypted at rest using <strong>AES-256-GCM</strong> authenticated encryption with unique per-token nonces. Plaintext tokens are never stored in databases.</li>
      <li><strong>HTTPS & TLS 1.3 In-Transit:</strong> All API communication, OAuth handshakes, and MCP streaming connections occur exclusively over TLS 1.2+ / TLS 1.3 encrypted channels.</li>
      <li><strong>Dual-Layer Secret Scrubbing:</strong> All logging, exception traces, and metrics streams pass through automated regex scrubbers to redact sensitive tokens (<code>[REDACTED]</code>).</li>
    </ul>

    <h2>4. Sub-Processors & Infrastructure</h2>
    <div class="table-wrapper">
      <table>
        <thead>
          <tr>
            <th>Service / Sub-Processor</th>
            <th>Role / Purpose</th>
            <th>Security Standards</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td><strong>Supabase (PostgreSQL)</strong></td>
            <td>Persistent database for scheduled posts, personas, and encrypted token vault</td>
            <td>SOC2 Type II, ISO 27001, AES-256 Encrypted Storage</td>
          </tr>
          <tr>
            <td><strong>Upstash (Redis)</strong></td>
            <td>Ephemeral MCP session management, distributed queue locks, and rate limits</td>
            <td>TLS Encrypted Cache, Ephemeral In-Memory Store</td>
          </tr>
          <tr>
            <td><strong>Google Gemini API</strong></td>
            <td>AI content generation, viral hooks, CTR metadata & SEO tag generation</td>
            <td>Google Cloud Enterprise AI Privacy & Security Terms</td>
          </tr>
          <tr>
            <td><strong>Render</strong></td>
            <td>Hardened cloud container runtime hosting the HTTP / MCP server</td>
            <td>TLS 1.3 Termination, Isolated Linux Containers</td>
          </tr>
        </tbody>
      </table>
    </div>

    <h2>5. How Google Gemini AI Is Used</h2>
    <p>When you invoke AI tools (e.g. <code>scout_trending_topics</code>, <code>update_post_metadata</code>, or autonomous scheduling), content drafts and brand guidelines are processed via <strong>Google Gemini API</strong> (<code>models/gemini-2.5-flash</code>) to produce viral hooks, tailored captions, and SEO tags. Your private data is <strong>never used to train public foundation models</strong>.</p>

    <h2>6. Zero Data Selling & Non-Monetization Commitment</h2>
    <p>We strictly adhere to a <strong>zero-commercialization policy</strong>: We do <strong>NOT</strong> sell, rent, monetize, trade, or share your personal data, post content, or OAuth credentials with any third-party advertisers, data brokers, or marketing networks under any circumstances.</p>

    <h2>7. User Control & Immediate Revocation</h2>
    <p>You have full ownership of your accounts and can revoke access or delete stored data at any time:</p>
    <ul>
      <li><strong>Via the MCP Server:</strong> Use the <code>disconnect_platform</code> tool or send a <code>DELETE</code> request to <code>/auth/{platform}/disconnect</code> to delete your tokens immediately.</li>
      <li><strong>Via Upstream Platform Settings:</strong>
        <ul>
          <li><a href="https://myaccount.google.com/permissions" target="_blank" rel="noopener">Google Account Permissions (YouTube)</a></li>
          <li><a href="https://www.instagram.com/accounts/manage_access/" target="_blank" rel="noopener">Meta / Instagram Apps and Websites</a></li>
          <li><a href="https://twitter.com/settings/connected_apps" target="_blank" rel="noopener">X (Twitter) Connected Apps</a></li>
        </ul>
      </li>
    </ul>

    <h2>8. Contact Information</h2>
    <p>For any privacy inquiries, data deletion requests, or technical support, please contact:</p>
    <p>
      <strong>Developer & Maintainer:</strong> Kuldeep Poonia<br>
      <strong>Direct Email:</strong> <a href="mailto:kuldeeppoonia20298@gmail.com">kuldeeppoonia20298@gmail.com</a><br>
      <strong>GitHub Repository:</strong> <a href="https://github.com/kuldeep-poonia/social-publish-mcp-server" target="_blank" rel="noopener">github.com/kuldeep-poonia/social-publish-mcp-server</a>
    </p>

    <div class="footer">
      &copy; 2026 Social Publishing & Analytics MCP Server. All rights reserved. &bull; <a href="/terms">Terms of Service</a>
    </div>
  </div>
</body>
</html>`))
}

func (s *HTTPServer) handleTerms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Terms of Service - Social Publishing & Analytics MCP Server</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --border: #334155;
      --text: #f8fafc;
      --text-muted: #94a3b8;
      --primary: #38bdf8;
      --primary-glow: rgba(56, 189, 248, 0.15);
      --accent: #818cf8;
      --success: #34d399;
      --warning: #fbbf24;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      line-height: 1.7;
      padding: 40px 20px;
    }
    .container {
      max-width: 880px;
      margin: 0 auto;
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 16px;
      padding: 48px;
      box-shadow: 0 20px 40px rgba(0,0,0,0.4);
    }
    .header {
      border-bottom: 1px solid var(--border);
      padding-bottom: 24px;
      margin-bottom: 32px;
    }
    .badge {
      display: inline-block;
      padding: 4px 12px;
      border-radius: 9999px;
      font-size: 0.8rem;
      font-weight: 600;
      background: var(--primary-glow);
      color: var(--primary);
      border: 1px solid rgba(56, 189, 248, 0.3);
      margin-bottom: 12px;
    }
    h1 { font-size: 2.2rem; font-weight: 800; color: #fff; margin-bottom: 8px; letter-spacing: -0.02em; }
    .meta { font-size: 0.9rem; color: var(--text-muted); }
    h2 { font-size: 1.35rem; font-weight: 700; color: var(--primary); margin: 32px 0 12px; border-left: 4px solid var(--primary); padding-left: 12px; }
    p, li { color: #cbd5e1; font-size: 1rem; margin-bottom: 14px; }
    ul { padding-left: 24px; margin-bottom: 18px; }
    li { margin-bottom: 8px; }
    strong { color: #fff; font-weight: 600; }
    .callout {
      background: rgba(251, 191, 36, 0.08);
      border: 1px solid rgba(251, 191, 36, 0.25);
      border-radius: 12px;
      padding: 20px;
      margin: 20px 0;
    }
    .callout-title { font-weight: 700; color: var(--warning); margin-bottom: 6px; }
    a { color: var(--primary); text-decoration: none; transition: color 0.2s; }
    a:hover { color: #7dd3fc; text-decoration: underline; }
    .footer {
      border-top: 1px solid var(--border);
      margin-top: 40px;
      padding-top: 24px;
      text-align: center;
      font-size: 0.85rem;
      color: var(--text-muted);
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <span class="badge">Legal Terms & Usage Agreement</span>
      <h1>Terms of Service</h1>
      <div class="meta">
        <strong>Application:</strong> Social Publishing & Analytics MCP Server &bull; 
        <strong>Effective Date:</strong> August 2026 &bull; 
        <strong>Contact:</strong> <a href="mailto:kuldeeppoonia20298@gmail.com">kuldeeppoonia20298@gmail.com</a>
      </div>
    </div>

    <h2>1. Acceptance of Terms</h2>
    <p>By connecting to, accessing, or using the <strong>Social Publishing & Analytics MCP Server</strong> ("the Service"), you agree to be bound by these Terms of Service ("Terms"). If you do not agree with any part of these Terms, you must immediately disconnect your accounts and cease using the Service.</p>

    <h2>2. Description of the Service</h2>
    <p>The Service provides a Model Context Protocol (MCP) backend interface enabling authorized AI clients (including Claude Desktop, Cursor, ChatGPT, and custom LLM workflows) to:</p>
    <ul>
      <li>Connect social media accounts on <strong>Twitter/X</strong>, <strong>Instagram (Meta)</strong>, and <strong>YouTube (Google)</strong> via OAuth 2.0 / 2.1 PKCE.</li>
      <li>Publish, schedule, and automate multimedia posts, Reels, YouTube Shorts/Videos, and Tweets.</li>
      <li>Retrieve post engagement metrics, audience reach, and algorithmic account health insights.</li>
      <li>Scout live trending discussions (Reddit, Hacker News) and optimize SEO / CTR metadata using Google Gemini AI.</li>
      <li>Enforce Brand Persona voice, tone, and forbidden buzzword policies across generated copy.</li>
    </ul>

    <h2>3. User Responsibilities & AI Content Review</h2>
    <div class="callout">
      <div class="callout-title">⚠️ Mandatory AI Content Review Policy</div>
      <p style="margin-bottom:0;">The Service leverages Artificial Intelligence (including <strong>Google Gemini API</strong>) for content drafting, hashtag generation, and autonomous scheduling. <strong>You are solely and fully responsible for reviewing, auditing, and approving all AI-generated content before or at publication time.</strong></p>
    </div>
    <p>You agree not to use the Service to publish content that:</p>
    <ul>
      <li>Violates applicable local, national, or international laws.</li>
      <li>Contains hate speech, harassment, defamation, intellectual property infringement, malware, or deceptive spam.</li>
      <li>Violates the terms, developer policies, or community guidelines of Twitter/X, YouTube/Google, or Meta/Instagram.</li>
    </ul>

    <h2>4. Platform Policy Compliance</h2>
    <p>Your usage of this Service is conditioned upon strict compliance with third-party platform developer terms:</p>
    <ul>
      <li><a href="https://www.youtube.com/t/terms" target="_blank" rel="noopener">YouTube Terms of Service</a> &amp; <a href="https://policies.google.com/privacy" target="_blank" rel="noopener">Google Privacy Policy</a></li>
      <li><a href="https://developers.facebook.com/terms/" target="_blank" rel="noopener">Meta Platform Terms &amp; Instagram Community Guidelines</a></li>
      <li><a href="https://developer.x.com/en/developer-terms/agreement-and-policy" target="_blank" rel="noopener">X (Twitter) Developer Agreement &amp; Developer Policy</a></li>
    </ul>

    <h2>5. Service Availability & Uptime Disclaimer</h2>
    <p>The Service is provided on an <strong>"AS IS" and "AS AVAILABLE" basis</strong> without express or implied warranties of any kind. Infrastructure components may be hosted on cloud free-tier or shared hosting services (including Render, Supabase, and Upstash) that may experience occasional restarts, cold-starts, maintenance windows, or temporary downtime. We do not guarantee uninterrupted, error-free 100% uptime.</p>

    <h2>6. Limitation of Liability</h2>
    <p>To the maximum extent permitted by applicable law, the developers and maintainers of the Service shall not be liable for any indirect, incidental, special, consequential, or punitive damages, including loss of profits, data loss, API rate limit exhaustion, platform quota suspensions, or account restrictions/bans imposed by third-party social networks.</p>

    <h2>7. Account Disconnection & Termination</h2>
    <p>You may disconnect your social accounts and terminate access at any time via the <code>disconnect_platform</code> tool, the <code>/auth/{platform}/disconnect</code> endpoint, or via your Google, Meta, or Twitter account security settings. We reserve the right to revoke access to any client that abuses the Service or attempts to compromise server security.</p>

    <h2>8. Governing Law & Jurisdiction</h2>
    <p>These Terms shall be governed by, construed, and enforced in accordance with the <strong>laws of India</strong>, without regard to conflict of law principles. Any dispute arising out of or relating to these Terms shall be subject to the exclusive jurisdiction of the competent courts located in India.</p>

    <h2>9. Contact Information</h2>
    <p>If you have any questions, feedback, or legal notices concerning these Terms of Service, please contact:</p>
    <p>
      <strong>Developer & Maintainer:</strong> Kuldeep Poonia<br>
      <strong>Direct Email:</strong> <a href="mailto:kuldeeppoonia20298@gmail.com">kuldeeppoonia20298@gmail.com</a><br>
      <strong>GitHub Project:</strong> <a href="https://github.com/kuldeep-poonia/social-publish-mcp-server" target="_blank" rel="noopener">github.com/kuldeep-poonia/social-publish-mcp-server</a>
    </p>

    <div class="footer">
      &copy; 2026 Social Publishing & Analytics MCP Server. All rights reserved. &bull; <a href="/privacy">Privacy Policy</a>
    </div>
  </div>
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

func (s *HTTPServer) handleRESTPersona(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		callReq := mcp.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/call",
		}
		paramsJSON, _ := json.Marshal(map[string]interface{}{
			"name":      "get_brand_persona",
			"arguments": map[string]interface{}{},
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

	case http.MethodPost, http.MethodPut:
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
			"name":      "set_brand_persona",
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

	default:
		http.Error(w, "Method Not Allowed, use GET or POST", http.StatusMethodNotAllowed)
	}
}
