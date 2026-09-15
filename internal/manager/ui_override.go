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
// restart like any other code change. The self-update also stages the release's
// ui/index.html next to the private state; this file serves that copy when it exists, which
// makes interface-only updates take effect on a page refresh. The library itself still
// requires a restart, which the UI reports separately.
//
// A staged copy is only served while it belongs to a release that is strictly newer than
// the running library. A host plugin-store install replaces the .so without touching this
// directory, so without that rule an override left behind by an older release would keep
// masking the newer interface the running library already carries.
const (
	uiOverrideFileName = "index.html"
	// uiOverrideVersionFileName is the sidecar recording which release staged the interface.
	// An override written before this bookkeeping existed (an older release) has no sidecar
	// and is therefore never served.
	uiOverrideVersionFileName = "index.version"
	uiOverrideMaxBytes        = 32 << 20
	// uiOverrideMaxVersionBytes bounds the sidecar read: a release version is a few bytes, so
	// a larger file is corrupt and must not be parsed.
	uiOverrideMaxVersionBytes = 128
	// uiIndexArchiveMember is the path the release pipeline puts the built interface under.
	uiIndexArchiveMember = "ui/index.html"
)

// uiOverrideNote* are fixed, sanitized notes explaining why a staged interface was ignored.
// They are surfaced through the self-update snapshot so a stale override is diagnosable
// instead of silent. They intentionally carry no path and no file content.
const (
	uiOverrideNoteUnusable  = "the staged interface file could not be read"
	uiOverrideNoteNoVersion = "the staged interface has no usable release version"
	uiOverrideNoteStale     = "the staged interface is not newer than the running library"
)

// uiOverrideLookup caches the resolved file so a page load does not re-stat on every asset
// request, while still noticing a staged replacement. Both the interface file and its
// version sidecar are tracked, because either one changing must invalidate the decision.
type uiOverrideLookup struct {
	mu       sync.Mutex
	path     string
	loadedAt time.Time
	found    bool

	fileExists  bool
	fileSize    int64
	fileModTime time.Time

	versionExists  bool
	versionSize    int64
	versionModTime time.Time
}

var uiOverrideCache uiOverrideLookup

// uiOverrideNotes holds the latest override decision for diagnostics.
var uiOverrideNotes = struct {
	mu   sync.Mutex
	note string
}{}

const uiOverrideStatInterval = 2 * time.Second

// uiOverridePath returns the staged interface file for one data directory.
func uiOverridePath(dataDir string) string {
	trimmed := strings.TrimSpace(dataDir)
	if trimmed == "" {
		return ""
	}
	return filepath.Join(trimmed, "ui", uiOverrideFileName)
}

// uiOverrideVersionPath returns the release-version sidecar staged next to the interface.
func uiOverrideVersionPath(dataDir string) string {
	path := uiOverridePath(dataDir)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), uiOverrideVersionFileName)
}

// readUIOverride returns the staged interface when it is present, usable and newer than the
// running library. The embedded copy stays authoritative for anything that is missing,
// empty, implausibly large, not HTML, or not recorded as belonging to a newer release.
// A read failure degrades to the embedded copy instead of surfacing an error.
func readUIOverride(dataDir string) ([]byte, bool) {
	path := uiOverridePath(dataDir)
	if path == "" {
		return nil, false
	}
	versionPath := uiOverrideVersionPath(dataDir)
	uiOverrideCache.mu.Lock()
	defer uiOverrideCache.mu.Unlock()
	now := time.Now()
	if uiOverrideCache.path == path && now.Sub(uiOverrideCache.loadedAt) < uiOverrideStatInterval {
		if !uiOverrideCache.stateMatches(path, versionPath) {
			// Either the interface or its sidecar changed; resolve it again below.
			uiOverrideCache.loadedAt = time.Time{}
		} else {
			if !uiOverrideCache.found {
				return nil, false
			}
			payload, errRead := os.ReadFile(path)
			if errRead == nil && len(payload) > 0 {
				return payload, true
			}
			uiOverrideCache.loadedAt = time.Time{}
		}
	}
	uiOverrideCache.path = path
	uiOverrideCache.loadedAt = now
	uiOverrideCache.found = false
	uiOverrideCache.fileExists, uiOverrideCache.fileSize, uiOverrideCache.fileModTime = uiOverrideFileState(path)
	uiOverrideCache.versionExists, uiOverrideCache.versionSize, uiOverrideCache.versionModTime = uiOverrideFileState(versionPath)
	if !uiOverrideCache.fileExists {
		// No staged interface at all (or a directory): nothing to report.
		setUIOverrideNote("")
		return nil, false
	}
	if uiOverrideCache.fileSize <= 0 || uiOverrideCache.fileSize > uiOverrideMaxBytes {
		setUIOverrideNote(uiOverrideNoteUnusable)
		return nil, false
	}
	// The version is read by its own path so an unreadable sidecar simply reads as absent,
	// which is the same verdict as an override written by an older release.
	version, _ := readUIOverrideVersion(versionPath)
	payload, errRead := os.ReadFile(path)
	if errRead != nil || len(payload) == 0 || !looksLikeHTMLDocument(payload) {
		setUIOverrideNote(uiOverrideNoteUnusable)
		return nil, false
	}
	if !uiOverrideVersionIsNewer(version) {
		setUIOverrideNote(uiOverrideVersionNote(version))
		return nil, false
	}
	setUIOverrideNote("")
	uiOverrideCache.found = true
	return payload, true
}

// stateMatches reports whether the tracked interface and sidecar still match the cached
// metadata. Either file appearing, disappearing or changing invalidates the cache.
func (l *uiOverrideLookup) stateMatches(path, versionPath string) bool {
	fileExists, fileSize, fileModTime := uiOverrideFileState(path)
	if fileExists != l.fileExists || fileSize != l.fileSize || !fileModTime.Equal(l.fileModTime) {
		return false
	}
	versionExists, versionSize, versionModTime := uiOverrideFileState(versionPath)
	return versionExists == l.versionExists && versionSize == l.versionSize && versionModTime.Equal(l.versionModTime)
}

// uiOverrideFileState captures the bounded metadata the lookup cache compares. A directory
// counts as absent: the override is only ever a regular file.
func uiOverrideFileState(path string) (bool, int64, time.Time) {
	info, errStat := os.Stat(path)
	if errStat != nil || info.IsDir() {
		return false, 0, time.Time{}
	}
	return true, info.Size(), info.ModTime()
}

// readUIOverrideVersion reads the release version staged next to the interface. A missing,
// empty, oversized or unreadable sidecar yields an empty version and an error, which the
// caller treats as "written by an older release" and ignores.
func readUIOverrideVersion(path string) (string, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return "", errStat
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > uiOverrideMaxVersionBytes {
		return "", fmt.Errorf("the staged interface version file is unusable")
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return "", errRead
	}
	return strings.TrimSpace(string(raw)), nil
}

// uiOverrideVersionIsNewer reports whether a staged interface belongs to a release strictly
// newer than the running library, which is the only case in which the running library cannot
// already carry that interface itself. A missing, empty or unparsable version reads as older:
// the running library is the thing that actually knows its own interface age.
func uiOverrideVersionIsNewer(version string) bool {
	if !uiOverrideVersionLooksComparable(version) {
		return false
	}
	return compareVersions(version, PluginVersion) > 0
}

// uiOverrideVersionNote classifies why an override may not be served in fixed, sanitized
// terms. It never echoes the recorded string, which could be arbitrary file content.
func uiOverrideVersionNote(version string) string {
	if !uiOverrideVersionLooksComparable(version) {
		return uiOverrideNoteNoVersion
	}
	return uiOverrideNoteStale
}

// uiOverrideVersionLooksComparable accepts the plain semver-like strings releases carry.
// Anything else (a truncated sidecar, arbitrary bytes) is treated as missing, because
// comparing it would silently read as 0.0.0 and a running 0.0.0-dev library would then serve
// a stranger's age.
func uiOverrideVersionLooksComparable(version string) bool {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if trimmed == "" {
		return false
	}
	digits := 0
	for _, char := range trimmed {
		switch {
		case char >= '0' && char <= '9':
			digits++
		case char == '.' || char == '-' || char == '+':
		default:
			return false
		}
	}
	return digits > 0
}

// setUIOverrideNote records the latest override decision for diagnostics. Only the fixed
// note constants above are ever stored; a path or file content never is.
func setUIOverrideNote(note string) {
	uiOverrideNotes.mu.Lock()
	uiOverrideNotes.note = note
	uiOverrideNotes.mu.Unlock()
}

// uiOverrideDiagnostic returns the last override decision, if any, for the self-update
// snapshot. An empty string means the staged interface was either served or is absent.
func uiOverrideDiagnostic() string {
	uiOverrideNotes.mu.Lock()
	defer uiOverrideNotes.mu.Unlock()
	return uiOverrideNotes.note
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

// stageUIOverride writes the interface that came with a release next to the release version
// that decides whether it may be served. It replaces the served page atomically and verifies
// the payload first, so a bad extract never breaks the UI. The sidecar is written after the
// payload: if that write fails the override is ignored by readers, which is safer than
// serving a file whose age is unknown.
func stageUIOverride(dataDir, version string, payload []byte) (string, error) {
	path := uiOverridePath(dataDir)
	if path == "" {
		return "", fmt.Errorf("the data directory is unknown")
	}
	trimmedVersion := strings.TrimSpace(version)
	if !uiOverrideVersionLooksComparable(trimmedVersion) {
		return "", fmt.Errorf("the release version is unknown")
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
	if errVersion := writePrivateFileAtomically(uiOverrideVersionPath(dataDir), []byte(trimmedVersion)); errVersion != nil {
		return "", errVersion
	}
	uiOverrideCache.mu.Lock()
	uiOverrideCache.loadedAt = time.Time{}
	uiOverrideCache.mu.Unlock()
	return path, nil
}
