package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const policyStoreVersion = 2

type persistedPolicyState struct {
	Version      int                        `json:"version"`
	Policy       DefaultPolicy              `json:"policy"`
	LastScan     PolicyScanSummary          `json:"last_scan"`
	Fingerprints map[string]authFingerprint `json:"fingerprints,omitempty"`
}

func policyStorePath(dataDir string) string {
	return filepath.Join(dataDir, "default-policy.json")
}

func loadPolicyState(path string) (DefaultPolicy, PolicyScanSummary, error) {
	policy, lastScan, _, errLoad := loadPolicyRuntimeState(path)
	return policy, lastScan, errLoad
}

func loadPolicyRuntimeState(path string) (DefaultPolicy, PolicyScanSummary, map[string]authFingerprint, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return DefaultPolicy{}, PolicyScanSummary{}, nil, errRead
	}
	var persisted persistedPolicyState
	if errDecode := json.Unmarshal(raw, &persisted); errDecode != nil {
		return DefaultPolicy{}, PolicyScanSummary{}, nil, fmt.Errorf("decode default policy state: %w", errDecode)
	}
	if persisted.Version != 1 && persisted.Version != policyStoreVersion {
		return DefaultPolicy{}, PolicyScanSummary{}, nil, fmt.Errorf("unsupported default policy store version %d", persisted.Version)
	}
	policy, errValidate := validateDefaultPolicy(persisted.Policy)
	if errValidate != nil {
		return DefaultPolicy{}, PolicyScanSummary{}, nil, fmt.Errorf("validate stored default policy: %w", errValidate)
	}
	return policy, persisted.LastScan, clonePolicyFingerprints(persisted.Fingerprints), nil
}

func savePolicyState(path string, policy DefaultPolicy, lastScan PolicyScanSummary) error {
	return savePolicyRuntimeState(path, policy, lastScan, nil)
}

func savePolicyRuntimeState(path string, policy DefaultPolicy, lastScan PolicyScanSummary, fingerprints map[string]authFingerprint) error {
	return savePrivateJSON(path, persistedPolicyState{
		Version:      policyStoreVersion,
		Policy:       cloneDefaultPolicy(policy),
		LastScan:     lastScan,
		Fingerprints: clonePolicyFingerprints(fingerprints),
	})
}

func clonePolicyFingerprints(fingerprints map[string]authFingerprint) map[string]authFingerprint {
	cloned := make(map[string]authFingerprint, len(fingerprints))
	for authIndex, fingerprint := range fingerprints {
		cloned[authIndex] = fingerprint
	}
	return cloned
}
