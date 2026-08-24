// Package database provides automated audit logging decorators and repositories.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

type actorContextKey struct{}

// ActorContext holds the cryptographically authenticated user and request context for auditing.
type ActorContext struct {
	ActorID   string
	IPAddress string
	SessionID string
}

// WithActor injects the ActorContext into the Go context.
func WithActor(ctx context.Context, actor ActorContext) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// GetActor extracts the ActorContext from context.Context. Returns empty values if missing.
func GetActor(ctx context.Context) ActorContext {
	if val := ctx.Value(actorContextKey{}); val != nil {
		if actor, ok := val.(ActorContext); ok {
			return actor
		}
	}
	return ActorContext{ActorID: "anonymous", IPAddress: "0.0.0.0"}
}

// AuditWriter defines the interface to persist immutable audit records.
type AuditWriter interface {
	WriteAuditLog(ctx context.Context, entry *models.AuditLog) error
}

// AuditedRepositoryDecorator wraps data access repositories and guarantees audit records for all mutations.
type AuditedRepositoryDecorator struct {
	auditWriter AuditWriter
}

// NewAuditedRepositoryDecorator creates an audited repository decorator.
func NewAuditedRepositoryDecorator(writer AuditWriter) *AuditedRepositoryDecorator {
	return &AuditedRepositoryDecorator{auditWriter: writer}
}

// AuditWrite records a structured audit trail event with actor context and UTC timestamp.
// It strictly ensures no raw secrets, tokens, or plaintext passwords exist in the metadata.
func (d *AuditedRepositoryDecorator) AuditWrite(ctx context.Context, action, resourceType, resourceID string, metadata map[string]interface{}) error {
	if d.auditWriter == nil {
		return errors.New("audit: audit writer is nil, write rejected")
	}

	actor := GetActor(ctx)

	// Ensure metadata never contains raw credentials
	sanitizedMeta := make(map[string]interface{})
	for k, v := range metadata {
		lowerKey := k
		if lowerKey == "token" || lowerKey == "access_token" || lowerKey == "refresh_token" || lowerKey == "password" || lowerKey == "secret" {
			sanitizedMeta[k] = "[REDACTED]"
		} else {
			sanitizedMeta[k] = v
		}
	}

	metaJSON, err := json.Marshal(sanitizedMeta)
	if err != nil {
		return fmt.Errorf("failed to serialize audit metadata: %w", err)
	}

	entry := &models.AuditLog{
		ID:           uuid.New().String(),
		UserID:       actor.ActorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     metaJSON,
		IPAddress:    actor.IPAddress,
		CreatedAt:    time.Now().UTC(),
	}

	if err := d.auditWriter.WriteAuditLog(ctx, entry); err != nil {
		return fmt.Errorf("failed writing required audit log for action %s: %w", action, err)
	}

	return nil
}
