// Package auth provides OAuth 2.1 authorization server capabilities with mandatory PKCE (S256) and replay protection.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// AuthCodeTTL defines the lifespan of an authorization code (60 seconds).
	AuthCodeTTL = 60 * time.Second
	// CodeChallengeMethodS256 defines the only permitted PKCE transformation method.
	CodeChallengeMethodS256 = "S256"
)

var (
	// ErrInvalidClient is returned when client_id is unknown or unregistered.
	ErrInvalidClient = errors.New("oauth2: invalid or unregistered client_id")
	// ErrInvalidRedirectURI is returned when redirect_uri does not strictly match the registered allowlist.
	ErrInvalidRedirectURI = errors.New("oauth2: redirect_uri not in allowed redirect URI list")
	// ErrUnsupportedResponseType is returned when response_type is not 'code'.
	ErrUnsupportedResponseType = errors.New("oauth2: unsupported response_type, only 'code' is allowed")
	// ErrPKCERequired is returned when code_challenge or code_challenge_method is missing.
	ErrPKCERequired = errors.New("oauth2: PKCE is mandatory; code_challenge and code_challenge_method must be provided")
	// ErrInvalidCodeChallengeMethod is returned when code_challenge_method is 'plain' or anything other than 'S256'.
	ErrInvalidCodeChallengeMethod = errors.New("oauth2: plain code_challenge_method is forbidden, only S256 is accepted")
	// ErrUnsupportedGrantType is returned when grant_type is not supported.
	ErrUnsupportedGrantType = errors.New("oauth2: unsupported grant_type")
	// ErrInvalidOrConsumedCode is returned when authorization code is invalid, expired, or already used.
	ErrInvalidOrConsumedCode = errors.New("oauth2: authorization code is invalid, expired, or has already been consumed")
	// ErrInvalidCodeVerifier is returned when PKCE code_verifier does not match code_challenge.
	ErrInvalidCodeVerifier = errors.New("oauth2: invalid PKCE code_verifier")
)

// OAuthClient represents a registered OAuth 2.1 client application.
type OAuthClient struct {
	ClientID             string
	ClientSecret         string
	AllowedRedirectURIs []string
	Name                 string
}

// AuthCodeRecord represents an issued authorization code with PKCE challenge.
type AuthCodeRecord struct {
	Code                string
	ClientID            string
	UserID              string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	ExpiresAt           time.Time
	IsConsumed          bool
}

// AuthorizeRequest contains query parameters for the /oauth/authorize endpoint.
type AuthorizeRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	UserID              string // Authenticated user initiating authorization
}

// TokenExchangeRequest contains parameters for the /oauth/token endpoint.
type TokenExchangeRequest struct {
	GrantType    string
	Code         string
	ClientID     string
	CodeVerifier string
	RedirectURI  string
	RefreshToken string
}

// OAuthServer manages OAuth 2.1 client registration, authorization codes, and token issuance.
type OAuthServer struct {
	mu            sync.RWMutex
	clients       map[string]*OAuthClient
	codes         map[string]*AuthCodeRecord
	signingSecret []byte
}

// NewOAuthServer initializes an OAuth 2.1 server.
func NewOAuthServer(signingSecret []byte) *OAuthServer {
	return &OAuthServer{
		clients:       make(map[string]*OAuthClient),
		codes:         make(map[string]*AuthCodeRecord),
		signingSecret: signingSecret,
	}
}

// RegisterClient registers a new OAuth client with strict redirect URI allowlist.
func (s *OAuthServer) RegisterClient(clientID, clientSecret, name string, allowedRedirectURIs []string) error {
	if strings.TrimSpace(clientID) == "" {
		return errors.New("oauth2: clientID cannot be empty")
	}
	if len(allowedRedirectURIs) == 0 {
		return errors.New("oauth2: at least one allowed redirect URI is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.clients[clientID] = &OAuthClient{
		ClientID:             clientID,
		ClientSecret:         clientSecret,
		AllowedRedirectURIs: allowedRedirectURIs,
		Name:                 name,
	}
	return nil
}

// Authorize validates authorization request, enforces mandatory PKCE S256, and issues a 60s authorization code.
func (s *OAuthServer) Authorize(req *AuthorizeRequest) (string, error) {
	if req.ResponseType != "code" {
		return "", ErrUnsupportedResponseType
	}

	s.mu.RLock()
	client, exists := s.clients[req.ClientID]
	s.mu.RUnlock()
	if !exists {
		return "", ErrInvalidClient
	}

	// Strict exact matching on redirect URI allowlist (no wildcards or partial matches)
	if !isRedirectURIAllowed(req.RedirectURI, client.AllowedRedirectURIs) {
		return "", ErrInvalidRedirectURI
	}

	// Mandatory PKCE check — plain is forbidden
	if strings.TrimSpace(req.CodeChallenge) == "" || strings.TrimSpace(req.CodeChallengeMethod) == "" {
		return "", ErrPKCERequired
	}
	if req.CodeChallengeMethod != CodeChallengeMethodS256 {
		return "", ErrInvalidCodeChallengeMethod
	}

	if strings.TrimSpace(req.UserID) == "" {
		return "", ErrEmptyUserID
	}

	// Generate high-entropy 32-byte authorization code
	codeBytes := make([]byte, 32)
	if _, err := rand.Read(codeBytes); err != nil {
		return "", fmt.Errorf("failed generating random code bytes: %w", err)
	}
	authCode := hex.EncodeToString(codeBytes)

	record := &AuthCodeRecord{
		Code:                authCode,
		ClientID:            req.ClientID,
		UserID:              req.UserID,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		Scope:               req.Scope,
		ExpiresAt:           time.Now().UTC().Add(AuthCodeTTL),
		IsConsumed:          false,
	}

	s.mu.Lock()
	s.codes[authCode] = record
	s.mu.Unlock()

	return authCode, nil
}

// ExchangeCodeForTokens verifies PKCE verifier, marks authorization code as consumed, and issues session tokens.
func (s *OAuthServer) ExchangeCodeForTokens(ctx context.Context, req *TokenExchangeRequest, store SessionStore) (*TokenPair, error) {
	if req.GrantType == "refresh_token" {
		return RotateRefreshToken(ctx, req.RefreshToken, store, s.signingSecret)
	}

	if req.GrantType != "authorization_code" {
		return nil, ErrUnsupportedGrantType
	}

	s.mu.Lock()
	record, exists := s.codes[req.Code]
	if !exists {
		s.mu.Unlock()
		return nil, ErrInvalidOrConsumedCode
	}

	// Check expiration or replay
	now := time.Now().UTC()
	if record.IsConsumed || record.ExpiresAt.Before(now) {
		// If replaying a consumed code, remove it and reject
		delete(s.codes, req.Code)
		s.mu.Unlock()
		return nil, ErrInvalidOrConsumedCode
	}

	// Validate client binding
	if record.ClientID != req.ClientID {
		s.mu.Unlock()
		return nil, ErrInvalidClient
	}

	// Validate redirect URI binding
	if record.RedirectURI != req.RedirectURI {
		s.mu.Unlock()
		return nil, ErrInvalidRedirectURI
	}

	// Validate PKCE S256 verifier
	if !ValidatePKCES256(req.CodeVerifier, record.CodeChallenge) {
		s.mu.Unlock()
		return nil, ErrInvalidCodeVerifier
	}

	// Single-use guarantee: consume immediately
	record.IsConsumed = true
	delete(s.codes, req.Code)
	userID := record.UserID
	s.mu.Unlock()

	// Issue token pair
	pair, err := IssueSessionTokens(userID, "user", s.signingSecret)
	if err != nil {
		return nil, fmt.Errorf("failed issuing session tokens: %w", err)
	}

	// Store refresh session record
	session := &UserSession{
		RefreshTokenHash: pair.RefreshTokenHash,
		UserID:           userID,
		ExpiresAt:        pair.RefreshTokenExpiresAt,
		CreatedAt:        now,
		IsRevoked:        false,
	}
	if err := store.StoreSession(ctx, session); err != nil {
		return nil, fmt.Errorf("failed storing new session: %w", err)
	}

	return pair, nil
}

// ValidatePKCES256 computes BASE64URL(SHA256(verifier)) and compares with challenge in constant time.
func ValidatePKCES256(codeVerifier, expectedChallenge string) bool {
	if strings.TrimSpace(codeVerifier) == "" || strings.TrimSpace(expectedChallenge) == "" {
		return false
	}
	h := sha256.Sum256([]byte(codeVerifier))
	computedChallenge := base64.RawURLEncoding.EncodeToString(h[:])
	return hmac.Equal([]byte(computedChallenge), []byte(expectedChallenge))
}

// GeneratePKCEPair helper generates a compliant random verifier and S256 challenge for testing and clients.
func GeneratePKCEPair() (verifier string, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

func isRedirectURIAllowed(target string, allowedList []string) bool {
	cleanTarget := strings.TrimSpace(target)
	for _, allowed := range allowedList {
		if cleanTarget == strings.TrimSpace(allowed) {
			return true
		}
	}
	return false
}
