package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	legacyOperationStoreVersion = 1
	operationManifestVersion    = 2
	operationSegmentVersion     = 1
)

type legacyPersistedOperationState struct {
	Version    int              `json:"version"`
	Operations []OperationEntry `json:"operations"`
}

type persistedOperationSegment struct {
	ID    uint64 `json:"id"`
	Count int    `json:"count"`
}

type persistedOperationManifest struct {
	Version         int                         `json:"version"`
	ExtendedHistory bool                        `json:"extended_history"`
	NextSegmentID   uint64                      `json:"next_segment_id"`
	Segments        []persistedOperationSegment `json:"segments,omitempty"`
	Operations      []OperationEntry            `json:"operations"`
}

type persistedOperationPage struct {
	Version    int              `json:"version"`
	Operations []OperationEntry `json:"operations"`
}

func legacyOperationStorePath(dataDir string) string {
	return filepath.Join(dataDir, "operation-log.json")
}

func operationStoreDirectory(dataDir string) string {
	return filepath.Join(dataDir, "operation-log")
}

func operationManifestPath(store string) string {
	return filepath.Join(store, "manifest.json")
}

func operationSegmentPath(store string, id uint64) string {
	return filepath.Join(store, fmt.Sprintf("segment-%08d.json", id))
}

func loadLegacyOperationState(path string) (legacyPersistedOperationState, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return legacyPersistedOperationState{}, errRead
	}
	var state legacyPersistedOperationState
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return legacyPersistedOperationState{}, fmt.Errorf("decode legacy operation journal: %w", errDecode)
	}
	if state.Version != legacyOperationStoreVersion {
		return legacyPersistedOperationState{}, fmt.Errorf("unsupported legacy operation journal version %d", state.Version)
	}
	state.Operations = sanitizePersistedOperations(state.Operations, operationPageSize)
	return state, nil
}

func loadOperationManifest(path string) (persistedOperationManifest, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persistedOperationManifest{}, errRead
	}
	var manifest persistedOperationManifest
	if errDecode := json.Unmarshal(raw, &manifest); errDecode != nil {
		return persistedOperationManifest{}, fmt.Errorf("decode operation journal manifest: %w", errDecode)
	}
	if manifest.Version != operationManifestVersion {
		return persistedOperationManifest{}, fmt.Errorf("unsupported operation journal manifest version %d", manifest.Version)
	}
	manifest.Operations = sanitizePersistedOperations(manifest.Operations, operationPageSize)
	seen := make(map[uint64]struct{}, len(manifest.Segments))
	segments := make([]persistedOperationSegment, 0, len(manifest.Segments))
	maxID := uint64(0)
	for _, segment := range manifest.Segments {
		if segment.ID == 0 || segment.Count != operationPageSize {
			return persistedOperationManifest{}, fmt.Errorf("invalid operation journal segment metadata")
		}
		if _, exists := seen[segment.ID]; exists {
			return persistedOperationManifest{}, fmt.Errorf("duplicate operation journal segment metadata")
		}
		seen[segment.ID] = struct{}{}
		segments = append(segments, segment)
		if segment.ID > maxID {
			maxID = segment.ID
		}
	}
	sort.Slice(segments, func(left, right int) bool { return segments[left].ID < segments[right].ID })
	if !manifest.ExtendedHistory {
		segments = nil
	}
	manifest.Segments = segments
	if manifest.NextSegmentID <= maxID {
		manifest.NextSegmentID = maxID + 1
	}
	if manifest.NextSegmentID == 0 {
		manifest.NextSegmentID = 1
	}
	return manifest, nil
}

func saveOperationManifest(store string, manifest persistedOperationManifest) error {
	manifest.Version = operationManifestVersion
	manifest.Operations = cloneOperationEntries(manifest.Operations)
	manifest.Segments = append([]persistedOperationSegment(nil), manifest.Segments...)
	return savePrivateJSON(operationManifestPath(store), manifest)
}

func loadOperationSegment(store string, segment persistedOperationSegment) ([]OperationEntry, error) {
	raw, errRead := os.ReadFile(operationSegmentPath(store, segment.ID))
	if errRead != nil {
		return nil, errRead
	}
	var page persistedOperationPage
	if errDecode := json.Unmarshal(raw, &page); errDecode != nil {
		return nil, fmt.Errorf("decode operation journal segment: %w", errDecode)
	}
	if page.Version != operationSegmentVersion {
		return nil, fmt.Errorf("unsupported operation journal segment version %d", page.Version)
	}
	page.Operations = sanitizePersistedOperations(page.Operations, operationPageSize)
	if len(page.Operations) != operationPageSize || len(page.Operations) != segment.Count {
		return nil, fmt.Errorf("operation journal segment has an invalid entry count")
	}
	return page.Operations, nil
}

func saveOperationSegment(store string, id uint64, operations []OperationEntry) error {
	if id == 0 || len(operations) != operationPageSize {
		return fmt.Errorf("operation journal segment must contain exactly %d entries", operationPageSize)
	}
	return savePrivateJSON(operationSegmentPath(store, id), persistedOperationPage{
		Version:    operationSegmentVersion,
		Operations: cloneOperationEntries(operations),
	})
}

func removeOperationSegmentFiles(store string) error {
	return removeUnreferencedOperationSegmentFiles(store, nil)
}

func removeUnreferencedOperationSegmentFiles(store string, retained []persistedOperationSegment) error {
	entries, errRead := os.ReadDir(store)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return errRead
	}
	retainedNames := make(map[string]struct{}, len(retained))
	for _, segment := range retained {
		retainedNames[filepath.Base(operationSegmentPath(store, segment.ID))] = struct{}{}
	}
	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() || !operationSegmentFilename(entry.Name()) {
			continue
		}
		if _, keep := retainedNames[entry.Name()]; keep {
			continue
		}
		if errRemove := os.Remove(filepath.Join(store, entry.Name())); errRemove != nil && !os.IsNotExist(errRemove) && firstErr == nil {
			firstErr = errRemove
		}
	}
	return firstErr
}

func operationSegmentFilename(name string) bool {
	if len(name) != len("segment-00000000.json") || name[:8] != "segment-" || name[len(name)-5:] != ".json" {
		return false
	}
	for _, character := range name[8 : len(name)-5] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
