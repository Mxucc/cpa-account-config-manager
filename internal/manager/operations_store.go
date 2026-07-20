package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const operationStoreVersion = 1

type persistedOperationState struct {
	Version    int              `json:"version"`
	Operations []OperationEntry `json:"operations"`
}

func operationStorePath(dataDir string) string {
	return filepath.Join(dataDir, "operation-log.json")
}

func loadOperationState(path string) (persistedOperationState, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persistedOperationState{}, errRead
	}
	var state persistedOperationState
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return persistedOperationState{}, fmt.Errorf("decode operation journal: %w", errDecode)
	}
	if state.Version != operationStoreVersion {
		return persistedOperationState{}, fmt.Errorf("unsupported operation journal version %d", state.Version)
	}
	state.Operations = sanitizePersistedOperations(state.Operations)
	return state, nil
}

func saveOperationState(path string, operations []OperationEntry) error {
	return savePrivateJSON(path, persistedOperationState{
		Version:    operationStoreVersion,
		Operations: cloneOperationEntries(operations),
	})
}
