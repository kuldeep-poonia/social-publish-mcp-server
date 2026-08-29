# Social Publishing & Analytics MCP Server

[![Go Version](https://img.shields.io/badge/Go-1.26.6-00ADD8?style=flat&logo=go)](https://golang.org)
[![MCP Specification](https://img.shields.io/badge/MCP-2024--11--05-8A2BE2?style=flat)](https://modelcontextprotocol.io)
[![Live Gateway](https://img.shields.io/badge/Production-Live%20Online-34c759?style=flat&logo=render)](https://social-mcp.duckdns.org)
[![Security Standard](https://img.shields.io/badge/OWASP-API%20Top%2010%20Compliant-green)](SECURITY.md)
[![Privacy Policy](https://img.shields.io/badge/Legal-Privacy%20Policy-blue)](https://social-mcp.duckdns.org/privacy)
[![Terms of Service](https://img.shields.io/badge/Legal-Terms%20of%20Service-blue)](https://social-mcp.duckdns.org/terms)

An enterprise-grade **Model Context Protocol (MCP)** server that connects AI assistants (**Claude Desktop**, **Cursor**, **ChatGPT**, **Windsurf**, and autonomous multi-agent swarms) to **Twitter / X**, **YouTube**, and **Instagram** for automated publishing, engagement analytics, autonomous scheduling, trending topic scouting, CTR optimization, and brand persona voice locking under strict authenticated user authorization.

---

### 🌐 Official Gateway & Production Endpoints

| Resource | URL | Protocol / Format | Access Level |
| :--- | :--- | :--- | :--- |
| **Official Web Landing Page** | [https://social-mcp.duckdns.org/](https://social-mcp.duckdns.org/) | HTML5 / Dark-Mode | Public |
| **Streamable HTTP MCP Endpoint** | `https://social-mcp.duckdns.org/mcp` | MCP JSON-RPC 2.0 / Streamable HTTP | Authenticated |
| **Legacy SSE MCP Endpoint** | `https://social-mcp.duckdns.org/mcp/sse` | MCP Server-Sent Events | Authenticated |
| **OAuth 2.1 Server Metadata** | `https://social-mcp.duckdns.org/.well-known/oauth-authorization-server` | RFC 8414 JSON | Public |
| **Dynamic Client Registration** | `https://social-mcp.duckdns.org/oauth/register` | RFC 7591 JSON | Public |
| **ChatGPT OpenAPI Specification** | `https://social-mcp.duckdns.org/openapi.json` | OpenAPI 3.1.0 JSON | Public |
| **Privacy Policy** | [https://social-mcp.duckdns.org/privacy](https://social-mcp.duckdns.org/privacy) | HTML / Markdown | Public |
| **Terms of Service** | [https://social-mcp.duckdns.org/terms](https://social-mcp.duckdns.org/terms) | HTML / Markdown | Public |
| **System Healthcheck** | [https://social-mcp.duckdns.org/health](https://social-mcp.duckdns.org/health) | JSON | Public |

---

## 📑 Table of Contents
1. [Core Features & Capabilities](#1-core-features--capabilities)
2. [Complete MCP Tool Catalog (14 Tools)](#2-complete-mcp-tool-catalog-14-tools)
3. [MCP Protocol Compliance & Transports](#3-mcp-protocol-compliance--transports)
4. [Supported Platforms & Feature Matrix](#4-supported-platforms--feature-matrix)
5. [Quickstart Setup Guide](#5-quickstart-setup-guide)
6. [Security & Zero-Trust Threat Mitigation](#6-security--zero-trust-threat-mitigation)
7. [Sub-Processors & Cloud Infrastructure](#7-sub-processors--cloud-infrastructure)
8. [Documentation Hub & Contact](#8-documentation-hub--contact)

---

## 1. Core Features & Capabilities

- 🚀 **Multi-Platform Publishing Engine**: Immediate publishing to Twitter/X (tweets & multi-tweet threads), Instagram (feed photos, 10-item carousels, 9:16 vertical Reels), and YouTube (8MB resumable chunk video uploads & YouTube Shorts).
- ⏱️ **Autonomous Multi-Platform Scheduler**: Schedule posts with dual-trigger execution: internal 30-second polling worker combined with external serverless cron triggers (`POST /api/v1/cron/execute-scheduled`).
- 🔍 **Real-Time Trending Topic Scout**: Ingests real-time discussions from Reddit JSON feeds and Hacker News Firebase API, using Google Gemini 2.5 Flash to synthesize viral hooks, captions, and sanitized hashtags.
- 🎯 **CTR-Driven Metadata Optimizer**: Evaluates drafts and generates 3 psychological title angles (Curiosity Gap, Data-Driven, Contrarian), description hooks, and categorical SEO tags.
- 🎭 **Brand Persona & Voice Lock Engine**: Enforces consistent brand voice guidelines, tone, aesthetic palettes, and contextual LLM-based rewrites of forbidden corporate buzzwords.
- 🔒 **Zero-Trust Security & Vault**: OAuth tokens encrypted at rest using **AES-256-GCM** with per-record nonces. Socket-layer kernel SSRF guard prevents internal IP probing.

---

## 2. Complete MCP Tool Catalog (14 Tools)

The server exposes 14 production-ready Model Context Protocol tools:

### Core Publishing & Media
1. `publish_post`: Publishes text, media, carousels, threads, reels, or videos immediately to Twitter, Instagram, or YouTube.
   - *Arguments*: `platform` (string), `content` (string), `media_urls` (array), `media_type` (string), `title` (string, YouTube only), `tags` (array).
2. `upload_media`: Validates, transcodes, and stages remote or base64 media assets for upcoming publications.
   - *Arguments*: `media_url` (string), `media_type` (string), `platform` (string).

### Engagement Analytics & Insights
3. `get_analytics`: Fetches real-time impressions, views, retweets, likes, and comments for specific posts or video IDs.
   - *Arguments*: `platform` (string), `post_id` (string).
4. `get_account_insights`: Retrieves high-level 30-day follower growth, profile reach, and total engagement breakdown for a connected account.
   - *Arguments*: `platform` (string), `timeframe` (string).

### Autonomous Scheduling
5. `schedule_post`: Queues a post for automated publishing at a specified future UTC ISO-8601 timestamp.
   - *Arguments*: `platform` (string), `content` (string), `scheduled_time` (string ISO-8601), `media_urls` (array), `media_type` (string).
6. `list_scheduled_posts`: Returns all pending, queued, and completed scheduled publications across accounts.
   - *Arguments*: `platform` (optional string), `status` (optional string).
7. `cancel_scheduled_post`: Cancels and deletes an unexecuted scheduled post.
   - *Arguments*: `scheduled_id` (string).

### Content Intelligence & Scouting
8. `scout_trending_topics`: Scrapes live discussion feeds from Reddit and Hacker News to generate tailored post drafts with hooks and sanitized hashtags.
   - *Arguments*: `category` (string, e.g. "tech", "ai", "general"), `limit` (integer 1-10).

### Optimization & Search Discovery
9. `update_post_metadata`: Generates high-CTR title variations (Curiosity Gap, Data-Driven, Contrarian) and description hooks for existing drafts.
   - *Arguments*: `post_id` (optional string), `draft_title` (optional string), `draft_description` (optional string), `platform` (string).
10. `optimize_content_seo`: Performs algorithmic keyword optimization, hashtag sanitization, and categorical tag generation for maximum platform discoverability.
    - *Arguments*: `content` (string), `platform` (string), `target_keywords` (array).

### Brand Persona & Governance
11. `set_brand_persona`: Configures persistent voice guidelines, tone, aesthetic palettes, and forbidden buzzwords.
    - *Arguments*: `tone` (string), `voice_guidelines` (string), `visual_palette` (string), `forbidden_buzzwords` (array).
12. `get_brand_persona`: Retrieves the active brand persona rules, tone guidelines, and forbidden word filters for the current tenant.
    - *Arguments*: None.

### Account Authentication & Health
13. `connect_platform`: Generates an authenticated OAuth 2.0 PKCE link to connect or refresh Twitter, Instagram, or YouTube tokens.
    - *Arguments*: `platform` (string: "twitter", "instagram", "youtube").
14. `ping`: Diagnostics endpoint verifying database connectivity, Redis stream latency, and gateway health.
    - *Arguments*: None.

---

## 3. MCP Protocol Compliance & Transports

The Social MCP Server adheres strictly to the **Model Context Protocol (MCP)** specification:

- **Streamable HTTP Transport (`POST /mcp`)**: Full support for session-aware HTTP requests using the `Mcp-Session-Id` header and Redis-backed session tracking.
- **Legacy Server-Sent Events Transport (`GET /mcp/sse` + `POST /mcp/messages`)**: Maintained in parallel for full backward compatibility with older MCP client builds.
- **Dynamic Client Registration (RFC 7591)**: `POST /oauth/register` enables MCP clients (Cursor, Claude, Claude Desktop) to dynamically register client credentials on-the-fly.
- **OAuth 2.1 Authorization Server Metadata (RFC 8414)**: Served at `/.well-known/oauth-authorization-server` and `/.well-known/openid-configuration` with strict `https://` issuer scheme enforcement.
- **PKCE S256 Security**: Enforces Proof Key for Code Exchange (S256) on all authorization code exchanges.

---

## 4. Supported Platforms & Feature Matrix

| Feature | Twitter / X API v2 | Instagram Graph API v21.0 | YouTube Data & Upload v3 |
| :--- | :---: | :---: | :---: |
| **Text Posts / Tweets** | ✅ Multi-Tweet Threads | N/A (Media Required) | N/A (Video Required) |
| **Image Posts** | ✅ Single & Multi-Image | ✅ Feed Photo & 10-Item Carousels | N/A |
| **Short-Form Video** | ✅ Video Tweets | ✅ 9:16 Vertical Reels | ✅ YouTube Shorts |
| **Long-Form Video** | ✅ Media Chunking | ❌ Max 15 mins (Reels) | ✅ 8MB Resumable Streaming |
| **Automated Scheduling** | ✅ Dual-Trigger Worker | ✅ Dual-Trigger Worker | ✅ Dual-Trigger Worker |
| **Engagement Analytics** | ✅ Impressions, Likes, RTs | ✅ Reach, Impressions, Saves | ✅ Views, Watch Time, Likes |
| **Daily Quota Budgeting** | Standard Tier Protection | Container Polling State Machine | 10,000 Unit Quota Budget Vault |

---

## 5. Quickstart Setup Guide

### Claude Desktop Configuration
Add the server entry to `%APPDATA%\Claude\claude_desktop_config.json` (Windows) or `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS):

```json
{
  "mcpServers": {
    "social-publisher": {
      "url": "https://social-mcp.duckdns.org/mcp"
    }
  }
}
```

### Cursor IDE Configuration
1. Open **Settings** $\rightarrow$ **Features** $\rightarrow$ **MCP Servers**.
2. Click **Add New MCP Server**.
3. Name: `social-publisher` | Type: `SSE` | URL: `https://social-mcp.duckdns.org/mcp/sse`.

For detailed, click-by-click walkthroughs and prompt cheat-sheets, read the [USER_GUIDE.md](USER_GUIDE.md).

---

## 6. Security & Zero-Trust Threat Mitigation

- **AES-256-GCM Token Encryption**: All social access tokens and refresh tokens are encrypted at rest with unique 96-bit nonces. Decryption occurs exclusively in-memory during platform API dispatch.
- **Kernel-Level Socket SSRF Defense**: Prevents DNS rebinding and TOCTOU attacks via `net.Dialer.Control` socket pinning, rejecting requests to loopback (`127.0.0.1`), private RFC1918 subnets (`10.0.0.0/8`, `192.168.0.0/16`), and cloud metadata IP (`169.254.169.254`).
- **Cryptographic Secret Scrubbing**: All server logs and error messages pass through a dual-pass regex filter that automatically redacts API keys, bearer tokens, and credentials.
- **Idempotency & Deduplication**: Cryptographic hashing prevents double-posting during transient network retries.

---

## 7. Sub-Processors & Cloud Infrastructure

- **Application Container Runtime**: Render Cloud Platform
- **Relational Storage & Token Vault**: Supabase (PostgreSQL 16 Multi-Tenant)
- **Distributed Caching & Session Storage**: Upstash (Redis 7)
- **AI Content Intelligence & SEO**: Google Gemini API (`models/gemini-2.5-flash`)

---

## 8. Documentation Hub & Contact

- 📖 **User Guide & Prompt Cheat-Sheet**: [USER_GUIDE.md](USER_GUIDE.md)
- 🏗️ **Technical Architecture Specification**: [ARCHITECTURE.md](ARCHITECTURE.md)
- 🛡️ **Security Standard & Threat Model**: [SECURITY.md](SECURITY.md)
- 📜 **Privacy Policy**: [https://social-mcp.duckdns.org/privacy](https://social-mcp.duckdns.org/privacy)
- ⚖️ **Terms of Service**: [https://social-mcp.duckdns.org/terms](https://social-mcp.duckdns.org/terms)

**Maintainer & Support Contact**: [kuldeeppoonia20298@gmail.com](mailto:kuldeeppoonia20298@gmail.com)
