// Package database provides production-grade parameterized PostgreSQL repositories.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("database: record not found")
	// ErrUnauthorizedAccess is returned when a user attempts to access another user's resources.
	ErrUnauthorizedAccess = errors.New("database: unauthorized cross-user resource access")
)

// Repository encapsulates all database queries and audited data operations.
type Repository struct {
	db          *sql.DB
	cryptoKey   []byte
	auditWriter AuditWriter
	decorator   *AuditedRepositoryDecorator
}

// NewRepository initializes a Repository with database handle, 32-byte encryption key, and audit decorator.
func NewRepository(db *sql.DB, cryptoKey []byte, auditWriter AuditWriter) *Repository {
	return &Repository{
		db:          db,
		cryptoKey:   cryptoKey,
		auditWriter: auditWriter,
		decorator:   NewAuditedRepositoryDecorator(auditWriter),
	}
}

// ============================================================================
// AUDIT LOG REPOSITORY
// ============================================================================

// WriteAuditLog inserts an immutable audit trail entry.
func (r *Repository) WriteAuditLog(ctx context.Context, entry *models.AuditLog) error {
	query := `
		INSERT INTO audit_logs (id, user_id, action, resource_type, resource_id, metadata, ip_address, created_at)
		VALUES ($1, NULLIF($2, 'anonymous')::uuid, $3, $4, $5, $6, $7, $8);
	`
	_, err := r.db.ExecContext(ctx, query,
		entry.ID, entry.UserID, entry.Action, entry.ResourceType, entry.ResourceID,
		entry.Metadata, entry.IPAddress, entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed inserting audit log: %w", err)
	}
	return nil
}

// ============================================================================
// USER & SESSION REPOSITORY
// ============================================================================

// CreateUser inserts a new user record.
func (r *Repository) CreateUser(ctx context.Context, email, username string) (*models.User, error) {
	now := time.Now().UTC()
	user := &models.User{
		ID:        uuid.New().String(),
		Email:     email,
		Username:  username,
		CreatedAt: now,
		UpdatedAt: now,
	}

	query := `
		INSERT INTO users (id, email, username, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5);
	`
	if _, err := r.db.ExecContext(ctx, query, user.ID, user.Email, user.Username, user.CreatedAt, user.UpdatedAt); err != nil {
		return nil, fmt.Errorf("failed creating user: %w", err)
	}

	_ = r.decorator.AuditWrite(ctx, "USER_CREATED", "user", user.ID, map[string]interface{}{
		"email":    email,
		"username": username,
	})

	return user, nil
}

// GetUserByID retrieves a user by their UUID.
func (r *Repository) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	query := `SELECT id, email, username, created_at, updated_at FROM users WHERE id = $1;`
	row := r.db.QueryRowContext(ctx, query, userID)

	var u models.User
	if err := row.Scan(&u.ID, &u.Email, &u.Username, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed fetching user by id: %w", err)
	}
	return &u, nil
}

// GetOrCreateUserByUsername retrieves an existing user by username or creates a new one.
func (r *Repository) GetOrCreateUserByUsername(ctx context.Context, username, email string) (*models.User, error) {
	if email == "" {
		email = fmt.Sprintf("%s@example.com", username)
	}

	query := `SELECT id, email, username, created_at, updated_at FROM users WHERE username = $1;`
	row := r.db.QueryRowContext(ctx, query, username)
	var u models.User
	if err := row.Scan(&u.ID, &u.Email, &u.Username, &u.CreatedAt, &u.UpdatedAt); err == nil {
		return &u, nil
	}

	return r.CreateUser(ctx, email, username)
}

// StoreUserSession persists a hashed refresh token session with UTC timestamps.
func (r *Repository) StoreUserSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error {
	now := time.Now().UTC()
	query := `
		INSERT INTO user_sessions (refresh_token_hash, user_id, expires_at, created_at, is_revoked)
		VALUES ($1, $2, $3, $4, FALSE);
	`
	if _, err := r.db.ExecContext(ctx, query, tokenHash, userID, expiresAt.UTC(), now); err != nil {
		return fmt.Errorf("failed storing user session: %w", err)
	}

	prefix := tokenHash
	if len(prefix) > 8 {
		prefix = prefix[:8] + "..."
	}

	return r.decorator.AuditWrite(ctx, "SESSION_CREATED", "user_session", prefix, map[string]interface{}{
		"user_id":    userID,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

// RevokeUserSessionByHash marks a session as revoked.
func (r *Repository) RevokeUserSessionByHash(ctx context.Context, tokenHash string) error {
	query := `UPDATE user_sessions SET is_revoked = TRUE WHERE refresh_token_hash = $1;`
	res, err := r.db.ExecContext(ctx, query, tokenHash)
	if err != nil {
		return fmt.Errorf("failed revoking user session: %w", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}

	prefix := tokenHash
	if len(prefix) > 8 {
		prefix = prefix[:8] + "..."
	}

	return r.decorator.AuditWrite(ctx, "SESSION_REVOKED", "user_session", prefix, nil)
}

// ============================================================================
// PLATFORM CONNECTION REPOSITORY (TRANSPARENT AES ENCRYPTION/DECRYPTION)
// ============================================================================

// SavePlatformConnection encrypts raw OAuth tokens and upserts the platform connection.
func (r *Repository) SavePlatformConnection(ctx context.Context, userID, platform string, rawAccessToken, rawRefreshToken []byte, tokenExpiresAt time.Time, scopes []string) error {
	if !models.IsValidPlatform(platform) {
		return models.ErrInvalidPlatform
	}

	// Encrypt tokens before writing to database
	encAccess, err := crypto.EncryptOAuthToken(rawAccessToken, r.cryptoKey)
	if err != nil {
		return fmt.Errorf("failed encrypting access token: %w", err)
	}

	var encRefresh []byte
	if len(rawRefreshToken) > 0 {
		encRefresh, err = crypto.EncryptOAuthToken(rawRefreshToken, r.cryptoKey)
		if err != nil {
			return fmt.Errorf("failed encrypting refresh token: %w", err)
		}
	}

	now := time.Now().UTC()
	scopesArray := models.StringArray(scopes)

	query := `
		INSERT INTO platform_connections (id, user_id, platform, encrypted_access_token, encrypted_refresh_token, token_expires_at, scopes, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, $8, $8)
		ON CONFLICT (user_id, platform) DO UPDATE SET
			encrypted_access_token = EXCLUDED.encrypted_access_token,
			encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
			token_expires_at = EXCLUDED.token_expires_at,
			scopes = EXCLUDED.scopes,
			is_active = TRUE,
			updated_at = EXCLUDED.updated_at;
	`
	id := uuid.New().String()
	_, err = r.db.ExecContext(ctx, query, id, userID, platform, encAccess, encRefresh, tokenExpiresAt.UTC(), scopesArray, now)
	if err != nil {
		return fmt.Errorf("failed upserting platform connection: %w", err)
	}

	return r.decorator.AuditWrite(ctx, "PLATFORM_CONNECTED", "platform_connection", fmt.Sprintf("%s:%s", userID, platform), map[string]interface{}{
		"platform": platform,
		"scopes":   scopes,
	})
}

// GetDecryptedPlatformConnection retrieves the platform connection and decrypts the OAuth credentials.
func (r *Repository) GetDecryptedPlatformConnection(ctx context.Context, userID, platform string) (decryptedAccess, decryptedRefresh []byte, expiresAt time.Time, scopes []string, err error) {
	query := `
		SELECT encrypted_access_token, encrypted_refresh_token, token_expires_at, scopes, is_active
		FROM platform_connections
		WHERE user_id = $1 AND platform = $2;
	`
	row := r.db.QueryRowContext(ctx, query, userID, platform)

	var encAccess, encRefresh []byte
	var exp time.Time
	var scps models.StringArray
	var isActive bool

	if err := row.Scan(&encAccess, &encRefresh, &exp, &scps, &isActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, time.Time{}, nil, ErrNotFound
		}
		return nil, nil, time.Time{}, nil, fmt.Errorf("failed scanning platform connection: %w", err)
	}

	if !isActive {
		return nil, nil, time.Time{}, nil, errors.New("platform connection is inactive")
	}

	decAccess, err := crypto.DecryptOAuthToken(encAccess, r.cryptoKey)
	if err != nil {
		return nil, nil, time.Time{}, nil, fmt.Errorf("failed decrypting stored access token: %w", err)
	}

	var decRefresh []byte
	if len(encRefresh) > 0 {
		decRefresh, err = crypto.DecryptOAuthToken(encRefresh, r.cryptoKey)
		if err != nil {
			return nil, nil, time.Time{}, nil, fmt.Errorf("failed decrypting stored refresh token: %w", err)
		}
	}

	return decAccess, decRefresh, exp, []string(scps), nil
}

// RevokePlatformConnection disables the connection and clears encrypted tokens.
func (r *Repository) RevokePlatformConnection(ctx context.Context, userID, platform string) error {
	now := time.Now().UTC()
	query := `
		UPDATE platform_connections
		SET is_active = FALSE, encrypted_access_token = '\x'::bytea, encrypted_refresh_token = NULL, updated_at = $3
		WHERE user_id = $1 AND platform = $2;
	`
	res, err := r.db.ExecContext(ctx, query, userID, platform, now)
	if err != nil {
		return fmt.Errorf("failed revoking platform connection: %w", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return r.decorator.AuditWrite(ctx, "PLATFORM_DISCONNECTED", "platform_connection", fmt.Sprintf("%s:%s", userID, platform), map[string]interface{}{
		"platform": platform,
	})
}
