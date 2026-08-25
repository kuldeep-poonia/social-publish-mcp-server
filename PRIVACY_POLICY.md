# Privacy Policy

**Effective Date**: August 2026  
**Last Updated**: August 2026  
**Application**: Social Publishing & Analytics MCP Server  

---

## 1. Introduction & Overview

The **Social Publishing & Analytics MCP Server** ("we", "our", or "the Service") provides an open standard Model Context Protocol (MCP) server enabling users and AI assistants to publish content and query engagement analytics on Twitter/X, YouTube, and Instagram.

We respect your privacy. This Privacy Policy details what data we collect, how it is encrypted and processed, our strict retention standards, and your rights over your data.

---

## 2. Information We Collect

We only collect the minimal technical data necessary to provide social publishing and analytics services under your explicit authorization:

1. **Account & Authentication Information**:
   - Platform User ID (e.g. your Twitter user ID, YouTube channel ID, or Instagram business account ID).
   - OAuth 2.0 / 2.1 Access Tokens and Refresh Tokens granted during your account connection.
2. **Publishing Metadata**:
   - Content text, media URLs, post titles, descriptions, and published post identifiers (`post_id`).
3. **Engagement Telemetry**:
   - Post-level metrics (impressions, likes, views, comments, shares, retweets) retrieved via official platform APIs.
4. **Audit Logs**:
   - Timestamps, action names (e.g. `publish_post`, `token_refresh`), and request IP addresses for security auditing.

> [!IMPORTANT]
> **We Do NOT Collect**:
> - We never collect personal passwords, billing credentials, private direct messages (DMs), or unauthenticated browsing history.

---

## 3. How Your Information Is Used

Your information is used strictly to:
- Execute content publishing actions requested by you through your AI assistant or client.
- Fetch and aggregate engagement metrics for posts you publish.
- Maintain valid OAuth sessions via token refresh flows.
- Enforce rate limits and prevent abuse.

---

## 4. Cryptographic Data Protection & Security

We enforce rigorous, industry-standard cryptographic safeguards across all data at rest and in transit:

- **AES-256-GCM Encryption at Rest**: All OAuth access tokens and refresh tokens are encrypted using **AES-256-GCM** authenticated encryption with unique per-token nonces.
- **Out-of-Band Key Management**: Master encryption keys are managed independently in external secret managers and are never bundled with database backups.
- **In-Transit Encryption**: All external API communications with Twitter, YouTube, and Meta occur over mandatory **TLS 1.2+** connections with modern AEAD cipher suites.
- **Telemetry Secret Scrubbing**: All logging and telemetry streams pass through automated dual-layer secret scrubbers that mask all credentials (`[REDACTED]`).

---

## 5. Zero Data Selling & Third-Party Sharing Statement

> [!CAUTION]
> **Zero Data Selling Commitment**:
> We do **NOT** sell, rent, monetize, trade, or share your personal data, post content, or OAuth credentials with any third-party advertisers, brokers, or external AI model trainers under any circumstances.

Data is only transmitted to:
1. **The Target Social Platforms** (Twitter/X, Google/YouTube, Meta/Instagram) strictly as required to publish your posts or retrieve your analytics.
2. **Your Chosen AI Client** (e.g., Claude Desktop, custom agent) via standard MCP protocol responses.

---

## 6. Data Retention & Right to Erasure

- **Active Connections**: OAuth credentials are retained only as long as your account remains connected to the MCP server.
- **Right to Erasure (Immediate Deletion)**:
  - You can immediately revoke access and delete your credentials at any time by calling the `disconnect_platform` tool or issuing a `DELETE` request to `/auth/{platform}/disconnect`.
  - Upon disconnection, your stored OAuth tokens are permanently deleted from the vault and revoked with the upstream platform.
- **Audit Logs**: Security audit logs are automatically purged after 30 days.

---

## 7. Compliance with Platform Developer Policies

The Service operates in strict compliance with:
- **Meta Platform Terms & Developer Policies**: Data is fetched solely through the official Instagram Graph API and used exclusively for your requested analytics.
- **YouTube API Services Terms of Service**: Video uploads and quota usage adhere strictly to Google Developer Guidelines.
- **X (Twitter) Developer Agreement & Policy**: Posts and media uploads adhere to X API terms.

---

## 8. Contact Information

If you have questions regarding this Privacy Policy or wish to exercise your data privacy rights, please contact:

- **Privacy & Security Team**: `privacy@socialmcp.io`
- **GitHub Repository**: [https://github.com/kuldeep-poonia/social-publish-mcp-server](https://github.com/kuldeep-poonia/social-publish-mcp-server)
