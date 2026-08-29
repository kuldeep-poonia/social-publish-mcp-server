# Privacy Policy

**Effective Date**: August 2026  
**Last Updated**: August 2026  
**Application**: Social Publishing & Analytics MCP Server  
**Official Endpoint**: [https://social-mcp.duckdns.org/privacy](https://social-mcp.duckdns.org/privacy)  
**Contact Email**: [kuldeeppoonia20298@gmail.com](mailto:kuldeeppoonia20298@gmail.com)

---

## 1. Introduction & Scope

The **Social Publishing & Analytics MCP Server** ("we", "our", or "the Service") provides an open-standard Model Context Protocol (MCP) server that empowers users and authorized AI clients (such as Claude Desktop, Cursor, and ChatGPT) to publish multimedia content, manage scheduled queues, optimize metadata, and analyze engagement metrics across connected social platforms including **Twitter/X**, **Instagram (Meta)**, and **YouTube (Google)**.

We are committed to user privacy and data security. This Privacy Policy discloses our data collection practices, encryption standards, third-party sub-processors, and user data rights.

---

## 2. Information We Collect & Process

We collect only the minimum necessary data to perform authorized publishing, scheduling, and analytics actions on your behalf:

1. **OAuth Platform Authentication Credentials**:
   - Platform-specific User / Channel IDs (Twitter User ID, YouTube Channel ID, Instagram Business Account ID).
   - OAuth 2.0 / 2.1 Access Tokens and Refresh Tokens granted during authorization flows.
2. **Publishing & Content Data**:
   - Post text, captions, hashtags, video titles, descriptions, and media URLs provided for publishing or scheduling.
   - Brand Persona guidelines (brand voice, tone, visual aesthetic, forbidden buzzwords) set by the user.
3. **Analytics & Performance Telemetry**:
   - Post-level and account-level metrics (impressions, reach, likes, comments, retweets, views, follower growth) retrieved strictly via official platform APIs.
4. **Security Audit & Rate-Limiting Logs**:
   - Timestamps, anonymized IP addresses, and request action identifiers for telemetry and DDoS protection.

> [!IMPORTANT]
> **We Do NOT Collect**:
> - We **never** collect, access, or store personal account passwords, payment/credit card details, private direct messages (DMs), or unauthenticated browsing history.

---

## 3. Cryptographic Storage & Security Architecture

We employ defense-in-depth cryptographic protections for all stored credentials and communications:

- **AES-256-GCM Token Encryption Vault**: All OAuth access tokens and refresh tokens are encrypted at rest using **AES-256-GCM** authenticated encryption with unique 96-bit per-record nonces. Plaintext tokens are never persisted in databases or written to disk.
- **TLS 1.2+ / HTTPS In-Transit Protection**: All API traffic, OAuth exchanges, and MCP streaming connections are strictly encrypted over TLS with modern cipher suites.
- **Dual-Layer Secret Scrubbing**: All internal logging, error traces, and metrics streams pass through automated regex scrubbers that redact credentials and private tokens (`[REDACTED]`).

---

## 4. Sub-Processors & Cloud Infrastructure

Our infrastructure utilizes reputable enterprise cloud providers:

| Sub-Processor | Purpose | Location | Security / Compliance |
| :--- | :--- | :--- | :--- |
| **Supabase (PostgreSQL)** | Persistent database for scheduled posts, personas, and encrypted token vault | US / Global | SOC2 Type II, ISO 27001, AES-256 Encrypted |
| **Upstash (Redis)** | Ephemeral MCP session state, distributed locks, and rate limiting | US / Global | TLS Encrypted, Ephemeral Cache |
| **Google Gemini API** | AI content generation, hook generation, CTR metadata optimization | Global | Google Cloud Enterprise Privacy Terms |
| **Render** | Cloud container runtime hosting the MCP HTTP / SSE server | US / Global | TLS 1.3 Termination, Hardened Linux Containers |

---

## 5. How Google Gemini AI Is Used

When you invoke AI tools (e.g. `scout_trending_topics`, `update_post_metadata`, `optimize_content_seo`, or autonomous scheduling):
- Content prompts, topic keywords, and brand tone guidelines are processed via **Google Gemini API** (`models/gemini-2.5-flash`).
- Gemini is used solely to generate creative drafts, viral hooks, SEO search tags, and hashtag suggestions.
- Your data is **not used to train external public foundation models** without your explicit consent.

---

## 6. Zero Data Selling Commitment

> [!CAUTION]
> **Strict Non-Monetization Policy**:
> We do **NOT** sell, rent, lease, monetize, or trade your personal data, post content, analytics history, or OAuth credentials with third-party advertisers, data brokers, or external AI model vendors under any circumstances.

Data is transmitted strictly to:
1. **Target Social Networks** (Twitter/X, Google/YouTube, Meta/Instagram) exclusively to execute your authorized publishing or analytics requests.
2. **Your Connected MCP Client** (Claude, Cursor, custom agents) over authenticated JSON-RPC sessions.

---

## 7. User Control, Revocation & Data Erasure

You maintain complete ownership and control over your accounts and data:

- **Immediate Access Revocation**: You can disconnect your social platforms at any time via:
  - The `disconnect_platform` MCP tool or `DELETE /auth/{platform}/disconnect` REST endpoint.
  - Upstream provider settings:
    - **Google Account Permissions**: [https://myaccount.google.com/permissions](https://myaccount.google.com/permissions)
    - **Meta / Instagram Apps and Websites**: [https://www.instagram.com/accounts/manage_access/](https://www.instagram.com/accounts/manage_access/)
    - **X (Twitter) Connected Apps**: [https://twitter.com/settings/connected_apps](https://twitter.com/settings/connected_apps)
- **Data Erasure**: Upon account disconnection, all corresponding encrypted tokens and platform records are permanently deleted from our database.

---

## 8. Compliance with Platform Developer Policies

The Service operates in compliance with all relevant platform developer agreements:
- **Meta Platform Terms & Instagram Graph API Policies**
- **YouTube API Services Terms of Service & Google Developer Policies**
- **X (Twitter) Developer Agreement & Developer Policy**

---

## 9. Contact Information

If you have any questions, privacy inquiries, or data deletion requests, please reach out to:

- **Maintainer & Lead Developer**: Kuldeep Poonia
- **Direct Email**: [kuldeeppoonia20298@gmail.com](mailto:kuldeeppoonia20298@gmail.com)
- **GitHub Project**: [https://github.com/kuldeep-poonia/social-publish-mcp-server](https://github.com/kuldeep-poonia/social-publish-mcp-server)
