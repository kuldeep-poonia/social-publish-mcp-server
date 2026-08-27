// Package mcp provides the core JSON-RPC 2.0 dispatching engine and tool management for the MCP server.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ToolHandler defines the function signature for executing registered MCP tools.
type ToolHandler func(ctx context.Context, args map[string]interface{}) (*CallToolResult, error)

// Server coordinates MCP protocol methods and tool execution.
type Server struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	handlers map[string]ToolHandler
}

// NewServer initializes a new MCP Server instance and registers standard default tools (e.g. ping).
func NewServer() *Server {
	s := &Server{
		tools:    make(map[string]Tool),
		handlers: make(map[string]ToolHandler),
	}

	// Register standard ping tool
	s.registerPingTool()
	return s
}

func (s *Server) registerPingTool() {
	schema := `{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`

	tool := Tool{
		Name:        "ping",
		Description: "Healthcheck and MCP protocol connection handshake verification",
		InputSchema: json.RawMessage(schema),
	}

	s.RegisterTool(tool, func(_ context.Context, _ map[string]interface{}) (*CallToolResult, error) {
		payload := map[string]interface{}{
			"status":    "ok",
			"server":    ServerName,
			"version":   ServerVersion,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}
		bytes, _ := json.Marshal(payload)
		return &CallToolResult{
			Content: []ToolContent{
				{Type: "text", Text: string(bytes)},
			},
			IsError: false,
		}, nil
	})
}

// RegisterSocialTools registers standard schemas for publish_post, get_analytics, and connect_platform tools.
func (s *Server) RegisterSocialTools(publishHandler, analyticsHandler, connectHandler ToolHandler) {
	publishSchema := `{
		"type": "object",
		"properties": {
			"platform": {
				"type": "string",
				"enum": ["twitter", "youtube", "instagram"],
				"description": "Target social media platform"
			},
			"content": {
				"type": "string",
				"description": "Text content or caption of the post"
			},
			"media_urls": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional URLs of media attachments to upload"
			},
			"idempotency_key": {
				"type": "string",
				"description": "Optional unique idempotency key to prevent duplicate publishing"
			}
		},
		"required": ["platform", "content"],
		"additionalProperties": false
	}`

	s.RegisterTool(Tool{
		Name:        "publish_post",
		Description: "Publish a text, image, or video post to a connected social platform with idempotency protection",
		InputSchema: json.RawMessage(publishSchema),
	}, publishHandler)

	analyticsSchema := `{
		"type": "object",
		"properties": {
			"platform": {
				"type": "string",
				"enum": ["twitter", "youtube", "instagram"],
				"description": "Target social media platform"
			},
			"post_id": {
				"type": "string",
				"description": "Platform post/tweet/video ID to retrieve analytics for"
			}
		},
		"required": ["platform", "post_id"],
		"additionalProperties": false
	}`

	s.RegisterTool(Tool{
		Name:        "get_analytics",
		Description: "Retrieve engagement metrics (impressions, likes, retweets, replies) for a published post",
		InputSchema: json.RawMessage(analyticsSchema),
	}, analyticsHandler)

	connectSchema := `{
		"type": "object",
		"properties": {
			"platform": {
				"type": "string",
				"enum": ["twitter", "youtube", "instagram"],
				"description": "Social media platform to connect via OAuth 2.0 PKCE"
			},
			"redirect_uri": {
				"type": "string",
				"description": "Optional OAuth callback redirect URI"
			}
		},
		"required": ["platform"],
		"additionalProperties": false
	}`

	s.RegisterTool(Tool{
		Name:        "connect_platform",
		Description: "Generate an OAuth 2.0 PKCE authorization URL to connect a user's social media account",
		InputSchema: json.RawMessage(connectSchema),
	}, connectHandler)
}

// RegisterTool adds an executable tool with schema to the MCP server.
func (s *Server) RegisterTool(tool Tool, handler ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = tool
	s.handlers[tool.Name] = handler
}

// HandleRequest dispatches an incoming JSON-RPC 2.0 request and returns a structured response.
func (s *Server) HandleRequest(ctx context.Context, rawReq []byte) *JSONRPCResponse {
	var req JSONRPCRequest
	if err := json.Unmarshal(rawReq, &req); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error: &JSONRPCError{
				Code:    CodeParseError,
				Message: "Parse error: invalid JSON",
			},
		}
	}

	if req.JSONRPC != "2.0" {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    CodeInvalidRequest,
				Message: "Invalid Request: jsonrpc must be '2.0'",
			},
		}
	}

	switch req.Method {
	case "server/discover":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": ProtocolVersion,
				"serverInfo": Implementation{
					Name:    ServerName,
					Version: ServerVersion,
				},
				"capabilities": ServerCapabilities{
					Tools: map[string]interface{}{"listChanged": false},
				},
			},
		}

	case "initialize":
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &initParams)
		}
		protoVer := ProtocolVersion
		if initParams.ProtocolVersion != "" {
			protoVer = initParams.ProtocolVersion
		}

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: InitializeResult{
				ProtocolVersion: protoVer,
				Capabilities: ServerCapabilities{
					Tools: map[string]interface{}{"listChanged": false},
				},
				ServerInfo: Implementation{
					Name:    ServerName,
					Version: ServerVersion,
				},
			},
		}

	case "notifications/initialized", "notifications/cancelled":
		// Standard JSON-RPC notification (no response should be sent back to client)
		return nil

	case "tools/list":
		s.mu.RLock()
		toolsList := make([]Tool, 0, len(s.tools))
		for _, t := range s.tools {
			toolsList = append(toolsList, t)
		}
		s.mu.RUnlock()

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  ToolsListResult{Tools: toolsList},
		}

	case "tools/call":
		var params CallToolParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return &JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &JSONRPCError{
						Code:    CodeInvalidParams,
						Message: fmt.Sprintf("Invalid parameters: %v", err),
					},
				}
			}
		}

		s.mu.RLock()
		handler, exists := s.handlers[params.Name]
		s.mu.RUnlock()

		if !exists {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    CodeMethodNotFound,
					Message: fmt.Sprintf("Tool not found: %s", params.Name),
				},
			}
		}

		result, err := handler(ctx, params.Arguments)
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: &CallToolResult{
					Content: []ToolContent{{Type: "text", Text: err.Error()}},
					IsError: true,
				},
			}
		}

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}

	case "ping":
		// Direct RPC ping
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"status":    "pong",
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			},
		}

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    CodeMethodNotFound,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			},
		}
	}
}
