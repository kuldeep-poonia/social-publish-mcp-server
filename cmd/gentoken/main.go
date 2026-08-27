// Package main provides a quick CLI utility to generate cryptographically signed JWT tokens for testing.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed loading config: %v", err)
	}

	userID := "kuldeep"
	if len(os.Args) > 1 && os.Args[1] != "" {
		userID = os.Args[1]
	}

	pair, err := auth.IssueSessionTokens(userID, "user", cfg.JWTSigningSecret)
	if err != nil {
		log.Fatalf("Failed generating JWT session: %v", err)
	}

	fmt.Println("================================================================")
	fmt.Printf("✅ JWT Token Generated for User: %s\n", userID)
	fmt.Println("================================================================")
	fmt.Println("\n🔑 YOUR ACCESS TOKEN:")
	fmt.Println(pair.AccessToken)
	fmt.Println("\n----------------------------------------------------------------")
	fmt.Println("📋 CLAUDE DESKTOP CONFIG (claude_desktop_config.json):")
	fmt.Printf(`{
  "mcpServers": {
    "social-publisher": {
      "url": "https://social-mcp.duckdns.org/mcp/sse",
      "headers": {
        "Authorization": "Bearer %s"
      }
    }
  }
}`+"\n", pair.AccessToken)
	fmt.Println("----------------------------------------------------------------")
	fmt.Println("🧪 INSTAGRAM TEST CURL COMMAND:")
	fmt.Printf(`curl -X POST https://social-mcp.duckdns.org/mcp/rpc \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer %s" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "publish_post",
      "arguments": {
        "platform": "instagram",
        "content": "Testing our AI Social Publishing MCP Server! 🚀 Live from Go MCP Gateway. #AI #MCP #SocialMedia",
        "media_urls": ["https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe?w=1080&q=80"],
        "media_type": "IMAGE"
      }
    }
  }'`+"\n", pair.AccessToken)
	fmt.Println("================================================================")
}
