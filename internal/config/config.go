// Package config provides centralized environment configuration loading and validation.
package config

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all server configuration variables.
type Config struct {
	// Server
	ServerPort  int
	ServerHost  string
	Environment string

	// Database (Postgres)
	PostgresHost     string
	PostgresPort     int
	PostgresUser     string
	PostgresPassword string
	PostgresDB       string
	PostgresSSLMode  string

	// Redis
	RedisHost     string
	RedisPort     int
	RedisPassword string

	// Security & Cryptography
	TokenEncryptionKey []byte // Exactly 32 bytes for AES-256-GCM
	JWTSigningSecret   []byte // Min 32 bytes for HMAC-SHA256

	// Social Platform OAuth Credentials
	TwitterClientID       string
	TwitterClientSecret   string
	YouTubeClientID       string
	YouTubeClientSecret   string
	InstagramClientID     string
	InstagramClientSecret string
}

// LoadConfig reads configuration from environment variables and validates critical constraints.
func LoadConfig() (*Config, error) {
	// Load .env file automatically if present
	loadDotEnv(".env")
	loadDotEnv("../.env")
	loadDotEnv("../../.env")

	cfg := &Config{
		ServerPort:            getEnvAsInt("SERVER_PORT", 8080),
		ServerHost:            getEnv("SERVER_HOST", "0.0.0.0"),
		Environment:           getEnv("ENVIRONMENT", "development"),
		PostgresHost:          getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:          getEnvAsInt("POSTGRES_PORT", 5432),
		PostgresUser:          getEnv("POSTGRES_USER", "postgres"),
		PostgresPassword:      getEnv("POSTGRES_PASSWORD", "postgres_secure_local_dev"),
		PostgresDB:            getEnv("POSTGRES_DB", "social_mcp_db"),
		PostgresSSLMode:       getEnv("POSTGRES_SSLMODE", "disable"),
		RedisHost:             getEnv("REDIS_HOST", "localhost"),
		RedisPort:             getEnvAsInt("REDIS_PORT", 6379),
		RedisPassword:         getEnv("REDIS_PASSWORD", ""),
		TwitterClientID:       getEnv("TWITTER_CLIENT_ID", ""),
		TwitterClientSecret:   getEnv("TWITTER_CLIENT_SECRET", ""),
		YouTubeClientID:       getEnv("YOUTUBE_CLIENT_ID", ""),
		YouTubeClientSecret:   getEnv("YOUTUBE_CLIENT_SECRET", ""),
		InstagramClientID:     getEnv("INSTAGRAM_CLIENT_ID", ""),
		InstagramClientSecret: getEnv("INSTAGRAM_CLIENT_SECRET", ""),
	}

	// Validate and decode Token Encryption Key
	rawKeyHex := os.Getenv("TOKEN_ENCRYPTION_KEY")
	if rawKeyHex == "" {
		// Use development fallback only if explicitly in development mode
		if cfg.Environment == "development" {
			rawKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		} else {
			return nil, fmt.Errorf("mandatory environment variable TOKEN_ENCRYPTION_KEY is missing")
		}
	}

	decodedKey, err := hex.DecodeString(rawKeyHex)
	if err != nil {
		return nil, fmt.Errorf("TOKEN_ENCRYPTION_KEY must be a valid hex-encoded string: %w", err)
	}
	if len(decodedKey) != 32 {
		return nil, fmt.Errorf("TOKEN_ENCRYPTION_KEY must be exactly 32 bytes (64 hex characters) for AES-256, got %d bytes", len(decodedKey))
	}
	cfg.TokenEncryptionKey = decodedKey

	// Validate JWT Signing Secret
	jwtSecret := os.Getenv("JWT_SIGNING_SECRET")
	if jwtSecret == "" {
		if cfg.Environment == "development" {
			jwtSecret = "local_dev_jwt_secret_change_in_production_min_32_chars"
		} else {
			return nil, fmt.Errorf("mandatory environment variable JWT_SIGNING_SECRET is missing")
		}
	}
	if len(jwtSecret) < 32 {
		return nil, fmt.Errorf("JWT_SIGNING_SECRET must be at least 32 characters long for secure HMAC-SHA256, got %d", len(jwtSecret))
	}
	cfg.JWTSigningSecret = []byte(jwtSecret)

	return cfg, nil
}

// PostgresDSN returns the standard PostgreSQL connection string for the configured parameters.
func (c *Config) PostgresDSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.PostgresHost, c.PostgresPort, c.PostgresUser, c.PostgresPassword, c.PostgresDB, c.PostgresSSLMode)
}

// RedisAddr returns the standard host:port address for Redis.
func (c *Config) RedisAddr() string {
	return fmt.Sprintf("%s:%d", c.RedisHost, c.RedisPort)
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return fallback
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return fallback
	}
	return val
}

func loadDotEnv(filepath string) {
	file, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
}
