# Social Publishing & Analytics MCP Server

[![Go Version](https://img.shields.io/badge/Go-1.26.6-00ADD8?style=flat&logo=go)](https://golang.org)
[![MCP Specification](https://img.shields.io/badge/MCP-2024--11--05-8A2BE2?style=flat)](https://modelcontextprotocol.io)
[![Security Standard](https://img.shields.io/badge/OWASP-API%20Top%2010%20Compliant-green)](SECURITY.md)
[![Docker Ready](https://img.shields.io/badge/Docker-Multi--Container%20Ready-2496ED?style=flat&logo=docker)](deploy/docker-compose.yml)
[![License: Commercial](https://img.shields.io/badge/License-Commercial%20%2F%20Proprietary-red.svg)](LICENSE)

A high-performance, secure **Model Context Protocol (MCP)** server that connects AI assistants (Claude Desktop, ChatGPT connectors, Gemini, and autonomous agent swarms) to **Twitter / X**, **YouTube**, and **Instagram** for automated content publishing and engagement analytics under authenticated user authorization.

---

## 📑 Table of Contents
1. [The Problem & The Solution](#1-the-problem--the-solution)
2. [Key Architecture & Capabilities](#2-key-architecture--capabilities)
3. [Supported Platforms & Feature Matrix](#3-supported-platforms--feature-matrix)
4. [MCP Tool Catalog & API Specification](#4-mcp-tool-catalog--api-specification)
5. [OAuth 2.1 Authentication & User Flow](#5-oauth-21-authentication--user-flow)
6. [Security & Threat Mitigation](#6-security--threat-mitigation)
7. [Production Deployment & Observability](#7-production-deployment--observability)
8. [Complete Documentation Hub](#8-complete-documentation-hub)
9. [Commercial Licensing & Purchasing](#9-commercial-licensing--purchasing)

---

## 1. The Problem & The Solution

### The Challenge with AI Agents & Social APIs
Allowing Large Language Models (LLMs) and autonomous agents to publish directly to social networks introduces critical risks:
- **Credential Exposure**: Storing raw API tokens in client environments or passing them through prompt contexts risks accidental leakages.
- **SSRF & DNS-Rebinding Vulnerabilities**: When agents ingest remote media URLs to publish, malicious inputs can probe cloud metadata services (`169.254.169.254`) or internal private networks.
- **Duplicate Publishing on Retries**: Network blips often cause agents to re-send requests, accidentally creating duplicate tweets or double-consuming daily video quotas.
- **Upstream Rate Limiting**: Social networks enforce strict rate limits (e.g. YouTube's $10{,}000\text{ units/day}$ quota budget; Twitter's per-tier rate caps) that crash naive integrations.

### The Solution: Social Publishing MCP Server
Written in pure **Go (1.26.6)** for microsecond latency and minimal memory footprint, the server acts as a hardened gateway between LLM clients and social APIs:
- **Zero-Trust Token Vault**: Credentials never touch LLM prompts; all OAuth tokens are encrypted at rest with AES-256-GCM using out-of-band keys.
- **Kernel-Level Socket SSRF Defense**: Remote URLs are validated at the operating system socket layer (`net.Dialer.Control`), defeating DNS rebinding and TOCTOU attacks.
- **Sync-First Resilient Retry Queue**: First-attempt synchronous execution with automatic Redis 7 Stream fallback for transient $429 / 503$ platform errors.
- **Transactional Idempotency**: Cryptographic deduplication prevents double-posting during connection retries.

---

## 2. Key Architecture & Capabilities

```mermaid
flowchart TD
    subgraph ClientLayer["AI Assistants & Agents"]
        Claude["Claude Desktop / Claude Web"]
        Agent["Autonomous Agent Swarms"]
        GPT["ChatGPT Connectors"]
    end

    subgraph ServerGateway["Social MCP Server Gateway"]
        Router["HTTP Gateway & SSE Transport"]
        AuthGuard["OAuth 2.1 + PKCE S256 Guard"]
        RateLimiter["Redis Token-Bucket Rate Limiter"]
        SSRF["Kernel Socket SSRF & IP-Pinning Guard"]
    end

    subgraph Storage["Persistence & Resilience Tier"]
        PG[("PostgreSQL 16 Multi-Tenant Vault")]
        Redis[("Redis 7 Stream Retry Queue")]
    end

    subgraph Adapters["Platform Adapters"]
        Twitter["Twitter / X API v2"]
        YouTube["YouTube Upload & Data v3"]
        Instagram["Meta / Instagram Graph API v20.0"]
    end

    subgraph Upstream["Social Media Networks"]
        XNet["Twitter / X"]
        YTNet["YouTube"]
        MetaNet["Instagram"]
    end

    ClientLayer ==>|JSON-RPC 2.0 / SSE| Router
    Router --> AuthGuard
    AuthGuard --> RateLimiter
    RateLimiter --> SSRF
    SSRF --> Adapters
    Adapters <==>|AES-256-GCM Decrypt| PG
    Adapters -->|Transient Retry Jobs| Redis
    Redis --> Adapters
    Adapters ==>|HTTPS TLS 1.2+| Upstream
```

---

## 3. Supported Platforms & Feature Matrix

| Feature | Twitter / X | YouTube | Instagram |
| :--- | :---: | :---: | :---: |
| **Supported Media** | Plain Text, Images (`JPEG`, `PNG`, `GIF`, `WebP`) | Long-Form Videos & Shorts (`MP4`, `MOV`) | Feed Photos, Carousels, Reels (`JPEG`, `PNG`, `MP4`) |
| **Resumable Uploads** | Chunked Media Upload | $8\text{MB}$ Streaming Chunks with Resumption | Container State Machine Polling |
| **Image Transcoding** | Automatic Dimension Check | N/A (Video Only) | Automatic PNG-to-JPEG Conversion |
| **Idempotency Locks** | Transactional Lock Keys | Resumable Upload Session ID | Container Deduplication |
| **Engagement Telemetry** | Impressions, Likes, Retweets, Replies, Quotes | Views, Likes, Comments, Watch Duration | Impressions, Reach, Likes, Comments, Shares, Saves |
| **Daily Quota Budget** | Rate-Limit Token Bucket | $10{,}000\text{ Units/Day}$ Tracker with Auto-Refund | Meta Rate-Limit Header Tracker |

---

## 4. MCP Tool Catalog & API Specification

The server exposes standard Model Context Protocol tools over Streamable HTTP and SSE transports:

### 1. `publish_post`
Publishes text, image attachments, or video content to the target social network.

**Tool Parameters:**
```json
{
  "platform": "twitter | youtube | instagram",
  "content": "Text caption or description for the post",
  "media_urls": ["https://example.com/image.jpg"],
  "title": "Required for YouTube video uploads",
  "visibility": "public | unlisted | private",
  "media_type": "IMAGE | VIDEO | REEL | CAROUSEL"
}
```

**Success Response (JSON-RPC 2.0):**
```json
{
  "jsonrpc": "2.0",
  "result": {
    "platform": "twitter",
    "post_id": "1894726190283741184",
    "status": "published",
    "url": "https://twitter.com/user/status/1894726190283741184",
    "published_at": "2026-08-25T15:30:00Z"
  },
  "id": 1
}
```

---

### 2. `get_post_analytics`
Retrieves live engagement analytics and metric telemetry for a published post.

**Tool Parameters:**
```json
{
  "platform": "twitter | youtube | instagram",
  "post_id": "1894726190283741184"
}
```

**Sample Analytics Output:**
```json
{
  "platform": "instagram",
  "post_id": "17992834710294811",
  "metrics": {
    "impressions": 14250,
    "reach": 11890,
    "likes": 842,
    "comments": 67,
    "shares": 128,
    "saved": 94
  },
  "retrieved_at": "2026-08-25T15:30:00Z"
}
```

---

### 3. `list_connections`
Returns all active social network connections for the authenticated actor.

**Sample Output:**
```json
{
  "connections": [
    { "platform": "twitter", "connected_at": "2026-08-20T10:00:00Z", "status": "active" },
    { "platform": "youtube", "connected_at": "2026-08-21T12:00:00Z", "status": "active" },
    { "platform": "instagram", "connected_at": "2026-08-22T14:00:00Z", "status": "active" }
  ]
}
```

---

### 4. `disconnect_platform`
Revokes upstream platform authorization and deactivates stored credentials.

**Tool Parameters:**
```json
{
  "platform": "twitter | youtube | instagram"
}
```

---

## 5. OAuth 2.1 Authentication & User Flow

The server implements RFC 7636 Authorization Code Flow with mandatory **PKCE S256**:

```mermaid
flowchart LR
    User[User / AI] -->|1. Request Connect Link| Server[MCP Server]
    Server -->|2. Generate PKCE Challenge| Browser[Web Browser]
    Browser -->|3. User Grants Permissions| Social[Twitter / Google / Meta]
    Social -->|4. Auth Code Redirect| Server
    Server -->|5. Exchange Token & AES Encrypt| DB[(PostgreSQL Vault)]
```

1. **Connect Endpoint**: Navigate to `http://localhost:8080/auth/{platform}/connect` in your browser.
2. **Permission Grant**: Approve the requested publishing and analytics scopes.
3. **Encrypted Storage**: The server exchanges the one-time authorization code for long-lived OAuth tokens, encrypts them with **AES-256-GCM**, and saves them to the relational vault.

---

## 6. Security & Threat Mitigation

| Threat Vector | Defense Mechanism | Empirical Verification |
| :--- | :--- | :--- |
| **SSRF & DNS Rebinding** | Kernel-level `net.Dialer.Control` socket hook inspects resolved IP during socket dial. Blocks cloud metadata (`169.254.169.254`), loopbacks, and RFC 1918 subnets. | **48/48 payloads blocked (100%)** |
| **Token Theft at Rest** | AES-256-GCM authenticated encryption with 96-bit random nonces. Master key isolated out-of-band in secrets manager. | **100% Verified** in test suites |
| **Multi-Tenant IDOR** | Cryptographic session context binding (`database.GetActor`). Queries enforce `WHERE user_id = $1`. | **110/110 probes blocked (0 leaks)** |
| **SQL Injection** | Strict parameterization ($1, $2) across all PostgreSQL query builders. | **85/85 payloads neutralized (100%)** |
| **Telemetry Leaks** | Dual-layer log scrubber masks access tokens, bearer headers, and secrets (`[REDACTED]`). | **500/500 probes scrubbed (0 leaks)** |
| **Man-in-the-Middle** | Mandatory TLS 1.2+ listener with forward-secret AEAD cipher suites. Rejects SSLv3, TLS 1.0, TLS 1.1. | **Verified via live network handshakes** |
| **Supply Chain Safety** | Pinned Go 1.26.6 runtime and audited dependency call-graph. | **`govulncheck`: 0 vulnerabilities** |

---

## 7. Production Deployment & Observability

### Multi-Container Stack Deployment
Deploy the full multi-service stack (MCP App, PostgreSQL 16, Redis 7, Prometheus, and Grafana) with Docker Compose:

```bash
# Launch multi-container stack
docker compose -f deploy/docker-compose.yml up -d
```

### Telemetry & Monitoring Infrastructure
- **Prometheus Metrics (`:8080/metrics`)**: Bearer-token protected telemetry stream scraping request rates, latency histograms ($p50, p90, p99$), rate-limit blocks, and retry stream depth.
- **Pre-Configured Grafana Dashboard (`:3000`)**: Auto-provisioned dashboard displaying live RPS by platform, retry stream backlog, and Dead-Letter Queue (DLQ) volume.
- **Healthcheck (`:8080/health`)**: Sanitized JSON health status endpoint returning minimal operational metadata.

---

## 8. Complete Documentation Hub

Explore the full documentation suite in the repository root:

- 📖 **[User Guide](USER_GUIDE.md)** — Step-by-step account onboarding, natural language prompts, and publishing workflows.
- 🏗️ **[Technical Architecture Specification](ARCHITECTURE.md)** — Distributed systems blueprint, publish sequence diagrams, and queue design.
- 🛡️ **[Security Policy & Threat Model](SECURITY.md)** — Cryptographic standards, SSRF defenses, and vulnerability disclosure policy.
- 🔬 **[Penetration Test Report](PEN_TEST_REPORT.md)** — Empirical security audit, fuzzing battery, and vulnerability scan logs.
- 🚨 **[Incident Response Runbooks](INCIDENT_RESPONSE.md)** — Operational P0-P3 response procedures for on-call engineers.
- 📜 **[Privacy Policy](PRIVACY_POLICY.md)** — Data collection limits, retention policies, and zero data-selling commitment.
- ❓ **[Frequently Asked Questions (FAQ)](FAQ.md)** — Quotas, token lifetimes, rate limits, and commercial licensing.

---

## 9. Commercial Licensing & Purchasing

This software is a **commercial proprietary product** and is **not open-source or free software**. Deployment, integration, or commercial usage requires a valid commercial license agreement.

- **Commercial Inquiries & Purchasing**: `licensing@socialmcp.io`
- **License Terms**: See [LICENSE](LICENSE) for full details.
