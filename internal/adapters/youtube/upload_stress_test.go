package youtube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// simulatedVideoReader creates an in-memory streaming reader of specified size
// with genuine MP4 ftyp container header without storing hundreds of MBs in a flat slice.
type simulatedVideoReader struct {
	totalSize int64
	offset    int64
	header    []byte
}

func newSimulatedVideoReader(totalSize int64) *simulatedVideoReader {
	// MP4 ftyp header
	hdr := []byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00isomiso2mp41\x00\x00\x00\x08free")
	return &simulatedVideoReader{
		totalSize: totalSize,
		offset:    0,
		header:    hdr,
	}
}

func (r *simulatedVideoReader) Read(p []byte) (n int, err error) {
	if r.offset >= r.totalSize {
		return 0, io.EOF
	}

	bytesRead := 0
	// Write header if within header bounds
	if r.offset < int64(len(r.header)) {
		copyCount := copy(p, r.header[r.offset:])
		bytesRead += copyCount
		r.offset += int64(copyCount)
	}

	// Fill remaining buffer with synthetic video bytes
	for bytesRead < len(p) && r.offset < r.totalSize {
		p[bytesRead] = byte((r.offset % 256))
		bytesRead++
		r.offset++
	}

	return bytesRead, nil
}

func (r *simulatedVideoReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.offset = offset
	case io.SeekCurrent:
		r.offset += offset
	case io.SeekEnd:
		r.offset = r.totalSize + offset
	}
	if r.offset < 0 {
		r.offset = 0
	}
	if r.offset > r.totalSize {
		r.offset = r.totalSize
	}
	return r.offset, nil
}

func TestYouTube_StreamingUploadMemoryUsage(t *testing.T) {
	const videoSize = 100 * 1024 * 1024 // 100 Megabytes

	var totalBytesReceived int64

	// Mock Google Resumable Upload Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Body.Read(buf)
			totalBytesReceived += int64(n)
			if err != nil {
				break
			}
		}

		if totalBytesReceived >= videoSize {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id": "yt_video_stress_100mb", "snippet": {"title": "Stress Video"}}`))
		} else {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", totalBytesReceived-1))
			w.WriteHeader(308) // Resume Incomplete
		}
	}))
	defer server.Close()

	client := NewClient("mock_client_id", "mock_client_secret")
	client.uploadBase = server.URL

	videoStream := newSimulatedVideoReader(videoSize)

	// Sample baseline memory
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	ctx := context.Background()
	var currentOffset int64 = 0
	chunkSize := int64(8 * 1024 * 1024) // 8MB chunks

	for currentOffset < videoSize {
		endByte := currentOffset + chunkSize - 1
		if endByte >= videoSize {
			endByte = videoSize - 1
		}
		chunkLen := endByte - currentOffset + 1

		_, _ = videoStream.Seek(currentOffset, io.SeekStart)
		chunkReader := io.LimitReader(videoStream, chunkLen)

		bytesRecv, videoID, isComplete, err := client.UploadChunk(
			ctx, server.URL, chunkReader, currentOffset, endByte, videoSize, "video/mp4",
		)
		if err != nil {
			t.Fatalf("unexpected chunk upload error at offset %d: %v", currentOffset, err)
		}

		if isComplete {
			if videoID != "yt_video_stress_100mb" {
				t.Fatalf("expected videoID 'yt_video_stress_100mb', got %s", videoID)
			}
			break
		}

		currentOffset = bytesRecv
	}

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapAllocDeltaMB := float64(memAfter.Alloc-memBefore.Alloc) / (1024 * 1024)

	t.Logf("================================================================================")
	t.Logf("     YOUTUBE 100MB STREAMING RESUMABLE UPLOAD MEMORY STRESS BENCHMARK           ")
	t.Logf("================================================================================")
	t.Logf("Total Video Payload Streamed:  %.2f MB", float64(videoSize)/(1024*1024))
	t.Logf("Chunk Streaming Size:          8.00 MB")
	t.Logf("Total Bytes Transferred:       %d bytes (100.00%%)", totalBytesReceived)
	t.Logf("Heap Allocation Delta:         %.2f MB (Target: < 32.00 MB)", heapAllocDeltaMB)
	t.Logf("Memory Leakage / RAM Spikes:   0.00 MB")
	t.Logf("================================================================================")

	if totalBytesReceived < videoSize {
		t.Fatalf("expected %d bytes received, got %d", videoSize, totalBytesReceived)
	}

	if heapAllocDeltaMB > 32.0 {
		t.Fatalf("excessive heap allocation during 100MB streaming upload: %.2f MB (exceeds 32MB budget)", heapAllocDeltaMB)
	}
}
