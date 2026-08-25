package telemetry

import (
	"fmt"
	"strings"
	"testing"
)

// TestTelemetry_AutomatedSecretScrubbingScan runs 500 varied test requests containing injected credentials
// and scans 100% of the output buffer to verify a 0-match (100.00% scrubbed) guarantee.
func TestTelemetry_AutomatedSecretScrubbingScan(t *testing.T) {
	buf, logger := CaptureLogBuffer()

	// Injected test secrets across platforms and formats
	testCredentials := []string{
		"ya29.a0AfH6SMB_1234567890abcdefghijklmnopqrstuvwxyz_GOOGLE_TEST_SECRET",
		"EAABsbCS1ic8BAO1234567890abcdefghijklmnopqrstuvwxyz_META_TEST_SECRET",
		"AAAA1234567890abcdefghijklmnopqrstuvwxyz_TWITTER_APP_SECRET",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		"Bearer my_super_secret_raw_bearer_token_xyz_123456",
		"postgres_super_secure_production_password_to_be_masked",
		"instagram_webhook_secret_verification_key_secret_999",
	}

	const numRequests = 500
	t.Logf("=== RUNNING 500-REQUEST DUAL-LAYER SECRET SCRUBBER AUDIT ===")

	for i := 0; i < numRequests; i++ {
		credIdx := i % len(testCredentials)
		cred := testCredentials[credIdx]

		switch i % 5 {
		case 0:
			// Direct message string with token
			logger.Info(fmt.Sprintf("Request %d: processing token payload %s for upstream publish", i, cred), map[string]interface{}{
				"request_id": fmt.Sprintf("req_%d", i),
			})
		case 1:
			// Layer 1 Denylist: structured map field with sensitive keys
			logger.Error(fmt.Sprintf("Request %d: upstream returned authentication failure", i), map[string]interface{}{
				"access_token":  cred,
				"client_secret": cred,
				"password":      cred,
				"authorization": fmt.Sprintf("Bearer %s", cred),
			})
		case 2:
			// URL Query parameter string
			logger.Warn(fmt.Sprintf("Request %d: failed redirect URL https://api.instagram.com/oauth/authorize?access_token=%s&state=xyz", i, cred), nil)
		case 3:
			// Nested map with sensitive keys and values
			logger.Info(fmt.Sprintf("Request %d: session verification payload", i), map[string]interface{}{
				"session_data": map[string]interface{}{
					"refresh_token": cred,
					"jwt_secret":    cred,
					"user_notes":    fmt.Sprintf("token is %s", cred),
				},
			})
		case 4:
			// Slice containing raw secrets
			logger.Info(fmt.Sprintf("Request %d: token list payload", i), map[string]interface{}{
				"tokens": []interface{}{cred, fmt.Sprintf("Bearer %s", cred)},
			})
		}
	}

	rawLogOutput := buf.String()
	t.Logf("Total Log Output Generated: %d bytes across %d log entries", len(rawLogOutput), numRequests)

	// Scan 100% of the log buffer for each injected credential
	var leakCount int
	for _, cred := range testCredentials {
		if strings.Contains(rawLogOutput, cred) {
			t.Errorf("CRITICAL SECURITY VIOLATION: Secret leaked in log output: %s", cred)
			leakCount++
		}
	}

	t.Logf("=== SECRET SCRUBBING SCAN RESULTS ===")
	t.Logf("Total Injected Secret Probes:   %d", numRequests)
	t.Logf("Total Distinct Secret Patterns: %d", len(testCredentials))
	t.Logf("Secrets Leaked in Logs:         %d (Target: 0)", leakCount)
	t.Logf("Scrubbing Success Rate:         %.2f%% (Target: 100.00%%)", float64(numRequests-leakCount)/float64(numRequests)*100.0)

	if leakCount != 0 {
		t.Fatalf("FAILED: %d secrets leaked into structured logs", leakCount)
	}
}
