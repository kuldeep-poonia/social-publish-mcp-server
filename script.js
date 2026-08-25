/**
 * Social Publishing MCP Server — Clean Apple Light Scripts
 */

document.addEventListener('DOMContentLoaded', () => {
  // Toast Popup Helper
  const toast = document.getElementById('toast');
  function showToast(message = 'Copied to clipboard') {
    if (!toast) return;
    toast.textContent = message;
    toast.classList.add('show');
    setTimeout(() => {
      toast.classList.remove('show');
    }, 2000);
  }

  // Universal Clipboard Copy
  function copyText(text) {
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

  // 1. Copy Config Button
  const btnCopyConfig = document.getElementById('btn-copy-config');
  const claudeConfigText = `{\n  "mcpServers": {\n    "social-publisher": {\n      "command": "docker",\n      "args": ["exec", "-i", "social_mcp_app", "/app/social-mcp-server"]\n    }\n  }\n}`;

  if (btnCopyConfig) {
    btnCopyConfig.addEventListener('click', () => copyText(claudeConfigText));
  }

  // 2. Interactive Demo Action Switcher
  const chatThread = document.getElementById('chat-thread');
  const actionChips = document.querySelectorAll('.action-chip');

  const demoScenarios = {
    combo: {
      userQuery: 'Upload our 120MB video to YouTube with title "Autonomous Go Architecture", and cross-post a teaser tweet to Twitter with our image banner.',
      aiResponse: `
        <p class="ai-intro">Executing simultaneous multi-platform publishing:</p>
        <div class="step-card">
          <div class="step-header">
            <span class="platform-tag yt"><i class="fa-brands fa-youtube"></i> YouTube (Data API v3)</span>
            <span class="step-speed">8MB Resumable Chunks</span>
          </div>
          <div class="progress-line">
            <div class="progress-bar-fill" style="width: 100%;"></div>
          </div>
          <div class="step-result text-success">
            <i class="fa-solid fa-circle-check"></i> Uploaded Video ID: <strong>yt_v90281a</strong> • Quota Reserved: 1,600 / 10,000
          </div>
        </div>

        <div class="step-card">
          <div class="step-header">
            <span class="platform-tag x"><i class="fa-brands fa-x-twitter"></i> Twitter / X (API v2)</span>
            <span class="step-speed">14ms Direct Dispatch</span>
          </div>
          <div class="step-result text-success">
            <i class="fa-solid fa-circle-check"></i> Published Tweet ID: <strong>1894726190283741184</strong> • Idempotency Lock: ACQUIRED
          </div>
        </div>
      `
    },
    instagram: {
      userQuery: 'Post this photo to my Instagram Feed with caption "Behind the scenes at our summit! 📸✨" from https://cdn.example.com/photo.png',
      aiResponse: `
        <p class="ai-intro">Processing container lifecycle via Meta Graph API v20.0:</p>
        <div class="step-card">
          <div class="step-header">
            <span class="platform-tag ig"><i class="fa-brands fa-instagram"></i> Instagram (Meta Graph API)</span>
            <span class="step-speed">PNG-to-JPEG Auto-Transcode</span>
          </div>
          <div class="step-result text-success">
            <i class="fa-solid fa-circle-check"></i> Container Created: <strong>179948291048</strong> • Published Instagram Media ID: <strong>1789502948201</strong>
          </div>
        </div>
      `
    },
    analytics: {
      userQuery: 'Query real-time engagement telemetry for our published Instagram post ID 1789502948201.',
      aiResponse: `
        <p class="ai-intro">Aggregating live metric telemetry from upstream social APIs:</p>
        <div class="step-card">
          <div class="step-header">
            <span class="platform-tag ig"><i class="fa-brands fa-instagram"></i> Instagram Telemetry</span>
            <span class="step-speed">Live Sync</span>
          </div>
          <div class="step-result text-success">
            <i class="fa-solid fa-circle-check"></i> Impressions: <strong>14,250</strong> • Reach: <strong>11,890</strong> • Likes: <strong>842</strong> • Comments: <strong>67</strong> • Shares: <strong>128</strong>
          </div>
        </div>
      `
    }
  };

  actionChips.forEach((chip) => {
    chip.addEventListener('click', () => {
      actionChips.forEach((c) => c.classList.remove('active'));
      chip.classList.add('active');

      const actionKey = chip.getAttribute('data-action');
      const data = demoScenarios[actionKey];
      if (!data) return;

      chatThread.innerHTML = `
        <div class="chat-row user-side">
          <div class="chat-label">You</div>
          <div class="chat-card user-bubble">
            ${data.userQuery}
          </div>
        </div>

        <div class="chat-row ai-side">
          <div class="chat-label">Claude (via Social MCP)</div>
          <div class="chat-card ai-bubble">
            ${data.aiResponse}
          </div>
        </div>
      `;
    });
  });
});
