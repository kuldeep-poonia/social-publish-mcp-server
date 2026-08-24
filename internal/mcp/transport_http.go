// Package mcp provides Streamable HTTP and SSE transport handlers for MCP clients.
package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// HTTPTransport coordinates HTTP/SSE client connections to the MCP Server.
type HTTPTransport struct {
	server       *Server
	sessionsMu   sync.RWMutex
	sessions     map[string]chan *JSONRPCResponse
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
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	ctx := r.Context()
	resp := t.server.HandleRequest(ctx, body)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleSSE establishes a persistent Server-Sent Events stream with the client (e.g. Claude Desktop).
func (t *HTTPTransport) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
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
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send endpoint event with session URI per MCP spec
	endpointURI := fmt.Sprintf("/mcp/messages?sessionId=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURI)
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case resp, ok := <-eventChan:
			if !ok {
				return
			}
			data, err := json.Marshal(resp)
			if err == nil {
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
				flusher.Flush()
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
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "Missing sessionId query parameter", http.StatusBadRequest)
		return
	}

	t.sessionsMu.RLock()
	eventChan, exists := t.sessions[sessionID]
	t.sessionsMu.RUnlock()

	if !exists {
		http.Error(w, "Unknown or expired sessionId", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	ctx := r.Context()
	resp := t.server.HandleRequest(ctx, body)

	// Send response through the SSE channel
	select {
	case eventChan <- resp:
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	case <-time.After(2 * time.Second):
		http.Error(w, "SSE channel blocked", http.StatusGatewayTimeout)
	}
}
