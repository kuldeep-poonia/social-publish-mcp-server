# Social Publishing & Analytics MCP Server — Architecture Specification

This document provides a detailed technical and visual breakdown of the architecture, cryptographic security model, data flows, and component interactions within the **Social Publishing & Analytics MCP Server**.

---

## 1. High-Level System Architecture

```mermaid
flowchart TD
    subgraph Clients["MCP Compliant Clients"]
        Claude["Claude Desktop / Claude AI (SSE Transport)"]
        Gemini["Gemini / Custom Agents (Direct JSON-RPC)"]
        GPT["ChatGPT / LLM Connectors"]
    end

    subgraph Gateway["MCP Gateway & Ingress Layer (internal/server)"]
        CORS["CORS & Origin Validation"]
        Limiter["Token-Bucket Rate Limiter (internal/ratelimit)"]
        AuthMiddleware["Bearer JWT Auth Middleware"]
        Transport["Streamable HTTP & SSE Transport Engine (internal/mcp)"]
    end

    subgraph Core["Core Dispatcher & Protocol Engine"]
        OAuthServer["OAuth 2.1 Server + PKCE S256 (internal/auth)"]
        MCPServer["MCP JSON-RPC 2.0 Dispatcher (internal/mcp)"]
    end

    subgraph Security["Zero-Trust Security & Vault Layer"]
        CipherVault["AES-256-GCM Cryptographic Vault (internal/crypto)"]
        AuditDecorator["AuditedRepositoryDecorator (internal/database)"]
    end

    subgraph Persistence["Storage & Infrastructure Layer"]
        PG[("PostgreSQL 16\n(Least-Privilege social_app_user)")]
        Redis[("Redis 7\n(Distributed Quotas & Cache)")]
    end

    subgraph Adapters["Platform Adapters (Phase 3+)"]
        Twitter["Twitter / X API v2 Adapter"]
        YouTube["YouTube Data API v3 Adapter"]
        Instagram["Instagram Graph API Adapter"]
    end

    subgraph External["External Social Platforms"]
        TwitterAPI["api.twitter.com / upload.twitter.com"]
        GoogleAPI["www.googleapis.com"]
        MetaAPI["graph.facebook.com"]
    end

    Claude -->|SSE Stream /mcp/sse| CORS
    Gemini -->|POST /mcp/rpc| CORS
    GPT -->|POST /mcp/rpc| CORS

    CORS --> Limiter --> AuthMiddleware --> Transport
    Transport --> MCPServer
    AuthMiddleware -.->|Authorization Code Flow| OAuthServer

    MCPServer -->|Tool Execution| Twitter & YouTube & Instagram
    OAuthServer -->|Session Storage| AuditDecorator

    Twitter & YouTube & Instagram -->|Fetch/Store Tokens| AuditDecorator
    AuditDecorator --> CipherVault
    AuditDecorator -->|Parameterized SQL| PG
    Limiter -.-> Redis

    Twitter --> TwitterAPI
    YouTube --> GoogleAPI
    Instagram --> MetaAPI
```

---

## 2. OAuth 2.1 Authentication & PKCE Handshake Flow

```mermaid
sequenceDiagram
    autonumber
    actor User as User / LLM Client
    participant Server as MCP Gateway (/oauth)
    participant AuthEngine as OAuth 2.1 Engine (PKCE S256)
    participant Vault as Token Vault (AES-256-GCM)
    participant DB as PostgreSQL (user_sessions)

    Note over User,AuthEngine: Step 1: Authorization Code Request
    User->>Server: GET /oauth/authorize?client_id=...&code_challenge=...&code_challenge_method=S256
    Server->>AuthEngine: Validate Client, Exact Redirect URI, S256 Challenge
    AuthEngine-->>Server: Generate 60s Single-Use Auth Code
    Server-->>User: 302 Redirect (callback?code=AUTH_CODE)

    Note over User,DB: Step 2: PKCE Token Exchange
    User->>Server: POST /oauth/token (code, code_verifier, client_id)
    Server->>AuthEngine: Validate S256(verifier) == challenge & Mark Code Consumed
    AuthEngine->>Vault: Issue JWT Access Token (15-min TTL) + 32-byte Refresh Token
    AuthEngine->>DB: INSERT INTO user_sessions (SHA-256(refresh_token), user_id, expires_at)
    AuthEngine-->>Server: TokenPair (access_token, raw_refresh_token)
    Server-->>User: 200 OK (access_token, refresh_token, token_type=Bearer)

    Note over User,DB: Step 3: Authenticated MCP Tool Invocations
    User->>Server: POST /mcp/rpc (Authorization: Bearer JWT)
    Server->>Server: Validate JWT HMAC signature & Inject ActorContext
    Server->>DB: Execute Audited Operation with Actor ID
```

---

## 3. Multi-Tenant Cryptographic Isolation & Token Security

```mermaid
flowchart LR
    subgraph Request["Incoming Application Request"]
        RawToken["Plaintext OAuth Access/Refresh Token"]
        ActorCtx["ActorContext (UserID, IP, Audit Details)"]
    end

    subgraph VaultLayer["Token Vault & Decorator Layer"]
        AES["AES-256-GCM Engine\n12-byte Unique Nonce per Write"]
        Redactor["Audit Redactor\n(Replaces tokens with [REDACTED])"]
    end

    subgraph DBLayer["PostgreSQL Storage (Least-Privilege Role)"]
        ConnTable["platform_connections\n(encrypted_access_token, nonce, user_id)"]
        AuditTable["audit_logs\n(actor_id, action, redacted_metadata, created_at)"]
    end

    RawToken --> AES
    AES -->|Ciphertext + Nonce| ConnTable
    ActorCtx --> Redactor --> AuditTable
```

---

## 4. Connection Pool & Scalability Architecture

```mermaid
graph TD
    ClientTraffic["Incoming Concurrent Handshakes (100 - 1,000+ RPS)"]
    GoPool["Go sql.DB Connection Pool\n(MaxOpenConns=25, MaxIdleConns=10)"]
    ActiveSockets["Active Physical TCP Sockets (2 - 10 conns)"]
    PostgresEngine["PostgreSQL 16 Database Engine\n(Fast Query Execution ~2-4ms)"]

    ClientTraffic --> GoPool
    GoPool -->|Recycles Idle Connections| ActiveSockets
    ActiveSockets -->|Executes Parameterized DML| PostgresEngine
    PostgresEngine -->|Returns in 2-4ms| GoPool
```

> **Connection Lifecycle Dynamics**:
> In high-throughput micro-benchmarks with sub-5ms query times, the connection pool continuously reuses warm idle sockets rather than spawning physical sockets up to `MaxOpenConns`, ensuring minimal socket churn and low kernel overhead.
