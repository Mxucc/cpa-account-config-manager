package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const experimentalSettingsStoreVersion = 1

type persistedExperimentalSettings struct {
	Version  int                  `json:"version"`
	Settings ExperimentalSettings `json:"settings"`
}

func experimentalSettingsStorePath(dataDir string) string {
	return filepath.Join(dataDir, "experimental-settings.json")
}

func loadExperimentalSettings(path string) (ExperimentalSettings, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return ExperimentalSettings{}, errRead
	}
	var state persistedExperimentalSettings
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil {
		return ExperimentalSettings{}, fmt.Errorf("decode experimental settings: %w", errDecode)
	}
	if state.Version != experimentalSettingsStoreVersion {
		return ExperimentalSettings{}, fmt.Errorf("unsupported experimental settings version %d", state.Version)
	}
	return state.Settings, nil
}

func saveExperimentalSettings(path string, settings ExperimentalSettings) error {
	return savePrivateJSON(path, persistedExperimentalSettings{Version: experimentalSettingsStoreVersion, Settings: settings})
}
