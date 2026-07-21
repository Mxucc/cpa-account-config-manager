package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestOperationJournalEmptyListUsesJSONArray(t *testing.T) {
	var journal *OperationJournal
	if response := journal.List(OperationQuery{Page: 1, PageSize: 50}); response.Operations == nil {
		t.Fatal("nil journal Operations is nil, want an empty JSON array")
	}
	if response := (&OperationJournal{}).List(OperationQuery{Page: 1, PageSize: 50}); response.Operations == nil {
		t.Fatal("empty journal Operations is nil, want an empty JSON array")
	}
}

func TestOperationJournalPersistsFiltersUpsertsAndBoundsEntries(t *testing.T) {
	dataDir := t.TempDir()
	journal := NewOperationJournal()
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	journal.now = func() time.Time { return now }
	journal.Configure(Config{DataDir: dataDir})

	journal.Record(OperationEntry{
		Category:    OperationCategoryImport,
		Action:      OperationActionImport,
		Status:      OperationStatusSucceeded,
		Source:      OperationSourceImport,
		Scope:       OperationScopeAll,
		TargetCount: 4,
		Succeeded:   4,
	})
	now = now.Add(time.Minute)
	journal.Upsert("batch:job-1", OperationEntry{
		Category:     OperationCategoryBatch,
		Action:       OperationActionBatchEdit,
		Status:       OperationStatusRunning,
		Source:       OperationSourceManual,
		Scope:        OperationScopeSelected,
		TargetCount:  3,
		RelatedJobID: "job-1",
	})
	now = now.Add(time.Minute)
	journal.Upsert("batch:job-1", OperationEntry{
		Category:     OperationCategoryBatch,
		Action:       OperationActionBatchEdit,
		Status:       OperationStatusPartial,
		Source:       OperationSourceManual,
		Scope:        OperationScopeSelected,
		TargetCount:  3,
		Succeeded:    2,
		Failed:       1,
		ReasonCode:   "partial_failure",
		RelatedJobID: "job-1",
		StartedAt:    now.Add(-time.Minute),
		FinishedAt:   now,
	})

	response := journal.List(OperationQuery{Page: 1, PageSize: 20, Category: OperationCategoryBatch, Search: "job-1"})
	if response.Total != 1 || len(response.Operations) != 1 || response.Summary.Attention != 1 {
		t.Fatalf("response = %#v", response)
	}
	if response.Operations[0].Succeeded != 2 || response.Operations[0].Failed != 1 {
		t.Fatalf("operation = %#v", response.Operations[0])
	}

	reloaded := NewOperationJournal()
	reloaded.Configure(Config{DataDir: dataDir})
	loaded := reloaded.List(OperationQuery{Page: 1, PageSize: 20})
	if loaded.Total != 2 || loaded.Operations[0].RelatedJobID != "job-1" {
		t.Fatalf("loaded = %#v", loaded)
	}
	info, errStat := os.Stat(filepath.Join(dataDir, "operation-log.json"))
	if errStat != nil {
		t.Fatal(errStat)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("operation journal permissions = %o", info.Mode().Perm())
	}
}

func TestOperationJournalSanitizesPersistedFieldsAndRetainsClearEvent(t *testing.T) {
	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: t.TempDir()})
	entry := journal.Record(OperationEntry{
		Category:     OperationCategoryInspection,
		Action:       OperationActionAutoDisable,
		Status:       OperationStatusFailed,
		Source:       OperationSourceInspection,
		TargetID:     "auth-1",
		ReasonCode:   "Bearer raw-secret",
		Version:      "not-a-version",
		Format:       "private-format",
		RelatedJobID: "job-1\nAuthorization: secret",
	})
	if entry.ReasonCode != "operation_failed" || entry.Version != "" || entry.Format != "" || entry.RelatedJobID != "" {
		t.Fatalf("entry was not sanitized: %#v", entry)
	}
	cleared := journal.Clear()
	response := journal.List(OperationQuery{Page: 1, PageSize: 20})
	if response.Total != 1 || cleared.Action != OperationActionJournalClear || response.Operations[0].Action != OperationActionJournalClear {
		t.Fatalf("clear response = %#v entry=%#v", response, cleared)
	}
	raw, errRead := os.ReadFile(journal.store)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, secret := range []string{"raw-secret", "Authorization", "private-format"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("journal leaked %q: %s", secret, raw)
		}
	}
}

func TestOperationManagementRoutesListExportClearAndStrictRecord(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir()))
	defer app.Close()

	invalid := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations/record",
		Body:   []byte(`{"action":"update_install","status":"succeeded","message":"Bearer secret"}`),
	})
	if invalid.StatusCode != http.StatusBadRequest || bytes.Contains(invalid.Body, []byte("Bearer secret")) {
		t.Fatalf("invalid response = %d %s", invalid.StatusCode, invalid.Body)
	}
	recorded := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations/record",
		Body:   []byte(`{"action":"update_install","status":"warning","version":"v0.3.0"}`),
	})
	if recorded.StatusCode != http.StatusCreated {
		t.Fatalf("record status = %d body=%s", recorded.StatusCode, recorded.Body)
	}
	var entry OperationEntry
	if errDecode := json.Unmarshal(recorded.Body, &entry); errDecode != nil {
		t.Fatal(errDecode)
	}
	if entry.Version != "0.3.0" || entry.ReasonCode != "restart_required" || entry.Source != OperationSourcePluginStore {
		t.Fatalf("entry = %#v", entry)
	}

	listed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations",
		Query:  url.Values{"category": []string{"update"}, "page_size": []string{"20"}},
	})
	if listed.StatusCode != http.StatusOK || !bytes.Contains(listed.Body, []byte(`"total":1`)) {
		t.Fatalf("list response = %d %s", listed.StatusCode, listed.Body)
	}
	exported := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations/export",
		Query:  url.Values{"format": []string{"csv"}},
	})
	if exported.StatusCode != http.StatusOK || !strings.Contains(exported.Headers.Get("Content-Type"), "text/csv") || !bytes.Contains(exported.Body, []byte("update_install")) {
		t.Fatalf("export response = %d headers=%v body=%s", exported.StatusCode, exported.Headers, exported.Body)
	}
	cleared := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodDelete,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations",
	})
	if cleared.StatusCode != http.StatusOK || !bytes.Contains(cleared.Body, []byte(OperationActionJournalClear)) {
		t.Fatalf("clear response = %d %s", cleared.StatusCode, cleared.Body)
	}
}

func TestReconcileOperationSourcesDeduplicatesJobsScansActionsAndUpdates(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir()))
	defer app.Close()
	now := time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC)

	app.jobs.mu.Lock()
	app.jobs.snapshot = JobSnapshot{
		ID: "job-1", State: JobStatePartial, Total: 3, Succeeded: 2, Failed: 1,
		StartedAt: now.Add(-time.Minute), FinishedAt: now,
	}
	app.jobs.mu.Unlock()
	app.policies.mu.Lock()
	app.policies.lastScan = PolicyScanSummary{
		StartedAt: now.Add(-2 * time.Minute), FinishedAt: now.Add(-time.Minute), Scanned: 4, Changed: 3, Skipped: 1,
	}
	app.policies.mu.Unlock()
	app.inspection.mu.Lock()
	app.inspection.lastRun = InspectionRunSummary{
		StartedAt: now.Add(-3 * time.Minute), FinishedAt: now.Add(-2 * time.Minute), Scanned: 4,
	}
	app.inspection.actions = []InspectionAction{{
		ID: "action-1", AccountID: "auth-1", Action: InspectionActionDisable,
		Status: InspectionActionSucceeded, ReasonCode: "invalid_credentials", CreatedAt: now,
	}}
	app.inspection.mu.Unlock()
	app.updates.mu.Lock()
	app.updates.checkedAt = now
	app.updates.mu.Unlock()

	app.reconcileOperationSources()
	app.reconcileOperationSources()
	response := app.operations.List(OperationQuery{Page: 1, PageSize: 20})
	if response.Total != 5 {
		t.Fatalf("operations total = %d operations=%#v", response.Total, response.Operations)
	}
	seen := make(map[string]OperationEntry)
	for _, operation := range response.Operations {
		seen[operation.Action] = operation
	}
	if seen[OperationActionBatchEdit].Status != OperationStatusPartial || seen[OperationActionBatchEdit].RelatedJobID != "job-1" {
		t.Fatalf("batch operation = %#v", seen[OperationActionBatchEdit])
	}
	if seen[OperationActionAutoDisable].TargetID != "auth-1" || seen[OperationActionAutoDisable].ReasonCode != "invalid_credentials" {
		t.Fatalf("inspection action = %#v", seen[OperationActionAutoDisable])
	}
	if seen[OperationActionUpdateCheck].Status != OperationStatusSucceeded || seen[OperationActionUpdateCheck].Source != OperationSourcePluginStore || seen[OperationActionUpdateCheck].ReasonCode != "check_completed" || seen[OperationActionUpdateCheck].Version != "" {
		t.Fatalf("update operation = %#v", seen[OperationActionUpdateCheck])
	}
}
