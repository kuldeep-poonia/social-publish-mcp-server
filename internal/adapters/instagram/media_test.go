package instagram

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMediaStager_LifecycleAndServing(t *testing.T) {
	tempDir := filepath.Join(os.TempDir(), "mcp_media_test_lifecycle")
	defer os.RemoveAll(tempDir)

	stager, err := NewMediaStager(tempDir, "https://api.example.com")
	if err != nil {
		t.Fatalf("failed creating MediaStager: %v", err)
	}
	defer stager.Close()

	dummyPayload := []byte("FAKE_JPEG_IMAGE_PAYLOAD_BYTES")
	pubURL, token, cleanup, err := stager.StageMedia(dummyPayload, "jpg", "image/jpeg")
	if err != nil {
		t.Fatalf("failed staging media: %v", err)
	}

	if token == "" {
		t.Fatal("expected non-empty token")
	}
	expectedURLPrefix := "https://api.example.com/media/ephemeral/"
	if len(pubURL) <= len(expectedURLPrefix) {
		t.Fatalf("unexpected pubURL format: %s", pubURL)
	}

	// Test HTTP Serving
	req := httptest.NewRequest(http.MethodGet, "/media/ephemeral/"+token+".jpg", nil)
	w := httptest.NewRecorder()
	stager.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from staged media, got %d", resp.StatusCode)
	}

	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff header")
	}
	if resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("expected Content-Type image/jpeg, got %s", resp.Header.Get("Content-Type"))
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != string(dummyPayload) {
		t.Errorf("served body does not match staged payload")
	}

	// Test Cleanup
	cleanup()

	// Subsequent request must 404
	w2 := httptest.NewRecorder()
	stager.ServeHTTP(w2, req)
	if w2.Result().StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after cleanup, got %d", w2.Result().StatusCode)
	}
}

func TestMediaStager_SecurityAndTraversalRejection(t *testing.T) {
	tempDir := filepath.Join(os.TempDir(), "mcp_media_test_traversal")
	defer os.RemoveAll(tempDir)

	stager, err := NewMediaStager(tempDir, "https://api.example.com")
	if err != nil {
		t.Fatalf("failed creating MediaStager: %v", err)
	}
	defer stager.Close()

	maliciousPaths := []string{
		"/media/ephemeral/..%2F..%2Fetc%2Fpasswd",
		"/media/ephemeral/invalid_token_with_symbols!",
		"/media/ephemeral/",
		"/media/ephemeral/short",
		"/media/ephemeral/1234567890123456789012345678901234567890_too_long",
	}

	for _, path := range maliciousPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		stager.ServeHTTP(w, req)

		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 for malicious path '%s', got %d", path, w.Result().StatusCode)
		}
	}
}
