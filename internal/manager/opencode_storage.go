package manager

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenCode credentials live in the plugin's private state directory, which is implicit unless
// the operator pins `data_dir`. An implicit relative path ("data/cpa-account-config-manager")
// follows the working directory of whoever started CPA, so restarting CPA from another
// directory used to make every stored credential look gone. These helpers resolve the store
// once per start and adopt an existing file from a known location instead of starting empty.

const opencodeQuotaStoreFileName = "opencode-quota.json"

// stateFileNames are the files that prove a directory really holds this plugin's state.
var stateFileNames = []string{
	opencodeQuotaStoreFileName,
	"usage-snapshots.json",
	"self-update.json",
	"codex-model-control.json",
}

// openCodeQuotaStoreSearchDirs lists the places an existing store may live, most specific
// first. The implicit default keeps working; the alternates only matter when it is empty.
func openCodeQuotaStoreSearchDirs(config Config) []string {
	directories := make([]string, 0, 6)
	seen := map[string]struct{}{}
	appendDir := func(directory string) {
		trimmed := strings.TrimSpace(directory)
		if trimmed == "" {
			return
		}
		if absolute, errAbs := filepath.Abs(trimmed); errAbs == nil {
			trimmed = absolute
		}
		trimmed = filepath.Clean(trimmed)
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		directories = append(directories, trimmed)
	}
	if config.DataDir != "" {
		// The effective data directory always stays first: an operator-configured path is
		// authoritative, and the implicit one is what this plugin has always used.
		appendDir(config.DataDir)
	}
	for _, alternate := range config.DataDirAlternates {
		appendDir(alternate)
	}
	return directories
}

// resolveOpenCodeQuotaStore returns the store to use and, when it was adopted, the directory
// it came from. Adoption only happens when the preferred directory holds no store at all, and
// the newest candidate wins, so a stale copy cannot shadow recent credentials.
func resolveOpenCodeQuotaStore(config Config) (string, string) {
	primary := ""
	for index, directory := range openCodeQuotaStoreSearchDirs(config) {
		store := filepath.Join(directory, opencodeQuotaStoreFileName)
		if index == 0 {
			primary = store
			if _, errStat := os.Stat(store); errStat == nil {
				return store, ""
			}
			continue
		}
		if info, errStat := os.Stat(store); errStat == nil && !info.IsDir() {
			return store, directory
		}
	}
	return primary, ""
}

// preserveUnreadableOpenCodeStore copies a store that could not be parsed next to itself, so
// the operator can still recover the credentials from it after the plugin writes a new file.
func preserveUnreadableOpenCodeStore(storePath string) {
	trimmed := strings.TrimSpace(storePath)
	if trimmed == "" {
		return
	}
	raw, errRead := os.ReadFile(trimmed)
	if errRead != nil || len(raw) == 0 {
		return
	}
	backup := trimmed + ".unreadable"
	if _, errStat := os.Stat(backup); errStat == nil {
		backup = trimmed + "." + time.Now().UTC().Format("20060102T150405Z") + ".unreadable"
	}
	_ = writePrivateFileAtomically(backup, raw)
}

// resolvedStorePath reports the file this service reads and writes.
func (s *OpenCodeQuotaService) resolvedStorePath() string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(s.storePath) != "" {
		return s.storePath
	}
	if strings.TrimSpace(s.dataDir) == "" {
		return ""
	}
	return openCodeQuotaStorePath(s.dataDir)
}

// OpenCodeStorageInfo is the redacted storage state exposed to the UI, so an operator can see
// which directory the plugin actually uses instead of guessing why credentials look missing.
type OpenCodeStorageInfo struct {
	DataDir       string    `json:"data_dir"`
	StorePath     string    `json:"store_path"`
	StoreExists   bool      `json:"store_exists"`
	StoreBytes    int64     `json:"store_bytes,omitempty"`
	StoreModified time.Time `json:"store_modified,omitempty"`
	Accounts      int       `json:"accounts"`
	// AdoptedFrom names the directory a store was taken from when the preferred directory had
	// none; it is empty for the normal case.
	AdoptedFrom string `json:"adopted_from,omitempty"`
	// Hint explains a suspicious state, such as an empty store in an implicit directory.
	Hint string `json:"hint,omitempty"`
}

// Storage reports where the OpenCode credentials live.
func (s *OpenCodeQuotaService) Storage() OpenCodeStorageInfo {
	if s == nil {
		return OpenCodeStorageInfo{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	info := OpenCodeStorageInfo{
		DataDir:     s.dataDir,
		StorePath:   s.resolvedStorePath(),
		Accounts:    len(s.accounts),
		AdoptedFrom: s.adoptedFrom,
	}
	if detail, errStat := os.Stat(info.StorePath); errStat == nil && !detail.IsDir() {
		info.StoreExists = true
		info.StoreBytes = detail.Size()
		info.StoreModified = detail.ModTime()
	}
	switch {
	case info.AdoptedFrom != "":
		info.Hint = "adopted"
	case !info.StoreExists:
		info.Hint = "missing"
	}
	return info
}
