// Package mcp provides Streamable HTTP and SSE transport handlers for MCP clients.
package mcp

import (
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

// HTTPTransport coordinates HTTP/SSE client connections to the MCP Server.
type HTTPTransport struct {
	server     *Server
	sessionsMu sync.RWMutex
	sessions   map[string]chan *JSONRPCResponse
}

// NewHTTPTransport initializes an HTTP transport wrapping the core MCP server.
func NewHTTPTransport(server *Server) *HTTPTransport {
	return &HTTPTransport{
		server:   server,
		sessions: make(map[string]chan *JSONRPCResponse),
	}
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
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && !strings.Contains(r.Host, "duckdns.org") && !strings.Contains(r.Host, "onrender.com") && !strings.Contains(r.Host, ".org") && !strings.Contains(r.Host, ".com") {
		scheme = "http"
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		host = xfh
	}
	endpointURI := fmt.Sprintf("%s://%s/mcp/messages?sessionId=%s", scheme, host, sessionID)

	log.Printf("[MCP SSE] STREAM CONNECTED: session_id=%s, Host=%s, EndpointURI=%s, RemoteAddr=%s", sessionID, host, endpointURI, r.RemoteAddr)

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
