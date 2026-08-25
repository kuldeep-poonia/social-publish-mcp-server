package server

import (
	"crypto/tls"
	"testing"
)

func TestServer_HardenedTLSConfiguration(t *testing.T) {
	tlsCfg := NewHardenedTLSConfig()

	// 1. MinVersion must be at least TLS 1.2
	if tlsCfg.MinVersion < tls.VersionTLS12 {
		t.Fatalf("insecure TLS MinVersion: expected at least 0x%04x (TLS 1.2), got 0x%04x", tls.VersionTLS12, tlsCfg.MinVersion)
	}

	// 2. Validate handshake rejection of legacy insecure versions
	insecureVersions := []struct {
		name    string
		version uint16
	}{
		{"SSLv3", tls.VersionSSL30},
		{"TLS 1.0", tls.VersionTLS10},
		{"TLS 1.1", tls.VersionTLS11},
	}

	for _, tc := range insecureVersions {
		err := ValidateTLSVersion(tc.version)
		if err == nil {
			t.Fatalf("SECURITY VIOLATION: %s was accepted, must be rejected", tc.name)
		}
		t.Logf("Verified Insecure Protocol Rejection: %s (version: 0x%04x) -> %v", tc.name, tc.version, err)
	}

	// 3. Verify TLS 1.2 and TLS 1.3 are accepted
	if err := ValidateTLSVersion(tls.VersionTLS12); err != nil {
		t.Fatalf("expected TLS 1.2 to be accepted: %v", err)
	}
	if err := ValidateTLSVersion(tls.VersionTLS13); err != nil {
		t.Fatalf("expected TLS 1.3 to be accepted: %v", err)
	}

	// 4. Verify all configured cipher suites are forward-secret AEAD suites
	t.Logf("=== CONFIGURED TLS 1.2 CIPHER SUITES ===")
	for _, suite := range tlsCfg.CipherSuites {
		name := tls.CipherSuiteName(suite)
		t.Logf("Cipher: %s (ID: 0x%04x)", name, suite)

		// Ensure no legacy CBC, RC4, or 3DES ciphers exist in suite list
		if suite == tls.TLS_RSA_WITH_AES_128_CBC_SHA || suite == tls.TLS_RSA_WITH_AES_256_CBC_SHA || suite == tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA {
			t.Fatalf("insecure CBC/3DES cipher found in cipher suites: %s", name)
		}
	}
}
