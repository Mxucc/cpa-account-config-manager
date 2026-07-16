package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestJobEngineContinuesAfterFailurePersistsRedactedStateAndRetriesFailedOnly(t *testing.T) {
	host := twoEditableAccountsHost()
	var failB atomic.Bool
	failB.Store(true)
	var requestMu sync.Mutex
	requestedNames := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		name, _ := body["name"].(string)
		requestMu.Lock()
		requestedNames = append(requestedNames, name)
		requestMu.Unlock()
		if name == "b.json" && failB.Load() {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, `{"error":"Bearer host-response-secret"}`)
			return
		}
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()

	accounts := NewAccountService(host)
	previews := NewPreviewService(accounts)
	proxyURL := "http://proxy-user:proxy-secret@127.0.0.1:7890"
	disabled := true
	preview, errPreview := previews.BuildTransient(context.Background(), TargetScope{
		Mode: "selected",
		IDs:  []string{"a", "b"},
	}, BatchPatch{
		Disabled: &disabled,
		ProxyURL: &proxyURL,
		Headers:  &HeaderPatch{Set: map[string]string{"Authorization": "Bearer upstream-secret"}},
	})
	if errPreview != nil {
		t.Fatalf("BuildTransient() error = %v", errPreview)
	}

	dataDir := t.TempDir()
	engine := NewJobEngine(accounts)
	engine.doer = server.Client()
	engine.Configure(Config{Workers: 2, DataDir: dataDir, ManagementBaseURL: server.URL})
	defer engine.Shutdown()
	if _, errStart := engine.Start(preview, "management-secret", ""); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	first := waitForTerminalJob(t, engine)
	if first.State != JobStatePartial || first.Succeeded != 1 || first.Failed != 1 || !first.RetryAvailable {
		t.Fatalf("first job = %#v", first)
	}
	encoded, errMarshal := json.Marshal(first)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	storeBytes, errRead := os.ReadFile(filepath.Join(dataDir, "results.json"))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	for _, secret := range []string{"management-secret", "proxy-secret", "proxy-user", "upstream-secret", "host-response-secret"} {
		if strings.Contains(string(encoded), secret) || strings.Contains(string(storeBytes), secret) {
			t.Fatalf("job state leaked %q\nresponse=%s\nstore=%s", secret, encoded, storeBytes)
		}
	}

	scope, retryPatch, parentJobID, errIntent := engine.RetryIntent()
	if errIntent != nil {
		t.Fatalf("RetryIntent() error = %v", errIntent)
	}
	if parentJobID != first.ID || len(scope.IDs) != 1 || scope.IDs[0] != "b" {
		t.Fatalf("retry intent = parent %q scope %#v", parentJobID, scope)
	}
	failB.Store(false)
	retryPreview, errRetryPreview := previews.BuildTransient(context.Background(), scope, retryPatch)
	if errRetryPreview != nil {
		t.Fatalf("retry preview error = %v", errRetryPreview)
	}
	requestMu.Lock()
	requestedNames = nil
	requestMu.Unlock()
	if _, errStart := engine.Start(retryPreview, "management-secret", parentJobID); errStart != nil {
		t.Fatalf("retry Start() error = %v", errStart)
	}
	retry := waitForTerminalJob(t, engine)
	if retry.State != JobStateCompleted || retry.Total != 1 || retry.Succeeded != 1 || retry.ParentJobID != first.ID {
		t.Fatalf("retry job = %#v", retry)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	for _, name := range requestedNames {
		if name != "b.json" {
			t.Fatalf("retry updated unexpected target %q", name)
		}
	}
}

func TestJobEngineFinishReleasesMutationBeforePublishingTerminalState(t *testing.T) {
	mutations := NewMutationCoordinator()
	engine := NewJobEngineWithCoordinator(NewAccountService(&fakeAuthHost{}), mutations)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()

	const jobID = "job-release-order"
	if !mutations.TryAcquire(jobID) {
		t.Fatal("failed to acquire the initial mutation slot")
	}
	engine.mu.Lock()
	engine.running = true
	engine.snapshot = JobSnapshot{
		ID:      jobID,
		State:   JobStateRunning,
		Running: true,
		Results: []JobResult{{ID: "a", Status: ResultSucceeded}},
	}
	engine.mu.Unlock()

	engine.finish(jobRun{jobID: jobID}, false)
	if snapshot := engine.Snapshot(true); snapshot.Running || snapshot.State != JobStateCompleted {
		t.Fatalf("terminal snapshot = %#v", snapshot)
	}
	if !mutations.TryAcquire("next-job") {
		t.Fatal("terminal snapshot was visible before the mutation slot was released")
	}
	mutations.Release("next-job")
}

func TestJobEngineReportsRevisionConflictWithoutWriting(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json"}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"note":"before"}`)},
		},
	}
	accounts := NewAccountService(host)
	previews := NewPreviewService(accounts)
	disabled := true
	preview, errPreview := previews.BuildTransient(context.Background(), TargetScope{Mode: "selected", IDs: []string{"a"}}, BatchPatch{Disabled: &disabled})
	if errPreview != nil {
		t.Fatalf("BuildTransient() error = %v", errPreview)
	}
	host.details["a"] = cpaapi.HostAuthGetResponse{AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"note":"changed"}`)}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, `{"status":"ok"}`)
	}))
	defer server.Close()

	engine := NewJobEngine(accounts)
	engine.doer = server.Client()
	engine.Configure(Config{DataDir: t.TempDir(), ManagementBaseURL: server.URL})
	defer engine.Shutdown()
	if _, errStart := engine.Start(preview, "management-secret", ""); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	snapshot := waitForTerminalJob(t, engine)
	if snapshot.State != JobStateFailed || snapshot.Conflicts != 1 || len(snapshot.Results) != 1 || snapshot.Results[0].Status != ResultConflict {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if calls.Load() != 0 {
		t.Fatalf("management API calls = %d, want 0", calls.Load())
	}
}

func TestJobEngineMarksPersistedRunningStateInterruptedOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	path := jobStorePath(dataDir)
	running := JobSnapshot{
		ID:       "job-1",
		State:    JobStateRunning,
		Running:  true,
		Total:    1,
		Eligible: 1,
		Results:  []JobResult{{ID: "a", Name: "a.json", Status: ResultRunning}},
	}
	if errSave := saveJobSnapshot(path, running); errSave != nil {
		t.Fatalf("saveJobSnapshot() error = %v", errSave)
	}
	engine := NewJobEngine(NewAccountService(&fakeAuthHost{}))
	engine.Configure(Config{DataDir: dataDir})
	snapshot := engine.Snapshot(true)
	if snapshot.State != JobStateInterrupted || snapshot.Running || snapshot.RetryAvailable {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(snapshot.Results) != 1 || snapshot.Results[0].Status != ResultInterrupted || snapshot.Results[0].Retryable {
		t.Fatalf("results = %#v", snapshot.Results)
	}
}

func TestJobEngineConvertsWorkerPanicToSanitizedFailure(t *testing.T) {
	host := &fakeAuthHost{
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	account := Account{ID: "a", Name: "a.json", path: "/auths/a.json", revision: revisionFor([]byte(`{"type":"codex"}`))}
	note := "value"
	result := NewJobEngine(NewAccountService(host)).applyAccountSafely(
		context.Background(), account, BatchPatch{Note: &note}, panicWriter{},
	)
	if result.Status != ResultFailed || result.Error != "unexpected worker failure" || !result.Retryable {
		t.Fatalf("result = %#v", result)
	}
}

func TestJobEngineClearsManagementKeyWhenStartFails(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json"}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}
	accounts := NewAccountService(host)
	preview, errPreview := NewPreviewService(accounts).BuildTransient(
		context.Background(),
		TargetScope{Mode: "selected", IDs: []string{"a"}},
		BatchPatch{Disabled: boolPointer(true)},
	)
	if errPreview != nil {
		t.Fatalf("BuildTransient() error = %v", errPreview)
	}

	parent := t.TempDir()
	blockingPath := filepath.Join(parent, "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	writer := &trackingWriter{key: "management-secret"}
	engine := NewJobEngine(accounts)
	engine.Configure(Config{DataDir: filepath.Join(blockingPath, "data")})
	engine.newWriter = func(string, string, HTTPDoer) (ManagementWriter, error) {
		return writer, nil
	}

	if _, errStart := engine.Start(preview, "management-secret", ""); !errors.Is(errStart, ErrJobStorageUnavailable) {
		t.Fatalf("Start() error = %v, want persistence failure", errStart)
	}
	if writer.key != "" {
		t.Fatalf("management key was not cleared after failed start")
	}
}

func waitForTerminalJob(t *testing.T, engine *JobEngine) JobSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot(true)
		if !snapshot.Running && snapshot.State != JobStateIdle {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job did not finish: %#v", engine.Snapshot(true))
	return JobSnapshot{}
}

func TestJobEngineSharesMutationCoordinatorWithForceSync(t *testing.T) {
	host := twoEditableAccountsHost()
	accounts := NewAccountService(host)
	disabled := true
	preview, errPreview := NewPreviewService(accounts).BuildTransient(
		context.Background(),
		TargetScope{Mode: "selected", IDs: []string{"a"}},
		BatchPatch{Disabled: &disabled},
	)
	if errPreview != nil {
		t.Fatalf("BuildTransient() error = %v", errPreview)
	}
	coordinator := NewMutationCoordinator()
	if !coordinator.TryAcquire("force-test") {
		t.Fatal("failed to reserve mutation coordinator")
	}
	engine := NewJobEngineWithCoordinator(accounts, coordinator)
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	writer := &trackingWriter{}
	engine.newWriter = func(_ string, key string, _ HTTPDoer) (ManagementWriter, error) {
		writer.key = key
		return writer, nil
	}
	if _, errStart := engine.Start(preview, "management-secret", ""); !errors.Is(errStart, ErrJobBusy) {
		t.Fatalf("Start() error = %v, want ErrJobBusy", errStart)
	}
	if writer.key != "" {
		t.Fatal("busy start retained its Management Key")
	}
	coordinator.Release("force-test")
	if _, errStart := engine.Start(preview, "management-secret", ""); errStart != nil {
		t.Fatalf("Start() after release error = %v", errStart)
	}
	waitForTerminalJob(t, engine)
}

func twoEditableAccountsHost() *fakeAuthHost {
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json"},
			{AuthIndex: "b", Name: "b.json", Provider: "gemini", Source: "file", Path: "/auths/b.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"access_token":"secret-a"}`)},
			"b": {AuthIndex: "b", Name: "b.json", Path: "/auths/b.json", JSON: json.RawMessage(`{"access_token":"secret-b"}`)},
		},
	}
}

type panicWriter struct{}

func (panicWriter) PatchFields(context.Context, string, BatchPatch) error {
	panic("secret panic detail")
}

func (panicWriter) PatchDisabled(context.Context, string, bool) error {
	panic("secret panic detail")
}

type trackingWriter struct {
	key string
}

func (w *trackingWriter) PatchFields(context.Context, string, BatchPatch) error {
	return nil
}

func (w *trackingWriter) PatchDisabled(context.Context, string, bool) error {
	return nil
}

func (w *trackingWriter) clearSecrets() {
	w.key = ""
}

func boolPointer(value bool) *bool {
	return &value
}
