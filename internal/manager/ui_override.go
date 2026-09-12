package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The interface is compiled into the plugin library, so a UI change used to need a CPA
// restart like any other code change. The self-update now also stages the release's
// ui/index.html next to the private state; this file serves that copy when it exists, which
// makes interface-only updates take effect on a page refresh. The library itself still
// requires a restart, which the UI reports separately.
const (
	uiOverrideFileName = "index.html"
	uiOverrideMaxBytes = 32 << 20
	// uiIndexArchiveMember is the path the release pipeline puts the built interface under.
	uiIndexArchiveMember = "ui/index.html"
)

// uiOverrideLookup caches the resolved file so a page load does not re-stat on every asset
// request, while still noticing a staged replacement.
type uiOverrideLookup struct {
	mu       sync.Mutex
	path     string
	loadedAt time.Time
	size     int64
	modTime  time.Time
	found    bool
}

var uiOverrideCache uiOverrideLookup

const uiOverrideStatInterval = 2 * time.Second

// uiOverridePath returns the staged interface file for one data directory.
func uiOverridePath(dataDir string) string {
	trimmed := strings.TrimSpace(dataDir)
	if trimmed == "" {
		return ""
	}
	return filepath.Join(trimmed, "ui", uiOverrideFileName)
}

// readUIOverride returns the staged interface when it is present and usable. The embedded
// copy stays authoritative for anything that is missing, empty or implausibly large.
func readUIOverride(dataDir string) ([]byte, bool) {
	path := uiOverridePath(dataDir)
	if path == "" {
		return nil, false
	}
	uiOverrideCache.mu.Lock()
	defer uiOverrideCache.mu.Unlock()
	now := time.Now()
	if uiOverrideCache.path == path && now.Sub(uiOverrideCache.loadedAt) < uiOverrideStatInterval {
		if !uiOverrideCache.found {
			return nil, false
		}
		info, errStat := os.Stat(path)
		if errStat != nil || info.Size() != uiOverrideCache.size || !info.ModTime().Equal(uiOverrideCache.modTime) {
			uiOverrideCache.loadedAt = time.Time{}
		} else {
			payload, errRead := os.ReadFile(path)
			if errRead == nil && len(payload) > 0 {
				return payload, true
			}
		}
	}
	uiOverrideCache.path = path
	uiOverrideCache.loadedAt = now
	uiOverrideCache.found = false
	info, errStat := os.Stat(path)
	if errStat != nil || info.IsDir() || info.Size() <= 0 || info.Size() > uiOverrideMaxBytes {
		return nil, false
	}
	payload, errRead := os.ReadFile(path)
	if errRead != nil || len(payload) == 0 || !looksLikeHTMLDocument(payload) {
		return nil, false
	}
	uiOverrideCache.size = info.Size()
	uiOverrideCache.modTime = info.ModTime()
	uiOverrideCache.found = true
	return payload, true
}

// looksLikeHTMLDocument rejects a truncated or unrelated file before it can be served as a
// page, so a failed staging step degrades to the embedded interface instead of a blank tab.
func looksLikeHTMLDocument(payload []byte) bool {
	window := payload
	if len(window) > 4096 {
		window = window[:4096]
	}
	lower := strings.ToLower(string(window))
	return strings.Contains(lower, "<!doctype html") || strings.Contains(lower, "<html")
}

// stageUIOverride writes the interface that came with a release. It replaces the served
// page atomically and verifies the payload first, so a bad extract never breaks the UI.
func stageUIOverride(dataDir string, payload []byte) (string, error) {
	path := uiOverridePath(dataDir)
	if path == "" {
		return "", fmt.Errorf("the data directory is unknown")
	}
	if len(payload) == 0 {
		return "", fmt.Errorf("the interface file is empty")
	}
	if len(payload) > uiOverrideMaxBytes {
		return "", fmt.Errorf("the interface file is too large")
	}
	if !looksLikeHTMLDocument(payload) {
		return "", fmt.Errorf("the interface file is not an HTML document")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return "", errMkdir
	}
	if errWrite := writePrivateFileAtomically(path, payload); errWrite != nil {
		return "", errWrite
	}
	uiOverrideCache.mu.Lock()
	uiOverrideCache.loadedAt = time.Time{}
	uiOverrideCache.mu.Unlock()
	return path, nil
}
