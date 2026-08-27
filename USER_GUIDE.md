# Complete User Guide: How to Connect Social MCP to Any LLM & Publish

This guide gives you the **exact, foolproof, click-by-click instructions** to connect the **Social Publishing MCP Server** to **Claude Desktop**, **Cursor IDE**, **Windsurf**, or any MCP-compatible AI client, and publish content immediately.

---

## 📑 Contents
1. [Choose Your AI Client & Connect in 60 Seconds](#1-choose-your-ai-client--connect-in-60-seconds)
   - [A. Claude Desktop (Windows & Mac)](#a-claude-desktop-windows--mac)
   - [B. Cursor IDE](#b-cursor-ide)
   - [C. Windsurf / VS Code (Cline / Roo-Code)](#c-windsurf--vs-code-cline--roo-code)
2. [How to Verify Connection (The Hammer 🔨 Icon)](#2-how-to-verify-connection)
3. [One-Time Social Media Account Linking](#3-one-time-social-media-account-linking)
4. [How to Prompt Your AI to Publish Content](#4-how-to-prompt-your-ai-to-publish-content)
   - [Instagram (Feed Photos, Carousels, Reels)](#instagram-publishing)
   - [Twitter / X (Tweets & Threads)](#twitter--x-publishing)
   - [YouTube (Videos & Shorts)](#youtube-publishing)
5. [Fetching Live Analytics & Impressions](#5-fetching-live-analytics--impressions)
6. [Managing Your Connected Accounts](#6-managing-your-connected-accounts)

---

## 1. Choose Your AI Client & Connect in 60 Seconds

### A. Claude Desktop (Windows & Mac)

#### Step 1: Open the Config File
- **On Windows**: Press `Windows Key + R` on your keyboard, paste this exact line, and press **Enter**:
  ```text
  notepad %APPDATA%\Claude\claude_desktop_config.json
  ```
- **On macOS**: Open Terminal and run:
  ```bash
  open -e ~/Library/Application\ Support/Claude/claude_desktop_config.json
  ```

#### Step 2: Paste the Server Config
In the Notepad / Text Editor window that opened, paste this JSON and press **Ctrl + S** (or Cmd + S on Mac) to save:

```json
{
  "mcpServers": {
    "social-publisher": {
      "url": "https://social-mcp.duckdns.org/mcp/sse"
    }
  }
}
```
*(If your file already has other MCP servers, just add `"social-publisher"` inside `"mcpServers"`).*

#### Step 3: Restart Claude Desktop
Close the Claude Desktop app completely and reopen it.

---

### B. Cursor IDE

1. Open **Cursor IDE**.
2. Open Settings by clicking the **Gear (⚙️) icon** in the top-right corner (or press `Ctrl + ,`).
3. In the left sidebar, click **Features** $\rightarrow$ **MCP Servers**.
4. Click the **`+ Add New MCP Server`** button.
5. Fill in the three fields:
   - **Name**: `social-publisher`
   - **Type**: `SSE`
   - **Server URL**: `https://social-mcp.duckdns.org/mcp/sse`
6. Click **Add**. A green status indicator will appear showing the server is active!

---

### C. Windsurf / VS Code (Cline / Roo-Code)

1. Open your MCP Settings in Windsurf or Cline extension settings (`mcp_settings.json`).
2. Add the following entry:
```json
{
  "mcpServers": {
    "social-publisher": {
      "url": "https://social-mcp.duckdns.org/mcp/sse"
    }
  }
}
```
3. Save and reload the window.

---

## 2. How to Verify Connection

Once you restart your AI client (Claude or Cursor):

1. Start a new chat.
2. Look at the bottom of the chat box: You will see a **Hammer (🔨) icon** (or MCP Tools indicator).
3. Click the hammer icon — you should see these 3 tools available:
   - `publish_post`: Publishes posts, reels, tweets, and videos.
   - `get_post_analytics`: Retrieves engagement metrics.
   - `list_connections`: Lists connected social platforms.

4. **Verification Test Prompt**:
   Type this in your chat:
   > *"What social media accounts do I currently have connected?"*

   Claude / Cursor will call the `list_connections` tool and reply with your connection status!

---

## 3. One-Time Social Media Account Linking

Before the AI can publish to your personal/company accounts, Meta, Twitter, and Google require your one-time permission.

### How it works:
1. In your AI chat, ask: *"I want to connect my Instagram account."*
2. The AI will return your direct 1-click authorization link:
   - **Instagram**: `https://social-mcp.duckdns.org/auth/instagram/connect?user_id=YOUR_NAME`
   - **Twitter / X**: `https://social-mcp.duckdns.org/auth/twitter/connect?user_id=YOUR_NAME`
   - **YouTube**: `https://social-mcp.duckdns.org/auth/youtube/connect?user_id=YOUR_NAME`
3. Click the link $\rightarrow$ Log in on the official platform popup $\rightarrow$ Click **Allow**.
4. Your account is now securely linked and encrypted in the vault. **You will never need to log in again.**

---

## 4. How to Prompt Your AI to Publish Content

Once connected, simply talk to your AI in plain language (English, Hindi, etc.).

---

### Instagram Publishing

#### Single Photo Post:
> **Prompt:**  
> *"Post this photo to my Instagram feed with caption 'Excited to announce our new AI product release! 🚀 #AI #Tech #Innovation': https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe"*

#### Multi-Photo Carousel (Up to 10 photos):
> **Prompt:**  
> *"Publish a carousel to my Instagram with these 2 photos:  
> 1. https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe  
> 2. https://images.unsplash.com/photo-1579546929518-9e396f3cc809  
> Caption: 'Swipe to see the evolution of our product! 💡 #Design #Tech'"*

#### Instagram Reel (Video):
> **Prompt:**  
> *"Post this vertical video as an Instagram Reel: https://storage.googleapis.com/my-bucket/sample_reel.mp4 with caption 'Quick 30-second coding tip! ⚡ #Reels #Code'"*

---

### Twitter / X Publishing

#### Standalone Tweet:
> **Prompt:**  
> *"Post a tweet saying: 'Just shipped our new Go Model Context Protocol gateway! 🚀 Check it out: https://social-mcp.duckdns.org' with this image: https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe"*

#### Multi-Tweet Thread:
> **Prompt:**  
> *"Write and publish a 3-tweet thread about why Model Context Protocol (MCP) will revolutionize AI assistants."*

---

### YouTube Publishing

#### YouTube Video Upload:
> **Prompt:**  
> *"Upload this video to my YouTube channel with the title 'Building High-Performance Go Microservices' and description 'In this tutorial, we build a scalable MCP gateway.', and set visibility to public: https://storage.googleapis.com/my-bucket/tutorial.mp4"*

#### YouTube Shorts:
> **Prompt:**  
> *"Upload this short clip as a YouTube Short with title 'AI Coding in 2026 #Shorts': https://storage.googleapis.com/my-bucket/short_clip.mp4"*

---

## 5. Fetching Live Analytics & Impressions

You don't need to open third-party dashboards to check engagement:

> **Prompt:**  
> *"Show me the engagement metrics and impressions for my latest Instagram post and Twitter tweet."*

**Returned Data:**
- **Instagram**: Total Impressions, Reach, Likes, Comments, Shares, Saved count.
- **Twitter / X**: Total Impressions, Retweets, Likes, Replies, Quote Tweets.
- **YouTube**: Total Views, Like Count, Comment Count, Watch Time.

---

## 6. Managing Your Connected Accounts

### List Connected Accounts:
> **Prompt:** *"Show me all my connected social accounts."*

### Disconnect an Account:
> **Prompt:** *"Disconnect my Instagram account."*
*(The server immediately revokes upstream access tokens and purges credentials from the database).*
