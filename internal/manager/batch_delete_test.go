package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestBatchDeletePreviewClassifiesEditableReadOnlyAndMissingTargets(t *testing.T) {
	host := editableAccountDeleteHost()
	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
		AuthIndex: "runtime-1", Name: "runtime.json", Provider: "codex", Source: "runtime", RuntimeOnly: true,
	})
	previews := NewPreviewService(NewAccountService(host))
	preview, errPreview := previews.CreateDelete(context.Background(), BatchDeletePreviewRequest{Scope: TargetScope{
		Mode: "selected", IDs: []string{"auth-1", "runtime-1", "missing-1"},
	}})
	if errPreview != nil {
		t.Fatalf("CreateDelete() error = %v", errPreview)
	}
	if preview.Operation != BatchOperationDelete || preview.Total != 3 || preview.Eligible != 1 || preview.ReadOnly != 1 || preview.Missing != 1 || preview.PhysicalFiles != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	if len(preview.Patch.Fields) != 0 || len(preview.Targets) != 3 || !preview.Targets[0].Eligible || preview.Targets[1].Eligible || preview.Targets[2].Eligible {
		t.Fatalf("targets = %#v patch=%#v", preview.Targets, preview.Patch)
	}
	encoded, errEncode := json.Marshal(preview)
	if errEncode != nil {
		t.Fatalf("Marshal() error = %v", errEncode)
	}
	for _, forbidden := range []string{"account-secret", "/auths", "access_token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBatchDeleteRoutesRequireConfirmationDeleteEligibleTargetsAndRetryFailures(t *testing.T) {
	host := twoEditableAccountsHost()
	var failB atomic.Bool
	failB.Store(true)
	deleteWriter := &batchDeleteWriter{failB: &failB}

	app := NewApp(host, []byte("index"))
	app.jobs.newWriter = func(_ string, key string, _ HTTPDoer) (ManagementWriter, error) {
		deleteWriter.setKey(key)
		return deleteWriter, nil
	}
	app.Configure([]byte("workers: 2\ndata_dir: " + quotedYAML(t.TempDir()) + "\n"))
	defer app.Close()

	previewBody, _ := json.Marshal(BatchDeletePreviewRequest{Scope: TargetScope{Mode: "selected", IDs: []string{"a", "b"}}})
	previewResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/batch/delete/preview", Body: previewBody,
	})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d %s", previewResponse.StatusCode, previewResponse.Body)
	}
	var preview BatchPreview
	if errDecode := json.Unmarshal(previewResponse.Body, &preview); errDecode != nil {
		t.Fatalf("decode preview: %v", errDecode)
	}
	startWithoutConfirmation, _ := json.Marshal(BatchDeleteStartRequest{PreviewID: preview.ID})
	bypass := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/batch/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}}, Body: startWithoutConfirmation,
	})
	if bypass.StatusCode != http.StatusBadRequest {
		t.Fatalf("generic start accepted delete preview = %d %s", bypass.StatusCode, bypass.Body)
	}
	unconfirmed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/batch/delete/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}}, Body: startWithoutConfirmation,
	})
	if unconfirmed.StatusCode != http.StatusBadRequest {
		t.Fatalf("unconfirmed start = %d %s", unconfirmed.StatusCode, unconfirmed.Body)
	}

	startBody, _ := json.Marshal(BatchDeleteStartRequest{PreviewID: preview.ID, Confirm: true})
	started := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/batch/delete/start",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}}, Body: startBody,
	})
	if started.StatusCode != http.StatusAccepted {
		t.Fatalf("start = %d %s", started.StatusCode, started.Body)
	}
	first := waitForTerminalJob(t, app.jobs)
	if first.Operation != BatchOperationDelete || first.State != JobStatePartial || first.Succeeded != 1 || first.Failed != 1 || !first.RetryAvailable {
		t.Fatalf("first job = %#v", first)
	}
	for _, result := range first.Results {
		if strings.Contains(result.Error, "response-secret") {
			t.Fatalf("result leaked upstream response: %#v", result)
		}
	}

	failB.Store(false)
	retried := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/batch/retry",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if retried.StatusCode != http.StatusAccepted {
		t.Fatalf("retry = %d %s", retried.StatusCode, retried.Body)
	}
	second := waitForTerminalJob(t, app.jobs)
	if second.Operation != BatchOperationDelete || second.ParentJobID != first.ID || second.State != JobStateCompleted || second.Total != 1 || second.Succeeded != 1 {
		t.Fatalf("retry job = %#v", second)
	}
	requestedNames := deleteWriter.namesSnapshot()
	if len(requestedNames) != 3 || requestedNames[2] != "b.json" {
		t.Fatalf("delete requests = %#v", requestedNames)
	}
}

func TestBatchDeleteJobRejectsStaleRevisionWithoutCallingCPA(t *testing.T) {
	host := editableAccountDeleteHost()
	accounts := NewAccountService(host)
	previews := NewPreviewService(accounts)
	preview, errPreview := previews.BuildDeleteTransient(context.Background(), TargetScope{Mode: "selected", IDs: []string{"auth-1"}})
	if errPreview != nil {
		t.Fatalf("BuildDeleteTransient() error = %v", errPreview)
	}
	host.mu.Lock()
	detail := host.details["auth-1"]
	detail.JSON = json.RawMessage(`{"type":"codex","access_token":"changed-secret"}`)
	host.details["auth-1"] = detail
	host.mu.Unlock()

	deleteWriter := &batchDeleteWriter{}
	engine := NewJobEngine(accounts)
	engine.newWriter = func(_ string, key string, _ HTTPDoer) (ManagementWriter, error) {
		deleteWriter.setKey(key)
		return deleteWriter, nil
	}
	engine.Configure(Config{DataDir: t.TempDir()})
	defer engine.Shutdown()
	if _, errStart := engine.Start(preview, "management-secret", ""); errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	snapshot := waitForTerminalJob(t, engine)
	if snapshot.State != JobStateFailed || snapshot.Conflicts != 1 || len(deleteWriter.namesSnapshot()) != 0 {
		t.Fatalf("snapshot = %#v calls=%#v", snapshot, deleteWriter.namesSnapshot())
	}
}

type batchDeleteWriter struct {
	mu    sync.Mutex
	key   string
	names []string
	failB *atomic.Bool
}

func (w *batchDeleteWriter) PatchFields(context.Context, string, BatchPatch) error {
	return nil
}

func (w *batchDeleteWriter) PatchDisabled(context.Context, string, bool) error {
	return nil
}

func (w *batchDeleteWriter) DeleteAuthFile(_ context.Context, name string) error {
	w.mu.Lock()
	w.names = append(w.names, name)
	w.mu.Unlock()
	if name == "b.json" && w.failB != nil && w.failB.Load() {
		return errors.New("Bearer upstream-response-secret")
	}
	return nil
}

func (w *batchDeleteWriter) clearSecrets() {
	w.mu.Lock()
	w.key = ""
	w.mu.Unlock()
}

func (w *batchDeleteWriter) setKey(key string) {
	w.mu.Lock()
	w.key = key
	w.mu.Unlock()
}

func (w *batchDeleteWriter) namesSnapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.names...)
}

func quotedYAML(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
