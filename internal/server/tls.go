// Package server provides HTTP/HTTPS server setup, routing, and TLS security hardening.
package server

import (
	"crypto/tls"
	"errors"
	"fmt"
)

// ErrInsecureTLSVersion is returned when a legacy SSL/TLS version is used.
var ErrInsecureTLSVersion = errors.New("insecure TLS version: minimum required is TLS 1.2")

// NewHardenedTLSConfig returns a production-hardened TLS configuration enforcing TLS 1.2+ and forward-secret AEAD ciphers.
func NewHardenedTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
		},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}
}

// ValidateTLSVersion validates whether a TLS version meets the minimum security threshold (TLS 1.2+).
func ValidateTLSVersion(version uint16) error {
	if version < tls.VersionTLS12 {
		return fmt.Errorf("%w: version 0x%04x rejected", ErrInsecureTLSVersion, version)
	}
	return nil
}
