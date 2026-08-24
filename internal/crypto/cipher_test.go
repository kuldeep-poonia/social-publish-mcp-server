package crypto

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestEncryptDecryptRoundTrip_10000Tokens(t *testing.T) {
	key := make([]byte, KeySizeAES256)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate random test key: %v", err)
	}

	const tokenCount = 10000
	syntheticTokens := make([][]byte, tokenCount)
	for i := 0; i < tokenCount; i++ {
		// Realistic token sizes between 32 and 256 bytes
		tokenLen := 32 + (i % 225)
		token := make([]byte, tokenLen)
		if _, err := rand.Read(token); err != nil {
			t.Fatalf("failed to generate synthetic token %d: %v", i, err)
		}
		syntheticTokens[i] = token
	}

	successCount := 0
	startTime := time.Now()

	for i := 0; i < tokenCount; i++ {
		original := syntheticTokens[i]
		encrypted, err := EncryptOAuthToken(original, key)
		if err != nil {
			t.Fatalf("encryption failed at iteration %d: %v", i, err)
		}

		decrypted, err := DecryptOAuthToken(encrypted, key)
		if err != nil {
			t.Fatalf("decryption failed at iteration %d: %v", i, err)
		}

		if !bytes.Equal(original, decrypted) {
			t.Fatalf("data mismatch at iteration %d: decrypted content does not match original", i)
		}
		successCount++
	}

	totalDuration := time.Since(startTime)
	avgLatencyMicroseconds := float64(totalDuration.Microseconds()) / float64(tokenCount)
	avgLatencyMilliseconds := avgLatencyMicroseconds / 1000.0
	successRate := (float64(successCount) / float64(tokenCount)) * 100.0

	t.Logf("=== 10,000 Token Round-Trip Benchmark Results ===")
	t.Logf("Total Operations: %d encrypt + %d decrypt", tokenCount, tokenCount)
	t.Logf("Total Time: %v", totalDuration)
	t.Logf("Success Count: %d / %d (Success Rate: %.2f%%)", successCount, tokenCount, successRate)
	t.Logf("Average Latency per Full Round-Trip: %.3f µs (%.4f ms)", avgLatencyMicroseconds, avgLatencyMilliseconds)

	if successRate != 100.0 {
		t.Fatalf("expected 100%% success rate, got %.2f%%", successRate)
	}
}

func TestTamperedCiphertext_BitFlipRejection(t *testing.T) {
	key := make([]byte, KeySizeAES256)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	const iterations = 1000
	rejectionCount := 0
	samplePlaintext := []byte("oauth2-access-token-live-secret-xyz-1234567890")

	for i := 0; i < iterations; i++ {
		encrypted, err := EncryptOAuthToken(samplePlaintext, key)
		if err != nil {
			t.Fatalf("encryption failed at iteration %d: %v", i, err)
		}

		// Create a copy and flip a bit
		tampered := make([]byte, len(encrypted))
		copy(tampered, encrypted)

		// Choose a random byte position to flip
		byteIndex := i % len(tampered)
		// Flip a bit (XOR with 1 << (i % 8))
		tampered[byteIndex] ^= 1 << (uint(i) % 8)

		decrypted, err := DecryptOAuthToken(tampered, key)
		if err != nil {
			if errors.Is(err, ErrDecryptionFailed) || errors.Is(err, ErrCiphertextTooShort) {
				rejectionCount++
			} else {
				t.Fatalf("unexpected error type on tampered ciphertext: %v", err)
			}
		} else {
			// Decryption succeeded on tampered ciphertext — Critical Security Violation!
			t.Fatalf("CRITICAL SECURITY FAILURE: Decryption succeeded on tampered ciphertext at iteration %d! Output: %s", i, string(decrypted))
		}
	}

	rejectionRate := (float64(rejectionCount) / float64(iterations)) * 100.0
	t.Logf("=== Bit-Flip Tampering Test Results ===")
	t.Logf("Iterations: %d", iterations)
	t.Logf("Rejections: %d / %d (Rejection Rate: %.2f%%)", rejectionCount, iterations, rejectionRate)
	t.Logf("Silent Corruption Leaks: 0")

	if rejectionRate != 100.0 {
		t.Fatalf("expected 100%% rejection rate for tampered ciphertexts, got %.2f%%", rejectionRate)
	}
}

func TestKeyValidation_InvalidKeySizes(t *testing.T) {
	invalidKeySizes := []int{0, 16, 24, 31, 33, 64}
	samplePlaintext := []byte("sample-test-token")

	for _, size := range invalidKeySizes {
		t.Run(fmt.Sprintf("KeySize_%d", size), func(t *testing.T) {
			badKey := make([]byte, size)
			_, err := EncryptOAuthToken(samplePlaintext, badKey)
			if !errors.Is(err, ErrInvalidKeySize) {
				t.Fatalf("expected ErrInvalidKeySize for key of size %d, got %v", size, err)
			}

			_, err = DecryptOAuthToken([]byte("short-ciphertext"), badKey)
			if !errors.Is(err, ErrInvalidKeySize) {
				t.Fatalf("expected ErrInvalidKeySize for key of size %d on decrypt, got %v", size, err)
			}
		})
	}
}

func TestDecrypt_TruncatedCiphertext(t *testing.T) {
	key := make([]byte, KeySizeAES256)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	truncatedInputs := [][]byte{
		{},
		make([]byte, 5),
		make([]byte, NonceSizeBytes), // Nonce without tag/ciphertext
		make([]byte, NonceSizeBytes+10), // Shorter than Nonce + 16 byte GCM overhead
	}

	for i, input := range truncatedInputs {
		t.Run(fmt.Sprintf("Truncated_%d", i), func(t *testing.T) {
			_, err := DecryptOAuthToken(input, key)
			if !errors.Is(err, ErrCiphertextTooShort) {
				t.Fatalf("expected ErrCiphertextTooShort, got %v", err)
			}
		})
	}
}
