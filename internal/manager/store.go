package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const jobStoreVersion = 1

type persistedJobSnapshot struct {
	Version int         `json:"version"`
	Job     JobSnapshot `json:"job"`
}

func jobStorePath(dataDir string) string {
	return filepath.Join(dataDir, "results.json")
}

func loadJobSnapshot(path string) (JobSnapshot, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return JobSnapshot{}, errRead
	}
	var persisted persistedJobSnapshot
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return JobSnapshot{}, fmt.Errorf("decode job state: %w", errDecode)
	}
	if persisted.Version != jobStoreVersion {
		return JobSnapshot{}, fmt.Errorf("unsupported job store version %d", persisted.Version)
	}
	if persisted.Job.State == "" {
		persisted.Job.State = JobStateIdle
	}
	return persisted.Job, nil
}

func saveJobSnapshot(path string, snapshot JobSnapshot) error {
	return savePrivateJSON(path, persistedJobSnapshot{Version: jobStoreVersion, Job: snapshot})
}

func savePrivateJSON(path string, payload any) error {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return fmt.Errorf("create private data directory: %w", errMkdir)
	}
	raw, errEncode := json.Marshal(payload)
	if errEncode != nil {
		return fmt.Errorf("encode private data: %w", errEncode)
	}
	temporaryPath := path + ".tmp"
	if errWrite := os.WriteFile(temporaryPath, raw, 0o600); errWrite != nil {
		return fmt.Errorf("write private data: %w", errWrite)
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		if _, errStat := os.Stat(path); errStat == nil {
			if errRemove := os.Remove(path); errRemove == nil {
				if errRetry := os.Rename(temporaryPath, path); errRetry == nil {
					return nil
				}
			}
		}
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace private data: %w", errRename)
	}
	if errChmod := os.Chmod(path, 0o600); errChmod != nil && !errors.Is(errChmod, os.ErrPermission) {
		return fmt.Errorf("protect private data: %w", errChmod)
	}
	return nil
}
