package config

import (
	"os"
	"testing"
)

func TestLoadConfig_ValidDevelopmentDefaults(t *testing.T) {
	os.Unsetenv("TOKEN_ENCRYPTION_KEY")
	os.Unsetenv("JWT_SIGNING_SECRET")
	os.Setenv("ENVIRONMENT", "development")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("expected successful config load with dev defaults, got error: %v", err)
	}

	if len(cfg.TokenEncryptionKey) != 32 {
		t.Fatalf("expected 32-byte encryption key, got %d", len(cfg.TokenEncryptionKey))
	}
	if len(cfg.JWTSigningSecret) < 32 {
		t.Fatalf("expected >= 32-byte JWT secret, got %d", len(cfg.JWTSigningSecret))
	}
}

func TestLoadConfig_InvalidHexKey(t *testing.T) {
	os.Setenv("TOKEN_ENCRYPTION_KEY", "not-hex-characters-invalid")
	defer os.Unsetenv("TOKEN_ENCRYPTION_KEY")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for invalid hex key, got nil")
	}
}

func TestLoadConfig_InvalidKeyLength(t *testing.T) {
	// 16 bytes hex (32 characters) instead of 32 bytes (64 characters)
	os.Setenv("TOKEN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("TOKEN_ENCRYPTION_KEY")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for 16-byte key (needs 32 bytes), got nil")
	}
}

func TestLoadConfig_ShortJWTSecret(t *testing.T) {
	os.Setenv("TOKEN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	os.Setenv("JWT_SIGNING_SECRET", "short_secret")
	defer os.Unsetenv("TOKEN_ENCRYPTION_KEY")
	defer os.Unsetenv("JWT_SIGNING_SECRET")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for short JWT secret (< 32 chars), got nil")
	}
}
