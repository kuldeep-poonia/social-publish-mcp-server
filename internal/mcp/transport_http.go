// Package mcp provides Streamable HTTP and SSE transport handlers for MCP clients.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionStore coordinates persistent storage (e.g. in Redis) for active MCP sessions.
type SessionStore interface {
	SetSession(ctx context.Context, sessionID, userID, clientID string, ttl time.Duration) error
	GetSession(ctx context.Context, sessionID string) (userID string, exists bool, err error)
	DeleteSession(ctx context.Context, sessionID string) error
}

// HTTPTransport coordinates HTTP/SSE client connections to the MCP Server.
type HTTPTransport struct {
	server        *Server
	sessionsMu    sync.RWMutex
	sessions      map[string]chan *JSONRPCResponse
	sessionStore  SessionStore
	publicBaseURL string
}

// NewHTTPTransport initializes an HTTP transport wrapping the core MCP server.
func NewHTTPTransport(server *Server) *HTTPTransport {
	return &HTTPTransport{
		server:   server,
		sessions: make(map[string]chan *JSONRPCResponse),
	}
}

// SetPublicBaseURL configures the public canonical base URL (e.g. https://social-mcp.duckdns.org).
func (t *HTTPTransport) SetPublicBaseURL(baseURL string) {
	t.sessionsMu.Lock()
	defer t.sessionsMu.Unlock()
	t.publicBaseURL = strings.TrimRight(baseURL, "/")
}

// SetSessionStore attaches a persistent session store (e.g., Redis) to the transport.
func (t *HTTPTransport) SetSessionStore(store SessionStore) {
	t.sessionsMu.Lock()
	defer t.sessionsMu.Unlock()
	t.sessionStore = store
}

// HandleStreamableHTTP is the primary single-endpoint Streamable HTTP MCP transport handler (POST /mcp).
// It supports session tracking via Mcp-Session-Id, bidirectional JSON-RPC execution, and streaming responses.
func (t *HTTPTransport) HandleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Accept, X-Requested-With, X-Client-ID")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	cleanPath := strings.TrimRight(r.URL.Path, "/")
	if cleanPath == "/mcp/sse" {
		t.HandleSSE(w, r)
		return
	}
	if cleanPath == "/mcp/messages" {
		t.HandleMessages(w, r)
		return
	}
	if cleanPath == "/mcp/rpc" {
		t.HandleDirectRPC(w, r)
		return
	}

	// Session ID management per MCP Streamable HTTP specification
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
	}
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Persist session metadata in Redis / session store
	t.sessionsMu.RLock()
	store := t.sessionStore
	t.sessionsMu.RUnlock()
	if store != nil {
		_ = store.SetSession(r.Context(), sessionID, "authenticated_user", r.Header.Get("X-Client-ID"), 24*time.Hour)
	}

	w.Header().Set("Mcp-Session-Id", sessionID)

	// DELETE /mcp terminates an active session
	if r.Method == http.MethodDelete {
		if store != nil {
			_ = store.DeleteSession(r.Context(), sessionID)
		}
		log.Printf("[MCP Streamable HTTP] SESSION TERMINATED: session_id=%s, RemoteAddr=%s", sessionID, r.RemoteAddr)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// GET /mcp with Accept: text/event-stream initiates an SSE stream
	if r.Method == http.MethodGet {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.HandleSSE(w, r)
			return
		}
		http.Error(w, "Method Not Allowed, use POST for Streamable HTTP JSON-RPC", http.StatusMethodNotAllowed)
		return
	}

	if r.Method != http.MethodPost {
		log.Printf("[MCP Streamable HTTP] REJECTED: Method not allowed: %s", r.Method)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[MCP Streamable HTTP] REJECTED: Failed reading body: %v", err)
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		log.Printf("[MCP Streamable HTTP] REJECTED: Empty request body")
		http.Error(w, "Empty request body", http.StatusBadRequest)
		return
	}

	log.Printf("[MCP Streamable HTTP] INCOMING: session_id=%s, Body=%s, RemoteAddr=%s", sessionID, string(body), r.RemoteAddr)

	ctx := r.Context()
	resp := t.server.HandleRequest(ctx, body)

	// If request was a notification (no response needed per JSON-RPC 2.0), return 202 Accepted
	if resp == nil {
		log.Printf("[MCP Streamable HTTP] NOTIFICATION PROCESSED: session_id=%s -> 202 Accepted", sessionID)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
		return
	}

	// If client specifically requested SSE event stream for this tool call
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		data, err := json.Marshal(resp)
		if err == nil {
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		log.Printf("[MCP Streamable HTTP] STREAMED EVENT: session_id=%s, RespID=%v", sessionID, resp.ID)
		return
	}

	// Standard synchronous JSON-RPC 2.0 Response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	log.Printf("[MCP Streamable HTTP] RESPONDED: session_id=%s, RespID=%v", sessionID, resp.ID)
}

// HandleDirectRPC processes standard non-streaming JSON-RPC 2.0 POST requests.
func (t *HTTPTransport) HandleDirectRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		log.Printf("[MCP Direct RPC] REJECTED: Method not allowed: %s", r.Method)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[MCP Direct RPC] REJECTED: Failed reading body: %v", err)
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	log.Printf("[MCP Direct RPC] INCOMING: Body=%s, RemoteAddr=%s", string(body), r.RemoteAddr)

	ctx := r.Context()
	resp := t.server.HandleRequest(ctx, body)

	// If request was a notification (no response needed per JSON-RPC 2.0), return 202 Accepted
	if resp == nil {
		log.Printf("[MCP Direct RPC] NOTIFICATION PROCESSED: 202 Accepted")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	log.Printf("[MCP Direct RPC] RESPONDED: ID=%v", resp.ID)
}

// HandleSSE establishes a persistent Server-Sent Events stream with the client (e.g. Claude Desktop).
func (t *HTTPTransport) HandleSSE(w http.ResponseWriter, r *http.Request) {
	// If client sends POST to /mcp/sse (Streamable HTTP JSON-RPC initialization), handle it directly
	if r.Method == http.MethodPost {
		log.Printf("[MCP SSE -> Direct RPC] Received POST on /mcp/sse, dispatching to direct JSON-RPC")
		t.HandleDirectRPC(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("[MCP SSE] REJECTED: Streaming unsupported by client: %s", r.RemoteAddr)
		http.Error(w, "Streaming unsupported by client", http.StatusInternalServerError)
		return
	}

	sessionID := uuid.New().String()
	eventChan := make(chan *JSONRPCResponse, 50)

	t.sessionsMu.Lock()
	t.sessions[sessionID] = eventChan
	t.sessionsMu.Unlock()

	defer func() {
		t.sessionsMu.Lock()
		delete(t.sessions, sessionID)
		close(eventChan)
		t.sessionsMu.Unlock()
		log.Printf("[MCP SSE] SESSION CLOSED: session_id=%s, RemoteAddr=%s", sessionID, r.RemoteAddr)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Determine absolute endpoint URL
	t.sessionsMu.RLock()
	baseURL := t.publicBaseURL
	t.sessionsMu.RUnlock()

	if baseURL == "" {
		scheme := "https"
		if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && !strings.Contains(r.Host, "duckdns.org") && !strings.Contains(r.Host, "onrender.com") && !strings.Contains(r.Host, ".org") && !strings.Contains(r.Host, ".com") {
			scheme = "http"
		}
		host := r.Host
		if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
			host = xfh
		}
		baseURL = fmt.Sprintf("%s://%s", scheme, host)
	}
	endpointURI := fmt.Sprintf("%s/mcp/messages?sessionId=%s", baseURL, sessionID)

	log.Printf("[MCP SSE] STREAM CONNECTED: session_id=%s, BaseURL=%s, EndpointURI=%s, RemoteAddr=%s", sessionID, baseURL, endpointURI, r.RemoteAddr)

	// Send endpoint event with session URI per MCP spec
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURI)
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			log.Printf("[MCP SSE] CLIENT DISCONNECTED: session_id=%s", sessionID)
			return
		case resp, ok := <-eventChan:
			if !ok {
				return
			}
			if resp != nil {
				data, err := json.Marshal(resp)
				if err == nil {
					log.Printf("[MCP SSE] DISPATCHING MESSAGE: session_id=%s, Payload=%s", sessionID, string(data))
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
					flusher.Flush()
				}
			}
		case <-time.After(15 * time.Second):
			// Keepalive heartbeat
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// HandleMessages receives POST messages for an active SSE session and dispatches them.
func (t *HTTPTransport) HandleMessages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		log.Printf("[MCP Messages] REJECTED: Invalid method=%s, RemoteAddr=%s", r.Method, r.RemoteAddr)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		log.Printf("[MCP Messages] REJECTED: Missing sessionId query param, RemoteAddr=%s", r.RemoteAddr)
		http.Error(w, "Missing sessionId query parameter", http.StatusBadRequest)
		return
	}

	t.sessionsMu.RLock()
	eventChan, exists := t.sessions[sessionID]
	t.sessionsMu.RUnlock()

	if !exists {
		log.Printf("[MCP Messages] REJECTED: Session not found or expired: session_id=%s, RemoteAddr=%s", sessionID, r.RemoteAddr)
		http.Error(w, "Unknown or expired sessionId", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[MCP Messages] REJECTED: Failed reading body: %v", err)
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	log.Printf("[MCP Messages] INCOMING: session_id=%s, Body=%s, RemoteAddr=%s", sessionID, string(body), r.RemoteAddr)

	ctx := r.Context()
	resp := t.server.HandleRequest(ctx, body)

	// If request was a notification (no response needed), accept immediately
	if resp == nil {
		log.Printf("[MCP Messages] NOTIFICATION PROCESSED: session_id=%s", sessionID)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
		return
	}

	// Send response through the SSE channel
	select {
	case eventChan <- resp:
		log.Printf("[MCP Messages] QUEUED RESPONSE: session_id=%s, RespID=%v", sessionID, resp.ID)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	case <-time.After(2 * time.Second):
		log.Printf("[MCP Messages] TIMEOUT: SSE channel blocked for session_id=%s", sessionID)
		http.Error(w, "SSE channel blocked", http.StatusGatewayTimeout)
	}
}
