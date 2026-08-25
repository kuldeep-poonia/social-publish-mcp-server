# Frequently Asked Questions (FAQ)

---

## 1. General & Pricing

### Q: Is the Social Publishing MCP Server free and open-source?
**A:** Yes! The core server is 100% open-source under the **MIT License**. You can self-host it on your own infrastructure with zero licensing fees.

### Q: Do I need paid API developer accounts for Twitter, YouTube, or Instagram?
**A:**
- **Twitter / X**: The adapter works with Twitter Free Tier (1,500 monthly posts) or Basic/Pro tiers.
- **YouTube**: Works with standard free Google Cloud developer projects ($10{,}000\text{ daily quota units}$).
- **Instagram**: Requires a free Meta Developer App and an Instagram Professional (Creator or Business) Account linked to a Facebook Page.

---

## 2. Security & Credentials

### Q: Can the AI assistant or LLM see my raw OAuth tokens or passwords?
**A:** **No.** The LLM interacts only via the Model Context Protocol tool schema. Raw OAuth access tokens and refresh tokens are encrypted with **AES-256-GCM** inside the database vault and are never passed back to the LLM context window.

### Q: What happens if my server's database is leaked?
**A:** All credentials inside the database are stored as AES-256-GCM ciphertexts with distinct nonces. Without the independently stored `TOKEN_ENCRYPTION_KEY` (which should reside in an external secrets manager like AWS Secrets Manager or HashiCorp Vault), the database contents cannot be decrypted.

### Q: How does the server defend against SSRF (Server-Side Request Forgery)?
**A:** The server uses socket-level kernel hooks (`net.Dialer.Control`) to verify the resolved destination IP at the exact millisecond of socket dialing. Any request attempting to access AWS/GCP metadata (`169.254.169.254`), internal Docker networks, or private RFC 1918 IPs is immediately blocked at the operating system level, preventing DNS rebinding attacks.

---

## 3. Platform Capabilities & Quotas

### Q: How does YouTube daily quota management work?
**A:** YouTube allocates $10{,}000\text{ quota units per day}$ per project. A standard video upload consumes $1{,}600\text{ units}$. The server includes an internal quota tracker that reserves units before initiating uploads and automatically refunds units if validation fails before upload. Quotas reset automatically at 00:00 UTC.

### Q: Why do Instagram tokens expire after 60 days?
**A:** Meta's Graph API issues Long-Lived User Access Tokens that are valid for 60 days. If the user publishes or interacts within that window, the token can be silently refreshed. If the 60-day window expires without activity, Meta requires the user to re-authenticate via the `/auth/instagram/connect` link.

### Q: Does the server support PNG images on Instagram?
**A:** **Yes.** Instagram's Graph API officially requires JPEG format. The server includes an automatic image transcoding pipeline that converts `.png` images to compliant `.jpeg` format before container creation.

---

## 4. Reliability & Queue Management

### Q: What happens if Twitter or Instagram is temporarily down (HTTP 503 / 429)?
**A:** The server operates on a **Sync-First** design:
1. It first attempts an immediate direct API call.
2. If the platform returns a transient error ($429$ Rate Limit or $503$ Service Unavailable), the post payload is AES-encrypted and enqueued into a **Redis 7 Stream**.
3. Background workers automatically retry publishing using jittered exponential backoff ($2\text{s} \rightarrow 8\text{s} \rightarrow 32\text{s} \rightarrow 128\text{s} \rightarrow 512\text{s}$).

### Q: What happens if Redis goes offline?
**A:** The rate limiter and queue **Fail Closed**. Rather than allowing unbounded requests that could violate platform limits or trigger billing overages, new rate-limited actions are safely throttled until Redis recovers.

---

## 5. Deployment & Self-Hosting

### Q: Can I run this server in Kubernetes?
**A:** Yes. Production Kubernetes manifests (Deployment, HPA, Ingress, ClusterIP Service, ConfigMap, and Secrets) are provided in [`deploy/k8s/`](../deploy/k8s/).

### Q: How do I view real-time metrics and Grafana dashboards?
**A:** Launch the Docker Compose stack (`docker compose up -d`) and navigate to `http://localhost:3000`. Log in with username `admin` and password `admin` to view the auto-provisioned **Social Publishing MCP Server - Production Telemetry** dashboard.
