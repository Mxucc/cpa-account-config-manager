package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The plugin updates itself directly from its GitHub releases. The host's plugin
// marketplace is an optional convenience, but it is a single point of failure for
// installations whose marketplace request never succeeds, so this service keeps an
// independent path: resolve the latest release, download the archive for the
// running platform, verify it against the release checksums, and stage or apply it.
//
// The plugin library is already loaded by the host process, so a replaced file only
// takes effect after CPA restarts. The service therefore never claims a running
// upgrade: it reports restart_required and keeps a backup of the previous library.

const (
	selfUpdateStoreFile = "self-update.json"
	selfUpdateVersion   = 1

	// The first check runs shortly after the host loads the plugin instead of after a
	// full interval, so a restarted instance reports the real release state immediately.
	selfUpdateStartupDelay  = 20 * time.Second
	selfUpdateCheckInterval = 6 * time.Hour
	selfUpdateTimeout       = 60 * time.Second
	selfUpdateMaxArchive    = 256 << 20
	selfUpdateMaxChecksums  = 1 << 20
	selfUpdateMaxLibrary    = 512 << 20

	selfUpdateSourceAPI      = "github_api"
	selfUpdateSourceRedirect = "github_redirect"
	selfUpdateSourceAtom     = "github_atom"

	selfUpdateStagedFileName = "update-staged"
)

// SelfUpdateSnapshot is the redacted self-update state exposed to the UI.
type SelfUpdateSnapshot struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	Source          string    `json:"source,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	AssetName       string    `json:"asset_name,omitempty"`
	AssetURL        string    `json:"asset_url,omitempty"`
	AssetBytes      int64     `json:"asset_bytes,omitempty"`
	ArchiveSHA256   string    `json:"archive_sha256,omitempty"`
	ChecksumOK      bool      `json:"checksum_ok"`
	// PluginFile is the library this plugin was loaded from, when it could be
	// located. PluginFileSource explains how: "setting", "proc" or "search".
	PluginFile       string `json:"plugin_file,omitempty"`
	PluginFileSource string `json:"plugin_file_source,omitempty"`
	// PluginFileExists reports whether the recorded library is present, so the UI
	// can warn about a stale path instead of offering a download that cannot apply.
	PluginFileExists bool `json:"plugin_file_exists"`
	// StagedPath is set when an update was downloaded but not applied.
	StagedPath      string `json:"staged_path,omitempty"`
	AppliedVersion  string `json:"applied_version,omitempty"`
	BackupPath      string `json:"backup_path,omitempty"`
	RestartRequired bool   `json:"restart_required"`
	// UIUpdated reports that the release also delivered the interface, which is served from
	// disk on the next page refresh even though the library still needs a restart.
	UIUpdated bool `json:"ui_updated"`
	// PendingRestart is derived: the library written to disk is newer than the one this process
	// loaded, so only a restart (or a reload) can activate it. It becomes false by itself once
	// the process runs the version that is on disk, which is what a persisted applied_version
	// alone could never express.
	PendingRestart       bool   `json:"pending_restart"`
	UIPath               string `json:"ui_path,omitempty"`
	InterfaceRefreshOnly bool   `json:"interface_refresh_only"`
	CanInstall           bool   `json:"can_install"`
	Error                string `json:"error,omitempty"`
	StorageError         string `json:"storage_error,omitempty"`
}

type persistedSelfUpdate struct {
	Version        int       `json:"version"`
	PluginFile     string    `json:"plugin_file,omitempty"`
	StagedPath     string    `json:"staged_path,omitempty"`
	AppliedVersion string    `json:"applied_version,omitempty"`
	BackupPath     string    `json:"backup_path,omitempty"`
	CheckedAt      time.Time `json:"checked_at,omitempty"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	Source         string    `json:"source,omitempty"`
}

// SelfUpdateService resolves and applies direct GitHub updates.
type SelfUpdateService struct {
	mu sync.Mutex
	// installMu serializes the download-and-replace sequence: the in-memory mu only guards
	// fields, and two concurrent installs would otherwise interleave their staged writes.
	installMu sync.Mutex
	config    Config
	store     string
	doer      HTTPDoer
	client    *http.Client
	current   string
	repoSlug  string
	loaded    bool
	policy    persistedSelfUpdate
	state     SelfUpdateSnapshot
	storageEr string
	now       func() time.Time
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func NewSelfUpdateService(currentVersion string) *SelfUpdateService {
	service := &SelfUpdateService{
		client: &http.Client{
			Timeout: selfUpdateTimeout,
			// Updates may follow GitHub's asset redirects, but never to an
			// arbitrary host.
			CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				if !selfUpdateHostAllowed(request.URL) {
					return fmt.Errorf("refusing to follow an update redirect to %s", request.URL.Host)
				}
				return nil
			},
		},
		current:  strings.TrimSpace(currentVersion),
		repoSlug: selfUpdateRepoSlug(PluginRepository),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		now:      func() time.Time { return time.Now().UTC() },
		state: SelfUpdateSnapshot{
			CurrentVersion: strings.TrimSpace(currentVersion),
		},
	}
	if service.repoSlug == "" {
		service.state.Error = "the plugin repository is not configured"
	}
	go service.run()
	return service
}

// selfUpdateRepoSlug converts the configured repository URL into "owner/name".
func selfUpdateRepoSlug(repository string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(repository), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil || !strings.EqualFold(parsed.Host, "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// selfUpdateHostAllowed restricts downloads to GitHub's own hosts.
func selfUpdateHostAllowed(target *url.URL) bool {
	if target == nil {
		return false
	}
	host := strings.ToLower(target.Hostname())
	if strings.EqualFold(target.Scheme, "https") {
		switch {
		case host == "github.com", host == "api.github.com", host == "objects.githubusercontent.com", host == "codeload.github.com":
			return true
		case strings.HasSuffix(host, ".githubusercontent.com"):
			return true
		}
	}
	return false
}

func selfUpdateStorePath(dataDir string) string {
	return filepath.Join(dataDir, selfUpdateStoreFile)
}

func (s *SelfUpdateService) Configure(config Config) {
	if s == nil {
		return
	}
	config = normalizeConfig(config)
	storePath := selfUpdateStorePath(config.DataDir)
	s.mu.Lock()
	changed := !s.loaded || s.store != storePath
	s.config, s.store, s.loaded = config, storePath, true
	if !changed {
		s.mu.Unlock()
		return
	}
	if raw, errRead := os.ReadFile(storePath); errRead == nil {
		var persisted persistedSelfUpdate
		if errDecode := json.Unmarshal(raw, &persisted); errDecode == nil && persisted.Version == selfUpdateVersion {
			s.policy = persisted
			s.state.LatestVersion = persisted.LatestVersion
			s.state.Source = persisted.Source
			s.state.CheckedAt = persisted.CheckedAt
			s.state.StagedPath = persisted.StagedPath
			s.state.AppliedVersion = persisted.AppliedVersion
			s.state.BackupPath = persisted.BackupPath
			s.refreshDerivedLocked()
			s.storageEr = ""
		} else {
			s.storageEr = "the self-update state could not be read"
		}
	} else if !errors.Is(errRead, os.ErrNotExist) {
		s.storageEr = "the self-update state could not be read"
	}
	s.mu.Unlock()
}

// SetPluginFile records the plugin library path when the operator overrides
// auto-detection.
func (s *SelfUpdateService) SetPluginFile(path string) (SelfUpdateSnapshot, error) {
	if s == nil {
		return SelfUpdateSnapshot{}, ErrSelfUpdateUnavailable
	}
	s.mu.Lock()
	trimmed := strings.TrimSpace(path)
	// A recorded path is what Install will replace, so refuse anything that is clearly not
	// a plugin library instead of letting a typo rename an unrelated file.
	if trimmed != "" && !selfUpdateLibraryTarget(trimmed) {
		s.mu.Unlock()
		return s.Snapshot(), fmt.Errorf("the plugin library path must point to a %s library", PluginID)
	}
	s.policy.PluginFile = trimmed
	errPersist := s.persistLocked()
	s.mu.Unlock()
	if errPersist != nil {
		return s.Snapshot(), errPersist
	}
	return s.Snapshot(), nil
}

var ErrSelfUpdateUnavailable = errors.New("self update is unavailable")

func (s *SelfUpdateService) persistLocked() error {
	if strings.TrimSpace(s.store) == "" {
		return ErrSelfUpdateUnavailable
	}
	s.policy.Version = selfUpdateVersion
	encoded, errEncode := json.Marshal(s.policy)
	if errEncode != nil {
		return errEncode
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.store), 0o700); errMkdir != nil {
		s.storageEr = "the self-update state could not be saved"
		return errMkdir
	}
	if errWrite := writePrivateFileAtomically(s.store, encoded); errWrite != nil {
		s.storageEr = "the self-update state could not be saved"
		return errWrite
	}
	s.storageEr = ""
	return nil
}

// Snapshot reports the current self-update state.
func (s *SelfUpdateService) Snapshot() SelfUpdateSnapshot {
	if s == nil {
		return SelfUpdateSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshDerivedLocked()
	snapshot := s.state
	snapshot.CurrentVersion = firstNonEmpty(strings.TrimSpace(s.current), snapshot.CurrentVersion)
	snapshot.StorageError = s.storageEr
	pluginFile, source := s.resolvePluginFileLocked()
	snapshot.PluginFile, snapshot.PluginFileSource = pluginFile, source
	snapshot.PluginFileExists = selfUpdateFileExists(pluginFile)
	snapshot.CanInstall = s.repoSlug != "" && pluginFile != "" && snapshot.PluginFileExists
	return snapshot
}

// refreshDerivedLocked recomputes everything that follows from the resolved release and
// the running version. A state restored from disk must not claim "up to date" merely
// because no check has run in this process yet, and a library that was already replaced
// must not keep offering the same download again.
func (s *SelfUpdateService) refreshDerivedLocked() {
	latest := normalizePluginVersion(s.state.LatestVersion)
	if latest == "" {
		s.state.UpdateAvailable = false
		s.state.AssetName = ""
		s.state.AssetURL = ""
		return
	}
	s.state.LatestVersion = latest
	applied := normalizePluginVersion(s.state.AppliedVersion)
	s.state.PendingRestart = applied != "" && compareVersions(applied, s.current) > 0
	if !s.state.PendingRestart {
		// Either nothing was applied, or this process already runs what is on disk.
		s.state.RestartRequired = false
	}
	s.state.UpdateAvailable = latest != applied && compareVersions(latest, s.current) > 0
	s.state.AssetName = selfUpdateAssetName(latest)
	s.state.AssetURL = s.assetURL(latest)
}

// selfUpdateLibraryTarget reports whether a path may be replaced by a downloaded library.
// The name must still identify this plugin, so a mistyped or hostile setting cannot turn
// an unrelated file into the update target.
func selfUpdateLibraryTarget(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, PluginID) && selfUpdateLibraryExtension(filepath.Ext(base))
}

// resolvePluginFileLocked locates the loaded plugin library: an explicit setting
// wins, then the process memory map (Linux), then a bounded directory search.
func (s *SelfUpdateService) resolvePluginFileLocked() (string, string) {
	if configured := strings.TrimSpace(s.policy.PluginFile); configured != "" {
		// An explicit setting is always reported, even when the file is missing:
		// the operator must be able to see and correct the recorded path.
		return configured, "setting"
	}
	if detected := selfUpdatePluginFileFromProcess(); detected != "" {
		return detected, "proc"
	}
	for _, directory := range selfUpdateSearchDirectories(s.config) {
		if found := selfUpdateFindLibrary(directory); found != "" {
			return found, "search"
		}
	}
	return "", ""
}

// selfUpdateFileExists reports whether a located plugin library is really present.
func selfUpdateFileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, errStat := os.Stat(path)
	return errStat == nil && !info.IsDir()
}

// selfUpdatePluginFileFromProcess reads the memory map for the loaded library. It
// only works on Linux, where /proc exposes the mapped file names.
func selfUpdatePluginFileFromProcess() string {
	raw, errRead := os.ReadFile("/proc/self/maps")
	if errRead != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		path := fields[len(fields)-1]
		if !strings.HasPrefix(path, "/") {
			continue
		}
		base := filepath.Base(path)
		if strings.Contains(base, PluginID) && selfUpdateLibraryExtension(filepath.Ext(base)) {
			if info, errStat := os.Stat(path); errStat == nil && !info.IsDir() {
				return path
			}
		}
	}
	return ""
}

func selfUpdateLibraryExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".so", ".dylib", ".dll":
		return true
	}
	return false
}

// selfUpdateSearchDirectories lists plausible plugin directories, nearest first.
func selfUpdateSearchDirectories(config Config) []string {
	directories := make([]string, 0, 6)
	if dataDir := strings.TrimSpace(config.DataDir); dataDir != "" {
		directories = append(directories, dataDir, filepath.Join(dataDir, "plugins"))
	}
	if executable, errExecutable := os.Executable(); errExecutable == nil {
		base := filepath.Dir(executable)
		directories = append(directories, base, filepath.Join(base, "plugins"))
	}
	return directories
}

func selfUpdateFindLibrary(directory string) string {
	if strings.TrimSpace(directory) == "" {
		return ""
	}
	entries, errRead := os.ReadDir(directory)
	if errRead != nil {
		return ""
	}
	for _, extension := range []string{".so", ".dylib", ".dll"} {
		candidate := filepath.Join(directory, PluginID+extension)
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() != filepath.Base(candidate) {
				continue
			}
			if info, errStat := os.Stat(candidate); errStat == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func (s *SelfUpdateService) run() {
	defer close(s.done)
	delay := selfUpdateStartupDelay
	for {
		timer := time.NewTimer(delay)
		select {
		case <-s.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout+10*time.Second)
		_, _ = s.Check(ctx)
		cancel()
		delay = selfUpdateCheckInterval
	}
}

func (s *SelfUpdateService) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	})
}

// Check resolves the latest release and selects the archive for this platform.
func (s *SelfUpdateService) Check(ctx context.Context) (SelfUpdateSnapshot, error) {
	if s == nil {
		return SelfUpdateSnapshot{}, ErrSelfUpdateUnavailable
	}
	if s.repoSlug == "" {
		return s.Snapshot(), errors.New("the plugin repository is not configured")
	}
	version, source, errResolve := s.resolveLatestVersion(ctx)
	if errResolve != nil {
		s.mu.Lock()
		s.state.Error = errResolve.Error()
		s.mu.Unlock()
		return s.Snapshot(), errResolve
	}
	s.mu.Lock()
	s.state.LatestVersion = version
	s.state.Source = source
	s.state.CheckedAt = s.now()
	s.state.Error = ""
	s.refreshDerivedLocked()
	s.policy.LatestVersion = version
	s.policy.Source = source
	s.policy.CheckedAt = s.state.CheckedAt
	_ = s.persistLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}

// resolveLatestVersion tries the API, then the release redirect, then the Atom
// feed. The plugin marketplace is deliberately not used: an installation whose
// marketplace requests fail must still be able to update.
func (s *SelfUpdateService) resolveLatestVersion(ctx context.Context) (string, string, error) {
	if version, errAPI := s.latestVersionFromAPI(ctx); errAPI == nil && selfUpdateVersionValid(version) {
		return version, selfUpdateSourceAPI, nil
	}
	if version, errRedirect := s.latestVersionFromRedirect(ctx); errRedirect == nil && selfUpdateVersionValid(version) {
		return version, selfUpdateSourceRedirect, nil
	}
	if version, errAtom := s.latestVersionFromAtom(ctx); errAtom == nil && selfUpdateVersionValid(version) {
		return version, selfUpdateSourceAtom, nil
	}
	return "", "", errors.New("the latest release could not be resolved")
}

func (s *SelfUpdateService) latestVersionFromAPI(ctx context.Context) (string, error) {
	body, errFetch := s.fetchBounded(ctx, "https://api.github.com/repos/"+s.repoSlug+"/releases/latest", 1<<20, "application/vnd.github+json")
	if errFetch != nil {
		return "", errFetch
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return "", errDecode
	}
	return strings.TrimSpace(payload.TagName), nil
}

func (s *SelfUpdateService) latestVersionFromRedirect(ctx context.Context) (string, error) {
	client := *s.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+s.repoSlug+"/releases/latest", nil)
	if errRequest != nil {
		return "", errRequest
	}
	request.Header.Set("Accept", "text/html")
	response, errDo := client.Do(request)
	if errDo != nil {
		return "", errDo
	}
	defer response.Body.Close()
	location := response.Header.Get("Location")
	if location == "" {
		return "", errors.New("the release redirect carried no location")
	}
	parsed, errParse := url.Parse(location)
	if errParse != nil {
		return "", errParse
	}
	index := strings.Index(parsed.Path, "/tag/")
	if index < 0 {
		return "", errors.New("the release redirect carried no tag")
	}
	return strings.TrimSpace(parsed.Path[index+len("/tag/"):]), nil
}

func (s *SelfUpdateService) latestVersionFromAtom(ctx context.Context) (string, error) {
	body, errFetch := s.fetchBounded(ctx, "https://github.com/"+s.repoSlug+"/releases.atom", selfUpdateMaxChecksums, "application/atom+xml")
	if errFetch != nil {
		return "", errFetch
	}
	// GitHub fills <title> with the release *name* (this repository publishes
	// "CPA Account Config Manager v1.2.3"), so the tag must come from the entry link or id.
	var feed struct {
		Entries []struct {
			ID    string `xml:"id"`
			Links []struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if errDecode := xml.Unmarshal(body, &feed); errDecode != nil {
		return "", errDecode
	}
	if len(feed.Entries) == 0 {
		return "", errors.New("the release feed was empty")
	}
	entry := feed.Entries[0]
	for _, link := range entry.Links {
		if tag := selfUpdateTagFromReference(link.Href); tag != "" {
			return tag, nil
		}
	}
	if tag := selfUpdateTagFromReference(entry.ID); tag != "" {
		return tag, nil
	}
	return "", errors.New("the release feed carried no tag")
}

// selfUpdateTagFromReference extracts the tag from a release URL or Atom id.
func selfUpdateTagFromReference(reference string) string {
	index := strings.Index(reference, "/tag/")
	if index < 0 {
		return ""
	}
	tag := strings.TrimSpace(reference[index+len("/tag/"):])
	if !selfUpdateVersionValid(tag) {
		return ""
	}
	return tag
}

// selfUpdateVersionValid accepts only the tag shape the release pipeline publishes, so a
// misparsed field can never turn into a download URL that cannot exist.
func selfUpdateVersionValid(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 32 {
		return false
	}
	trimmed = strings.TrimPrefix(trimmed, "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 9 {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func (s *SelfUpdateService) fetchBounded(ctx context.Context, rawURL string, maxBytes int64, accept string) ([]byte, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", PluginID+"/"+PluginVersion)
	response, errDo := s.client.Do(request)
	if errDo != nil {
		return nil, errDo
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("the update endpoint returned an empty response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the update endpoint returned HTTP status %d", response.StatusCode)
	}
	body, errRead := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("the update response is too large")
	}
	return body, nil
}

func normalizePluginVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func selfUpdateAssetName(version string) string {
	normalized := normalizePluginVersion(version)
	if normalized == "" {
		return ""
	}
	return fmt.Sprintf("%s_%s_%s_%s.zip", PluginID, normalized, runtime.GOOS, runtime.GOARCH)
}

func (s *SelfUpdateService) assetURL(version string) string {
	name := selfUpdateAssetName(version)
	if name == "" || s.repoSlug == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", s.repoSlug, normalizePluginVersion(version), name)
}

// Install downloads the selected release for this platform, verifies it against the
// release checksums, and replaces the plugin library on disk. The running process
// keeps the old library, so the caller must restart CPA for the change to apply.
func (s *SelfUpdateService) Install(ctx context.Context) (SelfUpdateSnapshot, error) {
	if s == nil {
		return SelfUpdateSnapshot{}, ErrSelfUpdateUnavailable
	}
	// A download-replace sequence must not interleave with a second one: both would stage
	// into the same file and the loser would report a misleading rename failure.
	if !s.installMu.TryLock() {
		return s.Snapshot(), errors.New("another update installation is already running")
	}
	defer s.installMu.Unlock()

	snapshot := s.Snapshot()
	pluginFile := snapshot.PluginFile
	if pluginFile == "" {
		return snapshot, errors.New("the plugin library file could not be located; set it explicitly and retry")
	}
	if !selfUpdateLibraryTarget(pluginFile) {
		return snapshot, fmt.Errorf("the recorded plugin library path %s is not a %s library", pluginFile, PluginID)
	}
	if !snapshot.PluginFileExists {
		return snapshot, fmt.Errorf("the plugin library file %s does not exist", pluginFile)
	}
	latest := snapshot.LatestVersion
	if latest == "" {
		if _, errCheck := s.Check(ctx); errCheck != nil {
			return s.Snapshot(), errCheck
		}
		latest = s.Snapshot().LatestVersion
	}
	if latest == "" {
		return s.Snapshot(), errors.New("the latest release version is unknown")
	}
	if compareVersions(latest, s.current) <= 0 {
		return s.Snapshot(), errors.New("the installed version is already current")
	}
	if normalizePluginVersion(latest) == normalizePluginVersion(snapshot.AppliedVersion) {
		return s.Snapshot(), errors.New("this release is already written to disk; restart CPA to load it")
	}
	archiveURL := s.assetURL(latest)
	if archiveURL == "" {
		return s.Snapshot(), errors.New("the release archive is unavailable for this platform")
	}
	// Clear the previous verdict before downloading: a failed attempt must not keep showing
	// the checksum of an older archive as if it described this one.
	s.mu.Lock()
	s.state.ChecksumOK = false
	s.state.ArchiveSHA256 = ""
	s.state.AssetBytes = 0
	s.state.Error = ""
	s.state.StagedPath = ""
	s.mu.Unlock()

	archive, errArchive := s.fetchReleaseArchive(ctx, archiveURL, latest)
	if errArchive != nil {
		return s.Snapshot(), errArchive
	}
	s.mu.Lock()
	s.state.AssetBytes = int64(len(archive))
	s.state.ArchiveSHA256 = selfUpdateSHA256(archive)
	s.mu.Unlock()

	library, errLibrary := selfUpdateExtractLibrary(archive)
	if errLibrary != nil {
		return s.Snapshot(), errLibrary
	}
	// A release may also carry the built interface. Staging it here means interface-only
	// changes apply on the next page refresh, while the library still needs a restart.
	stagedUI := ""
	uiUpdated := false
	if uiPayload, ok := selfUpdateExtractUI(archive); ok {
		if path, errUI := stageUIOverride(s.dataDir(), uiPayload); errUI == nil {
			stagedUI = path
			uiUpdated = true
		}
	}
	staged, errStage := s.stageLibrary(pluginFile, library)
	if errStage != nil {
		return s.Snapshot(), errStage
	}
	backup, errApply := selfUpdateApply(pluginFile, staged)
	s.mu.Lock()
	if errApply == nil {
		s.state.StagedPath = staged
		s.state.RestartRequired = true
		s.state.AppliedVersion = latest
		s.state.BackupPath = backup
		s.state.Error = ""
		s.state.UIUpdated = uiUpdated
		s.state.UIPath = stagedUI
		s.state.InterfaceRefreshOnly = uiUpdated && !s.state.RestartRequired
		s.policy.AppliedVersion = latest
		s.policy.BackupPath = backup
		s.policy.StagedPath = staged
		_ = s.persistLocked()
	}
	s.refreshDerivedLocked()
	s.mu.Unlock()
	if errApply != nil {
		return s.Snapshot(), errApply
	}
	return s.Snapshot(), nil
}

// fetchReleaseArchive downloads the platform archive and verifies it against the
// release checksums file. An unverifiable archive is never applied.
func (s *SelfUpdateService) fetchReleaseArchive(ctx context.Context, archiveURL, version string) ([]byte, error) {
	checksumURL := fmt.Sprintf("https://github.com/%s/releases/download/v%s/checksums.txt", s.repoSlug, normalizePluginVersion(version))
	checksums, errChecksums := s.fetchBounded(ctx, checksumURL, selfUpdateMaxChecksums, "text/plain")
	if errChecksums != nil {
		return nil, fmt.Errorf("the release checksums could not be read: %w", errChecksums)
	}
	expected := selfUpdateExpectedChecksum(string(checksums), filepath.Base(archiveURL))
	if expected == "" {
		return nil, errors.New("the release checksums do not list this platform archive")
	}
	archive, errArchive := s.fetchBounded(ctx, archiveURL, selfUpdateMaxArchive, "application/zip")
	if errArchive != nil {
		return nil, errArchive
	}
	actual := selfUpdateSHA256(archive)
	if !strings.EqualFold(actual, expected) {
		return nil, errors.New("the release archive failed checksum verification")
	}
	s.mu.Lock()
	s.state.ChecksumOK = true
	s.mu.Unlock()
	return archive, nil
}

func selfUpdateSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// selfUpdateExpectedChecksum finds the checksum line for one archive name.
func selfUpdateExpectedChecksum(checksums, name string) string {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

// selfUpdateExtractLibrary returns the plugin library stored in the release zip.
func selfUpdateExtractLibrary(archive []byte) ([]byte, error) {
	reader, errReader := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if errReader != nil {
		return nil, errors.New("the release archive is not a valid zip")
	}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || !selfUpdateLibraryExtension(filepath.Ext(file.Name)) {
			continue
		}
		if !strings.Contains(filepath.Base(file.Name), PluginID) {
			continue
		}
		if file.UncompressedSize64 > selfUpdateMaxLibrary {
			return nil, errors.New("the plugin library in the archive is too large")
		}
		opened, errOpen := file.Open()
		if errOpen != nil {
			return nil, errOpen
		}
		// Read one byte past the limit so an oversized entry is rejected instead of being
		// silently truncated into a corrupt library.
		payload, errRead := io.ReadAll(io.LimitReader(opened, selfUpdateMaxLibrary+1))
		opened.Close()
		if errRead != nil {
			return nil, errRead
		}
		if int64(len(payload)) > selfUpdateMaxLibrary {
			return nil, errors.New("the plugin library in the archive is too large")
		}
		return payload, nil
	}
	return nil, errors.New("the release archive carries no plugin library")
}

// dataDir reports the configured private state directory under the lock.
func (s *SelfUpdateService) dataDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.DataDir
}

// selfUpdateExtractUI returns the built interface carried by a release, when it has one.
// Older releases only contain the library, which is not an error: the embedded interface
// stays in use and an update still needs the usual restart.
func selfUpdateExtractUI(archive []byte) ([]byte, bool) {
	reader, errReader := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if errReader != nil {
		return nil, false
	}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(file.Name)
		if name != uiIndexArchiveMember {
			continue
		}
		if file.UncompressedSize64 > uiOverrideMaxBytes {
			return nil, false
		}
		opened, errOpen := file.Open()
		if errOpen != nil {
			return nil, false
		}
		payload, errRead := io.ReadAll(io.LimitReader(opened, uiOverrideMaxBytes+1))
		opened.Close()
		if errRead != nil || len(payload) == 0 || len(payload) > uiOverrideMaxBytes {
			return nil, false
		}
		if !looksLikeHTMLDocument(payload) {
			return nil, false
		}
		return payload, true
	}
	return nil, false
}

// stageLibrary writes the downloaded library next to the installed one. The caller passes
// the already validated target so a concurrent settings change cannot redirect the write.
func (s *SelfUpdateService) stageLibrary(pluginFile string, library []byte) (string, error) {
	if pluginFile == "" {
		return "", errors.New("the plugin library file could not be located")
	}
	staged := pluginFile + "." + selfUpdateStagedFileName
	// A leftover staged file from an interrupted attempt must never be appended to.
	_ = os.Remove(staged)
	handle, errOpen := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errOpen != nil {
		return "", fmt.Errorf("the update could not be staged: %w", errOpen)
	}
	if _, errWrite := handle.Write(library); errWrite != nil {
		handle.Close()
		_ = os.Remove(staged)
		return "", fmt.Errorf("the update could not be staged: %w", errWrite)
	}
	if errSync := handle.Sync(); errSync != nil {
		handle.Close()
		_ = os.Remove(staged)
		return "", fmt.Errorf("the update could not be staged: %w", errSync)
	}
	if errClose := handle.Close(); errClose != nil {
		_ = os.Remove(staged)
		return "", fmt.Errorf("the update could not be staged: %w", errClose)
	}
	return staged, nil
}

// selfUpdateApply replaces the installed library, keeping the previous file as a backup.
// The renames stay inside one directory, so they are atomic. The previous backup is only
// dropped once the new library is in place, and a failed swap reports where the original
// file went.
func selfUpdateApply(pluginFile, staged string) (string, error) {
	mode := os.FileMode(0o755)
	if info, errStat := os.Stat(pluginFile); errStat == nil {
		mode = info.Mode().Perm()
	}
	if errChmod := os.Chmod(staged, mode); errChmod != nil {
		return "", fmt.Errorf("the staged update could not be prepared: %w", errChmod)
	}
	backup := pluginFile + ".previous"
	if errRename := os.Rename(pluginFile, backup); errRename != nil {
		// A host may refuse to overwrite an existing backup; retry once without it. The
		// staged file stays on disk so the operator can still complete the update manually.
		if errRemove := os.Remove(backup); errRemove == nil {
			errRename = os.Rename(pluginFile, backup)
		}
		if errRename != nil {
			return "", fmt.Errorf("the installed plugin could not be replaced (%v); the update is staged at %s", errRename, staged)
		}
	}
	if errRename := os.Rename(staged, pluginFile); errRename != nil {
		if errRestore := os.Rename(backup, pluginFile); errRestore != nil {
			return "", fmt.Errorf("the update could not be moved into place (%v) and the previous library could not be restored (%v); it is kept at %s and the update is staged at %s", errRename, errRestore, backup, staged)
		}
		return "", fmt.Errorf("the update could not be moved into place: %w", errRename)
	}
	return backup, nil
}
