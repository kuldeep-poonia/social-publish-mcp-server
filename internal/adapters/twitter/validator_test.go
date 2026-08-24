package twitter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestTwitterValidator_TweetLengthAndURLWeighting(t *testing.T) {
	// 1. Short text
	len1, err := ValidateTweetText("Hello, World!")
	if err != nil || len1 != 13 {
		t.Fatalf("expected length 13, got %d (err: %v)", len1, err)
	}

	// 2. Long URL (120 chars) should count as exactly 23 chars per Twitter spec
	longURL := "https://example.com/a-very-long-url-path-that-would-normally-take-over-one-hundred-characters-in-a-standard-string-length-check"
	tweetWithURL := "Check this: " + longURL
	len2, err := ValidateTweetText(tweetWithURL)
	if err != nil {
		t.Fatalf("unexpected error for tweet with URL: %v", err)
	}
	expectedLen := len("Check this: ") + TwitterURLWeight
	if len2 != expectedLen {
		t.Fatalf("expected weighted length %d, got %d", expectedLen, len2)
	}

	// 3. Exact 280 character tweet
	exact280 := strings.Repeat("a", 280)
	len3, err := ValidateTweetText(exact280)
	if err != nil || len3 != 280 {
		t.Fatalf("expected exact 280 char tweet to pass, got len=%d, err=%v", len3, err)
	}

	// 4. Over 280 characters (281 chars)
	over280 := strings.Repeat("a", 281)
	_, err = ValidateTweetText(over280)
	if !errors.Is(err, ErrTweetTooLong) {
		t.Fatalf("expected ErrTweetTooLong, got: %v", err)
	}

	// 5. Unicode and emoji weighting
	emojiTweet := "🚀 Build in public! 🌐 💻"
	lenEmoji, err := ValidateTweetText(emojiTweet)
	if err != nil {
		t.Fatalf("emoji tweet failed validation: %v", err)
	}
	t.Logf("Emoji Tweet validated cleanly: '%s' (Weighted Runes: %d)", emojiTweet, lenEmoji)
}

func TestTwitterValidator_DisguisedMalwareRejection_100Percent(t *testing.T) {
	malwareSignatures := []struct {
		name    string
		payload []byte
	}{
		{name: "Windows PE / EXE Header (MZ)", payload: append([]byte{0x4D, 0x5A, 0x90, 0x00}, bytes.Repeat([]byte{0x00}, 100)...)},
		{name: "Linux ELF Header", payload: append([]byte{0x7F, 0x45, 0x4C, 0x46, 0x02, 0x01}, bytes.Repeat([]byte{0x00}, 100)...)},
		{name: "macOS Mach-O 64-bit Header", payload: append([]byte{0xFE, 0xED, 0xFA, 0xCF}, bytes.Repeat([]byte{0x00}, 100)...)},
		{name: "Shell Script Shebang", payload: []byte("#!/bin/bash\nrm -rf /")},
		{name: "PHP Script Injection", payload: []byte("<?php echo 'malicious payload'; ?>")},
		{name: "HTML / Javascript Payload", payload: []byte("<script>window.location='http://attacker.com'</script>")},
		{name: "Windows Batch Script", payload: []byte("@echo off\nformat C:")},
		{name: "Windows CMD REM Script", payload: []byte("REM Malicious Windows batch file")},
	}

	// Test each signature disguised as a .jpg/.png payload
	rejections := 0
	for _, m := range malwareSignatures {
		reader := bytes.NewReader(m.payload)
		_, err := ValidateMediaPayload(reader, int64(len(m.payload)))
		if errors.Is(err, ErrDisguisedExecutable) {
			rejections++
		} else {
			t.Errorf("SECURITY BREACH: Disguised malware '%s' was NOT rejected! (err: %v)", m.name, err)
		}
	}

	rejectionRate := (float64(rejections) / float64(len(malwareSignatures))) * 100.0
	t.Logf("=== DISGUISED MALWARE VALIDATION RESULTS ===")
	t.Logf("Total Malware Signatures Tested: %d", len(malwareSignatures))
	t.Logf("Rejections: %d / %d (Rejection Rate: %.2f%%)", rejections, len(malwareSignatures), rejectionRate)
	t.Logf("Security Target: 100%% Rejection (Met: %t)", rejectionRate == 100.0)

	if rejectionRate != 100.0 {
		t.Fatalf("CRITICAL SECURITY FAILURE: Malware validation failed to reject all disguised executables!")
	}
}

func TestTwitterValidator_ValidMediaFormats(t *testing.T) {
	validFormats := []struct {
		name         string
		expectedType MediaType
		header       []byte
	}{
		{name: "JPEG", expectedType: MediaTypeJPEG, header: []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}},
		{name: "PNG", expectedType: MediaTypePNG, header: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}},
		{name: "GIF87a", expectedType: MediaTypeGIF, header: []byte("GIF87a\x01\x00\x01\x00\x80\x00\x00")},
		{name: "GIF89a", expectedType: MediaTypeGIF, header: []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00")},
		{name: "WEBP", expectedType: MediaTypeWEBP, header: []byte("RIFF\x20\x00\x00\x00WEBPVP8 \x14\x00")},
	}

	for _, vf := range validFormats {
		reader := bytes.NewReader(vf.header)
		mediaType, err := ValidateMediaPayload(reader, int64(len(vf.header)))
		if err != nil {
			t.Fatalf("valid format %s failed: %v", vf.name, err)
		}
		if mediaType != vf.expectedType {
			t.Fatalf("expected media type %s, got %s", vf.expectedType, mediaType)
		}
	}
}

func TestTwitterValidator_MP4DurationParsing(t *testing.T) {
	// Helper to generate synthetic valid MP4 container with custom timescale & duration in mvhd
	buildSyntheticMP4 := func(timescale uint32, duration uint32) []byte {
		buf := new(bytes.Buffer)

		// 1. ftyp box (12 bytes)
		_ = binary.Write(buf, binary.BigEndian, uint32(12))
		buf.WriteString("ftyp")
		buf.WriteString("mp42")

		// 2. mvhd box (108 bytes)
		mvhdBuf := new(bytes.Buffer)
		_ = binary.Write(mvhdBuf, binary.BigEndian, uint32(108))
		mvhdBuf.WriteString("mvhd")
		mvhdBuf.WriteByte(0) // version 0
		mvhdBuf.Write([]byte{0, 0, 0}) // flags
		_ = binary.Write(mvhdBuf, binary.BigEndian, uint32(0)) // creation time
		_ = binary.Write(mvhdBuf, binary.BigEndian, uint32(0)) // modification time
		_ = binary.Write(mvhdBuf, binary.BigEndian, timescale) // timescale
		_ = binary.Write(mvhdBuf, binary.BigEndian, duration)  // duration
		// Pad rest of mvhd (108 - 8 - 16 = 84 bytes)
		mvhdBuf.Write(bytes.Repeat([]byte{0}, 84))

		// 3. moov box wrapping mvhd
		moovSize := uint32(8 + mvhdBuf.Len())
		_ = binary.Write(buf, binary.BigEndian, moovSize)
		buf.WriteString("moov")
		buf.Write(mvhdBuf.Bytes())

		return buf.Bytes()
	}

	// Scenario A: 60-second video (Allowed, under 140s)
	mp4_60s := buildSyntheticMP4(1000, 60000)
	mediaType, err := ValidateMediaPayload(bytes.NewReader(mp4_60s), int64(len(mp4_60s)))
	if err != nil || mediaType != MediaTypeMP4 {
		t.Fatalf("60s MP4 failed validation: %v", err)
	}

	// Scenario B: 139.9-second video (Allowed boundary)
	mp4_139s := buildSyntheticMP4(1000, 139900)
	_, err = ValidateMediaPayload(bytes.NewReader(mp4_139s), int64(len(mp4_139s)))
	if err != nil {
		t.Fatalf("139.9s MP4 failed validation: %v", err)
	}

	// Scenario C: 140.5-second video (Must be rejected, exceeds 140.0s)
	mp4_140_5s := buildSyntheticMP4(1000, 140500)
	_, err = ValidateMediaPayload(bytes.NewReader(mp4_140_5s), int64(len(mp4_140_5s)))
	if !errors.Is(err, ErrVideoTooLong) {
		t.Fatalf("expected ErrVideoTooLong for 140.5s video, got: %v", err)
	}

	// Scenario D: 300-second video (Must be rejected)
	mp4_300s := buildSyntheticMP4(1000, 300000)
	_, err = ValidateMediaPayload(bytes.NewReader(mp4_300s), int64(len(mp4_300s)))
	if !errors.Is(err, ErrVideoTooLong) {
		t.Fatalf("expected ErrVideoTooLong for 300s video, got: %v", err)
	}

	t.Logf("=== MP4 DURATION VALIDATOR RESULTS ===")
	t.Logf("60.0s video: PASS | 139.9s video: PASS | 140.5s video: REJECTED | 300.0s video: REJECTED")
}

func TestTwitterValidator_OversizedMediaRejection(t *testing.T) {
	// 1. Oversized Image (>5MB)
	oversizedImage := append([]byte{0xFF, 0xD8, 0xFF}, bytes.Repeat([]byte{0x00}, 5*1024*1024+10)...)
	_, err := ValidateMediaPayload(bytes.NewReader(oversizedImage), int64(len(oversizedImage)))
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge, got: %v", err)
	}

	// 2. Oversized Video (>512MB header check)
	hugeSize := int64(513 * 1024 * 1024)
	mp4Header := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	_, err = ValidateMediaPayload(bytes.NewReader(mp4Header), hugeSize)
	if !errors.Is(err, ErrVideoTooLarge) {
		t.Fatalf("expected ErrVideoTooLarge, got: %v", err)
	}
}
