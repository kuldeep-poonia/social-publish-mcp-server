/**
 * Social Publishing MCP Server — Apple-Grade Interactive Client Scripts
 */

document.addEventListener('DOMContentLoaded', () => {
  // Toast Notification Controller
  const toast = document.getElementById('toast');
  function showToast(message = 'Copied to clipboard') {
    if (!toast) return;
    toast.textContent = message;
    toast.classList.add('show');
    setTimeout(() => {
      toast.classList.remove('show');
    }, 2200);
  }

  // Universal Clipboard Copy
  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(() => showToast());
    } else {
      const el = document.createElement('textarea');
      el.value = text;
      document.body.appendChild(el);
      el.select();
      document.execCommand('copy');
      document.body.removeChild(el);
      showToast();
    }
  }

  // 1. Copy Claude Config Buttons
  const copyConfigTop = document.getElementById('copy-config-btn-top');
  const claudeConfigSnippet = `{\n  "mcpServers": {\n    "social-publisher": {\n      "command": "docker",\n      "args": ["exec", "-i", "social_mcp_app", "/app/social-mcp-server"]\n    }\n  }\n}`;

  if (copyConfigTop) {
    copyConfigTop.addEventListener('click', () => copyText(claudeConfigSnippet));
  }

  // 2. Interactive Tool Schema Studio
  const toolDefinitions = {
    publish: {
      title: 'publish_post.json',
      code: `// publish_post JSON-RPC 2.0 Protocol Schema\n{\n  "name": "publish_post",\n  "description": "Publishes text updates, photo attachments, or video content to Twitter/X, YouTube, or Instagram.",\n  "parameters": {\n    "type": "object",\n    "required": ["platform", "content"],\n    "properties": {\n      "platform": { "type": "string", "enum": ["twitter", "youtube", "instagram"] },\n      "content": { "type": "string", "description": "Text caption or description" },\n      "media_urls": { "type": "array", "items": { "type": "string" }, "description": "Public media URLs to attach" },\n      "title": { "type": "string", "description": "Required for YouTube video uploads" },\n      "visibility": { "type": "string", "enum": ["public", "unlisted", "private"], "default": "public" },\n      "media_type": { "type": "string", "enum": ["IMAGE", "VIDEO", "REEL", "CAROUSEL"] }\n    }\n  }\n}`
    },
    analytics: {
      title: 'get_post_analytics.json',
      code: `// get_post_analytics JSON-RPC 2.0 Protocol Schema\n{\n  "name": "get_post_analytics",\n  "description": "Retrieves real-time and historical engagement telemetry for a published post.",\n  "parameters": {\n    "type": "object",\n    "required": ["platform", "post_id"],\n    "properties": {\n      "platform": { "type": "string", "enum": ["twitter", "youtube", "instagram"] },\n      "post_id": { "type": "string", "description": "Upstream platform post/video/reel identifier" }\n    }\n  }\n}`
    },
    list: {
      title: 'list_connections.json',
      code: `// list_connections JSON-RPC 2.0 Protocol Schema\n{\n  "name": "list_connections",\n  "description": "Returns all active authenticated social network connections for the requesting user.",\n  "parameters": {\n    "type": "object",\n    "properties": {}\n  }\n}`
    },
    disconnect: {
      title: 'disconnect_platform.json',
      code: `// disconnect_platform JSON-RPC 2.0 Protocol Schema\n{\n  "name": "disconnect_platform",\n  "description": "Revokes upstream platform authorization tokens and deletes credentials from the encrypted vault.",\n  "parameters": {\n    "type": "object",\n    "required": ["platform"],\n    "properties": {\n      "platform": { "type": "string", "enum": ["twitter", "youtube", "instagram"] }\n    }\n  }\n}`
    }
  };

  const toolNavBtns = document.querySelectorAll('.tool-nav-btn');
  const toolTitleDisplay = document.getElementById('tool-title-display');
  const toolCodeViewContent = document.getElementById('tool-code-view-content');
  const copySchemaBtn = document.getElementById('copy-schema-btn');

  toolNavBtns.forEach((btn) => {
    btn.addEventListener('click', () => {
      toolNavBtns.forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');

      const toolKey = btn.getAttribute('data-tool');
      if (toolDefinitions[toolKey]) {
        toolTitleDisplay.textContent = toolDefinitions[toolKey].title;
        toolCodeViewContent.textContent = toolDefinitions[toolKey].code;
      }
    });
  });

  if (copySchemaBtn) {
    copySchemaBtn.addEventListener('click', () => {
      copyText(toolCodeViewContent.textContent);
    });
  }

  // 3. Studio Interactive Simulator Scenarios
  const terminalStream = document.getElementById('terminal-stream');
  const dockPills = document.querySelectorAll('.dock-pill');

  const studioScenarios = {
    combo: {
      userText: 'Upload our 120MB announcement video to YouTube with title "Autonomous Go Architecture", and cross-post a teaser to Twitter with media.',
      aiHtml: `
        <p class="ai-text">Executing coordinated publish across YouTube and Twitter/X via Social MCP Gateway:</p>
        <div class="studio-event-card">
          <div class="event-header">
            <span class="event-badge yt"><i class="fa-brands fa-youtube"></i> publish_post</span>
            <span class="event-time">8MB Resumable Chunk Stream</span>
          </div>
          <div class="event-payload">
            <code>{"platform": "youtube", "title": "Autonomous Go Architecture", "visibility": "public"}</code>
          </div>
          <div class="stream-progress-track">
            <div class="stream-progress-bar" style="width: 100%;"></div>
          </div>
          <div class="event-meta success">
            <i class="fa-solid fa-check"></i> Uploaded Video ID: <strong>yt_v90281a</strong> (Quota Reserved: 1,600 / 10,000)
          </div>
        </div>

        <div class="studio-event-card">
          <div class="event-header">
            <span class="event-badge x"><i class="fa-brands fa-x-twitter"></i> publish_post</span>
            <span class="event-time">14ms Direct Dispatch</span>
          </div>
          <div class="event-payload">
            <code>{"platform": "twitter", "content": "Autonomous Go Architecture is live now! 🚀"}</code>
          </div>
          <div class="event-meta success">
            <i class="fa-solid fa-check"></i> Published Tweet ID: <strong>1894726190283741184</strong> (Idempotency Lock: ACQUIRED)
          </div>
        </div>
      `
    },
    instagram: {
      userText: 'Publish this Reel to my Instagram account with caption "Autonomous AI Agents in Production ✨" from <code>https://cdn.example.com/reel_01.mp4</code>',
      aiHtml: `
        <p class="ai-text">Processing Instagram container lifecycle via Meta Graph API v20.0:</p>
        <div class="studio-event-card">
          <div class="event-header">
            <span class="event-badge" style="background: rgba(236, 72, 153, 0.15); color: #ec4899;"><i class="fa-brands fa-instagram"></i> publish_post</span>
            <span class="event-time">Container State Polling</span>
          </div>
          <div class="event-payload">
            <code>{"platform": "instagram", "content": "Autonomous AI Agents in Production ✨", "media_type": "REEL"}</code>
          </div>
          <div class="event-meta success">
            <i class="fa-solid fa-check"></i> Container Created: <strong>179948291048</strong> • Published Reel ID: <strong>1789502948201</strong>
          </div>
        </div>
      `
    },
    analytics: {
      userText: 'Fetch the real-time engagement telemetry for our published Instagram post ID 1789502948201.',
      aiHtml: `
        <p class="ai-text">Aggregating live metric telemetry from Meta Graph API:</p>
        <div class="studio-event-card">
          <div class="event-header">
            <span class="event-badge" style="background: rgba(6, 182, 212, 0.15); color: #06b6d4;"><i class="fa-solid fa-chart-line"></i> get_post_analytics</span>
            <span class="event-time">Graph API Sync</span>
          </div>
          <div class="event-payload">
            <code>{"platform": "instagram", "post_id": "1789502948201"}</code>
          </div>
          <div class="event-meta success">
            <i class="fa-solid fa-check"></i> Impressions: <strong>14,250</strong> • Reach: <strong>11,890</strong> • Likes: <strong>842</strong> • Shares: <strong>128</strong>
          </div>
        </div>
      `
    }
  };

  dockPills.forEach((pill) => {
    pill.addEventListener('click', () => {
      dockPills.forEach((p) => p.classList.remove('active'));
      pill.classList.add('active');

      const action = pill.getAttribute('data-action');
      const scenario = studioScenarios[action];
      if (!scenario) return;

      terminalStream.innerHTML = `
        <div class="chat-bubble-row user-row">
          <div class="avatar-pill user-avatar">User</div>
          <div class="bubble-glass user-bubble">
            ${scenario.userText}
          </div>
        </div>

        <div class="chat-bubble-row ai-row">
          <div class="avatar-pill ai-avatar">Claude</div>
          <div class="bubble-glass ai-bubble">
            ${scenario.aiHtml}
          </div>
        </div>
      `;
    });
  });
});
