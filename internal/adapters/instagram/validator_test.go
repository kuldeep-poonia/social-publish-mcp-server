package instagram

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// generateTestJPEG creates a real in-memory JPEG image with the specified width and height.
func generateTestJPEG(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 50, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

// generateTestPNG creates a real in-memory PNG image with the specified width and height.
func generateTestPNG(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 50, B: 150, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// generateTestMP4 creates a synthetic MP4 container with a valid ftyp and mvhd atom.
func generateTestMP4(durationSeconds uint32) []byte {
	var buf bytes.Buffer

	// 1. ftyp atom
	ftypPayload := []byte("isomiso2mp41")
	ftypSize := uint32(8 + len(ftypPayload))
	_ = binary.Write(&buf, binary.BigEndian, ftypSize)
	buf.WriteString("ftyp")
	buf.Write(ftypPayload)

	// 2. moov -> mvhd atom
	timescale := uint32(1000)
	duration := durationSeconds * timescale

	var mvhdPayload bytes.Buffer
	mvhdPayload.WriteByte(0)                  // version 0
	mvhdPayload.Write([]byte{0, 0, 0})        // flags
	mvhdPayload.Write(make([]byte, 8))        // create & mod time
	_ = binary.Write(&mvhdPayload, binary.BigEndian, timescale)
	_ = binary.Write(&mvhdPayload, binary.BigEndian, duration)
	mvhdPayload.Write(make([]byte, 80))       // rate, volume, matrix, etc.

	mvhdSize := uint32(8 + mvhdPayload.Len())
	var mvhdAtom bytes.Buffer
	_ = binary.Write(&mvhdAtom, binary.BigEndian, mvhdSize)
	mvhdAtom.WriteString("mvhd")
	mvhdAtom.Write(mvhdPayload.Bytes())

	moovSize := uint32(8 + mvhdAtom.Len())
	_ = binary.Write(&buf, binary.BigEndian, moovSize)
	buf.WriteString("moov")
	buf.Write(mvhdAtom.Bytes())

	return buf.Bytes()
}

func TestValidator_ValidImages(t *testing.T) {
	v := NewMediaValidator()

	testCases := []struct {
		name   string
		data   []byte
		ext    string
		minRat float64
		maxRat float64
	}{
		{"Square JPEG 1:1", generateTestJPEG(1080, 1080), "jpg", 0.99, 1.01},
		{"Portrait JPEG 4:5", generateTestJPEG(1080, 1350), "jpg", 0.79, 0.81},
		{"Landscape PNG 1.91:1", generateTestPNG(1200, 628), "png", 1.90, 1.92},
		{"Standard Landscape JPEG 16:9", generateTestJPEG(1920, 1080), "jpg", 1.76, 1.78},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := v.ValidateImage(bytes.NewReader(tc.data), int64(len(tc.data)))
			if err != nil {
				t.Fatalf("expected valid image, got error: %v", err)
			}
			if res.Extension != tc.ext {
				t.Errorf("expected extension '%s', got '%s'", tc.ext, res.Extension)
			}
			if res.Ratio < tc.minRat || res.Ratio > tc.maxRat {
				t.Errorf("expected ratio between %.2f and %.2f, got %.2f", tc.minRat, tc.maxRat, res.Ratio)
			}
		})
	}
}

func TestValidator_InvalidAspectRatios(t *testing.T) {
	v := NewMediaValidator()

	testCases := []struct {
		name string
		data []byte
	}{
		{"Extreme Vertical 1:3", generateTestJPEG(300, 900)},
		{"Extreme Landscape 4:1", generateTestPNG(1200, 300)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.ValidateImage(bytes.NewReader(tc.data), int64(len(tc.data)))
			if err == nil {
				t.Fatalf("expected aspect ratio validation error for '%s', got nil", tc.name)
			}
		})
	}
}

func TestValidator_ValidReelsVideo(t *testing.T) {
	v := NewMediaValidator()

	mp4Bytes := generateTestMP4(15)
	res, err := v.ValidateVideo(bytes.NewReader(mp4Bytes), int64(len(mp4Bytes)))
	if err != nil {
		t.Fatalf("expected valid MP4 reel, got error: %v", err)
	}
	if res.MediaType != MediaTypeReels {
		t.Errorf("expected MediaType '%s', got '%s'", MediaTypeReels, res.MediaType)
	}
	if res.Duration < 14.9 || res.Duration > 15.1 {
		t.Errorf("expected duration ~15s, got %.2fs", res.Duration)
	}
}

func TestValidator_MalwareSignatures(t *testing.T) {
	v := NewMediaValidator()

	malwareProbes := []struct {
		name    string
		payload []byte
	}{
		{name: "Windows PE / EXE Header", payload: append([]byte{0x4D, 0x5A, 0x90, 0x00}, bytes.Repeat([]byte{0x00}, 50)...)},
		{name: "Linux ELF Header", payload: append([]byte{0x7F, 0x45, 0x4C, 0x46, 0x02, 0x01}, bytes.Repeat([]byte{0x00}, 50)...)},
		{name: "macOS Mach-O 64-bit Header", payload: append([]byte{0xFE, 0xED, 0xFA, 0xCF}, bytes.Repeat([]byte{0x00}, 50)...)},
		{name: "Shell Script Execution", payload: []byte{0x23, 0x21, 0x2F, 0x62, 0x69, 0x6E, 0x2F, 0x73, 0x68, 0x0A}},
		{name: "Embedded HTML Script Tag", payload: []byte{0xFF, 0xD8, 0xFF, 0x3C, 0x73, 0x63, 0x72, 0x69, 0x70, 0x74, 0x3E}},
		{name: "Embedded PHP Tag", payload: []byte{0xFF, 0xD8, 0xFF, 0x3C, 0x3F, 0x70, 0x68, 0x70}},
	}

	for _, probe := range malwareProbes {
		t.Run(probe.name, func(t *testing.T) {
			_, err := v.ValidateImage(bytes.NewReader(probe.payload), int64(len(probe.payload)))
			if err == nil {
				t.Fatalf("SECURITY FLAW: malware probe '%s' was NOT rejected by ValidateImage", probe.name)
			}

			_, err = v.ValidateVideo(bytes.NewReader(probe.payload), int64(len(probe.payload)))
			if err == nil {
				t.Fatalf("SECURITY FLAW: malware probe '%s' was NOT rejected by ValidateVideo", probe.name)
			}
		})
	}
}

func TestValidator_OversizedMedia(t *testing.T) {
	v := NewMediaValidator()

	// Image > 8 MB
	_, err := v.ValidateImage(bytes.NewReader([]byte{0xFF, 0xD8, 0xFF}), MaxImageSizeBytes+1)
	if err == nil {
		t.Fatal("expected oversized image rejection, got nil")
	}

	// Video > 100 MB
	_, err = v.ValidateVideo(bytes.NewReader([]byte("\x00\x00\x00\x20ftypisom")), MaxVideoSizeBytes+1)
	if err == nil {
		t.Fatal("expected oversized video rejection, got nil")
	}
}
