// Package auth provides JWT session issuance, signature verification, and SHA-256 hashed refresh token rotation.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// AccessTokenTTL defines the lifespan of an access token (15 minutes).
	AccessTokenTTL = 15 * time.Minute
	// RefreshTokenTTL defines the lifespan of a refresh token (7 days).
	RefreshTokenTTL = 7 * 24 * time.Hour
	// RefreshTokenRandomBytes defines the entropy size (32 bytes = 256 bits).
	RefreshTokenRandomBytes = 32
)

var (
	// ErrInvalidTokenFormat is returned when a JWT does not conform to header.payload.signature format.
	ErrInvalidTokenFormat = errors.New("auth: invalid JWT token format")
	// ErrInvalidAlgorithm is returned when a token uses an unsupported signing algorithm.
	ErrInvalidAlgorithm = errors.New("auth: unsupported signing algorithm, only HS256 is accepted")
	// ErrInvalidSignature is returned when the HMAC signature verification fails.
	ErrInvalidSignature = errors.New("auth: invalid token signature")
	// ErrTokenExpired is returned when the access token has expired.
	ErrTokenExpired = errors.New("auth: token has expired")
	// ErrInvalidOrRevokedRefreshToken is returned when a refresh token is unknown, expired, or already revoked.
	ErrInvalidOrRevokedRefreshToken = errors.New("auth: refresh token is invalid, expired, or has already been revoked")
	// ErrEmptyUserID is returned when attempting to generate a session without a user ID.
	ErrEmptyUserID = errors.New("auth: user_id cannot be empty")
)

// SessionClaims represents the cryptographically verified session payload.
type SessionClaims struct {
	UserID    string `json:"sub"`
	Role      string `json:"role"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// TokenPair contains the newly generated access token, raw refresh token, and its SHA-256 hash.
type TokenPair struct {
	AccessToken            string    `json:"access_token"`
	RefreshToken           string    `json:"refresh_token"`
	RefreshTokenHash       string    `json:"-"` // Never exposed in JSON response
	AccessTokenExpiresAt   time.Time `json:"access_token_expires_at"`
	RefreshTokenExpiresAt  time.Time `json:"refresh_token_expires_at"`
}

// UserSession represents a stored session record in the database or store.
type UserSession struct {
	RefreshTokenHash string
	UserID           string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	IsRevoked        bool
}

// SessionStore defines the interface required to persist and validate refresh token sessions.
type SessionStore interface {
	StoreSession(ctx context.Context, session *UserSession) error
	GetSessionByHash(ctx context.Context, hash string) (*UserSession, error)
	RevokeSessionByHash(ctx context.Context, hash string) error
}

// InMemorySessionStore provides a thread-safe in-memory implementation of SessionStore for testing and standalone operation.
type InMemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*UserSession
}

// NewInMemorySessionStore creates a new in-memory session store.
func NewInMemorySessionStore() *InMemorySessionStore {
	return &InMemorySessionStore{
		sessions: make(map[string]*UserSession),
	}
}

// StoreSession persists a session in memory.
func (s *InMemorySessionStore) StoreSession(_ context.Context, session *UserSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.RefreshTokenHash] = session
	return nil
}

// GetSessionByHash retrieves a session by its SHA-256 hash.
func (s *InMemorySessionStore) GetSessionByHash(_ context.Context, hash string) (*UserSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[hash]
	if !ok {
		return nil, nil
	}
	// Return a copy to prevent data races
	cpy := *sess
	return &cpy, nil
}

// RevokeSessionByHash marks a session as revoked.
func (s *InMemorySessionStore) RevokeSessionByHash(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[hash]; ok {
		sess.IsRevoked = true
	}
	return nil
}

// HashRefreshToken calculates the deterministic SHA-256 hex string of a raw refresh token.
func HashRefreshToken(rawRefreshToken string) string {
	hash := sha256.Sum256([]byte(rawRefreshToken))
	return hex.EncodeToString(hash[:])
}

// IssueSessionTokens generates a signed access JWT and a cryptographically random refresh token.
func IssueSessionTokens(userID, role string, signingSecret []byte) (*TokenPair, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, ErrEmptyUserID
	}

	now := time.Now().UTC()
	accessExpiry := now.Add(AccessTokenTTL)
	refreshExpiry := now.Add(RefreshTokenTTL)

	// Build Access Token (JWT HS256)
	claims := SessionClaims{
		UserID:    userID,
		Role:      role,
		IssuedAt:  now.Unix(),
		ExpiresAt: accessExpiry.Unix(),
	}

	accessToken, err := signJWT(claims, signingSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access JWT: %w", err)
	}

	// Generate 32 bytes cryptographically secure random refresh token
	rawBytes := make([]byte, RefreshTokenRandomBytes)
	if _, err := rand.Read(rawBytes); err != nil {
		return nil, fmt.Errorf("failed to generate random refresh token bytes: %w", err)
	}
	rawRefreshToken := hex.EncodeToString(rawBytes)
	tokenHash := HashRefreshToken(rawRefreshToken)

	return &TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          rawRefreshToken,
		RefreshTokenHash:      tokenHash,
		AccessTokenExpiresAt:  accessExpiry,
		RefreshTokenExpiresAt: refreshExpiry,
	}, nil
}

// RotateRefreshToken validates the incoming refresh token, marks the previous token hash as revoked, and issues a new pair.
func RotateRefreshToken(ctx context.Context, oldRawRefreshToken string, store SessionStore, signingSecret []byte) (*TokenPair, error) {
	if strings.TrimSpace(oldRawRefreshToken) == "" {
		return nil, ErrInvalidOrRevokedRefreshToken
	}

	oldHash := HashRefreshToken(oldRawRefreshToken)
	session, err := store.GetSessionByHash(ctx, oldHash)
	if err != nil {
		return nil, fmt.Errorf("failed to query session store: %w", err)
	}

	now := time.Now().UTC()
	if session == nil || session.IsRevoked || session.ExpiresAt.Before(now) {
		return nil, ErrInvalidOrRevokedRefreshToken
	}

	// Immediately revoke the old session hash to prevent replay attacks
	if err := store.RevokeSessionByHash(ctx, oldHash); err != nil {
		return nil, fmt.Errorf("failed to revoke old session: %w", err)
	}

	// Issue a new token pair for the authenticated user
	newPair, err := IssueSessionTokens(session.UserID, "user", signingSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to issue rotated token pair: %w", err)
	}

	// Store the new session record
	newSession := &UserSession{
		RefreshTokenHash: newPair.RefreshTokenHash,
		UserID:           session.UserID,
		ExpiresAt:        newPair.RefreshTokenExpiresAt,
		CreatedAt:        now,
		IsRevoked:        false,
	}

	if err := store.StoreSession(ctx, newSession); err != nil {
		return nil, fmt.Errorf("failed to store new rotated session: %w", err)
	}

	return newPair, nil
}

// ValidateAccessToken parses, checks expiration, and verifies the HMAC-SHA256 signature of a JWT access token.
func ValidateAccessToken(tokenString string, signingSecret []byte) (*SessionClaims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidTokenFormat
	}

	headerSegment, payloadSegment, signatureSegment := parts[0], parts[1], parts[2]

	// 1. Verify Signature
	messageToSign := headerSegment + "." + payloadSegment
	expectedSig := computeHMACSHA256([]byte(messageToSign), signingSecret)
	expectedSigB64 := base64.RawURLEncoding.EncodeToString(expectedSig)

	if !hmac.Equal([]byte(signatureSegment), []byte(expectedSigB64)) {
		return nil, ErrInvalidSignature
	}

	// 2. Verify Header
	headerBytes, err := base64.RawURLEncoding.DecodeString(headerSegment)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT header: %w", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("failed to parse JWT header JSON: %w", err)
	}
	if header.Alg != "HS256" {
		return nil, ErrInvalidAlgorithm
	}

	// 3. Decode & Verify Claims
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadSegment)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims SessionClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims JSON: %w", err)
	}

	nowUnix := time.Now().UTC().Unix()
	if nowUnix > claims.ExpiresAt {
		return nil, ErrTokenExpired
	}

	if strings.TrimSpace(claims.UserID) == "" {
		return nil, ErrEmptyUserID
	}

	return &claims, nil
}

func signJWT(claims SessionClaims, secret []byte) (string, error) {
	header := struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{
		Alg: "HS256",
		Typ: "JWT",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	message := headerB64 + "." + payloadB64
	sig := computeHMACSHA256([]byte(message), secret)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return message + "." + sigB64, nil
}

func computeHMACSHA256(message, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(message)
	return h.Sum(nil)
}
