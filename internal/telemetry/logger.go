// Package telemetry provides structured logging and dual-layer secret scrubbing.
package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// LogLevel defines the severity level of a log message.
type LogLevel string

const (
	LevelDebug LogLevel = "DEBUG"
	LevelInfo  LogLevel = "INFO"
	LevelWarn  LogLevel = "WARN"
	LevelError LogLevel = "ERROR"
)

// Sensitive field key denylist (Layer 1: Deterministic Denylist)
var sensitiveKeyDenylist = map[string]bool{
	"access_token":           true,
	"refresh_token":          true,
	"client_secret":          true,
	"token":                  true,
	"password":               true,
	"authorization":          true,
	"code_verifier":          true,
	"jwt_secret":             true,
	"jwt_signing_secret":     true,
	"token_encryption_key":   true,
	"queue_encryption_key":   true,
	"db_password":            true,
	"postgres_password":      true,
	"redis_password":         true,
	"webhook_secret":         true,
	"instagram_client_secret": true,
	"twitter_client_secret":  true,
	"youtube_client_secret":  true,
	"metrics_bearer_token":   true,
	"secret":                 true,
	"bearer":                 true,
	"bearer_token":           true,
	"private_key":            true,
}

// Token signature regex scanners (Layer 2: Heuristic Token Signature & Pattern Scanner)
var secretRegexes = []*regexp.Regexp{
	// Google OAuth tokens (ya29.*)
	regexp.MustCompile(`ya29\.[a-zA-Z0-9_\-]+`),
	// Meta / Instagram Graph API tokens (EAA...)
	regexp.MustCompile(`EAA[a-zA-Z0-9]{20,}`),
	// Twitter Bearer / App tokens (AAAA...)
	regexp.MustCompile(`AAAA[a-zA-Z0-9%_\-]{20,}`),
	// JWT Signatures (header.payload.signature)
	regexp.MustCompile(`eyJ[a-zA-Z0-9_\-]{8,}\.eyJ[a-zA-Z0-9_\-]{8,}\.[a-zA-Z0-9_\-]{8,}`),
	// Bearer Header strings in free text
	regexp.MustCompile(`(?i)Bearer\s+[a-zA-Z0-9_\-\.]{16,}`),
	// PEM formatted private keys
	regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+ PRIVATE KEY-----`),
	// URL query params containing access tokens (e.g. ?access_token=...)
	regexp.MustCompile(`(?i)(access_token|refresh_token|client_secret|code_verifier|token)=([a-zA-Z0-9_\-\.%]{8,})`),
	// Free text tokens with secret/password keywords (e.g. postgres_super_secure_production_password_to_be_masked)
	regexp.MustCompile(`(?i)[a-zA-Z0-9_]+_(?:password|secret|token|key)_[a-zA-Z0-9_]+`),
	// Key-value pairs in logs like password: XYZ or secret=XYZ
	regexp.MustCompile(`(?i)(?:password|client_secret|token_secret)[\s:=]+([a-zA-Z0-9_\-\.]{8,})`),
}

// StructuredLogEntry defines the standard JSON log format.
type StructuredLogEntry struct {
	Timestamp string                 `json:"timestamp"`
	Level     LogLevel               `json:"level"`
	Message   string                 `json:"message"`
	TraceID   string                 `json:"trace_id,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	Platform  string                 `json:"platform,omitempty"`
	PostID    string                 `json:"post_id,omitempty"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// ScrubbingWriter wraps an io.Writer and intercepts all raw byte streams with dual-layer scrubbing before write.
type ScrubbingWriter struct {
	out io.Writer
	mu  sync.Mutex
}

// NewScrubbingWriter creates a new ScrubbingWriter wrapping out.
func NewScrubbingWriter(out io.Writer) *ScrubbingWriter {
	if out == nil {
		out = os.Stdout
	}
	return &ScrubbingWriter{out: out}
}

func (w *ScrubbingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	scrubbed := ScrubString(string(p))
	_, err = w.out.Write([]byte(scrubbed))
	return len(p), err // Return original byte length to satisfy io.Writer contract
}

// Logger provides structured JSON logging with built-in zero-leak secret scrubbing.
type Logger struct {
	writer *ScrubbingWriter
	mu     sync.Mutex
}

// Global default logger instance.
var defaultLogger = NewLogger(os.Stdout)

// DefaultLogger returns the application default structured logger.
func DefaultLogger() *Logger {
	return defaultLogger
}

// NewLogger initializes a structured logger with scrubbed output.
func NewLogger(out io.Writer) *Logger {
	return &Logger{
		writer: NewScrubbingWriter(out),
	}
}

// Log writes a scrubbed structured log entry to the destination writer.
func (l *Logger) Log(level LogLevel, message string, traceID, userID, platform, postID string, fields map[string]interface{}) {
	entry := StructuredLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Message:   ScrubString(message),
		TraceID:   traceID,
		UserID:    userID,
		Platform:  platform,
		PostID:    postID,
		Fields:    ScrubMap(fields),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	data = append(data, '\n')
	_, _ = l.writer.Write(data)
}

// Info logs an informational message.
func (l *Logger) Info(message string, fields map[string]interface{}) {
	l.Log(LevelInfo, message, "", "", "", "", fields)
}

// Warn logs a warning message.
func (l *Logger) Warn(message string, fields map[string]interface{}) {
	l.Log(LevelWarn, message, "", "", "", "", fields)
}

// Error logs an error message.
func (l *Logger) Error(message string, fields map[string]interface{}) {
	l.Log(LevelError, message, "", "", "", "", fields)
}

// ScrubString applies Layer 2 regex pattern scrubbing to a raw string.
func ScrubString(input string) string {
	if input == "" {
		return ""
	}

	result := input
	for _, reg := range secretRegexes {
		if strings.Contains(reg.String(), "access_token|") {
			result = reg.ReplaceAllString(result, "$1=[REDACTED_SECRET]")
		} else {
			result = reg.ReplaceAllString(result, "[REDACTED_SECRET]")
		}
	}

	return result
}

// ScrubMap applies Layer 1 (field denylist) and Layer 2 (regex value scrubbing) recursively to a key-value map.
func ScrubMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}

	scrubbed := make(map[string]interface{}, len(m))
	for k, v := range m {
		normKey := strings.ToLower(strings.TrimSpace(k))

		// Layer 1: Check Deterministic Key Denylist
		if sensitiveKeyDenylist[normKey] {
			scrubbed[k] = "[REDACTED_FIELD]"
			continue
		}

		// Layer 2: Recursive Scrubbing on Value
		switch val := v.(type) {
		case string:
			scrubbed[k] = ScrubString(val)
		case map[string]interface{}:
			scrubbed[k] = ScrubMap(val)
		case []interface{}:
			scrubbed[k] = scrubSlice(val)
		case fmt.Stringer:
			scrubbed[k] = ScrubString(val.String())
		default:
			scrubbed[k] = v
		}
	}

	return scrubbed
}

func scrubSlice(slice []interface{}) []interface{} {
	out := make([]interface{}, len(slice))
	for i, item := range slice {
		switch v := item.(type) {
		case string:
			out[i] = ScrubString(v)
		case map[string]interface{}:
			out[i] = ScrubMap(v)
		case []interface{}:
			out[i] = scrubSlice(v)
		default:
			out[i] = v
		}
	}
	return out
}

// CaptureLogBuffer captures log output into a buffer for testing.
func CaptureLogBuffer() (*bytes.Buffer, *Logger) {
	buf := new(bytes.Buffer)
	logger := NewLogger(buf)
	return buf, logger
}
