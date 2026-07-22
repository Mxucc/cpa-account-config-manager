package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	info, errStat := os.Stat(operationManifestPath(operationStoreDirectory(dataDir)))
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
	raw, errRead := os.ReadFile(operationManifestPath(journal.store))
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, secret := range []string{"raw-secret", "Authorization", "private-format"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("journal leaked %q: %s", secret, raw)
		}
	}
}

func TestOperationJournalDefaultsToLatestFixedPageAndMigratesLegacyStore(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	journal := NewOperationJournal()
	journal.now = func() time.Time { return now }
	journal.Configure(Config{DataDir: dataDir})
	for index := 0; index < operationPageSize+10; index++ {
		now = now.Add(time.Second)
		journal.Record(operationTestEntry(index, now))
	}
	response := journal.List(OperationQuery{Page: 1, PageSize: 20})
	if response.Total != operationPageSize || response.Retained != operationPageSize || response.PageSize != operationPageSize || response.Pages != 1 || response.ExtendedHistory || response.ArchivedSegments != 0 {
		t.Fatalf("default retention response = %#v", response)
	}
	if response.Operations[0].TargetID != fmt.Sprintf("auth-%d", operationPageSize+9) || response.Operations[len(response.Operations)-1].TargetID != "auth-10" {
		t.Fatalf("default retention boundaries = first:%#v last:%#v", response.Operations[0], response.Operations[len(response.Operations)-1])
	}
	if files := operationSegmentFiles(t, operationStoreDirectory(dataDir)); len(files) != 0 {
		t.Fatalf("default retention created segments: %#v", files)
	}
	hugePage := journal.List(OperationQuery{Page: int(^uint(0) >> 1)})
	if len(hugePage.Operations) != 0 || hugePage.Total != operationPageSize || hugePage.Pages != 1 {
		t.Fatalf("huge page boundary = %#v", hugePage)
	}
	reloaded := NewOperationJournal()
	reloaded.Configure(Config{DataDir: dataDir})
	if loaded := reloaded.List(OperationQuery{Page: 1}); loaded.Total != operationPageSize || loaded.PageSize != operationPageSize {
		t.Fatalf("reloaded default retention = %#v", loaded)
	}

	legacyDir := t.TempDir()
	legacyOperations := make([]OperationEntry, 0, operationPageSize+10)
	for index := 0; index < operationPageSize+10; index++ {
		legacyOperations = append(legacyOperations, operationTestEntry(index, now.Add(time.Duration(index)*time.Second)))
	}
	if errSave := savePrivateJSON(legacyOperationStorePath(legacyDir), legacyPersistedOperationState{
		Version: legacyOperationStoreVersion, Operations: legacyOperations,
	}); errSave != nil {
		t.Fatal(errSave)
	}
	migrated := NewOperationJournal()
	migrated.Configure(Config{DataDir: legacyDir})
	if result := migrated.List(OperationQuery{Page: 1}); result.Total != operationPageSize || result.Operations[len(result.Operations)-1].TargetID != "auth-10" {
		t.Fatalf("legacy migration = %#v", result)
	}
	if _, errStat := os.Stat(legacyOperationStorePath(legacyDir)); !os.IsNotExist(errStat) {
		t.Fatalf("legacy journal was not removed after migration: %v", errStat)
	}
}

func TestOperationJournalExtendedHistoryRotatesUpsertsDisablesAndClears(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	journal := NewOperationJournal()
	journal.now = func() time.Time { return now }
	journal.Configure(Config{DataDir: dataDir})
	settings, errEnable := journal.UpdateRetentionSettings(true)
	if errEnable != nil || !settings.ExtendedHistory || settings.PageSize != operationPageSize {
		t.Fatalf("enable extended history settings=%#v err=%v", settings, errEnable)
	}
	for index := 0; index <= operationPageSize; index++ {
		now = now.Add(time.Second)
		journal.Record(operationTestEntry(index, now))
	}
	settings = journal.RetentionSettings()
	if settings.Retained != operationPageSize+1 || settings.ArchivedSegments != 1 {
		t.Fatalf("rotated settings = %#v", settings)
	}
	if files := operationSegmentFiles(t, operationStoreDirectory(dataDir)); len(files) != 1 {
		t.Fatalf("rotated segment files = %#v", files)
	}
	firstPage := journal.List(OperationQuery{Page: 1, PageSize: 1})
	secondPage := journal.List(OperationQuery{Page: 2, PageSize: 1})
	if firstPage.PageSize != operationPageSize || firstPage.Total != operationPageSize+1 || len(firstPage.Operations) != operationPageSize ||
		secondPage.Pages != 2 || len(secondPage.Operations) != 1 {
		t.Fatalf("extended fixed pages first=%#v second=%#v", firstPage, secondPage)
	}

	now = now.Add(time.Minute)
	updated := journal.Upsert("event-0", OperationEntry{
		Category: OperationCategoryBatch, Action: OperationActionBatchEdit, Status: OperationStatusFailed,
		Source: OperationSourceManual, Scope: OperationScopeSelected, Failed: 1,
		StartedAt: now.Add(-time.Minute), FinishedAt: now, ReasonCode: "operation_failed",
	})
	if updated.EventID != "event-0" || updated.TargetID != "auth-0" || updated.Status != OperationStatusFailed {
		t.Fatalf("archived upsert = %#v", updated)
	}
	all := append(journal.List(OperationQuery{Page: 1}).Operations, journal.List(OperationQuery{Page: 2}).Operations...)
	matched := 0
	for _, entry := range all {
		if entry.EventID == "event-0" {
			matched++
			if entry.Status != OperationStatusFailed {
				t.Fatalf("archived upsert was not persisted: %#v", entry)
			}
		}
	}
	if matched != 1 {
		t.Fatalf("archived upsert matches = %d", matched)
	}
	orphanPath := operationSegmentPath(operationStoreDirectory(dataDir), 99999999)
	if errWrite := os.WriteFile(orphanPath, []byte(`{"version":1,"operations":[]}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	reloaded := NewOperationJournal()
	reloaded.Configure(Config{DataDir: dataDir})
	if loaded := reloaded.RetentionSettings(); !loaded.ExtendedHistory || loaded.Retained != operationPageSize+1 || loaded.ArchivedSegments != 1 {
		t.Fatalf("reloaded extended settings = %#v", loaded)
	}
	if _, errStat := os.Stat(orphanPath); !os.IsNotExist(errStat) {
		t.Fatalf("orphaned segment was not removed: %v", errStat)
	}
	disabled, errDisable := reloaded.UpdateRetentionSettings(false)
	if errDisable != nil || disabled.ExtendedHistory || disabled.Retained != operationPageSize || disabled.ArchivedSegments != 0 {
		t.Fatalf("disable extended history settings=%#v err=%v", disabled, errDisable)
	}
	if files := operationSegmentFiles(t, operationStoreDirectory(dataDir)); len(files) != 0 {
		t.Fatalf("disable retained segment files: %#v", files)
	}

	if _, errEnable = reloaded.UpdateRetentionSettings(true); errEnable != nil {
		t.Fatal(errEnable)
	}
	now = now.Add(time.Second)
	reloaded.now = func() time.Time { return now }
	reloaded.Record(operationTestEntry(operationPageSize+1, now))
	if files := operationSegmentFiles(t, operationStoreDirectory(dataDir)); len(files) != 1 {
		t.Fatalf("second rotation segment files = %#v", files)
	}
	cleared := reloaded.Clear()
	if cleared.Action != OperationActionJournalClear || reloaded.RetentionSettings().Retained != 1 {
		t.Fatalf("clear result=%#v settings=%#v", cleared, reloaded.RetentionSettings())
	}
	if files := operationSegmentFiles(t, operationStoreDirectory(dataDir)); len(files) != 0 {
		t.Fatalf("clear retained segment files: %#v", files)
	}
}

func TestOperationJournalPagesAcrossSegmentsAndExportsOneSortedSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: dataDir})
	if _, errEnable := journal.UpdateRetentionSettings(true); errEnable != nil {
		t.Fatal(errEnable)
	}
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	all := make([]OperationEntry, 0, operationPageSize*2+5)
	for index := 0; index < operationPageSize*2+5; index++ {
		all = append(all, operationTestEntry(index, base.Add(time.Duration(index)*time.Second)))
	}
	store := operationStoreDirectory(dataDir)
	if errSave := saveOperationSegment(store, 1, all[:operationPageSize]); errSave != nil {
		t.Fatal(errSave)
	}
	if errSave := saveOperationSegment(store, 2, all[operationPageSize:operationPageSize*2]); errSave != nil {
		t.Fatal(errSave)
	}
	journal.mu.Lock()
	journal.segments = []persistedOperationSegment{{ID: 1, Count: operationPageSize}, {ID: 2, Count: operationPageSize}}
	journal.operations = cloneOperationEntries(all[operationPageSize*2:])
	journal.nextSegmentID = 3
	journal.mu.Unlock()

	first := journal.List(OperationQuery{Page: 1})
	second := journal.List(OperationQuery{Page: 2})
	third := journal.List(OperationQuery{Page: 3})
	if first.Total != len(all) || first.Summary.Total != len(all) || first.Summary.Succeeded != len(all) || first.Pages != 3 {
		t.Fatalf("first page summary = %#v", first)
	}
	if len(first.Operations) != operationPageSize || first.Operations[0].TargetID != "auth-1004" || first.Operations[operationPageSize-1].TargetID != "auth-505" {
		t.Fatalf("first page boundaries = first:%#v last:%#v", first.Operations[0], first.Operations[len(first.Operations)-1])
	}
	if len(second.Operations) != operationPageSize || second.Operations[0].TargetID != "auth-504" || second.Operations[operationPageSize-1].TargetID != "auth-5" {
		t.Fatalf("second page boundaries = first:%#v last:%#v", second.Operations[0], second.Operations[len(second.Operations)-1])
	}
	if len(third.Operations) != 5 || third.Operations[0].TargetID != "auth-4" || third.Operations[4].TargetID != "auth-0" {
		t.Fatalf("third page = %#v", third.Operations)
	}
	exported, errExport := journal.ExportSnapshot(OperationQuery{})
	if errExport != nil || len(exported) != len(all) || exported[0].TargetID != "auth-1004" || exported[len(exported)-1].TargetID != "auth-0" {
		t.Fatalf("exported snapshot count=%d first=%#v last=%#v error=%v", len(exported), exported[0], exported[len(exported)-1], errExport)
	}
}

func operationTestEntry(index int, at time.Time) OperationEntry {
	return OperationEntry{
		EventID: fmt.Sprintf("event-%d", index), Category: OperationCategoryBatch,
		Action: OperationActionBatchEdit, Status: OperationStatusSucceeded, Source: OperationSourceManual,
		Scope: OperationScopeSingle, TargetID: fmt.Sprintf("auth-%d", index), TargetCount: 1, Succeeded: 1,
		StartedAt: at, FinishedAt: at,
	}
}

func operationSegmentFiles(t *testing.T, store string) []string {
	t.Helper()
	entries, errRead := os.ReadDir(store)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		t.Fatal(errRead)
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if operationSegmentFilename(entry.Name()) {
			files = append(files, entry.Name())
		}
	}
	return files
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
	settingsPath := "/v0/management/plugins/cpa-account-config-manager/operations/settings"
	defaultSettings := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: settingsPath})
	if defaultSettings.StatusCode != http.StatusOK || !bytes.Contains(defaultSettings.Body, []byte(`"extended_history":false`)) ||
		!bytes.Contains(defaultSettings.Body, []byte(`"page_size":500`)) {
		t.Fatalf("default settings = %d %s", defaultSettings.StatusCode, defaultSettings.Body)
	}
	invalidSettings := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: settingsPath, Body: []byte(`{}`),
	})
	if invalidSettings.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid settings = %d %s", invalidSettings.StatusCode, invalidSettings.Body)
	}
	savedSettings := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: settingsPath, Body: []byte(`{"extended_history":true}`),
	})
	if savedSettings.StatusCode != http.StatusOK || !bytes.Contains(savedSettings.Body, []byte(`"extended_history":true`)) {
		t.Fatalf("saved settings = %d %s", savedSettings.StatusCode, savedSettings.Body)
	}

	listed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations",
		Query:  url.Values{"category": []string{"update"}, "page_size": []string{"20"}},
	})
	if listed.StatusCode != http.StatusOK || !bytes.Contains(listed.Body, []byte(`"total":1`)) ||
		!bytes.Contains(listed.Body, []byte(`"page_size":500`)) || !bytes.Contains(listed.Body, []byte(`"extended_history":true`)) {
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

func TestOperationFromInspectionActionPreservesAutomaticDeleteSource(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	automatic := InspectionAction{
		ID: "action-auto-delete", AccountID: "auth-1", Action: InspectionActionDelete,
		Status: InspectionActionSucceeded, Source: OperationSourceInspection,
		ReasonCode: "account_deactivated", CreatedAt: now,
	}
	entry, ok := operationFromInspectionAction(automatic)
	if !ok || entry.Action != OperationActionAutoDelete || entry.Source != OperationSourceInspection ||
		entry.Scope != OperationScopeSingle || entry.TargetID != "auth-1" || entry.Succeeded != 1 {
		t.Fatalf("automatic delete operation = %#v ok=%t", entry, ok)
	}

	manual := automatic
	manual.ID = "action-manual-delete"
	manual.Source = OperationSourceManual
	if entry, ok = operationFromInspectionAction(manual); ok {
		t.Fatalf("manual delete produced duplicate operation = %#v", entry)
	}

	legacy := automatic
	legacy.ID = "action-legacy-delete"
	legacy.Source = ""
	if entry, ok = operationFromInspectionAction(legacy); !ok || entry.Action != OperationActionAutoDelete || entry.Source != OperationSourceInspection {
		t.Fatalf("legacy automatic delete operation = %#v ok=%t", entry, ok)
	}
}

func TestManualInspectionDeleteRecordsOneTruthfulBatchOperation(t *testing.T) {
	host := editableAccountDeleteHost()
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deleteCalls++
		if request.Method != http.MethodDelete || request.URL.Query().Get("name") != "operator.json" {
			t.Errorf("delete request = %s %s", request.Method, request.URL.String())
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.deletions.doer = server.Client()
	app.Configure([]byte(fmt.Sprintf("data_dir: %q\nmanagement_base_url: %q\n", t.TempDir(), server.URL)))
	defer app.Close()
	app.inspection.mu.Lock()
	app.inspection.records["auth-1"] = inspectionRecord{Result: InspectionResult{
		ID: "auth-1", Name: "operator.json", Provider: "codex", Editable: true,
		Health: InspectionHealthInvalidCredentials, ReasonCode: "invalid_credentials",
		Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
		SignalSource: InspectionSignalNative,
	}}
	app.inspection.mu.Unlock()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/inspection/delete",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    []byte(`{"account_ids":["auth-1"],"confirm":true}`),
	})
	if response.StatusCode != http.StatusOK || deleteCalls != 1 {
		t.Fatalf("manual delete response = %d %s calls=%d", response.StatusCode, response.Body, deleteCalls)
	}

	app.reconcileOperationSources()
	listed := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	if listed.Total != 1 || len(listed.Operations) != 1 {
		t.Fatalf("manual delete operations = %#v", listed.Operations)
	}
	entry := listed.Operations[0]
	if entry.Action != OperationActionInspectionManualDelete || entry.Source != OperationSourceManual ||
		entry.Scope != OperationScopeSelected || entry.TargetID != "" || entry.TargetCount != 1 ||
		entry.Succeeded != 1 || entry.Failed != 0 || entry.Skipped != 0 || entry.Status != OperationStatusSucceeded {
		t.Fatalf("manual delete operation = %#v", entry)
	}
	if entry.Action == OperationActionAutoDelete {
		t.Fatalf("manual delete was mislabeled as automatic: %#v", entry)
	}
}

func TestInspectionAutoDeleteRecordsScheduledInspectionOperation(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte(fmt.Sprintf("data_dir: %q\n", t.TempDir())))
	defer app.Close()
	app.inspection.mu.Lock()
	app.inspection.policy.AutoDelete = true
	app.inspection.mu.Unlock()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/inspection/auto-delete",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("auto-delete response = %d %s", response.StatusCode, response.Body)
	}

	listed := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize})
	if listed.Total != 1 || len(listed.Operations) != 1 {
		t.Fatalf("auto-delete operations = %#v", listed.Operations)
	}
	entry := listed.Operations[0]
	if entry.Action != OperationActionAutoDelete || entry.Source != OperationSourceInspection ||
		entry.Scope != OperationScopeScheduled || entry.TargetCount != 0 || entry.Status != OperationStatusSucceeded {
		t.Fatalf("auto-delete operation = %#v", entry)
	}
}
