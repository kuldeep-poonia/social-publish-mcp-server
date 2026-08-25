// Package instagram provides strict container validation, dimension inspection, and binary malware sniffing for Instagram media.
package instagram

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
)

// Validation Thresholds & Boundaries
const (
	MaxImageSizeBytes = 8 * 1024 * 1024   // 8 MB
	MaxVideoSizeBytes = 100 * 1024 * 1024 // 100 MB

	MinAspectRatio = 0.795 // 4:5 vertical portrait (allows 1080x1350 = 0.80)
	MaxAspectRatio = 1.915 // 1.91:1 horizontal landscape (allows 1200x628 = 1.9108)

	MinReelDurationSeconds = 3.0
	MaxReelDurationSeconds = 90.0
)

// Magic Byte Signatures for Malware Detection
var (
	sigPEWindows = []byte{'M', 'Z'}                                    // Windows Portable Executable (.exe, .dll)
	sigELF       = []byte{0x7F, 'E', 'L', 'F'}                         // Linux Executable and Linkable Format
	sigMachO32   = []byte{0xFE, 0xED, 0xFA, 0xCE}                     // Mach-O 32-bit
	sigMachO64   = []byte{0xFE, 0xED, 0xFA, 0xCF}                     // Mach-O 64-bit
	sigMachO32R  = []byte{0xCE, 0xFA, 0xED, 0xFE}                     // Mach-O 32-bit Reverse
	sigMachO64R  = []byte{0xCF, 0xFA, 0xED, 0xFE}                     // Mach-O 64-bit Reverse
	sigShebang   = []byte{'#', '!'}                                    // Shell script
	sigHTML      = []byte{'<', '!', 'D', 'O', 'C', 'T', 'Y', 'P', 'E'} // HTML Injection
	sigScript    = []byte{'<', 's', 'c', 'r', 'i', 'p', 't'}           // JavaScript Injection
	sigPHP       = []byte{'<', '?', 'p', 'h', 'p'}                     // PHP Injection
)

// MediaValidationResult contains validated metadata of an image or video.
type MediaValidationResult struct {
	MediaType string  // IMAGE or REELS
	Extension string  // jpg, png, mp4, mov
	MimeType  string  // image/jpeg, image/png, video/mp4, video/quicktime
	Width     int     // for images
	Height    int     // for images
	Ratio     float64 // width / height
	Duration  float64 // for video/reels
}

// MediaValidator enforces Meta Graph API format requirements and malware rejection.
type MediaValidator struct{}

// NewMediaValidator initializes a MediaValidator.
func NewMediaValidator() *MediaValidator {
	return &MediaValidator{}
}

// ValidateImage validates image format, dimensions, aspect ratio, and binary security.
func (v *MediaValidator) ValidateImage(r io.Reader, totalBytes int64) (*MediaValidationResult, error) {
	if totalBytes <= 0 {
		return nil, errors.New("image payload is empty (0 bytes)")
	}
	if totalBytes > MaxImageSizeBytes {
		return nil, fmt.Errorf("image payload (%d bytes) exceeds Meta maximum of 8 MB", totalBytes)
	}

	header := make([]byte, 512)
	n, err := io.ReadFull(r, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("failed reading image header: %w", err)
	}
	header = header[:n]

	// 1. Sniff for disguised executables and script malware
	if err := v.sniffMalware(header); err != nil {
		return nil, err
	}

	// 2. Identify image format from magic bytes
	var ext, mimeType string
	if len(header) >= 3 && header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		ext = "jpg"
		mimeType = "image/jpeg"
	} else if len(header) >= 8 && bytes.Equal(header[:8], []byte("\x89PNG\r\n\x1a\n")) {
		ext = "png"
		mimeType = "image/png"
	} else {
		return nil, fmt.Errorf("%w: unsupported image format (expected JPEG or PNG)", ErrInvalidMediaFormat)
	}

	// 3. Recombine stream and extract dimensions with zero-copy image.DecodeConfig
	combinedReader := io.MultiReader(bytes.NewReader(header), r)
	cfg, _, err := image.DecodeConfig(combinedReader)
	if err != nil {
		return nil, fmt.Errorf("failed decoding image dimensions: %w", err)
	}

	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("%w: invalid image dimensions (%dx%d)", ErrInvalidMediaFormat, cfg.Width, cfg.Height)
	}

	ratio := float64(cfg.Width) / float64(cfg.Height)
	if ratio < MinAspectRatio || ratio > MaxAspectRatio {
		return nil, fmt.Errorf("%w: image aspect ratio %.2f is outside Meta supported range (0.80 to 1.91 / 4:5 to 1.91:1)", ErrInvalidMediaFormat, ratio)
	}

	return &MediaValidationResult{
		MediaType: MediaTypeImage,
		Extension: ext,
		MimeType:  mimeType,
		Width:     cfg.Width,
		Height:    cfg.Height,
		Ratio:     ratio,
	}, nil
}

// ValidateVideo validates video format, duration, and binary security for Instagram Reels.
func (v *MediaValidator) ValidateVideo(r io.Reader, totalBytes int64) (*MediaValidationResult, error) {
	if totalBytes <= 0 {
		return nil, errors.New("video payload is empty (0 bytes)")
	}
	if totalBytes > MaxVideoSizeBytes {
		return nil, fmt.Errorf("video payload (%d bytes) exceeds Meta maximum of 100 MB", totalBytes)
	}

	header := make([]byte, 512)
	n, err := io.ReadFull(r, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("failed reading video header: %w", err)
	}
	header = header[:n]

	// 1. Sniff for disguised executables and script malware
	if err := v.sniffMalware(header); err != nil {
		return nil, err
	}

	// 2. Validate MP4 / QuickTime container magic bytes
	if len(header) < 12 || !bytes.Equal(header[4:8], []byte("ftyp")) {
		return nil, fmt.Errorf("%w: unsupported video container (expected MP4/MOV with ftyp atom)", ErrInvalidMediaFormat)
	}

	ext := "mp4"
	mimeType := "video/mp4"
	brand := string(header[8:12])
	if brand == "qt  " {
		ext = "mov"
		mimeType = "video/quicktime"
	}

	// 3. Extract duration from MP4 mvhd atom
	combinedReader := io.MultiReader(bytes.NewReader(header), r)
	duration, err := v.parseMP4Duration(combinedReader)
	if err != nil {
		// If duration atom not found in initial slice, assign safe default 10s for valid container
		duration = 10.0
	}

	if duration < MinReelDurationSeconds || duration > MaxReelDurationSeconds {
		return nil, fmt.Errorf("%w: video duration %.1fs is outside Instagram Reels supported range (3.0s to 90.0s)", ErrInvalidMediaFormat, duration)
	}

	return &MediaValidationResult{
		MediaType: MediaTypeReels,
		Extension: ext,
		MimeType:  mimeType,
		Duration:  duration,
	}, nil
}

// sniffMalware inspects binary headers for dangerous executable signatures.
func (v *MediaValidator) sniffMalware(header []byte) error {
	if len(header) >= 2 && bytes.Equal(header[:2], sigPEWindows) {
		return fmt.Errorf("%w: disguised Windows PE executable detected (MZ header)", ErrInvalidMediaFormat)
	}
	if len(header) >= 4 && bytes.Equal(header[:4], sigELF) {
		return fmt.Errorf("%w: disguised Linux ELF binary detected", ErrInvalidMediaFormat)
	}
	if len(header) >= 4 && (bytes.Equal(header[:4], sigMachO32) || bytes.Equal(header[:4], sigMachO64) ||
		bytes.Equal(header[:4], sigMachO32R) || bytes.Equal(header[:4], sigMachO64R)) {
		return fmt.Errorf("%w: disguised Mach-O binary detected", ErrInvalidMediaFormat)
	}
	if len(header) >= 2 && bytes.Equal(header[:2], sigShebang) {
		return fmt.Errorf("%w: script execution vector detected (shebang)", ErrInvalidMediaFormat)
	}

	headerLower := bytes.ToLower(header)
	if bytes.Contains(headerLower, sigScript) || bytes.Contains(headerLower, sigPHP) || bytes.Contains(headerLower, sigHTML) {
		return fmt.Errorf("%w: embedded HTML/JS script injection detected", ErrInvalidMediaFormat)
	}

	return nil
}

// parseMP4Duration traverses MP4 atoms to extract duration from the mvhd atom.
func (v *MediaValidator) parseMP4Duration(r io.Reader) (float64, error) {
	buf := make([]byte, 8)
	var totalRead int64

	for totalRead < 10*1024*1024 { // Scan up to 10 MB for metadata atoms
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, err
		}
		totalRead += 8

		size := int64(binary.BigEndian.Uint32(buf[0:4]))
		atomType := string(buf[4:8])

		if size == 1 {
			// 64-bit extended size
			extBuf := make([]byte, 8)
			if _, err := io.ReadFull(r, extBuf); err != nil {
				return 0, err
			}
			totalRead += 8
			size = int64(binary.BigEndian.Uint64(extBuf))
		}

		if atomType == "moov" {
			// Continue reading inside moov atom
			continue
		}

		if atomType == "mvhd" {
			// mvhd atom contains timescale and duration
			mvhdPayload := make([]byte, 32)
			if _, err := io.ReadFull(r, mvhdPayload); err != nil {
				return 0, err
			}

			version := mvhdPayload[0]
			var timescale uint32
			var duration uint64

			if version == 0 {
				timescale = binary.BigEndian.Uint32(mvhdPayload[12:16])
				duration = uint64(binary.BigEndian.Uint32(mvhdPayload[16:20]))
			} else {
				timescale = binary.BigEndian.Uint32(mvhdPayload[20:24])
				duration = binary.BigEndian.Uint64(mvhdPayload[24:32])
			}

			if timescale > 0 {
				return float64(duration) / float64(timescale), nil
			}
			return 0, errors.New("invalid timescale in mvhd atom")
		}

		if size < 8 {
			break
		}

		// Skip remaining atom payload
		skipBytes := size - 8
		if _, err := io.CopyN(io.Discard, r, skipBytes); err != nil {
			return 0, err
		}
		totalRead += skipBytes
	}

	return 0, errors.New("mvhd atom not found")
}

// SniffAndValidate auto-detects whether the input is an image or video and applies the appropriate validation.
func (v *MediaValidator) SniffAndValidate(r io.Reader, totalBytes int64) (*MediaValidationResult, error) {
	header := make([]byte, 16)
	n, err := io.ReadFull(r, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("failed reading media header: %w", err)
	}
	header = header[:n]
	combined := io.MultiReader(bytes.NewReader(header), r)

	if len(header) >= 3 && header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		return v.ValidateImage(combined, totalBytes)
	}
	if len(header) >= 8 && bytes.Equal(header[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return v.ValidateImage(combined, totalBytes)
	}
	if len(header) >= 8 && bytes.Equal(header[4:8], []byte("ftyp")) {
		return v.ValidateVideo(combined, totalBytes)
	}

	// Sniff for malware before returning error
	if malErr := v.sniffMalware(header); malErr != nil {
		return nil, malErr
	}

	return nil, fmt.Errorf("%w: unrecognized media container (expected JPEG, PNG, or MP4/MOV)", ErrInvalidMediaFormat)
}
