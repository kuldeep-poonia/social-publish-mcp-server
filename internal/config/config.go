// Package config provides centralized environment configuration loading and validation.
package config

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
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

	// Database (Postgres) - Dual Support: Single URL or Separate Fields
	DatabaseURL      string // Single connection URL (e.g., DATABASE_URL on Render / Supabase / Neon)
	PostgresHost     string
	PostgresPort     int
	PostgresUser     string
	PostgresPassword string
	PostgresDB       string
	PostgresSSLMode  string

	// Redis - Dual Support: Single URL or Separate Fields
	RedisURL      string // Single connection URL (e.g., REDIS_URL on Render / Upstash)
	RedisHost     string
	RedisPort     int
	RedisPassword string

	// Security & Cryptography
	TokenEncryptionKey []byte // Exactly 32 bytes for AES-256-GCM (OAuth Vault)
	QueueEncryptionKey []byte // Exactly 32 bytes for AES-256-GCM (Queue Payload Security)
	JWTSigningSecret   []byte // Min 32 bytes for HMAC-SHA256
	MetricsBearerToken string // Static Bearer token required for scraping /metrics

	// Queue & Reliability Engine
	QueueMaxRetries          int  // Maximum retry attempts for transient errors (default: 5)
	QueueMaxDeliveryAttempts int  // Maximum worker deliveries before poison-message DLQ diversion (default: 5)
	QueueWorkers             int  // Number of concurrent queue worker goroutines (default: 4)
	RateLimitFailClosed      bool // Fails closed (blocks) if Redis unreachable (default: true)

	// Social Platform OAuth Credentials
	TwitterClientID        string
	TwitterClientSecret    string
	TwitterRedirectURI     string
	YouTubeClientID        string
	YouTubeClientSecret    string
	YouTubeRedirectURI     string
	InstagramClientID      string
	InstagramClientSecret  string
	InstagramRedirectURI   string
	InstagramWebhookSecret string
	PublicBaseURL          string
	GeminiAPIKey           string
}

// LoadConfig reads configuration from environment variables and validates critical constraints.
func LoadConfig() (*Config, error) {
	// Load .env file automatically if present
	loadDotEnv(".env", "../.env", "../../.env")

	cfg := &Config{
		ServerPort:               getEnvAsInt("SERVER_PORT", 8080),
		ServerHost:               getEnv("SERVER_HOST", "0.0.0.0"),
		Environment:              getEnv("ENVIRONMENT", "development"),
		DatabaseURL:              getEnv("DATABASE_URL", ""),
		PostgresHost:             getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:             getEnvAsInt("POSTGRES_PORT", 5432),
		PostgresUser:             getEnv("POSTGRES_USER", "postgres"),
		PostgresPassword:         getEnv("POSTGRES_PASSWORD", "postgres_secure_local_dev"),
		PostgresDB:               getEnv("POSTGRES_DB", "social_mcp_db"),
		PostgresSSLMode:          getEnv("POSTGRES_SSLMODE", "disable"),
		RedisURL:                 getEnv("REDIS_URL", ""),
		RedisHost:                getEnv("REDIS_HOST", "localhost"),
		RedisPort:                getEnvAsInt("REDIS_PORT", 6379),
		RedisPassword:            getEnv("REDIS_PASSWORD", ""),
		QueueMaxRetries:          getEnvAsInt("QUEUE_MAX_RETRIES", 5),
		QueueMaxDeliveryAttempts: getEnvAsInt("QUEUE_MAX_DELIVERY_ATTEMPTS", 5),
		QueueWorkers:             getEnvAsInt("QUEUE_WORKERS", 4),
		RateLimitFailClosed:      getEnvAsBool("RATELIMIT_FAIL_CLOSED", true),
		TwitterClientID:          getEnv("TWITTER_CLIENT_ID", ""),
		TwitterClientSecret:      getEnv("TWITTER_CLIENT_SECRET", ""),
		TwitterRedirectURI:       getEnv("TWITTER_REDIRECT_URI", ""),
		YouTubeClientID:          getEnv("YOUTUBE_CLIENT_ID", ""),
		YouTubeClientSecret:      getEnv("YOUTUBE_CLIENT_SECRET", ""),
		YouTubeRedirectURI:       getEnv("YOUTUBE_REDIRECT_URI", ""),
		InstagramClientID:        getEnv("INSTAGRAM_CLIENT_ID", ""),
		InstagramClientSecret:    getEnv("INSTAGRAM_CLIENT_SECRET", ""),
		InstagramRedirectURI:     getEnv("INSTAGRAM_REDIRECT_URI", ""),
		InstagramWebhookSecret:   getEnv("INSTAGRAM_WEBHOOK_SECRET", ""),
		PublicBaseURL:            getEnv("PUBLIC_BASE_URL", "https://social-mcp.duckdns.org"),
		GeminiAPIKey:             getEnv("GEMINI_API_KEY", ""),
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

	// Validate or derive dedicated Queue Payload Encryption Key (Defense-in-depth separation)
	rawQueueKeyHex := os.Getenv("QUEUE_ENCRYPTION_KEY")
	if rawQueueKeyHex != "" {
		decQueueKey, qErr := hex.DecodeString(rawQueueKeyHex)
		if qErr != nil || len(decQueueKey) != 32 {
			return nil, fmt.Errorf("QUEUE_ENCRYPTION_KEY must be exactly 32 bytes hex-encoded: %w", qErr)
		}
		cfg.QueueEncryptionKey = decQueueKey
	} else {
		// Cryptographically derive separate isolated 32-byte key from TokenEncryptionKey using domain separation
		derivedKey := deriveQueueKey(cfg.TokenEncryptionKey)
		cfg.QueueEncryptionKey = derivedKey
	}

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

	// Configure Metrics Bearer Token for Prometheus scraping
	metricsToken := os.Getenv("METRICS_BEARER_TOKEN")
	if metricsToken == "" {
		if cfg.Environment == "development" {
			metricsToken = "local_dev_metrics_token_prometheus_12345"
		} else {
			// In production, derive deterministic secure bearer token from JWT secret if not explicitly provided
			metricsToken = hex.EncodeToString(deriveQueueKey(cfg.JWTSigningSecret))[:32]
		}
	}
	cfg.MetricsBearerToken = metricsToken

	return cfg, nil
}

// PostgresDSN returns the standard PostgreSQL connection string for the configured parameters.
// If DATABASE_URL is provided, it is returned with pooler compatibility settings (disabling statement cache for Supabase/PgBouncer).
func (c *Config) PostgresDSN() string {
	if c.DatabaseURL != "" {
		dsn := c.DatabaseURL
		if !strings.Contains(dsn, "default_query_exec_mode=") && !strings.Contains(dsn, "statement_cache_capacity=") {
			if strings.Contains(dsn, "?") {
				dsn += "&default_query_exec_mode=simple_protocol&statement_cache_capacity=0"
			} else {
				dsn += "?default_query_exec_mode=simple_protocol&statement_cache_capacity=0"
			}
		}
		return dsn
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s default_query_exec_mode=simple_protocol statement_cache_capacity=0",
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

func loadDotEnv(filenames ...string) {
	for _, fn := range filenames {
		file, err := os.Open(fn)
		if err != nil {
			continue
		}
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
				if key != "" && val != "" && os.Getenv(key) == "" {
					_ = os.Setenv(key, val)
				}
			}
		}
		_ = file.Close()
		return
	}
}

func getEnvAsBool(key string, fallback bool) bool {
	valStr := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if valStr == "" {
		return fallback
	}
	return valStr == "true" || valStr == "1" || valStr == "yes"
}

// deriveQueueKey derives a cryptographically isolated 32-byte key for queue encryption using HMAC-SHA256.
func deriveQueueKey(tokenKey []byte) []byte {
	h := hmac.New(sha256.New, tokenKey)
	h.Write([]byte("social_mcp_queue_payload_v1_domain_separation"))
	sum := h.Sum(nil)
	key := make([]byte, 32)
	copy(key, sum[:32])
	return key
}
