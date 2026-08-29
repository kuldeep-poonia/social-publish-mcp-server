package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/instagram"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/twitter"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/youtube"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
)

type twitterOAuthState struct {
	codeVerifier string
	userID       string
	redirectURI  string
	expiresAt    time.Time
}

func (s *HTTPServer) handleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Accept")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	baseURL := s.getBaseURL(r)
	log.Printf("[OAuth Discovery] Metadata requested: Host=%s, Path=%s, IP=%s", r.Host, r.URL.Path, extractClientIP(r))

	metadata := map[string]interface{}{
		"issuer":                                baseURL,
		"authorization_endpoint":                fmt.Sprintf("%s/oauth/authorize", baseURL),
		"token_endpoint":                        fmt.Sprintf("%s/oauth/token", baseURL),
		"registration_endpoint":                 fmt.Sprintf("%s/oauth/register", baseURL),
		"icon_url":                              fmt.Sprintf("%s/logo.png", baseURL),
		"logo_uri":                              fmt.Sprintf("%s/logo.png", baseURL),
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"read", "write", "publish"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(metadata)
}

type dynamicClientRegisterRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (s *HTTPServer) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Accept")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		log.Printf("[OAuth Register] REJECTED: Invalid method=%s", r.Method)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var req dynamicClientRegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	log.Printf("[OAuth Register] START: ClientName=%s, RedirectURIs=%v, IP=%s", req.ClientName, req.RedirectURIs, extractClientIP(r))

	if len(req.RedirectURIs) == 0 {
		req.RedirectURIs = []string{
			"https://claude.ai/api/mcp/auth_callback",
			"https://claude.ai/oauth/callback",
			"claude://oauth/callback",
			"cursor://oauth/callback",
			"*",
		}
	}

	rawBytes := make([]byte, 16)
	_, _ = rand.Read(rawBytes)
	clientID := fmt.Sprintf("client_%s", hex.EncodeToString(rawBytes))
	clientSecret := hex.EncodeToString(rawBytes)

	name := req.ClientName
	if name == "" {
		name = "Dynamic MCP Client"
	}

	_ = s.oauthServer.RegisterClient(clientID, clientSecret, name, req.RedirectURIs)
	log.Printf("[OAuth Register] SUCCESS: Dynamic client created: client_id=%s, name=%s", clientID, name)

	resp := map[string]interface{}{
		"client_id":                  clientID,
		"client_secret":              clientSecret,
		"client_id_issued_at":        time.Now().Unix(),
		"client_secret_expires_at":   0,
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *HTTPServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		log.Printf("[OAuth Authorize] REJECTED: Invalid method=%s", r.Method)
		http.Error(w, "Method Not Allowed, use GET", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	userID := q.Get("user_id")
	if userID == "" {
		userID = "kuldeep"
	}

	clientID := q.Get("client_id")
	if clientID == "" {
		clientID = "claude_desktop"
	}

	codeChallengeMethod := q.Get("code_challenge_method")
	if codeChallengeMethod == "" {
		codeChallengeMethod = "S256"
	}

	codeChallenge := q.Get("code_challenge")
	if codeChallenge == "" {
		codeChallenge = "E9Melhoa2OwvFrGMTJguCH5Zw_l5UG9UrQiAhboOdDA"
	}

	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "https://claude.ai/api/mcp/auth_callback"
	}

	log.Printf("[OAuth Authorize] START: client_id=%s, redirect_uri=%s, response_type=%s, state=%s, code_challenge_len=%d, method=%s, user_id=%s, IP=%s",
		clientID, redirectURI, q.Get("response_type"), q.Get("state"), len(codeChallenge), codeChallengeMethod, userID, extractClientIP(r))

	req := &auth.AuthorizeRequest{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		UserID:              userID,
	}

	code, err := s.oauthServer.Authorize(req)
	if err != nil {
		log.Printf("[OAuth Authorize] FAILED: %v", err)
		http.Error(w, fmt.Sprintf("Authorization Error: %v", err), http.StatusBadRequest)
		return
	}

	redirectTarget := fmt.Sprintf("%s?code=%s", req.RedirectURI, code)
	if req.State != "" {
		redirectTarget += fmt.Sprintf("&state=%s", req.State)
	}
	log.Printf("[OAuth Authorize] SUCCESS: Redirecting user to target: %s", redirectTarget)
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

func (s *HTTPServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		log.Printf("[OAuth Token] REJECTED: Invalid method=%s", r.Method)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var req auth.TokenExchangeRequest
	bodyBytes, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(bodyBytes, &req)

	if req.GrantType == "" {
		vals, _ := url.ParseQuery(string(bodyBytes))
		req.GrantType = vals.Get("grant_type")
		req.Code = vals.Get("code")
		req.ClientID = vals.Get("client_id")
		req.CodeVerifier = vals.Get("code_verifier")
		req.RedirectURI = vals.Get("redirect_uri")
		req.RefreshToken = vals.Get("refresh_token")
	}

	if req.GrantType == "" && req.Code != "" {
		req.GrantType = "authorization_code"
	}

	log.Printf("[OAuth Token] START: grant_type=%s, client_id=%s, code=%s, redirect_uri=%s, code_verifier_len=%d, IP=%s",
		req.GrantType, req.ClientID, req.Code, req.RedirectURI, len(req.CodeVerifier), extractClientIP(r))

	store := auth.NewInMemorySessionStore()
	pair, err := s.oauthServer.ExchangeCodeForTokens(r.Context(), &req, store)
	if err != nil {
		log.Printf("[OAuth Token] FAILED: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": fmt.Sprintf("%v", err),
		})
		return
	}

	resp := map[string]interface{}{
		"access_token":  pair.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    900,
		"refresh_token": pair.RefreshToken,
		"scope":         "read write publish",
	}

	log.Printf("[OAuth Token] SUCCESS: Issued Access Token for user")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *HTTPServer) handleTwitterConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "test_user_1"
	}

	if _, err := uuid.Parse(userID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), userID, fmt.Sprintf("%s@example.com", userID))
		if userErr == nil && user != nil {
			userID = user.ID
		}
	}

	verifierBytes := make([]byte, 32)
	_, _ = rand.Read(verifierBytes)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	callbackURL := strings.TrimSpace(s.cfg.TwitterRedirectURI)
	if callbackURL == "" || (strings.Contains(callbackURL, "localhost") && strings.Contains(r.Host, "duckdns.org")) {
		callbackURL = fmt.Sprintf("%s/auth/twitter/callback", s.getBaseURL(r))
	}

	s.oauthStatesMu.Lock()
	s.oauthStates[state] = twitterOAuthState{
		codeVerifier: codeVerifier,
		userID:       userID,
		redirectURI:  callbackURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	s.oauthStatesMu.Unlock()

	params := make(map[string][]string)
	params["response_type"] = []string{"code"}
	params["client_id"] = []string{s.cfg.TwitterClientID}
	params["redirect_uri"] = []string{callbackURL}
	params["scope"] = []string{strings.Join(twitter.RequiredScopes, " ")}
	params["state"] = []string{state}
	params["code_challenge"] = []string{codeChallenge}
	params["code_challenge_method"] = []string{"S256"}

	values := urlValues(params)
	authURL := twitter.OAuthAuthorizeURL + "?" + values

	http.Redirect(w, r, authURL, http.StatusFound)
}

func urlValues(m map[string][]string) string {
	var pairs []string
	for k, vs := range m {
		for _, v := range vs {
			pairs = append(pairs, fmt.Sprintf("%s=%s", strings.TrimSpace(k), strings.ReplaceAll(urlQueryEscape(v), " ", "%20")))
		}
	}
	return strings.Join(pairs, "&")
}

func urlQueryEscape(s string) string {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			buf.WriteByte(c)
		} else {
			buf.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return buf.String()
}

func (s *HTTPServer) handleTwitterCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")

	if errParam != "" {
		http.Error(w, fmt.Sprintf("Twitter OAuth Authorization Denied: %s", errParam), http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "Invalid callback: code and state required", http.StatusBadRequest)
		return
	}

	s.oauthStatesMu.Lock()
	oauthState, exists := s.oauthStates[state]
	if exists {
		delete(s.oauthStates, state)
	}
	s.oauthStatesMu.Unlock()

	if !exists || time.Now().After(oauthState.expiresAt) {
		http.Error(w, "Invalid or expired OAuth state parameter (replay attack prevented)", http.StatusBadRequest)
		return
	}

	tokenResp, err := s.twitterClient.ExchangeOAuthToken(r.Context(), code, oauthState.codeVerifier, oauthState.redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed exchanging authorization code with Twitter API v2: %v", err), http.StatusBadRequest)
		return
	}

	actualUserID := oauthState.userID
	if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
		if userErr == nil && user != nil {
			actualUserID = user.ID
		}
	}

	if s.repo != nil {
		expSeconds := tokenResp.ExpiresIn
		if expSeconds <= 0 {
			expSeconds = 7200 // 2 hours default
		}
		expiresAt := time.Now().UTC().Add(time.Duration(expSeconds) * time.Second)
		err = s.repo.SavePlatformConnection(r.Context(), actualUserID, "twitter", []byte(tokenResp.AccessToken), []byte(tokenResp.RefreshToken), expiresAt, twitter.RequiredScopes)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed saving encrypted credentials to vault: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head><title>Twitter Connected</title><style>body{font-family:system-ui,-apple-system,sans-serif;background:#0f1419;color:#fff;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;padding:20px;} .card{background:#1e2732;padding:40px;border-radius:16px;box-shadow:0 8px 32px rgba(0,0,0,0.5);text-align:center;max-width:500px;} h1{color:#1d9bf0;margin-bottom:12px;} p{color:#8b98a5;line-height:1.6;} .badge{background:#00ba7c22;color:#00ba7c;padding:6px 16px;border-radius:20px;display:inline-block;font-weight:bold;margin-bottom:16px;} .btn-group{margin-top:24px;display:flex;gap:12px;justify-content:center;flex-wrap:wrap;} .btn{display:inline-block;padding:12px 24px;border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;cursor:pointer;border:none;transition:all 0.2s;} .btn-close{background:#10a37f;color:#fff;}</style></head>
<body>
<div class="card">
<div class="badge">Connected Successfully</div>
<h1>Twitter/X Authorized</h1>
<p>Your Twitter account is now securely linked in the encrypted token vault for user <strong>%s</strong>.</p>
<p>You can now close this tab and return to your active ChatGPT / Claude chat!</p>
<div class="btn-group">
<button onclick="window.close()" class="btn btn-close">Close & Return to Chat</button>
</div>
</div>
<script>
if (window.opener) {
  setTimeout(function() { window.close(); }, 2000);
}
</script>
</body>
</html>`, oauthState.userID)
}

func (s *HTTPServer) handleYouTubeConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "test_user_1"
	}

	if _, err := uuid.Parse(userID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), userID, fmt.Sprintf("%s@example.com", userID))
		if userErr == nil && user != nil {
			userID = user.ID
		}
	}

	verifierBytes := make([]byte, 32)
	_, _ = rand.Read(verifierBytes)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	callbackURL := strings.TrimSpace(s.cfg.YouTubeRedirectURI)
	if callbackURL == "" || (strings.Contains(callbackURL, "localhost") && strings.Contains(r.Host, "duckdns.org")) {
		callbackURL = fmt.Sprintf("%s/auth/youtube/callback", s.getBaseURL(r))
	}

	s.oauthStatesMu.Lock()
	s.oauthStates[state] = twitterOAuthState{
		codeVerifier: codeVerifier,
		userID:       userID,
		redirectURI:  callbackURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	s.oauthStatesMu.Unlock()

	params := make(map[string][]string)
	params["response_type"] = []string{"code"}
	params["client_id"] = []string{s.cfg.YouTubeClientID}
	params["redirect_uri"] = []string{callbackURL}
	params["scope"] = []string{strings.Join(youtube.RequiredScopes, " ")}
	params["state"] = []string{state}
	params["code_challenge"] = []string{codeChallenge}
	params["code_challenge_method"] = []string{"S256"}
	params["access_type"] = []string{"offline"}
	params["prompt"] = []string{"consent"}

	values := urlValues(params)
	authURL := youtube.OAuthAuthorizeURL + "?" + values

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *HTTPServer) handleYouTubeCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")

	if errParam != "" {
		http.Error(w, fmt.Sprintf("Google OAuth Authorization Denied: %s", errParam), http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "Invalid callback: code and state required", http.StatusBadRequest)
		return
	}

	s.oauthStatesMu.Lock()
	oauthState, exists := s.oauthStates[state]
	if exists {
		delete(s.oauthStates, state)
	}
	s.oauthStatesMu.Unlock()

	if !exists || time.Now().After(oauthState.expiresAt) {
		http.Error(w, "Invalid or expired OAuth state parameter (replay attack prevented)", http.StatusBadRequest)
		return
	}

	tokenResp, err := s.youtubeClient.ExchangeOAuthToken(r.Context(), code, oauthState.codeVerifier, oauthState.redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed exchanging authorization code with Google OAuth: %v", err), http.StatusBadRequest)
		return
	}

	actualUserID := oauthState.userID
	if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
		if userErr == nil && user != nil {
			actualUserID = user.ID
		}
	}

	if s.repo != nil {
		expSeconds := tokenResp.ExpiresIn
		if expSeconds <= 0 {
			expSeconds = 3600 // 1 hour default
		}
		expiresAt := time.Now().UTC().Add(time.Duration(expSeconds) * time.Second)
		err = s.repo.SavePlatformConnection(r.Context(), actualUserID, "youtube", []byte(tokenResp.AccessToken), []byte(tokenResp.RefreshToken), expiresAt, youtube.RequiredScopes)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed saving encrypted credentials to vault: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head><title>YouTube Connected</title><style>body{font-family:system-ui,-apple-system,sans-serif;background:#0f0f0f;color:#fff;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;padding:20px;} .card{background:#212121;padding:40px;border-radius:16px;box-shadow:0 8px 32px rgba(0,0,0,0.5);text-align:center;max-width:500px;} h1{color:#ff0000;margin-bottom:12px;} p{color:#aaa;line-height:1.6;} .badge{background:#00ba7c22;color:#00ba7c;padding:6px 16px;border-radius:20px;display:inline-block;font-weight:bold;margin-bottom:16px;} .btn-group{margin-top:24px;display:flex;gap:12px;justify-content:center;flex-wrap:wrap;} .btn{display:inline-block;padding:12px 24px;border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;cursor:pointer;border:none;transition:all 0.2s;} .btn-close{background:#10a37f;color:#fff;}</style></head>
<body>
<div class="card">
<div class="badge">Connected Successfully</div>
<h1>YouTube Authorized</h1>
<p>Your Google YouTube account is now securely linked in the encrypted token vault for user <strong>%s</strong>.</p>
<p>You can now close this tab and return to your active ChatGPT / Claude chat!</p>
<div class="btn-group">
<button onclick="window.close()" class="btn btn-close">Close & Return to Chat</button>
</div>
</div>
<script>
if (window.opener) {
  setTimeout(function() { window.close(); }, 2000);
}
</script>
</body>
</html>`, oauthState.userID)
}

func (s *HTTPServer) handleInstagramConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "test_user_1"
	}

	if _, err := uuid.Parse(userID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), userID, fmt.Sprintf("%s@example.com", userID))
		if userErr == nil && user != nil {
			userID = user.ID
		}
	}

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	callbackURL := strings.TrimSpace(s.cfg.InstagramRedirectURI)
	if callbackURL == "" || (strings.Contains(callbackURL, "localhost") && strings.Contains(r.Host, "duckdns.org")) {
		callbackURL = fmt.Sprintf("%s/auth/instagram/callback", s.getBaseURL(r))
	}

	s.oauthStatesMu.Lock()
	s.oauthStates[state] = twitterOAuthState{
		codeVerifier: "",
		userID:       userID,
		redirectURI:  callbackURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	s.oauthStatesMu.Unlock()

	params := make(map[string][]string)
	params["response_type"] = []string{"code"}
	params["client_id"] = []string{s.cfg.InstagramClientID}
	params["redirect_uri"] = []string{callbackURL}
	params["scope"] = []string{strings.Join(instagram.RequiredScopes, ",")}
	params["state"] = []string{state}

	values := urlValues(params)
	authURL := instagram.MetaOAuthDialogURL + "?" + values

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *HTTPServer) handleInstagramCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")
	errReason := q.Get("error_reason")

	if errParam != "" || errReason != "" {
		http.Error(w, fmt.Sprintf("Meta OAuth Authorization Denied: %s (%s)", errParam, errReason), http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "Invalid callback: code and state required", http.StatusBadRequest)
		return
	}

	s.oauthStatesMu.Lock()
	oauthState, exists := s.oauthStates[state]
	if exists {
		delete(s.oauthStates, state)
	}
	s.oauthStatesMu.Unlock()

	if !exists || time.Now().After(oauthState.expiresAt) {
		http.Error(w, "Invalid or expired OAuth state parameter (replay attack prevented)", http.StatusBadRequest)
		return
	}

	// 1. Exchange short-lived authorization code
	shortLivedTok, err := s.instagramClient.ExchangeShortLivedToken(r.Context(), code, oauthState.redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed exchanging authorization code with Meta Graph API: %v", err), http.StatusBadRequest)
		return
	}

	// 2. Upgrade to 60-day long-lived access token
	longLivedTok, err := s.instagramClient.ExchangeLongLivedToken(r.Context(), shortLivedTok.AccessToken)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed upgrading to long-lived Meta token: %v", err), http.StatusBadRequest)
		return
	}

	// 3. Persist encrypted credentials into PostgreSQL Token Vault immediately
	actualUserID := oauthState.userID
	if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
		if userErr == nil && user != nil {
			actualUserID = user.ID
		}
	}

	if s.repo != nil {
		expSecs := longLivedTok.ExpiresIn
		if expSecs <= 0 {
			expSecs = 60 * 24 * 3600 // 60 days
		}
		expiresAt := time.Now().UTC().Add(time.Duration(expSecs) * time.Second)
		err = s.repo.SavePlatformConnection(r.Context(), actualUserID, "instagram", []byte(longLivedTok.AccessToken), nil, expiresAt, instagram.RequiredScopes)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed saving encrypted credentials to vault: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// 4. Discover Instagram Business/Creator Account
	igAccount, _, _ := s.instagramClient.GetInstagramBusinessAccount(r.Context(), longLivedTok.AccessToken)
	handleText := "Instagram User"
	accountIDText := "Connected"
	if igAccount != nil && igAccount.Username != "" {
		handleText = "@" + igAccount.Username
		accountIDText = igAccount.ID
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head><title>Instagram Connected</title><style>body{font-family:system-ui,-apple-system,sans-serif;background:#0d0d0d;color:#fff;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;padding:20px;} .card{background:#1a1a1a;padding:40px;border-radius:16px;box-shadow:0 8px 32px rgba(0,0,0,0.5);text-align:center;max-width:500px;border:1px solid #333;} h1{background:linear-gradient(45deg,#f09433,#e6683c,#dc2743,#cc2366,#bc1888);-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:12px;} p{color:#aaa;line-height:1.6;} .badge{background:#00ba7c22;color:#00ba7c;padding:6px 16px;border-radius:20px;display:inline-block;font-weight:bold;margin-bottom:16px;} .handle{font-weight:bold;color:#fff;} .btn-group{margin-top:24px;display:flex;gap:12px;justify-content:center;flex-wrap:wrap;} .btn{display:inline-block;padding:12px 24px;border-radius:8px;text-decoration:none;font-weight:600;font-size:15px;cursor:pointer;border:none;transition:all 0.2s;} .btn-close{background:#10a37f;color:#fff;}</style></head>
<body>
<div class="card">
<div class="badge">Connected Successfully</div>
<h1>Instagram Business Authorized</h1>
<p>Your Instagram account <span class="handle">%s</span> (ID: %s) is now securely linked in the encrypted token vault for user <strong>%s</strong>.</p>
<p>You can now close this tab and return to your active ChatGPT / Claude chat!</p>
<div class="btn-group">
<button onclick="window.close()" class="btn btn-close">Close & Return to Chat</button>
</div>
</div>
<script>
if (window.opener) {
  setTimeout(function() { window.close(); }, 2000);
}
</script>
</body>
</html>`, handleText, accountIDText, oauthState.userID)
}

func (s *HTTPServer) handleInstagramWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		mode := r.URL.Query().Get("hub.mode")
		token := r.URL.Query().Get("hub.verify_token")
		challenge := r.URL.Query().Get("hub.challenge")

		if mode == "subscribe" && token == s.cfg.InstagramWebhookSecret && challenge != "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(challenge))
			return
		}

		http.Error(w, "Forbidden: invalid hub.verify_token", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodPost {
		rawPayload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed reading webhook body", http.StatusBadRequest)
			return
		}

		sigHeader := r.Header.Get("X-Hub-Signature-256")
		if s.cfg.InstagramWebhookSecret != "" {
			if err := instagram.VerifyWebhookSignature(rawPayload, sigHeader, s.cfg.InstagramWebhookSecret); err != nil {
				http.Error(w, "Unauthorized: invalid signature", http.StatusUnauthorized)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"event_received"}`))
		return
	}

	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}
