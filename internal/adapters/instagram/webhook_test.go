package instagram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func generateValidSignature(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}

func TestInstagramWebhook_SignatureVerification(t *testing.T) {
	appSecret := "meta_app_secret_super_secure_1234567890"
	validPayload := []byte(`{"object":"instagram","entry":[{"id":"17841400000000","time":1700000000,"changes":[{"field":"media","value":{"id":"17999999","status_code":"FINISHED"}}]}]}`)

	// 1. Valid signature
	validSig := generateValidSignature(validPayload, appSecret)
	if err := VerifyWebhookSignature(validPayload, validSig, appSecret); err != nil {
		t.Fatalf("expected valid signature to pass, got error: %v", err)
	}

	// 2. Tampered payload
	tamperedPayload := []byte(`{"object":"instagram","entry":[{"id":"17841400000000","time":1700000000,"changes":[{"field":"media","value":{"id":"17999999","status_code":"ERROR"}}]}]}`)
	err := VerifyWebhookSignature(tamperedPayload, validSig, appSecret)
	if err == nil || !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for tampered payload, got: %v", err)
	}

	// 3. Spoofed signature (generated with wrong secret)
	spoofedSig := generateValidSignature(validPayload, "attacker_wrong_secret_999999")
	err = VerifyWebhookSignature(validPayload, spoofedSig, appSecret)
	if err == nil || !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for spoofed signature, got: %v", err)
	}

	// 4. Missing signature prefix
	malformedSig := hex.EncodeToString([]byte("random_hex_string_that_is_32_bytes_long_123456789012"))
	err = VerifyWebhookSignature(validPayload, malformedSig, appSecret)
	if err == nil || !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for missing prefix, got: %v", err)
	}

	// 5. Missing secret configuration
	err = VerifyWebhookSignature(validPayload, validSig, "")
	if err == nil {
		t.Fatal("expected error when appSecret is empty, got nil")
	}
}
