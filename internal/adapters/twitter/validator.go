// Package twitter provides tweet text validation, deep magic-byte media inspection, and pure Go MP4 duration parsing.
package twitter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// MaxTweetLength defines Twitter's standard 280-character limit.
	MaxTweetLength = 280
	// TwitterURLWeight defines the fixed character weight of any URL per Twitter t.co shortening.
	TwitterURLWeight = 23
	// MaxImageSizeBytes defines the maximum allowed image upload size (5 MB).
	MaxImageSizeBytes = 5 * 1024 * 1024
	// MaxVideoSizeBytes defines the maximum allowed video upload size (512 MB).
	MaxVideoSizeBytes = 512 * 1024 * 1024
	// MaxVideoDurationSeconds defines the maximum allowed video duration (140 seconds).
	MaxVideoDurationSeconds = 140.0
)

var (
	urlRegex = regexp.MustCompile(`https?://[^\s]+`)

	// ErrTweetTooLong is returned when weighted tweet text exceeds 280 characters.
	ErrTweetTooLong = errors.New("twitter validator: tweet text exceeds 280 character limit")
	// ErrEmptyTweetContent is returned when tweet text and media are both empty.
	ErrEmptyTweetContent = errors.New("twitter validator: tweet content cannot be completely empty")
	// ErrImageTooLarge is returned when image file exceeds 5MB.
	ErrImageTooLarge = errors.New("twitter validator: image exceeds maximum permitted size of 5MB")
	// ErrVideoTooLarge is returned when video file exceeds 512MB.
	ErrVideoTooLarge = errors.New("twitter validator: video exceeds maximum permitted size of 512MB")
	// ErrVideoTooLong is returned when video duration exceeds 140 seconds.
	ErrVideoTooLong = errors.New("twitter validator: video duration exceeds maximum permitted length of 140 seconds")
	// ErrDisguisedExecutable is returned when binary content matches an executable or script signature.
	ErrDisguisedExecutable = errors.New("twitter validator: disguised executable or malicious payload signature detected")
	// ErrUnsupportedMediaType is returned when media magic bytes do not match supported formats.
	ErrUnsupportedMediaType = errors.New("twitter validator: unsupported media format (only JPEG, PNG, GIF, WEBP, and MP4 are permitted)")
)

// MediaType represents validated media categorization.
type MediaType string

const (
	MediaTypeJPEG  MediaType = "image/jpeg"
	MediaTypePNG   MediaType = "image/png"
	MediaTypeGIF   MediaType = "image/gif"
	MediaTypeWEBP  MediaType = "image/webp"
	MediaTypeMP4   MediaType = "video/mp4"
)

// ValidateTweetText calculates the weighted character length of a tweet and enforces the 280-char limit.
func ValidateTweetText(text string) (int, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, nil
	}

	// Replace all URLs with a placeholder of exact TwitterURLWeight (23) length
	textWithoutURLs := urlRegex.ReplaceAllStringFunc(text, func(_ string) string {
		return strings.Repeat("x", TwitterURLWeight)
	})

	weightedLength := utf8.RuneCountInString(textWithoutURLs)
	if weightedLength > MaxTweetLength {
		return weightedLength, fmt.Errorf("%w: length is %d characters (max 280)", ErrTweetTooLong, weightedLength)
	}

	return weightedLength, nil
}

// ValidateMediaPayload inspects file size, sniffs magic bytes against disguised executables, and validates MP4 duration.
func ValidateMediaPayload(reader io.ReaderAt, size int64) (MediaType, error) {
	if size <= 0 {
		return "", errors.New("twitter validator: media payload size must be greater than zero")
	}

	// Read initial 512 bytes for magic-byte sniffing
	headerBufSize := int64(512)
	if size < headerBufSize {
		headerBufSize = size
	}
	header := make([]byte, headerBufSize)
	if _, err := reader.ReadAt(header, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("failed reading media header: %w", err)
	}

	// 1. Check for malicious executable signatures
	if err := checkExecutableSignatures(header); err != nil {
		return "", err
	}

	// 2. Sniff media format from magic bytes
	mediaType, err := detectMediaFormat(header)
	if err != nil {
		return "", err
	}

	// 3. Enforce size and duration constraints per format
	switch mediaType {
	case MediaTypeJPEG, MediaTypePNG, MediaTypeGIF, MediaTypeWEBP:
		if size > MaxImageSizeBytes {
			return "", fmt.Errorf("%w: actual size is %d bytes (max %d bytes)", ErrImageTooLarge, size, MaxImageSizeBytes)
		}

	case MediaTypeMP4:
		if size > MaxVideoSizeBytes {
			return "", fmt.Errorf("%w: actual size is %d bytes (max %d bytes)", ErrVideoTooLarge, size, MaxVideoSizeBytes)
		}

		durationSec, err := ParseMP4Duration(reader, size)
		if err != nil {
			return "", fmt.Errorf("failed parsing MP4 duration: %w", err)
		}
		if durationSec > MaxVideoDurationSeconds {
			return "", fmt.Errorf("%w: duration is %.2fs (max %.1fs)", ErrVideoTooLong, durationSec, MaxVideoDurationSeconds)
		}
	}

	return mediaType, nil
}

func checkExecutableSignatures(h []byte) error {
	if len(h) < 2 {
		return nil
	}

	// Windows DOS / PE executable header ("MZ")
	if h[0] == 0x4D && h[1] == 0x5A {
		return ErrDisguisedExecutable
	}

	// Linux ELF executable header ("\x7FELF")
	if len(h) >= 4 && h[0] == 0x7F && h[1] == 'E' && h[2] == 'L' && h[3] == 'F' {
		return ErrDisguisedExecutable
	}

	// macOS Mach-O headers (32-bit and 64-bit big/little endian)
	if len(h) >= 4 {
		magic := binary.BigEndian.Uint32(h[:4])
		if magic == 0xFEEDFACE || magic == 0xFEEDFACF || magic == 0xCEFAEDFE || magic == 0xCFFAEDFE {
			return ErrDisguisedExecutable
		}
	}

	// Script injection signatures
	headerStr := strings.ToLower(string(h[:min(len(h), 64)]))
	if strings.HasPrefix(headerStr, "#!") ||
		strings.HasPrefix(headerStr, "<?php") ||
		strings.Contains(headerStr, "<script") ||
		strings.HasPrefix(headerStr, "@echo") ||
		strings.HasPrefix(headerStr, "rem ") {
		return ErrDisguisedExecutable
	}

	return nil
}

func detectMediaFormat(h []byte) (MediaType, error) {
	if len(h) >= 3 && h[0] == 0xFF && h[1] == 0xD8 && h[2] == 0xFF {
		return MediaTypeJPEG, nil
	}

	if len(h) >= 8 && bytes.Equal(h[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		return MediaTypePNG, nil
	}

	if len(h) >= 6 && (bytes.Equal(h[:6], []byte("GIF87a")) || bytes.Equal(h[:6], []byte("GIF89a"))) {
		return MediaTypeGIF, nil
	}

	if len(h) >= 12 && bytes.Equal(h[:4], []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP")) {
		return MediaTypeWEBP, nil
	}

	// MP4 container: box type 'ftyp' at offset 4
	if len(h) >= 8 && bytes.Equal(h[4:8], []byte("ftyp")) {
		return MediaTypeMP4, nil
	}

	return "", ErrUnsupportedMediaType
}

// ParseMP4Duration parses the MP4 container structure to extract timescale and duration from the 'mvhd' atom.
func ParseMP4Duration(r io.ReaderAt, fileSize int64) (float64, error) {
	var offset int64

	for offset < fileSize {
		var boxHeader [8]byte
		if _, err := r.ReadAt(boxHeader[:], offset); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, err
		}

		boxSize := int64(binary.BigEndian.Uint32(boxHeader[0:4]))
		boxType := string(boxHeader[4:8])

		if boxSize == 1 {
			// 64-bit extended box size
			var extSizeBuf [8]byte
			if _, err := r.ReadAt(extSizeBuf[:], offset+8); err != nil {
				return 0, err
			}
			boxSize = int64(binary.BigEndian.Uint64(extSizeBuf[:]))
			if boxSize < 16 {
				return 0, errors.New("invalid extended box size in MP4")
			}
		} else if boxSize == 0 {
			// Box extends to end of file
			boxSize = fileSize - offset
		}

		if boxSize < 8 {
			return 0, errors.New("invalid box size in MP4")
		}

		if boxType == "moov" {
			// Search for 'mvhd' box inside 'moov'
			moovPayloadOffset := offset + 8
			moovPayloadSize := boxSize - 8
			return parseMoovBox(r, moovPayloadOffset, moovPayloadSize)
		}

		offset += boxSize
	}

	return 0, errors.New("moov/mvhd header not found in MP4 file")
}

func parseMoovBox(r io.ReaderAt, moovOffset int64, moovSize int64) (float64, error) {
	var offset int64

	for offset < moovSize {
		var subHeader [8]byte
		if _, err := r.ReadAt(subHeader[:], moovOffset+offset); err != nil {
			return 0, err
		}

		subSize := int64(binary.BigEndian.Uint32(subHeader[0:4]))
		subType := string(subHeader[4:8])

		if subSize < 8 || subSize > moovSize-offset {
			return 0, errors.New("invalid sub-box size inside moov")
		}

		if subType == "mvhd" {
			// Read mvhd box content
			mvhdData := make([]byte, subSize-8)
			if _, err := r.ReadAt(mvhdData, moovOffset+offset+8); err != nil {
				return 0, err
			}

			if len(mvhdData) < 20 {
				return 0, errors.New("mvhd atom too short")
			}

			version := mvhdData[0]
			var timescale uint32
			var duration uint64

			if version == 0 {
				// Version 0: 32-bit creation, modification, timescale, duration
				if len(mvhdData) < 20 {
					return 0, errors.New("mvhd version 0 atom truncated")
				}
				timescale = binary.BigEndian.Uint32(mvhdData[12:16])
				duration = uint64(binary.BigEndian.Uint32(mvhdData[16:20]))
			} else if version == 1 {
				// Version 1: 64-bit creation, modification; 32-bit timescale; 64-bit duration
				if len(mvhdData) < 32 {
					return 0, errors.New("mvhd version 1 atom truncated")
				}
				timescale = binary.BigEndian.Uint32(mvhdData[20:24])
				duration = binary.BigEndian.Uint64(mvhdData[24:32])
			} else {
				return 0, fmt.Errorf("unsupported mvhd version %d", version)
			}

			if timescale == 0 {
				return 0, errors.New("invalid timescale (0) in MP4 mvhd")
			}

			durationSeconds := float64(duration) / float64(timescale)
			return durationSeconds, nil
		}

		offset += subSize
	}

	return 0, errors.New("mvhd atom not found inside moov box")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
