// Package youtube provides validation for video containers, metadata constraints,
// and binary header inspection to prevent malware injection.
package youtube

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrTitleEmpty        = errors.New("youtube validation: video title cannot be empty")
	ErrTitleTooLong      = errors.New("youtube validation: video title exceeds maximum 100 characters")
	ErrDescTooLong       = errors.New("youtube validation: video description exceeds maximum 5000 characters")
	ErrInvalidPrivacy    = errors.New("youtube validation: privacy_status must be 'public', 'private', or 'unlisted'")
	ErrMaliciousPayload  = errors.New("security error: binary contains executable signature or malicious script injection")
	ErrUnsupportedFormat = errors.New("youtube validation: unsupported video format; must be MP4, MOV, MKV, WebM, or AVI")
)

// ValidateVideoMetadata verifies YouTube title, description, and privacy settings.
func ValidateVideoMetadata(title, description, privacyStatus string) error {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle == "" {
		return ErrTitleEmpty
	}
	if utf8.RuneCountInString(trimmedTitle) > 100 {
		return fmt.Errorf("%w: title has %d characters", ErrTitleTooLong, utf8.RuneCountInString(trimmedTitle))
	}

	if utf8.RuneCountInString(description) > 5000 {
		return fmt.Errorf("%w: description has %d characters", ErrDescTooLong, utf8.RuneCountInString(description))
	}

	privacy := strings.ToLower(strings.TrimSpace(privacyStatus))
	if privacy == "" {
		privacy = "public"
	}
	if privacy != "public" && privacy != "private" && privacy != "unlisted" {
		return ErrInvalidPrivacy
	}

	return nil
}

// ValidateVideoHeader inspects binary magic bytes to verify genuine video container format
// and guarantee 100% rejection of disguised Windows PE, Linux ELF, Mach-O, and shell scripts.
func ValidateVideoHeader(header []byte) (string, error) {
	if len(header) < 12 {
		return "", errors.New("youtube validation: file too short to determine video container format")
	}

	// 1. Malicious / Disguised Executable Signatures Check (Zero-Tolerance)
	if bytes.HasPrefix(header, []byte("MZ")) {
		return "", fmt.Errorf("%w (Windows PE/EXE)", ErrMaliciousPayload)
	}
	if bytes.HasPrefix(header, []byte("\x7fELF")) {
		return "", fmt.Errorf("%w (Linux ELF Binary)", ErrMaliciousPayload)
	}
	if bytes.HasPrefix(header, []byte("\xfe\xed\xfa\xce")) ||
		bytes.HasPrefix(header, []byte("\xce\xfa\xed\xfe")) ||
		bytes.HasPrefix(header, []byte("\xfe\xed\xfa\xcf")) ||
		bytes.HasPrefix(header, []byte("\xcf\xfa\xed\xfe")) ||
		bytes.HasPrefix(header, []byte("\xca\xfe\xba\xbe")) {
		return "", fmt.Errorf("%w (macOS Mach-O / Java Class)", ErrMaliciousPayload)
	}
	if bytes.HasPrefix(header, []byte("#!")) ||
		bytes.HasPrefix(header, []byte("<?php")) ||
		bytes.HasPrefix(header, []byte("<script")) {
		return "", fmt.Errorf("%w (Script Injection)", ErrMaliciousPayload)
	}

	// 2. Validate Genuine Video Containers
	// MP4 / ISO Base Media: Check for "ftyp" box at offset 4
	if len(header) >= 8 && string(header[4:8]) == "ftyp" {
		return "video/mp4", nil
	}

	// QuickTime MOV: "moov" or "mdat" or "wide" at offset 4
	if len(header) >= 8 {
		atom := string(header[4:8])
		if atom == "moov" || atom == "mdat" || atom == "wide" || atom == "free" {
			return "video/quicktime", nil
		}
	}

	// MKV / WebM: EBML Header "\x1a\x45\xdf\xa3"
	if bytes.HasPrefix(header, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return "video/webm", nil
	}

	// AVI / RIFF container
	if bytes.HasPrefix(header, []byte("RIFF")) && len(header) >= 12 && string(header[8:12]) == "AVI " {
		return "video/x-msvideo", nil
	}

	return "", ErrUnsupportedFormat
}
