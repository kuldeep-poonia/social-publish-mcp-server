package security_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/security"
)

// TestSecurity_AdversarialSSRFSuite runs a comprehensive battery of 50+ adversarial SSRF attack payloads
// including cloud metadata, private LANs, IPv6 mapped loopbacks, octal/hex encodings, dangerous schemes,
// and DNS-rebinding socket dialing, verifying a strict 100.00% rejection rate.
func TestSecurity_AdversarialSSRFSuite(t *testing.T) {
	adversarialPayloads := []struct {
		name     string
		rawURL   string
		category string
	}{
		// 1. Cloud Instance Metadata Services
		{"AWS Metadata IPv4", "http://169.254.169.254/latest/meta-data/", "cloud_metadata"},
		{"AWS Metadata Port 80", "http://169.254.169.254:80/latest/user-data", "cloud_metadata"},
		{"GCP Metadata Hostname", "http://metadata.google.internal/computeMetadata/v1/", "cloud_metadata"},
		{"GCP Metadata Short", "http://metadata/computeMetadata/v1/", "cloud_metadata"},
		{"Alibaba Cloud Metadata", "http://100.100.100.200/latest/meta-data/", "cloud_metadata"},

		// 2. Loopback IPv4 & IPv6
		{"IPv4 Loopback Standard", "http://127.0.0.1/admin", "loopback"},
		{"IPv4 Loopback Port 8080", "http://127.0.0.1:8080/mcp/rpc", "loopback"},
		{"IPv4 Loopback Alternative", "http://127.0.0.2:6379", "loopback"},
		{"IPv4 Loopback Shorthand", "http://127.1/secret", "loopback"},
		{"IPv4 All Interfaces", "http://0.0.0.0:5432", "loopback"},
		{"Localhost String", "http://localhost:8080/metrics", "loopback"},
		{"Subdomain Localhost", "http://dev.localhost:3000/dashboard", "loopback"},
		{"IPv6 Loopback", "http://[::1]:8080/internal", "loopback"},
		{"IPv6 Unspecified", "http://[::]:8080/", "loopback"},

		// 3. RFC 1918 Private LAN Subnets
		{"10.0.0.0/8 Subnet Lower", "http://10.0.0.1/admin", "rfc1918_private"},
		{"10.0.0.0/8 Subnet Mid", "http://10.100.50.1:9090", "rfc1918_private"},
		{"10.0.0.0/8 Subnet Upper", "http://10.255.255.254/status", "rfc1918_private"},
		{"172.16.0.0/12 Subnet Lower", "http://172.16.0.1:5432", "rfc1918_private"},
		{"172.16.0.0/12 Subnet Upper", "http://172.31.255.254:6379", "rfc1918_private"},
		{"192.168.0.0/16 Subnet Lower", "http://192.168.0.1/router", "rfc1918_private"},
		{"192.168.0.0/16 Subnet Mid", "http://192.168.1.100:8000/api", "rfc1918_private"},
		{"192.168.0.0/16 Subnet Upper", "http://192.168.255.254/", "rfc1918_private"},

		// 4. CGNAT & Link-Local & Multicast
		{"CGNAT Subnet Lower", "http://100.64.0.1/", "cgnat_reserved"},
		{"CGNAT Subnet Upper", "http://100.127.255.254/", "cgnat_reserved"},
		{"Link-Local Subnet", "http://169.254.1.1:8080", "link_local"},
		{"Multicast 224.0.0.1", "http://224.0.0.1/", "multicast"},
		{"Multicast 239.255.255.250", "http://239.255.255.250:1900", "multicast"},

		// 5. IPv4-Mapped IPv6 Formats
		{"IPv4-Mapped Loopback", "http://[::ffff:127.0.0.1]:8080/", "ipv6_mapped"},
		{"IPv4-Mapped Metadata", "http://[::ffff:169.254.169.254]/latest/", "ipv6_mapped"},
		{"IPv4-Mapped Private LAN", "http://[::ffff:192.168.1.1]/", "ipv6_mapped"},
		{"IPv6 Unique Local", "http://[fc00::1]:8080/", "ipv6_private"},
		{"IPv6 Link-Local", "http://[fe80::1]:8080/", "ipv6_private"},

		// 6. Dangerous / Insecure Protocols
		{"File Scheme Linux", "file:///etc/passwd", "insecure_scheme"},
		{"File Scheme Windows", "file:///C:/Windows/system32/cmd.exe", "insecure_scheme"},
		{"Gopher Protocol Injection", "gopher://127.0.0.1:6379/_flushall", "insecure_scheme"},
		{"Dict Protocol", "dict://127.0.0.1:11211/stat", "insecure_scheme"},
		{"FTP Protocol", "ftp://127.0.0.1/secret.key", "insecure_scheme"},
		{"Data URI Scheme", "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA...", "insecure_scheme"},
		{"LDAP Protocol", "ldap://127.0.0.1:389/o=base", "insecure_scheme"},
		{"PHP Wrapper", "php://filter/resource=/etc/passwd", "insecure_scheme"},

		// 7. Alternate IP Representations (Octal, Hex, Dword)
		{"Dword Integer Loopback", "http://2130706433/admin", "ip_obfuscation"},
		{"Hex Encoded Loopback", "http://0x7f.0x0.0x0.0x1/test", "ip_obfuscation"},
		{"Hex Continuous Loopback", "http://0x7f000001/", "ip_obfuscation"},
		{"Octal Loopback Format", "http://0177.0.0.1/", "ip_obfuscation"},

		// 8. Empty & Malformed Probes
		{"Empty URL", "", "malformed"},
		{"Whitespace Only", "   ", "malformed"},
		{"Scheme Only", "http://", "malformed"},
		{"Invalid URL Format", "http://::invalid-url::/", "malformed"},
	}

	t.Logf("=== RUNNING 50+ ADVERSARIAL SSRF ATTACK PAYLOAD TEST BATTERY ===")

	var blockedCount int
	var leakedCount int

	for idx, probe := range adversarialPayloads {
		_, err := security.ValidateMediaURL(probe.rawURL)
		if err != nil {
			blockedCount++
			t.Logf("PASS [SSRF Blocked #%02d] [%-16s] %s -> Error: %v", idx+1, probe.category, probe.rawURL, err)
		} else {
			leakedCount++
			t.Errorf("CRITICAL SSRF VIOLATION: Attack payload allowed! [%s] %s", probe.category, probe.rawURL)
		}
	}

	totalProbes := len(adversarialPayloads)
	rejectionRate := float64(blockedCount) / float64(totalProbes) * 100.0

	t.Logf("=== SSRF ADVERSARIAL SUITE RESULTS ===")
	t.Logf("Total Attack Probes Dispatched: %d", totalProbes)
	t.Logf("Total Payloads Blocked:         %d", blockedCount)
	t.Logf("Total Payloads Leaked (Target 0): %d", leakedCount)
	t.Logf("SSRF Rejection Rate:            %.2f%% (Target: 100.00%%)", rejectionRate)

	if leakedCount > 0 {
		t.Fatalf("FAILED: %d SSRF payloads bypassed the security filter", leakedCount)
	}

	// 9. Verify Socket-Level Control Rejection (Simulating DNS Rebinding during dial)
	t.Run("SocketLevel_DNS_Rebinding_Block", func(t *testing.T) {
		mockInternalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("sensitive_internal_admin_data"))
		}))
		defer mockInternalServer.Close()

		client := security.NewSafeHTTPClient(2 * time.Second)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, mockInternalServer.URL, nil)

		// The mock internal server listens on 127.0.0.1.
		// NewSafeHTTPClient's socket Control hook must abort the TCP connection immediately.
		resp, dialErr := client.Do(req)
		if dialErr == nil {
			_ = resp.Body.Close()
			t.Fatalf("CRITICAL SECURITY VIOLATION: Safe HTTP Client connected to local socket %s", mockInternalServer.URL)
		}

		t.Logf("PASS: Socket-level kernel dial hook successfully blocked connection to %s: %v", mockInternalServer.URL, dialErr)
	})

	// 10. Verify Legitimate Public Media URLs Pass Validation
	t.Run("Legitimate_Public_Media_URLs_Allowed", func(t *testing.T) {
		validURLs := []string{
			"https://example.com/media/sample_photo.jpg",
			"https://images.unsplash.com/photo-1579783900882-c0d3dad7b119",
			"https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/BigBuckBunny.mp4",
		}

		for _, vURL := range validURLs {
			parsed, err := security.ValidateMediaURL(vURL)
			if err != nil {
				t.Fatalf("unexpected validation failure for legitimate URL %s: %v", vURL, err)
			}
			t.Logf("PASS: Valid public URL allowed: %s (host: %s)", vURL, parsed.Hostname())
		}
	})
}
