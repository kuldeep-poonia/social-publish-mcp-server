package database

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

type MockAuditWriter struct {
	mu      sync.Mutex
	entries []*models.AuditLog
}

func (m *MockAuditWriter) WriteAuditLog(_ context.Context, entry *models.AuditLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func TestAuditedRepositoryDecorator_AutomaticWriteAuditing(t *testing.T) {
	writer := &MockAuditWriter{}
	decorator := NewAuditedRepositoryDecorator(writer)

	ctx := WithActor(context.Background(), ActorContext{
		ActorID:   "usr_audit_test_123",
		IPAddress: "192.168.1.50",
		SessionID: "sess_xyz_789",
	})

	metadata := map[string]interface{}{
		"platform": "twitter",
		"scopes":   []string{"tweet.read", "tweet.write"},
	}

	err := decorator.AuditWrite(ctx, "PLATFORM_CONNECTED", "platform_connection", "conn_abc", metadata)
	if err != nil {
		t.Fatalf("AuditWrite failed: %v", err)
	}

	if len(writer.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(writer.entries))
	}

	entry := writer.entries[0]
	if entry.UserID != "usr_audit_test_123" {
		t.Errorf("expected UserID usr_audit_test_123, got %s", entry.UserID)
	}
	if entry.IPAddress != "192.168.1.50" {
		t.Errorf("expected IPAddress 192.168.1.50, got %s", entry.IPAddress)
	}
	if entry.Action != "PLATFORM_CONNECTED" {
		t.Errorf("expected Action PLATFORM_CONNECTED, got %s", entry.Action)
	}
}

func TestAuditedRepositoryDecorator_SecretRedaction(t *testing.T) {
	writer := &MockAuditWriter{}
	decorator := NewAuditedRepositoryDecorator(writer)
	ctx := context.Background()

	sensitiveMetadata := map[string]interface{}{
		"access_token":  "super-secret-oauth2-token",
		"refresh_token": "super-secret-refresh-token",
		"password":      "plaintext-password",
		"secret":        "client-secret-123",
		"public_field":  "safe_data",
	}

	err := decorator.AuditWrite(ctx, "TOKEN_ROTATED", "token", "res_123", sensitiveMetadata)
	if err != nil {
		t.Fatalf("AuditWrite failed: %v", err)
	}

	entry := writer.entries[0]
	var parsedMeta map[string]string
	if err := json.Unmarshal(entry.Metadata, &parsedMeta); err != nil {
		t.Fatalf("failed parsing metadata JSON: %v", err)
	}

	if parsedMeta["access_token"] != "[REDACTED]" {
		t.Errorf("expected access_token to be [REDACTED], got %s", parsedMeta["access_token"])
	}
	if parsedMeta["refresh_token"] != "[REDACTED]" {
		t.Errorf("expected refresh_token to be [REDACTED], got %s", parsedMeta["refresh_token"])
	}
	if parsedMeta["password"] != "[REDACTED]" {
		t.Errorf("expected password to be [REDACTED], got %s", parsedMeta["password"])
	}
	if parsedMeta["public_field"] != "safe_data" {
		t.Errorf("expected public_field to remain unchanged, got %s", parsedMeta["public_field"])
	}
}

func TestUTCTimestampEnforcement(t *testing.T) {
	writer := &MockAuditWriter{}
	decorator := NewAuditedRepositoryDecorator(writer)

	ctx := context.Background()
	err := decorator.AuditWrite(ctx, "TEST_ACTION", "test", "id_1", nil)
	if err != nil {
		t.Fatalf("AuditWrite failed: %v", err)
	}

	entry := writer.entries[0]
	if entry.CreatedAt.Location() != time.UTC {
		t.Errorf("CRITICAL: Audit timestamp is not in UTC! Got: %v", entry.CreatedAt.Location())
	}
}
