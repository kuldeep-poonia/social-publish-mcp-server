# Complete User Guide: How to Connect Social MCP to Any LLM & Publish

This guide gives you the **exact, step-by-step instructions** to connect the **Social Publishing & Analytics MCP Server** to **Claude Desktop**, **Cursor IDE**, **Windsurf**, **VS Code (Cline / Roo-Code)**, and **ChatGPT Custom Actions**, link your social accounts, and use all 14 AI publishing tools.

---

## 📑 Contents
1. [Choose Your AI Client & Connect in 60 Seconds](#1-choose-your-ai-client--connect-in-60-seconds)
   - [A. Claude Desktop (Windows & Mac)](#a-claude-desktop-windows--mac)
   - [B. Cursor IDE](#b-cursor-ide)
   - [C. Windsurf & VS Code (Cline / Roo-Code / Continue)](#c-windsurf--vs-code-cline--roo-code--continue)
   - [D. ChatGPT Custom Actions (OpenAPI 3.1)](#d-chatgpt-custom-actions-openapi-31)
2. [How to Link Social Accounts (OAuth 2.0 PKCE)](#2-how-to-link-social-accounts-oauth-20-pkce)
3. [Real-World Example Prompts for All 14 Tools](#3-real-world-example-prompts-for-all-14-tools)
   - [Core Publishing & Media](#core-publishing--media)
   - [Analytics & Account Insights](#analytics--account-insights)
   - [Autonomous Scheduling](#autonomous-scheduling)
   - [Real-Time Trending Scout](#real-time-trending-scout)
   - [CTR & SEO Optimization](#ctr--seo-optimization)
   - [Brand Persona & Voice Lock](#brand-persona--voice-lock)
4. [Frequently Asked Questions (FAQ) & Disclosures](#4-frequently-asked-questions-faq--disclosures)

---

## 1. Choose Your AI Client & Connect in 60 Seconds

### A. Claude Desktop (Windows & Mac)

The server supports both the modern **Streamable HTTP** transport (`/mcp`) and the legacy **Server-Sent Events** transport (`/mcp/sse`).

#### Step 1: Open the Config File
- **On Windows**: Press `Win + R`, paste the following line, and press **Enter**:
  ```text
  notepad %APPDATA%\Claude\claude_desktop_config.json
  ```
- **On macOS**: Open Terminal and run:
  ```bash
  open -e ~/Library/Application\ Support/Claude/claude_desktop_config.json
  ```

#### Step 2: Paste the Server Config
Paste this JSON block into your config file and save (`Ctrl + S` or `Cmd + S`):

```json
{
  "mcpServers": {
    "social-publisher": {
      "url": "https://social-mcp.duckdns.org/mcp"
    }
  }
}
```
*(If you are using an older Claude client requiring Server-Sent Events, you can also use `"url": "https://social-mcp.duckdns.org/mcp/sse"`).*

#### Step 3: Restart Claude Desktop
Completely close Claude Desktop and relaunch it. Look for the **Hammer (🔨) icon** in the prompt box indicating 14 tools are loaded.

---

### B. Cursor IDE

1. Open **Cursor IDE**.
2. Navigate to **Settings** (`Ctrl + ,` or `Cmd + ,`) $\rightarrow$ **Features** $\rightarrow$ **MCP Servers**.
3. Click **`+ Add New MCP Server`**.
4. Enter the server configuration:
   - **Name**: `social-publisher`
   - **Type**: `SSE` (or `HTTP`)
   - **Server URL**: `https://social-mcp.duckdns.org/mcp/sse`
5. Click **Add**. A green status dot will confirm the server is live and ready.

---

### C. Windsurf & VS Code (Cline / Roo-Code / Continue)

1. Open your MCP configuration file (`mcp_settings.json` or `cline_mcp_settings.json`):
```json
{
  "mcpServers": {
    "social-publisher": {
      "url": "https://social-mcp.duckdns.org/mcp/sse"
    }
  }
}
```
2. Save and reload your editor window (`Developer: Reload Window`).

---

### D. ChatGPT Custom Actions (OpenAPI 3.1)

1. Go to **ChatGPT** $\rightarrow$ **Explore GPTs** $\rightarrow$ **Create a GPT** $\rightarrow$ **Configure**.
2. Scroll to **Actions** $\rightarrow$ **Create new action**.
3. Click **Import from URL** and paste:
   ```text
   https://social-mcp.duckdns.org/openapi.json
   ```
4. Set Authentication to **None** or **Bearer API Key** to start invoking REST endpoints directly from ChatGPT.

---

## 2. How to Link Social Accounts (OAuth 2.0 PKCE)

Before publishing, link your target platform accounts. You can do this either via the web interface or directly through your AI assistant.

### Option 1: Link via AI Assistant Prompt
Simply prompt your AI:
> *"I want to connect my YouTube channel (or Twitter / Instagram account)."*

The AI will call `connect_platform(platform="youtube")` and provide an official authorization URL. Click the link, sign in to your social account, and grant access.

### Option 2: 1-Click Browser Connection
Visit the official web hub at [https://social-mcp.duckdns.org/#connect-hub](https://social-mcp.duckdns.org/#connect-hub) and click:
- **Connect Instagram** (Meta Graph API popup)
- **Connect Twitter / X** (OAuth 2.0 PKCE popup)
- **Connect YouTube Channel** (Google OAuth 2.0 popup)

> [!NOTE]
> All tokens are encrypted at rest using **AES-256-GCM** in an isolated PostgreSQL vault. Raw passwords and access tokens never touch prompt logs.

---

## 3. Real-World Example Prompts for All 14 Tools

### Core Publishing & Media

#### 1. `publish_post`
Publish text, images, carousels, reels, tweets, or videos immediately across platforms.
- **Example Prompt (Instagram Image)**:
  > *"Publish a photo to Instagram with the image 'https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe' and caption 'Launching our new open-source MCP server today! 🚀 #OpenSource #AI'."*
- **Example Prompt (Twitter Thread)**:
  > *"Post a 3-part tweet thread on Twitter about how Model Context Protocol connects LLMs to real-world APIs."*
- **Example Prompt (YouTube Video)**:
  > *"Publish a YouTube video using media URL 'https://example.com/demo.mp4' with title 'Social MCP Server Setup Walkthrough' and description 'Step-by-step guide to installing Social MCP.' Categorize it under Science & Technology."*

#### 2. `upload_media`
Upload and stage raw media assets (Base64 payload or public URL) with automatic validation and transcoding.
- **Example Prompt**:
  > *"Upload and validate this image URL 'https://example.com/banner.png' for my next Instagram carousel post."*

---

### Analytics & Account Insights

#### 3. `get_analytics`
Retrieve real-time metrics (impressions, retweets, views, likes, comments) for a specific published post or video.
- **Example Prompt**:
  > *"Fetch the latest analytics and view count for my YouTube video with ID 'dQw4w9WgXcQ'."*

#### 4. `get_account_insights`
Retrieve high-level 30-day follower growth, reach, and total engagement breakdown for a connected platform.
- **Example Prompt**:
  > *"Show me a 30-day performance and audience growth breakdown for my Instagram business account."*

---

### Autonomous Scheduling

#### 5. `schedule_post`
Schedule a post, tweet, reel, or video to publish automatically at a future UTC timestamp.
- **Example Prompt**:
  > *"Schedule a reel to publish to my Instagram account tomorrow at 7:00 PM UTC with the video URL 'https://example.com/clip.mp4' and caption 'Behind the scenes at the lab 🔬 #DevLife'."*

#### 6. `list_scheduled_posts`
View all pending, queued, and completed scheduled posts across platforms.
- **Example Prompt**:
  > *"List all my pending scheduled posts for the next 7 days."*

#### 7. `cancel_scheduled_post`
Cancel and remove a pending scheduled post before its trigger time.
- **Example Prompt**:
  > *"Cancel the scheduled post with ID 'sch_34c9624f-22f5-4ffe-a12e-943687c577b2'."*

---

### Real-Time Trending Scout

#### 8. `scout_trending_topics`
Scouts real-time trending discussions from live Reddit feeds and Hacker News, analyzes community sentiment, and generates ready-to-publish hooks, captions, and sanitized hashtags using Google Gemini 2.5 Flash.
- **Example Prompt**:
  > *"Scout the top 3 trending AI and technology topics right now, and give me platform-specific drafts with viral hooks and hashtags for Twitter and Instagram."*

---

### CTR & SEO Optimization

#### 9. `update_post_metadata`
Re-evaluates post drafts and generates 3 psychological title angles (Curiosity Gap, Data-Driven, Contrarian) and description hooks to maximize Click-Through Rate (CTR).
- **Example Prompt**:
  > *"Generate 3 high-CTR title variations and an engaging description hook for my upcoming video about 'Why Autonomous AI Agents Will Replace Traditional Zapier Integrations'."*

#### 10. `optimize_content_seo`
Analyzes content to generate high-ranking search keywords, categorical tags, and platform-sanitized hashtags.
- **Example Prompt**:
  > *"Optimize this post for YouTube and Instagram search algorithms: 'How to build an MCP server in Go from scratch'."*

---

### Brand Persona & Voice Lock

#### 11. `set_brand_persona`
Configures persistent brand voice guidelines, tone, aesthetic palette, and forbidden buzzwords that automatically shape all AI drafting tools.
- **Example Prompt**:
  > *"Set my brand persona: Tone should be 'sarcastic tech commentary', Visual Palette should be 'dark cyberpunk with neon purple highlights', and forbid the buzzwords 'delve', 'synergy', 'game-changer', and 'tapestry'."*

#### 12. `get_brand_persona`
Inspects your currently active brand persona rules, tone guidelines, and forbidden word filters.
- **Example Prompt**:
  > *"What is my currently active brand persona configuration?"*

---

### Account Management & Health

#### 13. `connect_platform`
Generates an authenticated OAuth 2.0 PKCE authorization link to link or refresh Twitter, Instagram, or YouTube accounts.
- **Example Prompt**:
  > *"Generate an authorization link to connect my Twitter/X account."*

#### 14. `ping`
Performs a round-trip diagnostic ping to verify server health, database connectivity, and Redis queue status.
- **Example Prompt**:
  > *"Ping the Social MCP server to confirm all gateway systems are healthy."*

---

## 4. Frequently Asked Questions (FAQ) & Disclosures

### Q1: Does the AI automatically create social media accounts for me?
**No.** The Social MCP server does not and cannot register or create accounts on Twitter/X, Instagram, or YouTube. You must possess existing, verified accounts on these platforms and explicitly authorize them via standard OAuth 2.0 / 2.1 authentication.

### Q2: Does the server generate video or audio files from scratch?
**No.** The server does not perform generative video synthesis (e.g. text-to-video rendering). Users provide remote media URLs (from CDNs, Unsplash, AWS S3, Cloudinary) or raw media files. The server validates, downloads, transcodes (PNG $\rightarrow$ JPEG, container format alignment), chunks, and streams the media to upstream social APIs.

### Q3: Who is responsible for reviewing content before publishing?
**The user is strictly responsible for reviewing and approving all content.** As defined in our [Terms of Service](https://social-mcp.duckdns.org/terms), Large Language Models (LLMs) can occasionally produce unexpected or non-compliant text. Users must review all AI-generated drafts, captions, hashtags, and scheduled posts prior to or during publication.

### Q4: How does autonomous scheduling work if my computer is turned off?
Scheduled posts are stored securely in our cloud database (**Supabase PostgreSQL**) and managed by a dual-trigger architecture: an internal background worker polling every 30 seconds combined with an external serverless cron trigger (`/api/v1/cron/execute-scheduled`). Scheduled posts execute reliably on cloud infrastructure regardless of whether your local machine is on.

### Q5: How do I revoke access or delete my stored tokens?
You can revoke access at any time:
1. Direct API / MCP Tool: Call `disconnect_platform(platform="...")`.
2. Provider Security Settings: Revoke app permissions directly inside your [Google Account Permissions](https://myaccount.google.com/permissions), [Meta Connected Apps](https://www.facebook.com/settings?tab=business_tools), or [Twitter Connected Apps](https://twitter.com/settings/connected_apps).

---

## 🛡️ Legal & Privacy Policies
- **Privacy Policy**: [https://social-mcp.duckdns.org/privacy](https://social-mcp.duckdns.org/privacy)
- **Terms of Service**: [https://social-mcp.duckdns.org/terms](https://social-mcp.duckdns.org/terms)
- **Support & Maintainer Contact**: [kuldeeppoonia20298@gmail.com](mailto:kuldeeppoonia20298@gmail.com)
