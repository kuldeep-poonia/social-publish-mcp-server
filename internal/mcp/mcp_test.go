package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCP_CrossClientInteroperability(t *testing.T) {
	mcpServer := NewServer()
	transport := NewHTTPTransport(mcpServer)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/rpc", transport.HandleDirectRPC)
	mux.HandleFunc("/mcp/sse", transport.HandleSSE)
	mux.HandleFunc("/mcp/messages", transport.HandleMessages)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	t.Logf("================================================================================")
	t.Logf("           MCP SERVER CROSS-CLIENT INTEROPERABILITY TEST SUITE                  ")
	t.Logf("================================================================================")

	// ========================================================================
	// CLIENT 1: DIRECT JSON-RPC 2.0 HTTP POST CLIENT (e.g. Gemini / Custom Agent)
	// ========================================================================
	t.Run("Client1_Direct_JSONRPC_HTTP", func(t *testing.T) {
		// 1. Initialize
		initReq := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]interface{}{
				"protocolVersion": ProtocolVersion,
				"clientInfo": map[string]string{
					"name":    "custom-agent-client",
					"version": "1.0.0",
				},
			},
		}
		initBytes, _ := json.Marshal(initReq)
		resp, err := http.Post(ts.URL+"/mcp/rpc", "application/json", bytes.NewReader(initBytes))
		if err != nil {
			t.Fatalf("Client 1 initialize failed: %v", err)
		}
		defer resp.Body.Close()

		var initResp JSONRPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
			t.Fatalf("failed decoding Client 1 init response: %v", err)
		}
		if initResp.Error != nil {
			t.Fatalf("Client 1 init returned RPC error: %v", initResp.Error)
		}

		// 2. List Tools
		listReq := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/list",
		}
		listBytes, _ := json.Marshal(listReq)
		resp2, err := http.Post(ts.URL+"/mcp/rpc", "application/json", bytes.NewReader(listBytes))
		if err != nil {
			t.Fatalf("Client 1 tools/list failed: %v", err)
		}
		defer resp2.Body.Close()

		var listResp JSONRPCResponse
		if err := json.NewDecoder(resp2.Body).Decode(&listResp); err != nil {
			t.Fatalf("failed decoding Client 1 tools/list response: %v", err)
		}
		if listResp.Error != nil {
			t.Fatalf("Client 1 tools/list returned RPC error: %v", listResp.Error)
		}

		// 3. Call 'ping' Tool
		callReq := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/call",
			"params": map[string]interface{}{
				"name":      "ping",
				"arguments": map[string]interface{}{},
			},
		}
		callBytes, _ := json.Marshal(callReq)
		resp3, err := http.Post(ts.URL+"/mcp/rpc", "application/json", bytes.NewReader(callBytes))
		if err != nil {
			t.Fatalf("Client 1 tools/call ping failed: %v", err)
		}
		defer resp3.Body.Close()

		var callResp JSONRPCResponse
		if err := json.NewDecoder(resp3.Body).Decode(&callResp); err != nil {
			t.Fatalf("failed decoding Client 1 tools/call response: %v", err)
		}
		if callResp.Error != nil {
			t.Fatalf("Client 1 call ping returned error: %v", callResp.Error)
		}

		resultMap, ok := callResp.Result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected result object in ping response, got: %T", callResp.Result)
		}
		if isErr, ok := resultMap["isError"].(bool); ok && isErr {
			t.Fatalf("ping tool returned isError=true")
		}

		t.Logf("[Client 1: Direct JSON-RPC HTTP] Handshake, tools/list, and ping tool executed cleanly. (Success: 100%%)")
	})

	// ========================================================================
	// CLIENT 2: STREAMABLE SSE TRANSPORT CLIENT (e.g. Claude Desktop)
	// ========================================================================
	t.Run("Client2_ClaudeDesktop_SSE_Transport", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/mcp/sse", nil)
		sseResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Client 2 SSE connect failed: %v", err)
		}
		defer sseResp.Body.Close()

		reader := bufio.NewReader(sseResp.Body)
		var sessionURI string

		// Read initial 'endpoint' event
		for i := 0; i < 5; i++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("Client 2 reading SSE endpoint failed: %v", err)
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data: /mcp/messages?sessionId=") {
				sessionURI = strings.TrimPrefix(line, "data: ")
				break
			}
		}

		if sessionURI == "" {
			t.Fatal("Client 2 failed to receive SSE endpoint URI with sessionId")
		}

		// Send initialize message via POST /mcp/messages?sessionId=...
		initMsg := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      101,
			"method":  "initialize",
			"params": map[string]interface{}{
				"protocolVersion": ProtocolVersion,
				"clientInfo": map[string]string{
					"name":    "claude-desktop-test-client",
					"version": "0.1.0",
				},
			},
		}
		initBytes, _ := json.Marshal(initMsg)
		postResp, err := http.Post(ts.URL+sessionURI, "application/json", bytes.NewReader(initBytes))
		if err != nil {
			t.Fatalf("Client 2 POST initialize message failed: %v", err)
		}
		defer postResp.Body.Close()

		if postResp.StatusCode != http.StatusAccepted {
			t.Fatalf("Client 2 expected StatusAccepted (202), got %d", postResp.StatusCode)
		}

		// Read response from SSE stream
		var receivedInitResponse bool
		for i := 0; i < 10; i++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data: {") {
				rawJSON := strings.TrimPrefix(line, "data: ")
				var rpcResp JSONRPCResponse
				if err := json.Unmarshal([]byte(rawJSON), &rpcResp); err == nil {
					if rpcResp.ID == float64(101) || rpcResp.ID == 101 {
						receivedInitResponse = true
						break
					}
				}
			}
		}

		if !receivedInitResponse {
			t.Fatal("Client 2 did not receive JSON-RPC initialize response over SSE stream")
		}

		t.Logf("[Client 2: Claude Desktop SSE Stream] Connected, session established, initialize verified over SSE. (Success: 100%%)")
	})

	t.Logf("================================================================================")
}
