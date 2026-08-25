// Package security provides enterprise-grade SSRF defense, URL validation, and socket-level DNS rebinding protection.
package security

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrInsecureScheme is returned when a URL uses a non-HTTP/HTTPS protocol.
	ErrInsecureScheme = errors.New("ssrf security violation: URL scheme must be http or https")
	// ErrBlockedHost is returned when a hostname resolves to a loopback, private LAN, or cloud metadata address.
	ErrBlockedHost = errors.New("ssrf security violation: destination resolves to a private, loopback, or cloud metadata address")
	// ErrEmptyURL is returned when an empty media URL is supplied.
	ErrEmptyURL = errors.New("ssrf security violation: media URL is empty")
	// ErrPayloadTooLarge is returned when fetched media exceeds the configured byte ceiling.
	ErrPayloadTooLarge = errors.New("media payload exceeded maximum allowed size limit")
)

// Blocked CIDR ranges encompassing Loopback, RFC 1918 Private LAN, CGNAT, Link-Local, Metadata, and Multicast
var blockedCIDRs []*net.IPNet

func init() {
	rawCIDRs := []string{
		"0.0.0.0/8",          // Current network
		"10.0.0.0/8",         // RFC 1918 Private LAN
		"100.64.0.0/10",      // RFC 6598 Shared Carrier CGNAT
		"127.0.0.0/8",        // IPv4 Loopback
		"169.254.0.0/16",     // RFC 3927 Link-Local / Cloud Metadata (169.254.169.254)
		"172.16.0.0/12",      // RFC 1918 Private LAN
		"192.0.0.0/24",       // IETF Protocol Assignments
		"192.0.2.0/24",       // TEST-NET-1
		"192.168.0.0/16",     // RFC 1918 Private LAN
		"198.18.0.0/15",      // Network Interconnect Device Benchmark
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"224.0.0.0/4",        // IPv4 Multicast
		"240.0.0.0/4",        // Reserved / Future Use
		"255.255.255.255/32", // IPv4 Broadcast
		"::/128",             // Unspecified
		"::1/128",            // IPv6 Loopback
		"100::/64",           // Discard-Only Address Block
		"2001:db8::/32",      // IPv6 Documentation
		"fc00::/7",           // IPv6 Unique Local (ULA)
		"fe80::/10",          // IPv6 Link-Local
		"ff00::/8",           // IPv6 Multicast
	}

	for _, cidr := range rawCIDRs {
		_, netBlock, err := net.ParseCIDR(cidr)
		if err == nil {
			blockedCIDRs = append(blockedCIDRs, netBlock)
		}
	}
}

// IsPrivateOrReservedIP checks if an IP address belongs to any private, loopback, multicast, or cloud metadata ranges.
func IsPrivateOrReservedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	// Normalize IPv4-mapped IPv6 addresses (e.g. ::ffff:127.0.0.1 -> 127.0.0.1)
	isIPv4 := false
	if ipv4 := ip.To4(); ipv4 != nil {
		ip = ipv4
		isIPv4 = true
	}

	// Standard library checks
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}

	// Range evaluation against comprehensive CIDR blocks
	for _, block := range blockedCIDRs {
		blockIsIPv4 := block.IP.To4() != nil
		if isIPv4 != blockIsIPv4 {
			continue
		}
		if block.Contains(ip) {
			return true
		}
	}

	return false
}

// ValidateMediaURL performs strict preflight security validation on a media URL.
func ValidateMediaURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, ErrEmptyURL
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid media URL format: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w: '%s'", ErrInsecureScheme, parsed.Scheme)
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, fmt.Errorf("media URL missing valid hostname: %s", rawURL)
	}

	normHost := strings.ToLower(hostname)
	if normHost == "localhost" || strings.HasSuffix(normHost, ".localhost") || normHost == "metadata.google.internal" || normHost == "metadata" {
		return nil, fmt.Errorf("%w: host '%s' is prohibited", ErrBlockedHost, hostname)
	}

	// Resolve all IP addresses for hostname
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return nil, fmt.Errorf("failed resolving media URL hostname '%s': %w", hostname, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP addresses resolved for hostname '%s'", hostname)
	}

	for _, ip := range ips {
		if IsPrivateOrReservedIP(ip) {
			return nil, fmt.Errorf("%w: resolved IP '%s' for host '%s'", ErrBlockedHost, ip.String(), hostname)
		}
	}

	return parsed, nil
}

// NewSafeHTTPTransport constructs a hardened HTTP transport that performs socket-level
// IP re-verification on every TCP connection attempt to prevent DNS rebinding and TOCTOU attacks.
func NewSafeHTTPTransport(dialTimeout time.Duration) *http.Transport {
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}

	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}

			ip := net.ParseIP(host)
			if ip != nil && IsPrivateOrReservedIP(ip) {
				return fmt.Errorf("%w: socket dial to '%s' blocked by kernel control", ErrBlockedHost, address)
			}
			return nil
		},
	}

	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// NewSafeHTTPClient returns an http.Client backed by NewSafeHTTPTransport and a redirect validator.
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	transport := NewSafeHTTPTransport(10 * time.Second)

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			// Preflight validate redirect target URL to prevent redirect-based SSRF bypasses
			if _, err := ValidateMediaURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect target blocked by SSRF filter: %w", err)
			}
			return nil
		},
	}
}

// FetchMediaWithSSRFProtection safely downloads remote media bytes with SSRF protection,
// streaming size enforcement, and MIME type sniffing.
func FetchMediaWithSSRFProtection(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	if maxBytes <= 0 {
		maxBytes = 100 * 1024 * 1024 // Default 100 MB limit
	}

	// 1. Initial Preflight URL Validation
	parsedURL, err := ValidateMediaURL(rawURL)
	if err != nil {
		return nil, "", err
	}

	// 2. Build Safe Request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed creating media fetch request: %w", err)
	}
	req.Header.Set("User-Agent", "Social-Publish-MCP-Server/1.0 (Media Ingestion Bot)")

	client := NewSafeHTTPClient(45 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("safe media download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream media server returned HTTP %d", resp.StatusCode)
	}

	// 3. Read body with strict byte ceiling (prevents decompression bombs and infinite streaming attacks)
	limitReader := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, "", fmt.Errorf("error reading media response body: %w", err)
	}

	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("%w: payload exceeds %d bytes", ErrPayloadTooLarge, maxBytes)
	}

	// 4. Sniff MIME type
	mimeType := http.DetectContentType(data)
	return data, mimeType, nil
}
