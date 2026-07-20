package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const updateStoreVersion = 1

type persistedUpdateState struct {
	Version       int          `json:"version"`
	Policy        UpdatePolicy `json:"policy"`
	LatestVersion string       `json:"latest_version,omitempty"`
	CheckedAt     time.Time    `json:"checked_at,omitempty"`
	Error         string       `json:"error,omitempty"`
}

func updateStorePath(dataDir string) string {
	return filepath.Join(dataDir, "update-state.json")
}

func loadUpdateState(path string) (persistedUpdateState, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return persistedUpdateState{}, errRead
	}
	var state persistedUpdateState
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return persistedUpdateState{}, fmt.Errorf("decode update state: %w", errDecode)
	}
	if state.Version != updateStoreVersion {
		return persistedUpdateState{}, fmt.Errorf("unsupported update store version %d", state.Version)
	}
	policy, errPolicy := validateUpdatePolicy(state.Policy)
	if errPolicy != nil {
		return persistedUpdateState{}, fmt.Errorf("validate update policy: %w", errPolicy)
	}
	state.Policy = policy
	if _, normalized, ok := parseReleaseVersion(state.LatestVersion); ok {
		state.LatestVersion = normalized
	} else {
		state.LatestVersion = ""
	}
	state.CheckedAt = state.CheckedAt.UTC()
	state.Error = safeUpdateError(state.Error)
	return state, nil
}

func saveUpdateState(path string, state persistedUpdateState) error {
	state.Version = updateStoreVersion
	state.Policy = normalizeUpdatePolicy(state.Policy)
	state.Error = safeUpdateError(state.Error)
	return savePrivateJSON(path, state)
}

func safeUpdateError(value string) string {
	switch strings.TrimSpace(value) {
	case "", "update check is unavailable", "release metadata request failed", "release metadata response was invalid",
		"repository metadata is invalid", "current version is invalid", "update state could not be loaded", "update state could not be persisted":
		return strings.TrimSpace(value)
	default:
		return "release metadata request failed"
	}
}
