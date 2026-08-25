# Production Incident Response & Operational Runbooks

**System**: Social Publishing MCP Server  
**Target Audience**: On-Call Engineers, SREs, System Administrators  
**Last Updated**: August 2026  
**Classification**: Operational Procedures (Production Tier 1)  

---

## Overview & Incident Severity Matrix

| Severity Level | Definition | Response SLA | Examples |
| :--- | :--- | :--- | :--- |
| **P0 (Critical)** | Data leak, token compromise, total cluster outage, or database loss. | **< 15 minutes** | Active OAuth credential leak, DB corruption, Redis split-brain fail-open. |
| **P1 (High)** | Platform-wide publish outage, upstream API blackout, or mass token expiration. | **< 30 minutes** | Twitter 503 outage, Meta 60-day token mass expiration, Redis stream DLQ overflow. |
| **P2 (Medium)** | Single-tenant quota exhaustion, transient retry delays, non-critical telemetry loss. | **< 2 hours** | YouTube daily quota exhaustion for single tenant, elevated p99 latency (> 200ms). |
| **P3 (Low)** | Minor metrics scrape anomaly, non-blocking warning alerts. | **< 24 hours** | Prometheus scrape scrape timeout on auxiliary worker node. |

---

## Runbook 1: OAuth Token Compromise & Emergency Key Rotation (P0)

### 1.1 Trigger Condition
- Exposure of `TOKEN_ENCRYPTION_KEY`, database dump leak, or unauthorized access detected in audit logs (`action = 'token_decrypt_unauthorized'`).

### 1.2 Immediate Containment Procedure (< 15 mins)
1. **Revoke Active User Sessions**:
   ```sql
   -- Immediately invalidate all active JWT sessions in PostgreSQL
   UPDATE user_sessions SET is_revoked = TRUE WHERE is_revoked = FALSE;
   ```
2. **Deactivate Vulnerable Platform Connections**:
   ```sql
   -- Temporarily mark active connections as inactive to block automated publishing
   UPDATE platform_connections SET is_active = FALSE WHERE is_active = TRUE;
   ```
3. **Revoke Upstream Platform Tokens via API**:
   - **Twitter / X**: Call `POST https://api.twitter.com/2/oauth2/revoke` with client credentials.
   - **YouTube / Google**: Call `POST https://oauth2.googleapis.com/revoke?token={TOKEN}`.
   - **Instagram / Meta**: Call `DELETE https://graph.facebook.com/v20.0/me/permissions?access_token={TOKEN}`.

### 1.3 Master Key Rotation Procedure
1. Generate a new cryptographically secure 32-byte AES-256 key:
   ```bash
   openssl rand -hex 32
   ```
2. Set `NEW_TOKEN_ENCRYPTION_KEY` in your Secret Manager (e.g. AWS Secrets Manager / HashiCorp Vault).
3. Execute the atomic key re-encryption rotation utility:
   ```bash
   # Re-encrypts all platform_connections rows inside an atomic transaction
   ./bin/social-mcp-server --rotate-encryption-keys \
       --old-key="$OLD_TOKEN_ENCRYPTION_KEY" \
       --new-key="$NEW_TOKEN_ENCRYPTION_KEY"
   ```
4. Update `TOKEN_ENCRYPTION_KEY` on all container pods and perform a rolling restart:
   ```bash
   kubectl rollout restart deployment/social-mcp-server -n social-mcp
   ```

---

## Runbook 2: Platform Outages, 429/503 Blackouts & Meta 60-Day Expiration (P1/P2)

### 2.1 Upstream 429 Rate Limit / 503 Service Unavailable Handling
1. **Verify Redis Retry Stream Depth**:
   ```bash
   # Check pending retries in Redis Stream
   redis-cli -h $REDIS_HOST -p $REDIS_PORT -a $REDIS_PASSWORD \
       XLEN publish_retry_stream
   ```
2. **Inspect Upstream Error Distribution in Prometheus / Grafana**:
   - Navigate to Grafana Dashboard $\rightarrow$ **Platform Error Breakdown**.
   - Review HTTP response codes from `twitter`, `youtube`, and `instagram`.
3. **If Platform API is Down (503 / 504 Spike)**:
   - The server automatically captures transient errors and schedules exponential backoff retries ($2\text{s} \rightarrow 8\text{s} \rightarrow 32\text{s} \rightarrow 128\text{s} \rightarrow 512\text{s}$).
   - Do NOT flush the stream. Verify that worker concurrency does not hammer the recovering platform.

### 2.2 YouTube Daily Quota Exhaustion ($10{,}000\text{ units/day}$)
1. When a tenant hits their $10{,}000\text{ unit}$ quota limit, `internal/adapters/youtube/quota.go` blocks new video uploads (`1,600 units`) with `ErrDailyQuotaExceeded`.
2. Inspect tenant quota usage:
   ```sql
   SELECT user_id, units_used, last_reset_at FROM youtube_quota WHERE user_id = 'TARGET_USER_ID';
   ```
3. If quota was consumed by failed operations, verify automatic quota refund logs:
   ```sql
   SELECT * FROM audit_logs WHERE action = 'youtube_quota_refund' AND user_id = 'TARGET_USER_ID' ORDER BY created_at DESC LIMIT 10;
   ```
4. Quotas reset automatically at 00:00 UTC. If emergency quota expansion is granted by Google Cloud Console, update the quota row manually:
   ```sql
   UPDATE youtube_quota SET units_used = 0 WHERE user_id = 'TARGET_USER_ID';
   ```

### 2.3 Meta Instagram 60-Day Token Expiration & Bulk Re-Auth Procedure
1. **Background**: Meta long-lived user tokens expire after 60 days. If the user does not trigger an active publish or refresh within this 60-day window, Meta's Graph API permanently invalidates the token, and silent programmatic refresh is rejected.
2. **Detection Query**:
   ```sql
   SELECT id, user_id, platform, token_expires_at, updated_at 
   FROM platform_connections 
   WHERE platform = 'instagram' 
     AND is_active = TRUE 
     AND token_expires_at <= NOW() + INTERVAL '3 days'
   ORDER BY token_expires_at ASC;
   ```
3. **Automated Deactivation & Re-Auth Notification Flow**:
   - When a publish attempt fails with `OAuthException / Error 190 (Invalid OAuth 2.0 Access Token)`:
     1. The system automatically marks `platform_connections.is_active = FALSE`.
     2. An immutable audit record is logged (`action = 'instagram_token_expired'`).
     3. An alert/notification webhook is dispatched to the user's registered webhook endpoint prompting re-authentication.
4. **User Re-Authentication Flow**:
   - The user triggers the standard OAuth 2.1 authorization URL via `/auth/instagram/connect`.
   - Upon completing the Meta consent screen, a fresh 60-day long-lived token is exchanged, encrypted with AES-256-GCM, and stored with `is_active = TRUE`.

### 2.4 Dead-Letter Queue (DLQ) Manual Inspection & Replay
1. Inspect permanently failed jobs in the DLQ:
   ```bash
   redis-cli -h $REDIS_HOST -p $REDIS_PORT -a $REDIS_PASSWORD \
       LRANGE publish_dlq 0 10
   ```
2. Replay specific quarantined jobs after platform recovery:
   ```bash
   # Re-enqueues DLQ jobs back into the main processing stream
   ./bin/social-mcp-server --replay-dlq --job-id="job_xyz"
   ```

---

## Runbook 3: Redis Cluster Partition & Rate Limiter Fail-Closed Recovery (P0/P1)

### 3.1 Expected System Behavior during Redis Disconnection
- **Fail-Closed Security Guarantee**: If Redis becomes unreachable, `internal/ratelimit/limiter.go` strictly **FAILS CLOSED** (returns HTTP 429 / Rate Limit Exceeded). It **NEVER** fails open, preventing accidental platform API quota floods and billing spikes.
- Synchronous publishing continues with direct platform calls; background retries are temporarily held in memory or logged as failed.

### 3.2 Recovery Steps
1. Verify Redis cluster connectivity:
   ```bash
   redis-cli -h $REDIS_HOST -p $REDIS_PORT PING
   ```
2. Check for stalled or orphaned consumer locks:
   ```bash
   # Check pending messages in consumer group
   redis-cli -h $REDIS_HOST -p $REDIS_PORT XPENDING publish_retry_stream retry_workers
   ```
3. Workers utilize `XAUTOCLAIM` with a maximum reclaim attempts cap (threshold: 5). Jobs exceeding 5 failed reclaim attempts are automatically diverted to the DLQ to prevent poison-message infinite worker crash loops.

---

## Runbook 4: PostgreSQL Failover, Physical Backups & Integrity Drills (P0)

### 4.1 Physical Production Backup Architecture (Infrastructure Layer)
> [!IMPORTANT]
> **Production Database Backup Mechanism**:
> Physical database backups are managed via `pg_dump` and continuous **WAL-Archiving (Write-Ahead Logging)** with Point-in-Time Recovery (PITR) enabled via pgBackRest or AWS RDS Automated Backups.
> - **Full Snapshot**: Daily automated physical basebackup.
> - **Continuous WAL**: Streamed to dedicated S3/GCS immutable backup buckets.
> - **Retention**: 30 days rolling retention with encryption at rest.

### 4.2 Database Restore Procedure (From Physical Backup)
1. Provision a clean PostgreSQL 16 instance.
2. Restore the latest base backup and replay WAL logs to the desired recovery timestamp:
   ```bash
   # Example pg_restore execution
   pg_restore -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME --clean --if-exists /backups/social_mcp_latest.dump
   ```
3. Re-apply any pending database migrations:
   ```bash
   ./bin/social-mcp-server --migrate-up
   ```

### 4.3 Post-Restore Application Cryptographic Data Integrity Drill
Immediately following any database restore or failover, execute the application-layer cryptographic integrity verification drill to verify zero data corruption and valid key operation:

```bash
# Runs internal/database/integrity_drill.go against the restored DB
./bin/social-mcp-server --verify-integrity \
    --master-key="$TOKEN_ENCRYPTION_KEY"
```

#### Verification Criteria:
1. **Row Count Parity**: 100.00% matching expected baseline.
2. **SHA-256 Checksum Integrity**: 100.00% matching pre-incident table checksums.
3. **Out-of-Band Token Decryptability**: 100.00% of encrypted OAuth tokens must successfully decrypt using the independently supplied `TOKEN_ENCRYPTION_KEY`.

---

## Master Incident Verification Sign-Off

Upon completing any incident remediation, the on-call engineer must verify:
- [ ] `/health` returns `{"status":"ok"}`.
- [ ] `/metrics` endpoint is scraping with 0 errors.
- [ ] No unencrypted secrets or credentials exist in logs.
- [ ] Redis stream retry depth is $\le 10$ and DLQ depth is 0.
- [ ] Post-incident report logged in `audit_logs` table.
