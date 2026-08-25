// Package instagram provides Meta webhook HMAC-SHA256 signature verification and event parsing.
package instagram

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// VerifyWebhookSignature verifies the X-Hub-Signature-256 header using constant-time comparison.
func VerifyWebhookSignature(payload []byte, signatureHeader, appSecret string) error {
	if appSecret == "" {
		return errors.New("webhook verification failed: app secret is not configured")
	}
	if signatureHeader == "" {
		return fmt.Errorf("%w: missing X-Hub-Signature-256 header", ErrInvalidSignature)
	}

	// Header format: "sha256=<hex_digest>"
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return fmt.Errorf("%w: signature header must start with 'sha256='", ErrInvalidSignature)
	}

	receivedHex := strings.TrimPrefix(signatureHeader, prefix)
	receivedMAC, err := hex.DecodeString(receivedHex)
	if err != nil || len(receivedMAC) != sha256.Size {
		return fmt.Errorf("%w: invalid hex encoding in signature header", ErrInvalidSignature)
	}

	// Compute expected HMAC-SHA256
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(payload)
	expectedMAC := mac.Sum(nil)

	// Constant-time comparison protects against timing side-channel attacks
	if subtle.ConstantTimeCompare(receivedMAC, expectedMAC) != 1 {
		return fmt.Errorf("%w: signature mismatch", ErrInvalidSignature)
	}

	return nil
}

// ParseWebhookPayload parses a verified Meta webhook JSON payload.
func ParseWebhookPayload(r io.Reader) (*WebhookPayload, error) {
	var payload WebhookPayload
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed decoding webhook payload: %w", err)
	}
	return &payload, nil
}
