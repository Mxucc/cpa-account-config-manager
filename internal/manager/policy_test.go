package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestApplyDefaultPolicyMissingFillsAbsentZeroAndFalseWithoutTouchingSecrets(t *testing.T) {
	priority := 0
	websockets := false
	raw := json.RawMessage(`{
		"type":"codex",
		"priority":7,
		"access_token":"token-secret",
		"headers":{"Authorization":"Bearer header-secret"},
		"unknown":{"nested":true}
	}`)

	updated, applied, changed, errApply := applyDefaultPolicy(raw, DefaultPolicy{
		Priority:   &priority,
		Websockets: &websockets,
	}, applyMissing)
	if errApply != nil {
		t.Fatalf("applyDefaultPolicy() error = %v", errApply)
	}
	if !changed || len(applied) != 1 || applied[0] != policyFieldWebsockets {
		t.Fatalf("changed=%t applied=%#v", changed, applied)
	}

	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(7) || document[policyFieldWebsockets] != false {
		t.Fatalf("managed fields = priority:%#v websockets:%#v", document[policyFieldPriority], document[policyFieldWebsockets])
	}
	for _, secret := range []string{"token-secret", "Bearer header-secret", `"nested":true`} {
		if !bytes.Contains(updated, []byte(secret)) {
			t.Fatalf("updated document did not preserve %q: %s", secret, updated)
		}
	}
}

func TestApplyDefaultPolicyForceOverwritesOnlyManagedFields(t *testing.T) {
	priority := 0
	websockets := false
	raw := json.RawMessage(`{"priority":9,"websockets":true,"disabled":true,"proxy_url":"http://user:proxy-secret@127.0.0.1:7890","api_key":"api-secret"}`)

	updated, applied, changed, errApply := applyDefaultPolicy(raw, DefaultPolicy{
		Priority:   &priority,
		Websockets: &websockets,
	}, applyForce)
	if errApply != nil {
		t.Fatalf("applyDefaultPolicy() error = %v", errApply)
	}
	if !changed || strings.Join(applied, ",") != "priority,websockets" {
		t.Fatalf("changed=%t applied=%#v", changed, applied)
	}
	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(0) || document[policyFieldWebsockets] != false || document["disabled"] != true {
		t.Fatalf("document = %#v", document)
	}
	for _, secret := range []string{"proxy-secret", "api-secret"} {
		if !bytes.Contains(updated, []byte(secret)) {
			t.Fatalf("updated document did not preserve %q", secret)
		}
	}
}

func TestApplyDefaultPolicyAppliesResolvedProxyWithoutExposingProfileID(t *testing.T) {
	proxy := "socks5h://user:secret@proxy.internal:1080"
	updated, applied, changed, errApply := applyDefaultPolicy(json.RawMessage(`{"type":"codex","proxy_url":"direct","access_token":"token-secret"}`), DefaultPolicy{proxyURL: &proxy}, applyForce)
	if errApply != nil || !changed || len(applied) != 1 || applied[0] != policyFieldProxyURL {
		t.Fatalf("changed=%t applied=%#v err=%v", changed, applied, errApply)
	}
	var document map[string]any
	if err := json.Unmarshal(updated, &document); err != nil {
		t.Fatal(err)
	}
	if document[policyFieldProxyURL] != proxy || document["proxy_profile_id"] != nil {
		t.Fatalf("document=%#v", document)
	}
}

func TestPolicyStatePersistsReloadsAndUsesPrivatePermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "private")
	path := policyStorePath(dataDir)
	priority := 0
	websockets := false
	policy := normalizeDefaultPolicy(DefaultPolicy{
		Enabled:                        true,
		NewAccountModelProbeEnabled:    true,
		CodexQuotaMetadataProbeEnabled: true,
		ScanIntervalSeconds:            1,
		Priority:                       &priority,
		Websockets:                     &websockets,
	})
	lastScan := PolicyScanSummary{Scanned: 3, Changed: 2, FinishedAt: time.Now().UTC()}
	if errSave := savePolicyState(path, policy, lastScan); errSave != nil {
		t.Fatalf("savePolicyState() error = %v", errSave)
	}

	loaded, loadedScan, errLoad := loadPolicyState(path)
	if errLoad != nil {
		t.Fatalf("loadPolicyState() error = %v", errLoad)
	}
	if !loaded.Enabled || !loaded.NewAccountModelProbeEnabled || !loaded.CodexQuotaMetadataProbeEnabled || loaded.Priority == nil || *loaded.Priority != 0 || loaded.Websockets == nil || *loaded.Websockets || loaded.ScanIntervalSeconds != minPolicyScanIntervalSeconds {
		t.Fatalf("loaded policy = %#v", loaded)
	}
	if loadedScan.Scanned != 3 || loadedScan.Changed != 2 {
		t.Fatalf("loaded scan = %#v", loadedScan)
	}
	if runtime.GOOS != "windows" {
		fileInfo, errStat := os.Stat(path)
		if errStat != nil {
			t.Fatalf("Stat(file) error = %v", errStat)
		}
		dirInfo, errDirStat := os.Stat(dataDir)
		if errDirStat != nil {
			t.Fatalf("Stat(dir) error = %v", errDirStat)
		}
		if fileInfo.Mode().Perm() != 0o600 || dirInfo.Mode().Perm()&0o077 != 0 {
			t.Fatalf("permissions = file:%#o dir:%#o", fileInfo.Mode().Perm(), dirInfo.Mode().Perm())
		}
	}
}

func TestConfiguredPolicyOverridesStaleFileAndSurvivesFreshEngine(t *testing.T) {
	dataDir := t.TempDir()
	stalePriority := 1
	if errSave := savePolicyState(policyStorePath(dataDir), normalizeDefaultPolicy(DefaultPolicy{
		Enabled:  true,
		Priority: &stalePriority,
	}), PolicyScanSummary{Scanned: 2}); errSave != nil {
		t.Fatalf("savePolicyState() error = %v", errSave)
	}

	configuredPriority := 7
	configuredWebsockets := false
	config := ParseConfig([]byte("data_dir: " + dataDir + "\ndefault_policy:\n  enabled: true\n  new_account_model_probe_enabled: true\n  codex_quota_metadata_probe_enabled: true\n  scan_interval_seconds: 30\n  priority: 7\n  websockets: false\n"))
	if config.DefaultPolicy == nil || config.DefaultPolicy.Priority == nil || *config.DefaultPolicy.Priority != configuredPriority ||
		config.DefaultPolicy.Websockets == nil || *config.DefaultPolicy.Websockets != configuredWebsockets || !config.DefaultPolicy.NewAccountModelProbeEnabled || !config.DefaultPolicy.CodexQuotaMetadataProbeEnabled {
		t.Fatalf("parsed default policy = %#v", config.DefaultPolicy)
	}

	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(config)
	defer engine.Shutdown()
	snapshot := engine.Snapshot()
	if !snapshot.Policy.Enabled || !snapshot.Policy.NewAccountModelProbeEnabled || !snapshot.Policy.CodexQuotaMetadataProbeEnabled || snapshot.Policy.Priority == nil || *snapshot.Policy.Priority != configuredPriority ||
		snapshot.Policy.Websockets == nil || *snapshot.Policy.Websockets != configuredWebsockets || snapshot.Policy.ScanIntervalSeconds != 30 {
		t.Fatalf("configured snapshot = %#v", snapshot)
	}
}

func TestPolicyEngineProbesQuotaMetadataBeforeMatchingConditionalRules(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "codex-account", Name: "codex-account.json", Provider: "codex", Type: "codex",
			PlanType: "free", Source: "file", Path: "/auths/codex-account.json", Size: 32, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"codex-account": {
				AuthIndex: "codex-account", Name: "codex-account.json", Path: "/auths/codex-account.json",
				JSON: json.RawMessage(`{"type":"codex","plan_type":"free","access_token":"account-secret"}`),
			},
		},
	}
	websockets := false
	policy := normalizeDefaultPolicy(DefaultPolicy{
		CodexQuotaMetadataProbeEnabled: true,
		ConditionalRules: []ConditionalPolicyRule{{
			ID: "codex-plus", Name: "Codex Plus", Enabled: true, Priority: 100,
			Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: "codex"}, {Field: PolicyConditionAccountType, Value: "plus"}}},
			Actions:    ConditionalPolicyActions{Websockets: &websockets},
		}},
	})
	engine := NewPolicyEngine(host)
	engine.Arm("management-secret")
	engine.SetQuotaMetadataProbe(func(_ context.Context, account Account, managementKey string) (string, error) {
		if account.ID != "codex-account" || managementKey != "management-secret" {
			return "", fmt.Errorf("unexpected quota probe input for account %q", account.ID)
		}
		host.mu.Lock()
		defer host.mu.Unlock()
		if len(host.saves) != 0 {
			return "", errors.New("conditional action ran before quota metadata probing")
		}
		return "plus", nil
	})

	summary, fingerprints, observed := engine.scan(t.Context(), policy, time.Now().UTC())
	if summary.QuotaMetadataProbed != 1 || summary.QuotaMetadataUpdated != 1 || summary.QuotaMetadataFailed != 0 || summary.Failed != 0 || summary.Changed != 1 {
		t.Fatalf("scan summary = %#v", summary)
	}
	if len(observed) != 1 || observed[0].PlanType != "plus" {
		t.Fatalf("observed accounts = %#v", observed)
	}
	if fingerprint := fingerprints["codex-account"]; fingerprint.PlanType != "plus" {
		t.Fatalf("fingerprint = %#v", fingerprint)
	}
	host.mu.Lock()
	updated := append(json.RawMessage(nil), host.details["codex-account"].JSON...)
	host.mu.Unlock()
	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("decode updated auth: %v", errDecode)
	}
	if document["websockets"] != false || document["plan_type"] != "free" || !bytes.Contains(updated, []byte("account-secret")) {
		t.Fatalf("updated auth = %s", updated)
	}
}

func TestPolicyEngineDefersAlwaysOnQuotaMetadataUntilArmed(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "codex-account", Name: "codex-account.json", Provider: "codex", Type: "codex",
		Source: "file", Path: "/auths/codex-account.json",
	}}}
	engine := NewPolicyEngine(host)
	engine.SetQuotaMetadataProbe(func(context.Context, Account, string) (string, error) {
		t.Fatal("quota metadata probe ran before a management credential was available")
		return "", nil
	})
	summary, _, _ := engine.scan(t.Context(), normalizeDefaultPolicy(DefaultPolicy{}), time.Now().UTC())
	if summary.QuotaMetadataProbed != 0 || summary.QuotaMetadataUpdated != 0 || summary.QuotaMetadataFailed != 0 || summary.Skipped != 1 {
		t.Fatalf("scan summary = %#v", summary)
	}
}

func TestPolicyEngineSameStoreUnchangedConfigureDoesNotWakeScan(t *testing.T) {
	dataDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{})
	engine.mu.Lock()
	engine.started = true
	engine.store = policyStorePath(dataDir)
	engine.config = normalizeConfig(Config{DataDir: dataDir})
	engine.policy = normalizeDefaultPolicy(DefaultPolicy{})
	engine.mu.Unlock()

	configured := normalizeDefaultPolicy(DefaultPolicy{})
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &configured})
	select {
	case <-engine.wake:
		t.Fatal("an unchanged same-store configuration woke a default-policy scan")
	default:
	}
}

func TestPolicyEngineUnchangedPolicySavePreservesFingerprintsAndDoesNotWakeScan(t *testing.T) {
	dataDir := t.TempDir()
	priority := 4
	policy := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority})
	engine := NewPolicyEngine(&fakeAuthHost{})
	engine.mu.Lock()
	engine.started = true
	engine.store = policyStorePath(dataDir)
	engine.policy = policy
	engine.fingerprints["stable"] = authFingerprint{Name: "stable.json", Path: "/auths/stable.json"}
	engine.mu.Unlock()

	if _, errSet := engine.SetPolicy(policy); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	select {
	case <-engine.wake:
		t.Fatal("saving an unchanged policy woke a full scan")
	default:
	}
	_, _, storedFingerprints, errLoad := loadPolicyRuntimeState(policyStorePath(dataDir))
	if errLoad != nil || len(storedFingerprints) != 1 || storedFingerprints["stable"].Name != "stable.json" {
		t.Fatalf("stored fingerprints = %#v, error = %v", storedFingerprints, errLoad)
	}
}

func TestPolicyEngineChangedPolicySaveResetsFingerprintsWithoutWakingScan(t *testing.T) {
	dataDir := t.TempDir()
	priority := 4
	engine := NewPolicyEngine(&fakeAuthHost{})
	engine.mu.Lock()
	engine.started = true
	engine.store = policyStorePath(dataDir)
	engine.policy = normalizeDefaultPolicy(DefaultPolicy{})
	engine.fingerprints["stable"] = authFingerprint{Name: "stable.json", Path: "/auths/stable.json"}
	engine.mu.Unlock()

	if _, errSet := engine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	select {
	case <-engine.wake:
		t.Fatal("saving a changed policy woke a scan before explicit execution")
	default:
	}
	engine.mu.RLock()
	fingerprintCount := len(engine.fingerprints)
	engine.mu.RUnlock()
	if fingerprintCount != 0 {
		t.Fatalf("changed policy retained %d processed fingerprints", fingerprintCount)
	}
}

func TestPolicyEngineSetPolicyDoesNotPublishWhenPersistenceFails(t *testing.T) {
	dataDir := t.TempDir()
	storeParent := filepath.Join(dataDir, "not-a-directory")
	if errWrite := os.WriteFile(storeParent, []byte("file"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	engine := NewPolicyEngine(&fakeAuthHost{})
	oldPolicy := normalizeDefaultPolicy(DefaultPolicy{Enabled: true})
	engine.mu.Lock()
	engine.started = true
	engine.store = filepath.Join(storeParent, "default-policy.json")
	engine.policy = oldPolicy
	engine.mu.Unlock()

	_, errSet := engine.SetPolicy(DefaultPolicy{Enabled: false})
	if errSet == nil {
		t.Fatal("SetPolicy() unexpectedly succeeded when its store was not writable")
	}
	if !errors.Is(errSet, os.ErrNotExist) && !strings.Contains(errSet.Error(), "not-a-directory") {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	snapshot := engine.Snapshot()
	if !snapshot.Policy.Enabled {
		t.Fatalf("failed save replaced in-memory policy: %#v", snapshot.Policy)
	}
	if snapshot.LastScan.Error != policyLocalStoreError {
		t.Fatalf("failed save error marker = %q", snapshot.LastScan.Error)
	}
}

func TestPolicyEngineChangedSameStoreConfigureDoesNotWakeScan(t *testing.T) {
	dataDir := t.TempDir()
	priority := 4
	engine := NewPolicyEngine(&fakeAuthHost{})
	engine.mu.Lock()
	engine.started = true
	engine.store = policyStorePath(dataDir)
	engine.config = normalizeConfig(Config{DataDir: dataDir})
	engine.policy = normalizeDefaultPolicy(DefaultPolicy{})
	engine.fingerprints["stable"] = authFingerprint{Name: "stable.json", Path: "/auths/stable.json"}
	engine.mu.Unlock()

	configured := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority})
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &configured})
	select {
	case <-engine.wake:
		t.Fatal("persisting a changed same-store configuration woke a scan before explicit execution")
	default:
	}
	if snapshot := engine.Snapshot(); !snapshot.Policy.Enabled || snapshot.Policy.Priority == nil || *snapshot.Policy.Priority != priority {
		t.Fatalf("configured snapshot = %#v", snapshot)
	}
}

func TestPolicyEngineAppliesConfiguredPolicyDuringSameStoreReconfigure(t *testing.T) {
	dataDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()

	priority := 4
	policy := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority, ScanIntervalSeconds: 45})
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &policy})
	snapshot := engine.Snapshot()
	if !snapshot.Policy.Enabled || snapshot.Policy.Priority == nil || *snapshot.Policy.Priority != 4 || snapshot.Policy.ScanIntervalSeconds != 45 {
		t.Fatalf("reconfigured snapshot = %#v", snapshot)
	}
}

func TestPolicyEngineSameStoreReconfigureClearsOnlyConfiguredPolicyError(t *testing.T) {
	dataDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()

	invalid := DefaultPolicy{Enabled: true}
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &invalid})
	if got := engine.Snapshot().LastScan.Error; got != configuredPolicyError {
		t.Fatalf("invalid configured policy error = %q, want %q", got, configuredPolicyError)
	}

	valid := normalizeDefaultPolicy(DefaultPolicy{})
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &valid})
	if got := engine.Snapshot().LastScan.Error; got != "" {
		t.Fatalf("valid configured policy retained stale error %q", got)
	}

	engine.mu.Lock()
	engine.lastScan.Error = "auth file scan failed"
	engine.mu.Unlock()
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &valid})
	if got := engine.Snapshot().LastScan.Error; got != "auth file scan failed" {
		t.Fatalf("valid configured policy replaced scan error with %q", got)
	}
}

func TestPolicyEngineInvalidSameStoreReconfigureKeepsLastKnownGoodPolicy(t *testing.T) {
	dataDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()

	priority := 9
	configured := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority})
	if _, errSet := engine.SetPolicy(configured); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}

	// Simulate a bad live config reload.  The persisted/active policy must
	// remain the last valid one rather than being replaced with defaults.
	invalid := DefaultPolicy{Enabled: true, ScanIntervalSeconds: 1}
	engine.Configure(Config{DataDir: dataDir, DefaultPolicy: &invalid})
	snapshot := engine.Snapshot()
	if !snapshot.Policy.Enabled || snapshot.Policy.Priority == nil || *snapshot.Policy.Priority != priority {
		t.Fatalf("invalid reload replaced active policy: %#v", snapshot.Policy)
	}
	if snapshot.LastScan.Error != configuredPolicyError {
		t.Fatalf("invalid reload error = %q, want %q", snapshot.LastScan.Error, configuredPolicyError)
	}
}

func TestPolicyEngineReconcilesMissingFieldsAndDetectsNewFiles(t *testing.T) {
	modTime := time.Now().UTC().Add(-time.Minute)
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json", Size: 48, ModTime: modTime,
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex","access_token":"first-secret"}`)},
		},
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	priority := 0
	websockets := false
	if _, errSet := engine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority, Websockets: &websockets}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return !snapshot.Running && len(host.saves) == 1
	})

	host.mu.Lock()
	firstJSON := append(json.RawMessage(nil), host.details["a"].JSON...)
	firstSaveCount := len(host.saves)
	host.mu.Unlock()
	if firstSaveCount != 1 || !bytes.Contains(firstJSON, []byte("first-secret")) {
		t.Fatalf("first reconciliation saves=%d json=%s", firstSaveCount, firstJSON)
	}
	assertManagedPolicyValues(t, firstJSON, 0, false)

	firstFinished := engine.Snapshot().LastScan.FinishedAt
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.FinishedAt.After(firstFinished)
	})
	secondFinished := engine.Snapshot().LastScan.FinishedAt
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return snapshot.LastScan.FinishedAt.After(secondFinished)
	})
	host.mu.Lock()
	if len(host.saves) != 1 {
		host.mu.Unlock()
		t.Fatalf("unchanged auth file was saved repeatedly: %d", len(host.saves))
	}
	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
		AuthIndex: "b", Name: "b.json", Source: "file", Path: "/auths/b.json", Size: 42, ModTime: modTime,
	})
	host.details["b"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "b", Name: "b.json", Path: "/auths/b.json", JSON: json.RawMessage(`{"type":"codex","api_key":"second-secret"}`),
	}
	host.mu.Unlock()

	engine.RequestScan()
	waitForPolicy(t, engine, func(PolicySnapshot) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return len(host.saves) == 2
	})
	host.mu.Lock()
	secondJSON := append(json.RawMessage(nil), host.details["b"].JSON...)
	host.mu.Unlock()
	assertManagedPolicyValues(t, secondJSON, 0, false)
	if !bytes.Contains(secondJSON, []byte("second-secret")) {
		t.Fatalf("second reconciliation lost an unknown secret field: %s", secondJSON)
	}
}

func TestPolicyEngineScansForNewAccountProbeWithoutApplyingDefaultFields(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "new-auth", ID: "workspace-new", Name: "new-account.json", Provider: "codex", Type: "codex",
		AccountType: "oauth", Source: "file", Path: "/auths/new-account.json",
	}}}
	observed := make(chan []Account, 1)
	engine := NewPolicyEngine(host)
	engine.SetObserver(accountObserverFunc(func(accounts []Account) {
		select {
		case observed <- append([]Account(nil), accounts...):
		default:
		}
	}))
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	if _, errSet := engine.SetPolicy(DefaultPolicy{NewAccountModelProbeEnabled: true, ScanIntervalSeconds: 5}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine.RequestScan()
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return !snapshot.Running && snapshot.LastScan.Scanned == 1
	})

	select {
	case accounts := <-observed:
		if len(accounts) != 1 || accounts[0].ID != "new-auth" || accounts[0].AuthID != "workspace-new" {
			t.Fatalf("observed accounts = %#v", accounts)
		}
	case <-time.After(time.Second):
		t.Fatal("new-account policy scan did not publish observed accounts")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 0 {
		t.Fatalf("probe-only policy wrote %d auth files", len(host.saves))
	}
}

func TestPolicyEnginePublishesNewAccountsAfterDefaultMutationLockRelease(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "new-auth", ID: "workspace-new", Name: "new-account.json", Provider: "codex", Type: "codex",
			AccountType: "oauth", Source: "file", Path: "/auths/new-account.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"new-auth": {
				AuthIndex: "new-auth", Name: "new-account.json", Path: "/auths/new-account.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"test-secret"}`),
			},
		},
	}
	mutations := NewMutationCoordinator()
	lockAvailable := make(chan bool, 1)
	engine := NewPolicyEngineWithCoordinator(host, mutations)
	engine.SetObserver(accountObserverFunc(func([]Account) {
		acquired := mutations.TryAcquire("model-probe-observer")
		if acquired {
			mutations.Release("model-probe-observer")
		}
		select {
		case lockAvailable <- acquired:
		default:
		}
	}))
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	websockets := false
	if _, errSet := engine.SetPolicy(DefaultPolicy{
		Enabled: true, NewAccountModelProbeEnabled: true, ScanIntervalSeconds: 5, Websockets: &websockets,
	}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return !snapshot.Running && snapshot.LastScan.Scanned == 1
	})

	select {
	case acquired := <-lockAvailable:
		if !acquired {
			t.Fatal("new-account observer ran while the default-policy mutation lock was held")
		}
	case <-time.After(time.Second):
		t.Fatal("default-policy scan did not publish observed accounts")
	}
}

type accountObserverFunc func([]Account)

func (observer accountObserverFunc) ObserveAccounts(accounts []Account) {
	observer(accounts)
}

func TestPolicyEngineSkipsUnsupportedAndDuplicateEntries(t *testing.T) {
	now := time.Now().UTC()
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "runtime", Name: "runtime.json", Source: "runtime", RuntimeOnly: true},
			{AuthIndex: "text", Name: "notes.txt", Source: "file", Path: "/auths/notes.txt", ModTime: now},
			{AuthIndex: "duplicate-a", Name: "shared.json", Source: "file", Path: "/auths/shared.json", ModTime: now},
			{AuthIndex: "duplicate-b", Name: "shared.json", Source: "file", Path: "/auths/shared.json", ModTime: now},
		},
		details: map[string]cpaapi.HostAuthGetResponse{},
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	priority := 1
	if _, errSet := engine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine.RequestScan()
	snapshot := waitForPolicy(t, engine, func(snapshot PolicySnapshot) bool {
		return !snapshot.LastScan.FinishedAt.IsZero()
	})
	if snapshot.LastScan.Scanned != 4 || snapshot.LastScan.Eligible != 0 || snapshot.LastScan.Skipped != 4 || snapshot.LastScan.Failed != 0 {
		t.Fatalf("last scan = %#v", snapshot.LastScan)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 0 {
		t.Fatalf("unsupported entries were saved: %#v", host.saves)
	}
}

func TestPolicyEngineSharesMutationCoordinatorWithExplicitJobs(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json", Size: 10, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	coordinator := NewMutationCoordinator()
	engine := NewPolicyEngineWithCoordinator(host, coordinator)
	priority := 1
	engine.mu.Lock()
	engine.policy = normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority})
	engine.store = policyStorePath(t.TempDir())
	engine.mu.Unlock()

	if !coordinator.TryAcquire("batch-job") {
		t.Fatal("failed to reserve mutation coordinator")
	}
	if retrySoon := engine.reconcile(context.Background()); retrySoon {
		t.Fatal("busy background policy scan should be deferred on the normal interval")
	}
	host.mu.Lock()
	blockedSaves := len(host.saves)
	host.mu.Unlock()
	if blockedSaves != 0 {
		t.Fatalf("background policy scan wrote during an explicit job: %d saves", blockedSaves)
	}

	coordinator.Release("batch-job")
	if retrySoon := engine.reconcile(context.Background()); retrySoon {
		t.Fatal("policy scan requested another retry after the writer slot was released")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 1 {
		t.Fatalf("policy scan after release saves = %d, want 1", len(host.saves))
	}
}

func TestPolicyEngineArmNeverWakesScanForAuthenticatedPageRequests(t *testing.T) {
	engine := NewPolicyEngine(&fakeAuthHost{})
	engine.mu.Lock()
	engine.started = true
	engine.mu.Unlock()

	engine.Arm("management-secret")
	select {
	case <-engine.wake:
		t.Fatal("the first management credential woke a policy scan")
	default:
	}
	engine.Arm("management-secret")
	select {
	case <-engine.wake:
		t.Fatal("an unchanged management credential woke another policy scan")
	default:
	}
	engine.Arm("rotated-management-secret")
	select {
	case <-engine.wake:
		t.Fatal("a rotated management credential woke a policy scan")
	default:
	}
}

func TestPolicyEngineReloadsPersistedPolicyAndPerformsFullScan(t *testing.T) {
	dataDir := t.TempDir()
	priority := 3
	firstEngine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	firstEngine.Configure(Config{DataDir: dataDir})
	if _, errSet := firstEngine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	firstEngine.Shutdown()

	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "after-restart", Name: "after-restart.json", Source: "file", Path: "/auths/after-restart.json", Size: 8, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"after-restart": {
				AuthIndex: "after-restart",
				Name:      "after-restart.json",
				Path:      "/auths/after-restart.json",
				JSON:      json.RawMessage(`{"type":"codex","refresh_token":"restart-secret"}`),
			},
		},
	}
	secondEngine := NewPolicyEngine(host)
	secondEngine.Configure(Config{DataDir: dataDir})
	defer secondEngine.Shutdown()
	waitForPolicy(t, secondEngine, func(snapshot PolicySnapshot) bool {
		host.mu.Lock()
		defer host.mu.Unlock()
		return snapshot.Policy.Enabled && snapshot.Policy.Priority != nil &&
			*snapshot.Policy.Priority == 3 && len(host.saves) == 1
	})
	host.mu.Lock()
	updated := append(json.RawMessage(nil), host.details["after-restart"].JSON...)
	host.mu.Unlock()
	if !bytes.Contains(updated, []byte("restart-secret")) {
		t.Fatalf("restarted policy lost an unknown field: %s", updated)
	}
	var document map[string]any
	if errDecode := json.Unmarshal(updated, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(3) {
		t.Fatalf("priority = %#v, want 3", document[policyFieldPriority])
	}
}

func TestPolicyEngineRestoresProcessedAccountsAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	entry := cpaapi.HostAuthFileEntry{
		AuthIndex: "stable", Name: "stable.json", Provider: "claude", Type: "claude",
		Source: "file", Path: "/auths/stable.json", Size: 12, ModTime: time.Now().UTC(),
	}
	firstHost := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{entry},
		details: map[string]cpaapi.HostAuthGetResponse{
			"stable": {AuthIndex: "stable", Name: "stable.json", Path: "/auths/stable.json", JSON: json.RawMessage(`{"type":"claude"}`)},
		},
	}
	firstEngine := NewPolicyEngine(firstHost)
	firstEngine.Configure(Config{DataDir: dataDir})
	priority := 6
	if _, errSet := firstEngine.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	firstSnapshot := waitForPolicy(t, firstEngine, func(snapshot PolicySnapshot) bool {
		firstHost.mu.Lock()
		defer firstHost.mu.Unlock()
		return !snapshot.Running && len(firstHost.saves) == 1
	})
	firstEngine.Shutdown()
	_, _, storedFingerprints, errLoad := loadPolicyRuntimeState(policyStorePath(dataDir))
	if errLoad != nil || len(storedFingerprints) != 1 {
		t.Fatalf("persisted policy fingerprints = %#v, error = %v", storedFingerprints, errLoad)
	}
	firstHost.mu.Lock()
	updatedJSON := append(json.RawMessage(nil), firstHost.details["stable"].JSON...)
	firstHost.mu.Unlock()

	entry.Size = 999
	entry.ModTime = entry.ModTime.Add(time.Hour)
	secondHost := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{entry},
		details: map[string]cpaapi.HostAuthGetResponse{
			"stable": {AuthIndex: "stable", Name: "stable.json", Path: "/auths/stable.json", JSON: updatedJSON},
		},
	}
	secondEngine := NewPolicyEngine(secondHost)
	secondEngine.Configure(Config{DataDir: dataDir})
	defer secondEngine.Shutdown()
	waitForPolicy(t, secondEngine, func(snapshot PolicySnapshot) bool {
		secondHost.mu.Lock()
		defer secondHost.mu.Unlock()
		return secondHost.listCalls > 0 && snapshot.LastScan.FinishedAt.After(firstSnapshot.LastScan.FinishedAt)
	})
	secondHost.mu.Lock()
	defer secondHost.mu.Unlock()
	if len(secondHost.saves) != 0 {
		t.Fatalf("a processed account was saved again after restart: %d", len(secondHost.saves))
	}
	if secondEngine.Snapshot().LastScan.Skipped != 1 {
		t.Fatalf("restart scan = %#v, want one skipped account", secondEngine.Snapshot().LastScan)
	}
}

func TestPolicyEngineSanitizesSaveFailuresAndRetries(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Source: "file", Path: "/auths/a.json", Size: 10, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"access_token":"document-secret"}`)},
		},
		saveErrors: map[string]error{"a.json": errors.New("callback failed: token=callback-secret")},
	}
	mutations := NewMutationCoordinator()
	engine := NewPolicyEngineWithCoordinator(host, mutations)
	websockets := true
	policy := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Websockets: &websockets})
	firstStarted := time.Now().UTC()
	firstSummary, firstFingerprints, firstFailures, _ := engine.scanWithState(t.Context(), policy, firstStarted)
	engine.mu.Lock()
	engine.policy = policy
	engine.lastScan = firstSummary
	engine.fingerprints = firstFingerprints
	engine.failures = firstFailures
	engine.mu.Unlock()
	first := engine.Snapshot()
	if first.LastScan.Failed != 1 || len(firstFailures) != 1 {
		t.Fatalf("first failed scan = %#v failures=%#v", first.LastScan, firstFailures)
	}
	if len(first.LastScan.FailureDetails) != 1 || first.LastScan.FailureDetails[0].ReasonCode != OperationFailurePolicyAuthSave ||
		first.LastScan.FailureDetails[0].Count != 1 || !reflect.DeepEqual(first.LastScan.FailureDetails[0].SampleAccountIDs, []string{"a"}) {
		t.Fatalf("first failure details = %#v", first.LastScan.FailureDetails)
	}
	if !mutations.TryAcquire("post-failure-check") {
		t.Fatal("policy save failure retained the account mutation slot")
	}
	mutations.Release("post-failure-check")
	encoded, errMarshal := json.Marshal(first)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"callback-secret", "document-secret", "a.json", "/auths"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("policy status leaked %q: %s", secret, encoded)
		}
	}
	backedOff, _, backedOffFailures, _ := engine.scanWithState(t.Context(), policy, firstStarted.Add(time.Second))
	host.mu.Lock()
	backedOffCalls := host.saveCalls["a.json"]
	host.mu.Unlock()
	if backedOff.Failed != 0 || backedOff.Skipped != 1 || len(backedOffFailures) != 1 || backedOffCalls != 1 {
		t.Fatalf("automatic retry ignored failure backoff: summary=%#v failures=%#v calls=%d", backedOff, backedOffFailures, backedOffCalls)
	}
	engine.RequestScan()
	retried, _, _, _ := engine.scanWithState(t.Context(), policy, firstStarted.Add(2*time.Second))
	if retried.Failed != 1 {
		t.Fatalf("manual scan did not clear failure backoff: %#v", retried)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.saveCalls["a.json"] < 2 {
		t.Fatalf("failed save was not retried: %#v", host.saveCalls)
	}
}

func TestClassifyPolicyFailureUsesStableRedactedReasonCodes(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("get auth file: callback leaked-token"), OperationFailurePolicyAuthRead},
		{errors.New("auth index changed"), OperationFailurePolicyAccountIdentity},
		{errors.New("auth source changed"), OperationFailurePolicyAuthSource},
		{errors.New("auth filename changed"), OperationFailurePolicyAuthSource},
		{errors.New("auth filename is invalid"), OperationFailurePolicyAuthFilename},
		{errors.New("project auth account: private detail"), OperationFailurePolicyAuthProjection},
		{errors.New("auth json is invalid"), OperationFailurePolicyAuthJSON},
		{errors.New("auth json must be an object"), OperationFailurePolicyAuthJSON},
		{errors.New("save auth file: callback leaked-token"), OperationFailurePolicyAuthSave},
		{errors.New("conditional model policy is not armed"), OperationFailurePolicyModelPolicyUnavailable},
		{errors.New("apply conditional model policy: upstream leaked-token"), OperationFailurePolicyModelPolicyApply},
		{errors.New("unclassified private failure leaked-token"), OperationFailurePolicyAuthUpdate},
	}
	for _, test := range tests {
		if got := classifyPolicyFailure(test.err); got != test.want {
			t.Errorf("classifyPolicyFailure(%q) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestPolicyEngineShutdownCancelsBackgroundScan(t *testing.T) {
	host := &blockingPolicyHost{started: make(chan struct{})}
	dataDir := t.TempDir()
	priority := 1
	if errSave := savePolicyState(policyStorePath(dataDir), normalizeDefaultPolicy(DefaultPolicy{
		Enabled:  true,
		Priority: &priority,
	}), PolicyScanSummary{}); errSave != nil {
		t.Fatalf("savePolicyState() error = %v", errSave)
	}
	engine := NewPolicyEngine(host)
	engine.Configure(Config{DataDir: dataDir})
	select {
	case <-host.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background scan did not start")
	}
	done := make(chan struct{})
	go func() {
		engine.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() did not wait for the cancelled reconciler")
	}
	if !host.cancelled() {
		t.Fatal("host callback did not observe cancellation")
	}
}

type blockingPolicyHost struct {
	once      sync.Once
	started   chan struct{}
	mu        sync.Mutex
	wasCancel bool
}

func (h *blockingPolicyHost) ListAuth(ctx context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	h.once.Do(func() { close(h.started) })
	<-ctx.Done()
	h.mu.Lock()
	h.wasCancel = true
	h.mu.Unlock()
	return nil, ctx.Err()
}

func (*blockingPolicyHost) GetAuth(context.Context, string) (cpaapi.HostAuthGetResponse, error) {
	return cpaapi.HostAuthGetResponse{}, errors.New("unexpected get")
}

func (*blockingPolicyHost) SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	return cpaapi.HostAuthSaveResponse{}, errors.New("unexpected save")
}

func (h *blockingPolicyHost) cancelled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wasCancel
}

func waitForPolicy(t *testing.T, engine *PolicyEngine, predicate func(PolicySnapshot) bool) PolicySnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := engine.Snapshot()
	t.Fatalf("policy condition was not met; snapshot=%#v", snapshot)
	return PolicySnapshot{}
}

func assertManagedPolicyValues(t *testing.T, raw json.RawMessage, priority int, websockets bool) {
	t.Helper()
	var document map[string]any
	if errDecode := json.Unmarshal(raw, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document[policyFieldPriority] != float64(priority) || document[policyFieldWebsockets] != websockets {
		t.Fatalf("managed values = priority:%#v websockets:%#v", document[policyFieldPriority], document[policyFieldWebsockets])
	}
}

func TestSamePolicyAccountIncludesPlanTypeButIgnoresFileChurn(t *testing.T) {
	base := authFingerprint{Name: "account.json", Path: "/auth/account.json", PlanType: "free", Size: 10, ModTimeNS: 1, ModTimeSet: true}
	if !samePolicyAccount(base, authFingerprint{Name: base.Name, Path: base.Path, PlanType: "free", Size: 99, ModTimeNS: 2, ModTimeSet: true}) {
		t.Fatal("size/mtime churn should not force a policy rescan")
	}
	if samePolicyAccount(base, authFingerprint{Name: base.Name, Path: base.Path, PlanType: "plus"}) {
		t.Fatal("plan type changes must force a policy rescan")
	}
}

func TestPolicyEngineDefersOnlyQuotaProbeFailures(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{
		{AuthIndex: "good", Name: "good.json", Provider: "codex", Type: "codex", PlanType: "free", Source: "file", Path: "/auth/good.json"},
		{AuthIndex: "bad", Name: "bad.json", Provider: "codex", Type: "codex", PlanType: "free", Source: "file", Path: "/auth/bad.json"},
	}, details: map[string]cpaapi.HostAuthGetResponse{
		"good": {AuthIndex: "good", Name: "good.json", Path: "/auth/good.json", JSON: json.RawMessage(`{"type":"codex","plan_type":"free"}`)},
		"bad":  {AuthIndex: "bad", Name: "bad.json", Path: "/auth/bad.json", JSON: json.RawMessage(`{"type":"codex","plan_type":"free"}`)},
	}}
	websockets := false
	policy := normalizeDefaultPolicy(DefaultPolicy{
		CodexQuotaMetadataProbeEnabled: true,
		ConditionalRules: []ConditionalPolicyRule{{
			ID: "codex-plus", Name: "Codex Plus", Enabled: true, Priority: 100,
			Conditions: PolicyConditionGroup{Operator: PolicyConditionAll, Conditions: []PolicyCondition{{Field: PolicyConditionProvider, Value: "codex"}, {Field: PolicyConditionAccountType, Value: "plus"}}},
			Actions:    ConditionalPolicyActions{Websockets: &websockets},
		}},
	})
	engine := NewPolicyEngine(host)
	engine.Arm("management-secret")
	engine.SetQuotaMetadataProbe(func(_ context.Context, account Account, managementKey string) (string, error) {
		if managementKey != "management-secret" {
			return "", errors.New("unexpected management key")
		}
		if account.ID == "bad" {
			return "", errors.New("upstream quota probe failed")
		}
		return "plus", nil
	})

	summary, fingerprints, _ := engine.scan(t.Context(), policy, time.Now().UTC())
	if summary.QuotaMetadataProbed != 2 || summary.QuotaMetadataUpdated != 1 || summary.QuotaMetadataFailed != 1 {
		t.Fatalf("scan summary = %#v", summary)
	}
	if _, ok := fingerprints["bad"]; ok {
		t.Fatalf("failed account was fingerprinted: %#v", fingerprints["bad"])
	}
	if good, ok := fingerprints["good"]; !ok || good.PlanType != "plus" {
		t.Fatalf("successful account fingerprint = %#v, all=%#v", good, fingerprints)
	}
	if len(host.saves) != 1 || host.saves[0].Name != "good.json" {
		t.Fatalf("saves = %#v", host.saves)
	}
}

func TestPolicyEngineWithoutQuotaProbePersistsFingerprintForCodex(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "codex-account", Name: "codex-account.json", Provider: "codex", Type: "codex",
		Source: "file", Path: "/auths/codex-account.json",
	}}}
	priority := 7
	policy := normalizeDefaultPolicy(DefaultPolicy{Priority: &priority})
	engine := NewPolicyEngine(host)
	started := time.Now().UTC()
	summary, fingerprints, failures, _ := engine.scanWithState(t.Context(), policy, started)
	if summary.Failed != 0 || len(failures) != 0 || len(fingerprints) != 1 {
		t.Fatalf("first scan summary=%#v fingerprints=%#v failures=%#v", summary, fingerprints, failures)
	}
	engine.mu.Lock()
	engine.fingerprints = fingerprints
	engine.failures = failures
	engine.mu.Unlock()
	second, next, retry, _ := engine.scanWithState(t.Context(), policy, started.Add(time.Second))
	if second.Failed != 0 || second.Changed != 0 || second.Skipped != 1 || len(retry) != 0 || len(next) != 1 {
		t.Fatalf("unchanged scan repeated work: summary=%#v fingerprints=%#v failures=%#v", second, next, retry)
	}
}

func TestPolicyEngineMissingQuotaCredentialBacksOffInsteadOfTightLoop(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "codex-account", Name: "codex-account.json", Provider: "codex", Type: "codex",
		Source: "file", Path: "/auths/codex-account.json",
	}}}
	engine := NewPolicyEngine(host)
	engine.SetQuotaMetadataProbe(func(context.Context, Account, string) (string, error) {
		t.Fatal("quota probe should not run without a management credential")
		return "", nil
	})
	policy := normalizeDefaultPolicy(DefaultPolicy{CodexQuotaMetadataProbeEnabled: true})
	started := time.Now().UTC()
	summary, fingerprints, failures, _ := engine.scanWithState(t.Context(), policy, started)
	if summary.QuotaMetadataProbed != 0 || summary.Failed != 0 || len(fingerprints) != 0 {
		t.Fatalf("unexpected first scan state: summary=%#v fingerprints=%#v", summary, fingerprints)
	}
	failure, ok := failures["codex-account"]
	if !ok || !failure.RetryAt.After(started) {
		t.Fatalf("missing quota backoff: %#v", failures)
	}
	engine.mu.Lock()
	engine.failures = failures
	engine.mu.Unlock()
	second, _, retry, _ := engine.scanWithState(t.Context(), policy, started.Add(time.Second))
	if second.QuotaMetadataProbed != 0 || second.Failed != 0 || second.Skipped != 1 || len(retry) != 1 {
		t.Fatalf("missing quota credential caused a retry loop: summary=%#v failures=%#v", second, retry)
	}
}

func TestPolicyConfigureRetriesCorruptSameStore(t *testing.T) {
	dataDir := t.TempDir()
	storePath := policyStorePath(dataDir)
	if errWrite := os.WriteFile(storePath, []byte("{"), 0o600); errWrite != nil {
		t.Fatalf("write corrupt policy state: %v", errWrite)
	}
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	// This test exercises same-store recovery only. Keep the background
	// reconciler parked so it cannot legitimately replace the recovered
	// fingerprint set before the assertion (especially under -race).
	engine.SetBackgroundWorkOwner(backgroundWorkOwnerFunc(func() bool { return false }))
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	if got := engine.Snapshot().LastScan.Error; got != "stored default policy could not be loaded" {
		t.Fatalf("load error = %q", got)
	}
	priority := 8
	want := normalizeDefaultPolicy(DefaultPolicy{Enabled: true, Priority: &priority})
	if errSave := savePolicyRuntimeState(storePath, want, PolicyScanSummary{Scanned: 4}, map[string]authFingerprint{
		"known": {Name: "known.json", Path: "/auths/known.json", PlanType: "plus"},
	}); errSave != nil {
		t.Fatalf("save recovered policy state: %v", errSave)
	}
	engine.Configure(Config{DataDir: dataDir})
	snapshot := engine.Snapshot()
	if snapshot.LastScan.Error != "" || snapshot.Policy.Priority == nil || *snapshot.Policy.Priority != priority || snapshot.LastScan.Scanned != 4 {
		t.Fatalf("recovered snapshot = %#v", snapshot)
	}
	engine.mu.RLock()
	_, known := engine.fingerprints["known"]
	engine.mu.RUnlock()
	if !known {
		t.Fatal("recovered fingerprint is missing")
	}
}

func TestPolicyRetriesFailedRuntimePersistence(t *testing.T) {
	previousDelay := policyPersistRetryDelay
	policyPersistRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { policyPersistRetryDelay = previousDelay })

	dataDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(Config{DataDir: dataDir})
	defer engine.Shutdown()
	blockingPath := filepath.Join(dataDir, "blocked")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocking path: %v", errWrite)
	}
	engine.mu.Lock()
	engine.store = filepath.Join(blockingPath, "default-policy.json")
	engine.lastScan = PolicyScanSummary{Scanned: 7}
	engine.fingerprints["persist-me"] = authFingerprint{Name: "persist-me.json", Path: "/auths/persist-me.json"}
	engine.dirty = true
	engine.mu.Unlock()
	engine.operationMu.Lock()
	engine.persistRuntimeStateLocked()
	engine.operationMu.Unlock()
	if got := engine.Snapshot().LastScan.Error; got != policyLocalStoreError {
		t.Fatalf("persistence error = %q", got)
	}
	if errRemove := os.Remove(blockingPath); errRemove != nil {
		t.Fatalf("remove blocking path: %v", errRemove)
	}
	if errMkdir := os.MkdirAll(blockingPath, 0o700); errMkdir != nil {
		t.Fatalf("restore blocking path: %v", errMkdir)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && engine.Snapshot().LastScan.Error == policyLocalStoreError {
		time.Sleep(5 * time.Millisecond)
	}
	if got := engine.Snapshot().LastScan.Error; got != "" {
		t.Fatalf("persistence error after retry = %q", got)
	}
	_, loadedScan, loadedFingerprints, errLoad := loadPolicyRuntimeState(filepath.Join(blockingPath, "default-policy.json"))
	if errLoad != nil {
		t.Fatalf("load retried state: %v", errLoad)
	}
	if loadedScan.Scanned != 7 {
		t.Fatalf("loaded scan = %#v", loadedScan)
	}
	if _, exists := loadedFingerprints["persist-me"]; !exists {
		t.Fatal("retry lost fingerprint")
	}
	for _, detail := range loadedScan.FailureDetails {
		if detail.ReasonCode == OperationFailurePolicyStatePersist {
			t.Fatal("transient local persistence failure was written as scan evidence")
		}
	}
}

func TestPolicyDoesNotSwitchStoreWhenDirtyFlushFails(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(Config{DataDir: oldDir})
	defer engine.Shutdown()
	engine.mu.Lock()
	engine.fingerprints["keep-me"] = authFingerprint{Name: "keep-me.json", Path: "/auths/keep-me.json"}
	engine.dirty = true
	engine.mu.Unlock()
	if errRemove := os.RemoveAll(oldDir); errRemove != nil {
		t.Fatalf("remove old data dir: %v", errRemove)
	}
	if errWrite := os.WriteFile(oldDir, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("replace old data dir with file: %v", errWrite)
	}
	engine.Configure(Config{DataDir: newDir})
	engine.mu.RLock()
	store, dirty := engine.store, engine.dirty
	_, exists := engine.fingerprints["keep-me"]
	engine.mu.RUnlock()
	if store != policyStorePath(oldDir) || !dirty || !exists {
		t.Fatalf("dirty state switched or was lost: store=%q dirty=%v exists=%v", store, dirty, exists)
	}
	if got := engine.Snapshot().LastScan.Error; got != policyLocalStoreError {
		t.Fatalf("persistence error = %q", got)
	}
}

func TestPolicyShutdownStopsPersistenceRetry(t *testing.T) {
	previousDelay := policyPersistRetryDelay
	policyPersistRetryDelay = time.Hour
	t.Cleanup(func() { policyPersistRetryDelay = previousDelay })

	dataDir := t.TempDir()
	engine := NewPolicyEngine(&fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}})
	engine.Configure(Config{DataDir: dataDir})
	blockingPath := filepath.Join(dataDir, "blocked")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocking path: %v", errWrite)
	}
	engine.mu.Lock()
	engine.store = filepath.Join(blockingPath, "default-policy.json")
	engine.dirty = true
	engine.mu.Unlock()
	engine.operationMu.Lock()
	engine.persistRuntimeStateLocked()
	engine.operationMu.Unlock()
	engine.mu.RLock()
	scheduled := engine.retryScheduled && engine.retryTimer != nil
	engine.mu.RUnlock()
	if !scheduled {
		t.Fatal("persistence retry was not scheduled")
	}
	engine.Shutdown()
	engine.mu.RLock()
	scheduled = engine.retryScheduled || engine.retryTimer != nil
	engine.mu.RUnlock()
	if scheduled {
		t.Fatal("persistence retry survived shutdown")
	}
}
