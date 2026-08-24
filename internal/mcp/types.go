// Package mcp implements the Model Context Protocol (MCP) specification over Streamable HTTP and SSE transports.
package mcp

import (
	"encoding/json"
)

// MCP Protocol Version
const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "social-publish-mcp-server"
	ServerVersion   = "0.1.0"
)

// Standard JSON-RPC 2.0 Error Codes
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// JSONRPCRequest represents an incoming JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents an outgoing JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError represents a standard JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Implementation provides metadata identifying the client or server.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities defines features supported by this MCP server.
type ServerCapabilities struct {
	Tools map[string]interface{} `json:"tools,omitempty"`
}

// InitializeResult is returned in response to the 'initialize' method.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
}

// Tool defines an exposed tool schema in the MCP tools list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsListResult is returned in response to the 'tools/list' method.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// CallToolParams contains the arguments passed to 'tools/call'.
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// ToolContent represents a content item in the tool call result.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult is returned in response to 'tools/call'.
type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError"`
}
