/**
 * Social Publishing MCP Server — Interactive Landing Page Scripts
 */

document.addEventListener('DOMContentLoaded', () => {
  // Toast Helper
  const toast = document.getElementById('toast');
  function showToast(message = 'Copied to clipboard!') {
    toast.textContent = message;
    toast.classList.add('show');
    setTimeout(() => {
      toast.classList.remove('show');
    }, 2400);
  }

  // Copy to Clipboard generic helper
  function copyTextToClipboard(text) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(() => showToast());
    } else {
      const textarea = document.createElement('textarea');
      textarea.value = text;
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand('copy');
      document.body.removeChild(textarea);
      showToast();
    }
  }

  // 1. Copy Claude Configuration
  const copyConfigBtn = document.getElementById('copy-claude-config');
  if (copyConfigBtn) {
    copyConfigBtn.addEventListener('click', () => {
      const configText = `{\n  "mcpServers": {\n    "social-publisher": {\n      "command": "docker",\n      "args": ["exec", "-i", "social_mcp_app", "/app/social-mcp-server"]\n    }\n  }\n}`;
      copyTextToClipboard(configText);
    });
  }

  // 2. Interactive Tool Schema Switcher
  const toolSchemas = {
    publish: {
      title: 'Tool Schema: publish_post',
      code: `// publish_post JSON-RPC 2.0 Schema\n{\n  "name": "publish_post",\n  "description": "Publishes text updates, photo attachments, or video content to Twitter/X, YouTube, or Instagram.",\n  "parameters": {\n    "type": "object",\n    "required": ["platform", "content"],\n    "properties": {\n      "platform": { "type": "string", "enum": ["twitter", "youtube", "instagram"] },\n      "content": { "type": "string", "description": "Text caption or description" },\n      "media_urls": { "type": "array", "items": { "type": "string" }, "description": "Public media URLs to attach" },\n      "title": { "type": "string", "description": "Required for YouTube video uploads" },\n      "visibility": { "type": "string", "enum": ["public", "unlisted", "private"], "default": "public" },\n      "media_type": { "type": "string", "enum": ["IMAGE", "VIDEO", "REEL", "CAROUSEL"] }\n    }\n  }\n}`
    },
    analytics: {
      title: 'Tool Schema: get_post_analytics',
      code: `// get_post_analytics JSON-RPC 2.0 Schema\n{\n  "name": "get_post_analytics",\n  "description": "Retrieves real-time and historical engagement telemetry for a published post.",\n  "parameters": {\n    "type": "object",\n    "required": ["platform", "post_id"],\n    "properties": {\n      "platform": { "type": "string", "enum": ["twitter", "youtube", "instagram"] },\n      "post_id": { "type": "string", "description": "Upstream platform post/video/reel identifier" }\n    }\n  }\n}`
    },
    list: {
      title: 'Tool Schema: list_connections',
      code: `// list_connections JSON-RPC 2.0 Schema\n{\n  "name": "list_connections",\n  "description": "Returns all active authenticated social network connections for the requesting user.",\n  "parameters": {\n    "type": "object",\n    "properties": {}\n  }\n}`
    },
    disconnect: {
      title: 'Tool Schema: disconnect_platform',
      code: `// disconnect_platform JSON-RPC 2.0 Schema\n{\n  "name": "disconnect_platform",\n  "description": "Revokes upstream platform authorization tokens and deletes credentials from the encrypted vault.",\n  "parameters": {\n    "type": "object",\n    "required": ["platform"],\n    "properties": {\n      "platform": { "type": "string", "enum": ["twitter", "youtube", "instagram"] }\n    }\n  }\n}`
    }
  };

  const toolTabBtns = document.querySelectorAll('.tool-tab-btn');
  const toolNameDisplay = document.getElementById('tool-name-display');
  const toolCodeContent = document.getElementById('tool-code-content');
  const copyToolSchemaBtn = document.getElementById('copy-tool-schema');

  toolTabBtns.forEach((btn) => {
    btn.addEventListener('click', () => {
      toolTabBtns.forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');

      const toolKey = btn.getAttribute('data-tool');
      if (toolSchemas[toolKey]) {
        toolNameDisplay.innerHTML = `<i class="fa-solid fa-code"></i> ${toolSchemas[toolKey].title}`;
        toolCodeContent.textContent = toolSchemas[toolKey].code;
      }
    });
  });

  if (copyToolSchemaBtn) {
    copyToolSchemaBtn.addEventListener('click', () => {
      copyTextToClipboard(toolCodeContent.textContent);
    });
  }

  // 3. Interactive Terminal Simulator
  const terminalBody = document.getElementById('terminal-body');
  const simBtns = document.querySelectorAll('.sim-btn');

  const simulationScenarios = {
    twitter: {
      userPrompt: 'Publish our new video announcement to Twitter with image attachment: <code>https://cdn.example.com/teaser.png</code>',
      toolHtml: `
        <div class="mcp-tool-call">
          <div class="tool-call-header">
            <span class="tool-badge"><i class="fa-brands fa-x-twitter"></i> publish_post</span>
            <span class="tool-timer"><i class="fa-solid fa-bolt"></i> 14ms (Direct Dispatch)</span>
          </div>
          <div class="tool-json">
            <code>{"platform": "twitter", "content": "Autonomous Go Microservices live now! 🚀", "media_urls": ["https://cdn.example.com/teaser.png"]}</code>
          </div>
          <div class="tool-response success">
            <i class="fa-solid fa-circle-check"></i> Published Tweet ID: <strong>1894726190283741184</strong> • Idempotency Lock: <strong>ACQUIRED</strong>
          </div>
        </div>
        Your tweet is published live with the verified image attachment!
      `
    },
    youtube: {
      userPrompt: 'Upload this 120MB video to YouTube with title "Building Autonomous Go Agents", visibility public: <code>https://cdn.example.com/tutorial.mp4</code>',
      toolHtml: `
        <div class="mcp-tool-call">
          <div class="tool-call-header">
            <span class="tool-badge yt"><i class="fa-brands fa-youtube"></i> publish_post</span>
            <span class="tool-timer"><i class="fa-solid fa-arrows-rotate"></i> 8MB Resumable Chunk Stream</span>
          </div>
          <div class="tool-json">
            <code>{"platform": "youtube", "title": "Building Autonomous Go Agents", "visibility": "public", "media_type": "VIDEO"}</code>
          </div>
          <div class="upload-progress-bar">
            <div class="progress-fill" style="width: 100%;"></div>
          </div>
          <div class="tool-response success">
            <i class="fa-solid fa-circle-check"></i> Uploaded Video ID: <strong>yt_v927401a8</strong> • Memory Delta: <strong>&lt; 0.8 MB</strong> • Daily Quota: <strong>1,600 / 10,000</strong>
          </div>
        </div>
        Video uploaded cleanly using Google Resumable Chunk Protocol!
      `
    },
    instagram: {
      userPrompt: 'Post this photo to my Instagram Feed with caption "Behind the scenes at our summit! 📸✨" from <code>https://cdn.example.com/banner.png</code>',
      toolHtml: `
        <div class="mcp-tool-call">
          <div class="tool-call-header">
            <span class="tool-badge" style="background: rgba(236, 72, 153, 0.2); color: #f472b6;"><i class="fa-brands fa-instagram"></i> publish_post</span>
            <span class="tool-timer"><i class="fa-solid fa-gear"></i> PNG-to-JPEG Transcoded</span>
          </div>
          <div class="tool-json">
            <code>{"platform": "instagram", "content": "Behind the scenes at our summit! 📸✨", "media_urls": ["https://cdn.example.com/banner.png"], "media_type": "IMAGE"}</code>
          </div>
          <div class="tool-response success">
            <i class="fa-solid fa-circle-check"></i> Container ID: <strong>179948291048</strong> • Published Instagram Media ID: <strong>1789502948201</strong>
          </div>
        </div>
        Instagram container processed and feed image published successfully!
      `
    },
    analytics: {
      userPrompt: 'Fetch the real-time engagement telemetry and impressions for my latest Instagram post ID 1789502948201.',
      toolHtml: `
        <div class="mcp-tool-call">
          <div class="tool-call-header">
            <span class="tool-badge" style="background: rgba(6, 182, 212, 0.2); color: #67e8f9;"><i class="fa-solid fa-chart-line"></i> get_post_analytics</span>
            <span class="tool-timer"><i class="fa-solid fa-clock"></i> Live Graph API Sync</span>
          </div>
          <div class="tool-json">
            <code>{"platform": "instagram", "post_id": "1789502948201"}</code>
          </div>
          <div class="tool-response success">
            <i class="fa-solid fa-circle-check"></i> Impressions: <strong>14,250</strong> • Reach: <strong>11,890</strong> • Likes: <strong>842</strong> • Comments: <strong>67</strong> • Shares: <strong>128</strong> • Saves: <strong>94</strong>
          </div>
        </div>
        Here is your engagement report: Post reach is currently pacing 24% higher than your average!
      `
    }
  };

  simBtns.forEach((btn) => {
    btn.addEventListener('click', () => {
      simBtns.forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');

      const action = btn.getAttribute('data-action');
      const scenario = simulationScenarios[action];
      if (!scenario) return;

      terminalBody.innerHTML = `
        <div class="chat-message user-msg">
          <div class="msg-avatar"><i class="fa-solid fa-user"></i></div>
          <div class="msg-content">
            <div class="msg-header">You</div>
            <div class="msg-bubble">${scenario.userPrompt}</div>
          </div>
        </div>

        <div class="chat-message ai-msg">
          <div class="msg-avatar"><i class="fa-solid fa-sparkles"></i></div>
          <div class="msg-content">
            <div class="msg-header">Claude (via Social MCP Gateway)</div>
            <div class="msg-bubble">
              I'm executing this action through the Social Publishing MCP Server.
              ${scenario.toolHtml}
            </div>
          </div>
        </div>
      `;
    });
  });
});
