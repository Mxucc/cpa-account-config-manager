package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestForceSyncOverwritesManagedFieldsPreservesSecretsAndContinuesReadOnly(t *testing.T) {
	host := forceSyncHost()
	policies := NewPolicyEngine(host)
	policies.Configure(Config{DataDir: t.TempDir()})
	priority := 0
	websockets := false
	if _, errSet := policies.SetPolicy(DefaultPolicy{
		Enabled: true, Priority: &priority, Websockets: &websockets,
	}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	mutations := NewMutationCoordinator()
	engine := NewForceSyncEngine(NewAccountService(host), host, policies, mutations)
	engine.Configure(Config{Workers: 2})
	defer func() {
		engine.Shutdown()
		policies.Shutdown()
	}()

	preview, errPreview := engine.Preview(context.Background())
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if preview.Total != 3 || preview.Eligible != 2 || preview.ReadOnly != 1 || preview.PhysicalFiles != 2 {
		t.Fatalf("preview counts = %#v", preview)
	}
	if preview.Policy.Priority == nil || *preview.Policy.Priority != 0 || preview.Policy.Websockets == nil || *preview.Policy.Websockets {
		t.Fatalf("preview policy = %#v", preview.Policy)
	}
	if _, errStart := engine.Start(preview.ID); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	snapshot := waitForTerminalForceSync(t, engine)
	if snapshot.State != JobStatePartial || snapshot.Succeeded != 2 || snapshot.Skipped != 1 || snapshot.Failed != 0 || snapshot.Conflicts != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	host.mu.Lock()
	aJSON := append(json.RawMessage(nil), host.details["a"].JSON...)
	bJSON := append(json.RawMessage(nil), host.details["b"].JSON...)
	saves := append([]cpaapi.HostAuthSaveRequest(nil), host.saves...)
	host.mu.Unlock()
	if len(saves) != 1 || saves[0].Name != "a.json" {
		t.Fatalf("saves = %#v, want only a.json", saves)
	}
	assertManagedPolicyValues(t, aJSON, 0, false)
	assertManagedPolicyValues(t, bJSON, 0, false)
	var document map[string]any
	if errDecode := json.Unmarshal(aJSON, &document); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	if document["disabled"] != true || document["note"] != "keep-me" {
		t.Fatalf("unmanaged fields changed: %#v", document)
	}
	for _, secret := range []string{"access-secret", "header-secret", "proxy-secret"} {
		if !bytes.Contains(aJSON, []byte(secret)) {
			t.Fatalf("updated auth JSON lost %q: %s", secret, aJSON)
		}
	}
	publicPreview, _ := json.Marshal(preview)
	publicJob, _ := json.Marshal(snapshot)
	for _, secret := range []string{"access-secret", "header-secret", "proxy-secret", "/auths"} {
		if bytes.Contains(publicPreview, []byte(secret)) || bytes.Contains(publicJob, []byte(secret)) {
			t.Fatalf("public force-sync state leaked %q", secret)
		}
	}
}

func TestForceSyncDetectsRevisionConflictWithoutSaving(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json", Size: 10, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"priority":9,"access_token":"preview-secret"}`)},
		},
	}
	policies := NewPolicyEngine(host)
	policies.Configure(Config{DataDir: t.TempDir()})
	priority := 1
	if _, errSet := policies.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine := NewForceSyncEngine(NewAccountService(host), host, policies, NewMutationCoordinator())
	defer func() {
		engine.Shutdown()
		policies.Shutdown()
	}()
	preview, errPreview := engine.Preview(context.Background())
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	host.mu.Lock()
	detail := host.details["a"]
	detail.JSON = json.RawMessage(`{"priority":8,"access_token":"changed-secret"}`)
	host.details["a"] = detail
	host.mu.Unlock()
	if _, errStart := engine.Start(preview.ID); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	snapshot := waitForTerminalForceSync(t, engine)
	if snapshot.State != JobStateFailed || snapshot.Conflicts != 1 || snapshot.Succeeded != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.saves) != 0 {
		t.Fatalf("conflicted target was saved: %#v", host.saves)
	}
}

func TestForceSyncRejectsStalePreviewAndRetainsBusyPreview(t *testing.T) {
	host := forceSyncHost()
	policies := NewPolicyEngine(host)
	policies.Configure(Config{DataDir: t.TempDir()})
	priority := 1
	if _, errSet := policies.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	coordinator := NewMutationCoordinator()
	engine := NewForceSyncEngine(NewAccountService(host), host, policies, coordinator)
	defer func() {
		engine.Shutdown()
		policies.Shutdown()
	}()

	stalePreview, errPreview := engine.Preview(context.Background())
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	priority = 2
	if _, errSet := policies.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("second SetPolicy() error = %v", errSet)
	}
	if _, errStart := engine.Start(stalePreview.ID); !errors.Is(errStart, ErrForcePreviewStale) {
		t.Fatalf("Start() error = %v, want stale preview", errStart)
	}

	busyPreview, errPreview := engine.Preview(context.Background())
	if errPreview != nil {
		t.Fatalf("second Preview() error = %v", errPreview)
	}
	if !coordinator.TryAcquire("batch-test") {
		t.Fatal("failed to reserve mutation coordinator")
	}
	if _, errStart := engine.Start(busyPreview.ID); !errors.Is(errStart, ErrJobBusy) {
		t.Fatalf("busy Start() error = %v", errStart)
	}
	coordinator.Release("batch-test")
	if _, errStart := engine.Start(busyPreview.ID); errStart != nil {
		t.Fatalf("retained preview Start() error = %v", errStart)
	}
	waitForTerminalForceSync(t, engine)
}

func TestForceSyncSanitizesSaveFailureAndContinues(t *testing.T) {
	host := forceSyncHost()
	host.saveErrors = map[string]error{"a.json": errors.New("Bearer callback-secret")}
	policies := NewPolicyEngine(host)
	policies.Configure(Config{DataDir: t.TempDir()})
	priority := -4
	if _, errSet := policies.SetPolicy(DefaultPolicy{Enabled: true, Priority: &priority}); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine := NewForceSyncEngine(NewAccountService(host), host, policies, NewMutationCoordinator())
	engine.Configure(Config{Workers: 2})
	defer func() {
		engine.Shutdown()
		policies.Shutdown()
	}()
	preview, errPreview := engine.Preview(context.Background())
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if _, errStart := engine.Start(preview.ID); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	snapshot := waitForTerminalForceSync(t, engine)
	if snapshot.State != JobStatePartial || snapshot.Failed != 1 || snapshot.Succeeded != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, secret := range []string{"callback-secret", "access-secret", "header-secret", "proxy-secret"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("force-sync status leaked %q: %s", secret, encoded)
		}
	}
}

func forceSyncHost() *fakeAuthHost {
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json", Size: 120, ModTime: time.Now().UTC()},
			{AuthIndex: "b", Name: "b.json", Provider: "codex", Source: "file", Path: "/auths/b.json", Size: 80, ModTime: time.Now().UTC()},
			{AuthIndex: "runtime", Name: "runtime.json", Provider: "codex", Source: "runtime", RuntimeOnly: true},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {
				AuthIndex: "a", Name: "a.json", Path: "/auths/a.json",
				JSON: json.RawMessage(`{"priority":9,"websockets":true,"disabled":true,"note":"keep-me","access_token":"access-secret","headers":{"Authorization":"header-secret"},"proxy_url":"http://user:proxy-secret@127.0.0.1:7890"}`),
			},
			"b": {
				AuthIndex: "b", Name: "b.json", Path: "/auths/b.json",
				JSON: json.RawMessage(`{"priority":0,"websockets":false,"api_key":"second-secret"}`),
			},
		},
	}
}

func waitForTerminalForceSync(t *testing.T, engine *ForceSyncEngine) ForceSyncJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot(true)
		if !snapshot.Running && snapshot.State != JobStateIdle {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := engine.Snapshot(true)
	t.Fatalf("force-sync job did not finish: %#v", snapshot)
	return ForceSyncJobSnapshot{}
}
