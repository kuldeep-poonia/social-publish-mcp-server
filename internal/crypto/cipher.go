// Package crypto provides AES-256-GCM authenticated encryption and decryption for sensitive tokens at rest.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const (
	// KeySizeAES256 defines the required key size in bytes for AES-256.
	KeySizeAES256 = 32
	// NonceSizeBytes defines standard 96-bit (12 bytes) GCM nonce size.
	NonceSizeBytes = 12
)

var (
	// ErrInvalidKeySize is returned when an encryption key is not exactly 32 bytes.
	ErrInvalidKeySize = errors.New("crypto: invalid key size, must be exactly 32 bytes for AES-256")
	// ErrCiphertextTooShort is returned when ciphertext is shorter than the required nonce + tag size.
	ErrCiphertextTooShort = errors.New("crypto: ciphertext too short")
	// ErrDecryptionFailed is returned when authentication tag verification fails (tampered ciphertext).
	ErrDecryptionFailed = errors.New("crypto: message authentication failed, ciphertext is corrupted or tampered")
)

// EncryptOAuthToken encrypts plaintext OAuth tokens using AES-256-GCM with a randomly generated 12-byte nonce.
// The returned byte slice has the format: [12-byte nonce || ciphertext || 16-byte authentication tag].
func EncryptOAuthToken(plaintext []byte, key []byte) ([]byte, error) {
	if len(key) != KeySizeAES256 {
		return nil, ErrInvalidKeySize
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher block: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GCM mode: %w", err)
	}

	nonce := make([]byte, NonceSizeBytes)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate cryptographically secure nonce: %w", err)
	}

	// Seal appends ciphertext and authentication tag to dst (prefixing with nonce)
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// DecryptOAuthToken authenticates and decrypts an AES-256-GCM encrypted ciphertext using the provided 32-byte key.
// It verifies the authentication tag and returns ErrDecryptionFailed if the ciphertext or tag has been tampered with.
func DecryptOAuthToken(ciphertext []byte, key []byte) ([]byte, error) {
	if len(key) != KeySizeAES256 {
		return nil, ErrInvalidKeySize
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher block: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GCM mode: %w", err)
	}

	if len(ciphertext) < NonceSizeBytes+gcm.Overhead() {
		return nil, ErrCiphertextTooShort
	}

	nonce := ciphertext[:NonceSizeBytes]
	rawCiphertextWithTag := ciphertext[NonceSizeBytes:]

	plaintext, err := gcm.Open(nil, nonce, rawCiphertextWithTag, nil)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	return plaintext, nil
}
