package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const policyStoreVersion = 1

type persistedPolicyState struct {
	Version  int               `json:"version"`
	Policy   DefaultPolicy     `json:"policy"`
	LastScan PolicyScanSummary `json:"last_scan"`
}

func policyStorePath(dataDir string) string {
	return filepath.Join(dataDir, "default-policy.json")
}

func loadPolicyState(path string) (DefaultPolicy, PolicyScanSummary, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return DefaultPolicy{}, PolicyScanSummary{}, errRead
	}
	var persisted persistedPolicyState
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return DefaultPolicy{}, PolicyScanSummary{}, fmt.Errorf("decode default policy state: %w", errDecode)
	}
	if persisted.Version != policyStoreVersion {
		return DefaultPolicy{}, PolicyScanSummary{}, fmt.Errorf("unsupported default policy store version %d", persisted.Version)
	}
	policy, errValidate := validateDefaultPolicy(persisted.Policy)
	if errValidate != nil {
		return DefaultPolicy{}, PolicyScanSummary{}, fmt.Errorf("validate stored default policy: %w", errValidate)
	}
	return policy, persisted.LastScan, nil
}

func savePolicyState(path string, policy DefaultPolicy, lastScan PolicyScanSummary) error {
	return savePrivateJSON(path, persistedPolicyState{
		Version:  policyStoreVersion,
		Policy:   cloneDefaultPolicy(policy),
		LastScan: lastScan,
	})
}
