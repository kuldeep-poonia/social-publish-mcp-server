# Social Publishing & Analytics MCP Server

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![MCP](https://img.shields.io/badge/Protocol-Model%20Context%20Protocol-8A2BE2)](https://modelcontextprotocol.io)

A production-grade, multi-tenant, universal **Model Context Protocol (MCP)** server that enables MCP-compliant LLM clients (such as Claude, ChatGPT via connector, Gemini, and custom agents) to securely publish content across social platforms and retrieve engagement analytics under direct, authenticated user authorization.

---

## 🎯 Key Features

- **Multi-Platform Content Publishing**: Publish text, media, and video to **Twitter/X**, **YouTube**, and **Instagram** (Business/Creator accounts).
- **Engagement Analytics Retrieval**: Inspect impressions, reach, likes, comments, and shares directly within the LLM conversation.
- **Strict Zero-Trust Token Vault**: AES-256-GCM encryption at rest for all OAuth tokens with user-level isolation and cryptographic boundary enforcement.
- **Universal MCP Compliance**: Compatible with any client implementing the Model Context Protocol standard over Streamable HTTP.
- **Idempotent Operations & Rate Limiting**: Distributed Redis-backed rate limiting and idempotency controls preventing duplicate posts and API quota overages.
- **Auditability**: Structured audit logs capturing all actions and token lifecycle events without ever exposing plaintext secrets or payloads.

---

## 🏗️ High-Level Architecture

```
                         ┌───────────────────────────┐
   Any MCP Client   ───▶ │   MCP Gateway (Go, HTTP)   │
 (Claude/GPT/Gemini)     │  Streamable HTTP transport │
                         └─────────────┬─────────────┘
                                       │  OAuth 2.1 + PKCE
                                       ▼
                         ┌───────────────────────────┐
                         │  Identity & Session Layer  │
                         │  (maps MCP session → user) │
                         └─────────────┬─────────────┘
                                       │
                         ┌─────────────┴─────────────┐
                         ▼                             ▼
              ┌────────────────────┐        ┌────────────────────┐
              │  Publish Orchestr.  │        │  Analytics Service  │
              │  + Retry Queue      │        │                     │
              └──────────┬─────────┘        └──────────┬──────────┘
                         │                              │
              ┌──────────┴──────────────────────────────┴──────────┐
              ▼                       ▼                            ▼
      Twitter Adapter          YouTube Adapter            Instagram Adapter
              │                       │                            │
              └───────────┬───────────┴────────────┬───────────────┘
                          ▼                        ▼
                 ┌─────────────────┐      ┌──────────────────┐
                 │  Token Vault     │      │  Rate Limiter     │
                 │  (Postgres, AES) │      │  (Redis)          │
                 └─────────────────┘      └──────────────────┘
```

---

## 🔒 Security Principles

1. **Least Privilege**: Only the exact OAuth scopes required for publishing and analytics are requested.
2. **Zero Plaintext Credentials**: OAuth tokens, refresh tokens, and client secrets are never written in plaintext to databases, logs, or disk.
3. **Session-Bound Authorization**: Every data access and write operation cryptographically validates the user identity from the authenticated session.
4. **Immediate Revocation**: Disconnecting any platform immediately invalidates the local encryption keys and triggers an upstream revocation call.

---

## 🚀 Getting Started

### Prerequisites

- [Go](https://golang.org/dl/) (1.22+)
- [Docker](https://www.docker.com/) & Docker Compose
- API credentials for target social platforms (Twitter Developer Portal, Google Cloud Console, Meta for Developers)

### Local Setup

1. **Clone the repository**:
   ```bash
   git clone https://github.com/kuldeep-poonia/social-publish-mcp-server.git
   cd social-publish-mcp-server
   ```

2. **Configure environment variables**:
   ```bash
   cp .env.example .env
   # Edit .env and supply your configuration
   ```

3. **Start local infrastructure (Postgres & Redis)**:
   ```bash
   docker compose -f deploy/docker-compose.yml up -d
   ```

4. **Run the server**:
   ```bash
   go run cmd/server/main.go
   ```

---

## 📋 MCP Tools Exposed

| Tool Name | Parameters | Description |
|---|---|---|
| `ping` | None | Healthcheck and protocol handshake |
| `publish_post` | `platform`, `content`, `media_urls`, `scheduled_at` | Publish or schedule post across connected platforms |
| `get_post_analytics` | `platform`, `post_id` | Retrieve reach, impressions, and engagement metrics |
| `list_connected_platforms`| None | List active OAuth connections for current user |
| `disconnect_platform` | `platform` | Invalidate and purge stored platform credentials |

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
