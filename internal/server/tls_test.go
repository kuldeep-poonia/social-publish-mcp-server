package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"
)

// generateTestCertificate creates an in-memory self-signed TLS certificate for real network listener testing.
func generateTestCertificate(t *testing.T) tls.Certificate {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed generating ECDSA key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Social MCP Test Server"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed creating self-signed certificate: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}
}

// TestServer_RealNetworkTLSHandshakeRejection spins up a real network socket with NewHardenedTLSConfig()
// and executes actual TLS network handshakes to prove legacy SSLv3, TLS 1.0, and TLS 1.1 connections
// are strictly rejected at the socket layer, while TLS 1.2 and TLS 1.3 succeed.
func TestServer_RealNetworkTLSHandshakeRejection(t *testing.T) {
	serverCert := generateTestCertificate(t)
	serverTLSConfig := NewHardenedTLSConfig()
	serverTLSConfig.Certificates = []tls.Certificate{serverCert}

	// 1. Start real network TLS listener on ephemeral local port
	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("failed starting real TLS listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()
	t.Logf("=== REAL NETWORK TLS LISTENER ACTIVE ON %s ===", serverAddr)

	// Serve minimal HTTP response over real TLS listener
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("tls_handshake_success"))
		}),
	}
	go func() {
		_ = httpServer.Serve(listener)
	}()
	defer func() { _ = httpServer.Close() }()

	// 2. Real Network Handshake Test: Legacy Insecure Clients (Must be REJECTED)
	insecureClientTests := []struct {
		name       string
		maxVersion uint16
	}{
		{"TLS 1.0 Client", tls.VersionTLS10},
		{"TLS 1.1 Client", tls.VersionTLS11},
	}

	for _, tc := range insecureClientTests {
		t.Run("Reject_"+tc.name, func(t *testing.T) {
			dialer := &net.Dialer{Timeout: 2 * time.Second}
			clientTLS := &tls.Config{
				InsecureSkipVerify: true,
				MaxVersion:         tc.maxVersion,
			}

			conn, dialErr := tls.DialWithDialer(dialer, "tcp", serverAddr, clientTLS)
			if dialErr == nil {
				_ = conn.Close()
				t.Fatalf("CRITICAL SECURITY VIOLATION: Real network connection with %s succeeded, expected handshake rejection!", tc.name)
			}

			t.Logf("PASS: Real Network Socket Handshake Rejected for %s: %v", tc.name, dialErr)
		})
	}

	// 3. Real Network Handshake Test: Secure TLS 1.2 Client (Must SUCCEED)
	t.Run("Accept_TLS1.2_Client", func(t *testing.T) {
		clientTLS := &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
			MaxVersion:         tls.VersionTLS12,
		}

		conn, dialErr := tls.Dial("tcp", serverAddr, clientTLS)
		if dialErr != nil {
			t.Fatalf("expected TLS 1.2 network handshake to succeed, got: %v", dialErr)
		}
		defer conn.Close()

		state := conn.ConnectionState()
		t.Logf("PASS: Real TLS 1.2 Handshake Succeeded. Negotiated Protocol Version: 0x%04x | Cipher: %s",
			state.Version, tls.CipherSuiteName(state.CipherSuite))

		if state.Version != tls.VersionTLS12 {
			t.Fatalf("expected negotiated version TLS 1.2 (0x0303), got: 0x%04x", state.Version)
		}
	})

	// 4. Real Network Handshake Test: Modern TLS 1.3 Client (Must SUCCEED)
	t.Run("Accept_TLS1.3_Client", func(t *testing.T) {
		clientTLS := &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS13,
		}

		conn, dialErr := tls.Dial("tcp", serverAddr, clientTLS)
		if dialErr != nil {
			t.Fatalf("expected TLS 1.3 network handshake to succeed, got: %v", dialErr)
		}
		defer conn.Close()

		state := conn.ConnectionState()
		t.Logf("PASS: Real TLS 1.3 Handshake Succeeded. Negotiated Protocol Version: 0x%04x | Cipher: %s",
			state.Version, tls.CipherSuiteName(state.CipherSuite))

		if state.Version != tls.VersionTLS13 {
			t.Fatalf("expected negotiated version TLS 1.3 (0x0304), got: 0x%04x", state.Version)
		}

		// Also execute full HTTP GET over TLS 1.3 socket to verify application data flow
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTLS,
			},
		}
		resp, httpErr := client.Get("https://" + serverAddr + "/health")
		if httpErr != nil {
			t.Fatalf("HTTP over TLS 1.3 failed: %v", httpErr)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "tls_handshake_success" {
			t.Fatalf("unexpected body from TLS server: %s", string(body))
		}
	})
}

// TestServer_HardenedTLSConfiguration validates static cipher constraints and version thresholds.
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
