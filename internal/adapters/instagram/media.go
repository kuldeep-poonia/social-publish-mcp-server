// Package instagram provides the ephemeral media server and stager for Meta Graph API media crawling.
package instagram

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	// validTokenRegex strictly enforces 32-hex/UUID format to prevent path traversal
	validTokenRegex = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

const (
	// EphemeralHardTTL specifies the maximum time an uncleaned staged file may persist on disk
	EphemeralHardTTL = 2 * time.Hour
)

type stagedItem struct {
	token     string
	filePath  string
	mimeType  string
	createdAt time.Time
}

// MediaStager manages short-lived staged media files served to Meta's ingestion crawlers.
type MediaStager struct {
	mu         sync.RWMutex
	staged     map[string]*stagedItem
	baseDir    string
	publicBase string
	stopSweep  chan struct{}
}

// NewMediaStager initializes a MediaStager with a local storage directory and public URL base.
func NewMediaStager(baseDir, publicBase string) (*MediaStager, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "mcp_instagram_ephemeral")
	}
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed creating ephemeral media directory: %w", err)
	}

	stager := &MediaStager{
		staged:     make(map[string]*stagedItem),
		baseDir:    baseDir,
		publicBase: strings.TrimRight(publicBase, "/"),
		stopSweep:  make(chan struct{}),
	}

	go stager.backgroundSweeper()
	return stager, nil
}

// Close terminates the background sweeper and cleans up all active staged files.
func (m *MediaStager) Close() {
	close(m.stopSweep)
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, item := range m.staged {
		_ = os.Remove(item.filePath)
	}
	m.staged = make(map[string]*stagedItem)
}

// StageMedia writes binary media bytes to disk and returns a publicly-accessible HTTPS URL.
func (m *MediaStager) StageMedia(data []byte, ext, mimeType string) (publicURL string, token string, cleanup func(), err error) {
	if len(data) == 0 {
		return "", "", nil, errors.New("cannot stage empty media payload")
	}

	rawBytes := make([]byte, 16)
	if _, err := rand.Read(rawBytes); err != nil {
		return "", "", nil, fmt.Errorf("failed generating random token: %w", err)
	}
	tok := hex.EncodeToString(rawBytes)

	cleanExt := strings.TrimPrefix(ext, ".")
	if cleanExt == "" {
		cleanExt = "jpg"
	}
	fileName := fmt.Sprintf("%s.%s", tok, cleanExt)
	filePath := filepath.Join(m.baseDir, fileName)

	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return "", "", nil, fmt.Errorf("failed writing staged media file: %w", err)
	}

	item := &stagedItem{
		token:     tok,
		filePath:  filePath,
		mimeType:  mimeType,
		createdAt: time.Now().UTC(),
	}

	m.mu.Lock()
	m.staged[tok] = item
	m.mu.Unlock()

	pubURL := fmt.Sprintf("%s/media/ephemeral/%s", m.publicBase, fileName)
	cleanupFunc := func() {
		m.Remove(tok)
	}

	return pubURL, tok, cleanupFunc, nil
}

// Remove deletes a staged file from disk and unregisters it from the tracking map.
func (m *MediaStager) Remove(token string) {
	m.mu.Lock()
	item, exists := m.staged[token]
	if exists {
		delete(m.staged, token)
	}
	m.mu.Unlock()

	if exists && item != nil {
		_ = os.Remove(item.filePath)
	}
}

// ServeHTTP handles incoming HTTP requests from Meta's ingestion crawler.
func (m *MediaStager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract token from URL path: /media/ephemeral/{token}.{ext}
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) == 0 {
		http.NotFound(w, r)
		return
	}
	rawFileName := pathParts[len(pathParts)-1]
	dotIdx := strings.Index(rawFileName, ".")
	rawToken := rawFileName
	if dotIdx > 0 {
		rawToken = rawFileName[:dotIdx]
	}

	// Security: enforce strict token regex to prevent directory traversal
	if !validTokenRegex.MatchString(rawToken) {
		http.NotFound(w, r)
		return
	}

	m.mu.RLock()
	item, exists := m.staged[rawToken]
	m.mu.RUnlock()

	if !exists || item == nil {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(item.filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Security headers: strictly prevent MIME sniffing and directory exposure
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache, no-store, must-revalidate")
	if item.mimeType != "" {
		w.Header().Set("Content-Type", item.mimeType)
	}

	http.ServeContent(w, r, rawFileName, stat.ModTime(), file)
}

// backgroundSweeper periodically reclaims orphaned temporary files that exceeded EphemeralHardTTL.
func (m *MediaStager) backgroundSweeper() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopSweep:
			return
		case <-ticker.C:
			now := time.Now().UTC()
			var expiredTokens []string

			m.mu.RLock()
			for tok, item := range m.staged {
				if now.Sub(item.createdAt) > EphemeralHardTTL {
					expiredTokens = append(expiredTokens, tok)
				}
			}
			m.mu.RUnlock()

			for _, tok := range expiredTokens {
				m.Remove(tok)
			}
		}
	}
}
