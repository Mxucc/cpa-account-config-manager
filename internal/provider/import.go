package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Provider import, the Magpie half of the plan's M1 stage: a Magpie or OpenCode
// provider plugin is a package whose package.json names its entry point and,
// optionally, magpie.* metadata. Importing one registers a provider definition
// so it appears beside the built-in vendors.
//
// Importing never executes the package. The entry file, its fetch and its auth
// hooks are recorded but not run, and the resulting provider says so. That is
// what makes the import safe on a host without a JavaScript runtime, and it is
// also why an imported provider has no accounts until a later stage enables the
// sidecar.
type ImportedProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`
	Entry   string `json:"entry,omitempty"`
	Source  string `json:"source"`
	// Directory is the plugin package folder the sidecar imports. Empty means the
	// provider is manifest-only: it is listed but no hook runs.
	Directory     string       `json:"directory,omitempty"`
	Capabilities  Capabilities `json:"capabilities"`
	HookExecution bool         `json:"hook_execution"`
	ImportedAt    time.Time    `json:"imported_at"`
	Notes         []string     `json:"notes,omitempty"`
}

const (
	importStoreVersion = 1
	maxManifestBytes   = 256 << 10
)

type importStoreFile struct {
	Version   int                `json:"version"`
	Providers []ImportedProvider `json:"providers"`
}

// ImportStore keeps imported provider definitions in the plugin's data
// directory. Every method is safe for concurrent use; a read failure leaves the
// previous in-memory list intact rather than dropping providers.
type ImportStore struct {
	mu      sync.Mutex
	path    string
	entries []ImportedProvider
	loaded  bool
	err     string
}

func NewImportStore(path string) *ImportStore {
	return &ImportStore{path: path}
}

func (s *ImportStore) loadLocked() {
	if s.loaded || s.path == "" {
		return
	}
	s.loaded = true
	raw, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			s.err = errRead.Error()
		}
		return
	}
	var stored importStoreFile
	if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
		s.err = "imported provider store is unreadable"
		return
	}
	s.entries = stored.Providers
}

func (s *ImportStore) persistLocked() error {
	if s.path == "" {
		return fmt.Errorf("plugin data directory is unavailable")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o755); errMkdir != nil {
		return errMkdir
	}
	raw, errMarshal := json.MarshalIndent(importStoreFile{Version: importStoreVersion, Providers: s.entries}, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	temporary := s.path + ".tmp"
	if errWrite := os.WriteFile(temporary, raw, 0o600); errWrite != nil {
		return errWrite
	}
	return os.Rename(temporary, s.path)
}

// List returns the imported providers, newest first.
func (s *ImportStore) List() []ImportedProvider {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]ImportedProvider, len(s.entries))
	copy(out, s.entries)
	sort.SliceStable(out, func(left, right int) bool { return out[left].ImportedAt.After(out[right].ImportedAt) })
	return out
}

// Definitions adapts the imported providers to the provider registry.
func (s *ImportStore) Definitions() []Definition {
	imported := s.List()
	out := make([]Definition, 0, len(imported))
	for _, entry := range imported {
		out = append(out, Definition{
			ID:          entry.ID,
			DisplayName: entry.Name,
			Kind:        "plugin",
			Description: "Imported Magpie/OpenCode provider plugin; hooks are not executed in this build.",
			Capabilities: Capabilities{
				Models: entry.Capabilities.Models,
				Quota:  entry.Capabilities.Quota,
				Login:  entry.Capabilities.Login,
			},
		})
	}
	return out
}

// PluginsDir is where imported plugin packages may live. A manifest cannot point
// the sidecar anywhere else, so importing never becomes "run code from /tmp".
func (s *ImportStore) PluginsDir() string {
	if s == nil || s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "magpie-plugins")
}

// Import parses one package.json and records a provider definition. directory is
// optional; when given it must sit inside PluginsDir.
func (s *ImportStore) Import(manifest []byte, source, directory string) (ImportedProvider, error) {
	if s == nil {
		return ImportedProvider{}, fmt.Errorf("import store is unavailable")
	}
	if len(manifest) == 0 {
		return ImportedProvider{}, fmt.Errorf("manifest is empty")
	}
	if len(manifest) > maxManifestBytes {
		return ImportedProvider{}, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	var document struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Main        string          `json:"main"`
		Exports     json.RawMessage `json:"exports"`
		Keywords    []string        `json:"keywords"`
		Magpie      map[string]any  `json:"magpie"`
		Description string          `json:"description"`
	}
	if errUnmarshal := json.Unmarshal(manifest, &document); errUnmarshal != nil {
		return ImportedProvider{}, fmt.Errorf("manifest is not valid JSON: %w", errUnmarshal)
	}
	pkg := strings.TrimSpace(document.Name)
	if pkg == "" {
		return ImportedProvider{}, fmt.Errorf("manifest has no package name")
	}
	id := providerIDFromPackage(pkg)
	if id == "" {
		return ImportedProvider{}, fmt.Errorf("package name does not yield a provider id")
	}

	entry := strings.TrimSpace(document.Main)
	if entry == "" {
		var exported string
		if errUnmarshal := json.Unmarshal(document.Exports, &exported); errUnmarshal == nil {
			entry = strings.TrimSpace(exported)
		} else {
			var exportedMap map[string]any
			if errUnmarshal := json.Unmarshal(document.Exports, &exportedMap); errUnmarshal == nil {
				if value, ok := exportedMap["."].(string); ok {
					entry = strings.TrimSpace(value)
				}
			}
		}
	}

	name := id
	if document.Magpie != nil {
		if value, ok := document.Magpie["name"].(map[string]any); ok {
			if zh, ok := value["zh"].(string); ok && strings.TrimSpace(zh) != "" {
				name = strings.TrimSpace(zh)
			}
		}
	}
	if strings.TrimSpace(name) == "" {
		name = id
	}

	notes := []string{
		"Provider hooks (auth, models, usage) are recorded but not executed.",
		"Add the vendor's base URL and key as a CPA channel to route its models.",
	}
	magpieMarker := document.Magpie != nil || containsKeyword(document.Keywords, "magpie") || containsKeyword(document.Keywords, "opencode-plugin")
	if magpieMarker {
		notes = append(notes, "Recognised as a Magpie/OpenCode provider plugin.")
	}

	pluginDir := strings.TrimSpace(directory)
	if pluginDir != "" {
		absoluteDir, errAbs := filepath.Abs(pluginDir)
		if errAbs != nil {
			return ImportedProvider{}, fmt.Errorf("plugin directory is invalid")
		}
		root := s.PluginsDir()
		if root == "" {
			return ImportedProvider{}, fmt.Errorf("plugin data directory is unavailable; hooks cannot be enabled")
		}
		relative, errRel := filepath.Rel(root, absoluteDir)
		if errRel != nil || strings.HasPrefix(relative, "..") {
			return ImportedProvider{}, fmt.Errorf("plugin directory must live under %s", root)
		}
		if info, errStat := os.Stat(absoluteDir); errStat != nil || !info.IsDir() {
			return ImportedProvider{}, fmt.Errorf("plugin directory does not exist")
		}
		pluginDir = absoluteDir
	}

	entryRecord := ImportedProvider{
		ID:        id,
		Name:      name,
		Package:   pkg,
		Version:   strings.TrimSpace(document.Version),
		Entry:     entry,
		Source:    normalizeImportSource(source),
		Directory: pluginDir,
		Capabilities: Capabilities{
			// A provider plugin's whole purpose is a model list and usually usage;
			// login depends on whether it declares an auth entry, which M1 cannot
			// prove without executing it.
			Models: true,
			Quota:  true,
			Login:  false,
		},
		HookExecution: false,
		ImportedAt:    time.Now().UTC(),
		Notes:         notes,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	replaced := false
	for index := range s.entries {
		if s.entries[index].ID == id {
			s.entries[index] = entryRecord
			replaced = true
			break
		}
	}
	if !replaced {
		s.entries = append(s.entries, entryRecord)
	}
	if errPersist := s.persistLocked(); errPersist != nil {
		return ImportedProvider{}, fmt.Errorf("imported provider could not be saved: %w", errPersist)
	}
	s.err = ""
	return entryRecord, nil
}

// Remove forgets one imported provider.
func (s *ImportStore) Remove(id string) error {
	if s == nil {
		return fmt.Errorf("import store is unavailable")
	}
	target := strings.TrimSpace(id)
	if target == "" {
		return fmt.Errorf("id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	kept := s.entries[:0]
	removed := false
	for _, entry := range s.entries {
		if entry.ID == target {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	s.entries = kept
	if !removed {
		return fmt.Errorf("provider %s is not imported", target)
	}
	return s.persistLocked()
}

func providerIDFromPackage(pkg string) string {
	id := strings.ToLower(pkg)
	if index := strings.LastIndex(id, "/"); index >= 0 {
		id = id[index+1:]
	}
	for _, prefix := range []string{"opencode-", "magpie-", "provider-"} {
		id = strings.TrimPrefix(id, prefix)
	}
	for _, suffix := range []string{"-auth", "-plugin", "-provider"} {
		id = strings.TrimSuffix(id, suffix)
	}
	return strings.Trim(id, "-_ ")
}

func normalizeImportSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "opencode":
		return "opencode"
	case "magpie":
		return "magpie"
	default:
		return "manifest"
	}
}

func containsKeyword(keywords []string, want string) bool {
	for _, keyword := range keywords {
		if strings.EqualFold(strings.TrimSpace(keyword), want) {
			return true
		}
	}
	return false
}
