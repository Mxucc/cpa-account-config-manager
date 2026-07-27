package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type delayedDetailAuthHost struct {
	*fakeAuthHost
	delay time.Duration
}

type blockingDetailAuthHost struct {
	*fakeAuthHost
	mu        sync.Mutex
	active    int
	maxActive int
	started   chan struct{}
	release   chan struct{}
}

func (h *blockingDetailAuthHost) GetAuth(ctx context.Context, id string) (cpaapi.HostAuthGetResponse, error) {
	h.mu.Lock()
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.mu.Unlock()
	h.started <- struct{}{}
	select {
	case <-ctx.Done():
	case <-h.release:
	}
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	if errContext := ctx.Err(); errContext != nil {
		return cpaapi.HostAuthGetResponse{}, errContext
	}
	return h.fakeAuthHost.GetAuth(ctx, id)
}

func (h *delayedDetailAuthHost) GetAuth(ctx context.Context, id string) (cpaapi.HostAuthGetResponse, error) {
	timer := time.NewTimer(h.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return cpaapi.HostAuthGetResponse{}, ctx.Err()
	case <-timer.C:
		return h.fakeAuthHost.GetAuth(ctx, id)
	}
}

func BenchmarkUnrelatedManagementRequestWithInspectionHistory(b *testing.B) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.operations.Configure(Config{DataDir: b.TempDir()})

	actions := make([]InspectionAction, 500)
	for index := range actions {
		actions[index] = InspectionAction{
			ID:         fmt.Sprintf("action-%03d", index),
			AccountID:  fmt.Sprintf("account-%03d", index),
			Action:     InspectionActionDisable,
			Status:     InspectionActionSucceeded,
			Source:     OperationSourceInspection,
			ReasonCode: "quota_exhausted",
			CreatedAt:  time.Unix(int64(index+1), 0).UTC(),
		}
	}
	app.inspection.mu.Lock()
	app.inspection.actions = actions
	app.inspection.mu.Unlock()
	app.reconcileOperationSources()

	request := cpaapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management/plugins/cpa-account-config-manager/experiments",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	}
	if response := app.HandleManagement(context.Background(), request); response.StatusCode != http.StatusOK {
		b.Fatalf("prime response = %d", response.StatusCode)
	}
	b.ResetTimer()
	for range b.N {
		if response := app.HandleManagement(context.Background(), request); response.StatusCode != http.StatusOK {
			b.Fatalf("response = %d", response.StatusCode)
		}
	}
}

func BenchmarkAccountListDetailLoading(b *testing.B) {
	entries, details := accountDetailFixtures(50)
	host := &delayedDetailAuthHost{
		fakeAuthHost: &fakeAuthHost{entries: entries, details: details},
		delay:        time.Millisecond,
	}
	accounts := NewAccountService(host)
	b.ResetTimer()
	for range b.N {
		response, errList := accounts.List(context.Background(), ListQuery{Page: 1, PageSize: 50})
		if errList != nil || len(response.Accounts) != len(entries) {
			b.Fatalf("list = %d accounts, %v", len(response.Accounts), errList)
		}
	}
}

func TestAccountDetailLoadingUsesBoundedConcurrency(t *testing.T) {
	entries, details := accountDetailFixtures(accountDetailWorkers + 4)
	host := &blockingDetailAuthHost{
		fakeAuthHost: &fakeAuthHost{entries: entries, details: details},
		started:      make(chan struct{}, len(entries)),
		release:      make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		response, errList := NewAccountService(host).List(context.Background(), ListQuery{Page: 1, PageSize: len(entries)})
		if errList == nil && len(response.Accounts) != len(entries) {
			errList = fmt.Errorf("list returned %d accounts, want %d", len(response.Accounts), len(entries))
		}
		done <- errList
	}()
	for range accountDetailWorkers {
		select {
		case <-host.started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent account detail reads")
		}
	}
	host.mu.Lock()
	active, maxActive := host.active, host.maxActive
	host.mu.Unlock()
	if active < 2 {
		t.Fatalf("active detail reads = %d, want at least 2", active)
	}
	if maxActive > accountDetailWorkers {
		t.Fatalf("maximum detail reads = %d, want at most %d", maxActive, accountDetailWorkers)
	}
	close(host.release)
	if errList := <-done; errList != nil {
		t.Fatal(errList)
	}
}

func TestOperationReconciliationRunsOnlyForOperationReads(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.operations.Configure(Config{DataDir: t.TempDir()})
	app.inspection.mu.Lock()
	app.inspection.actions = []InspectionAction{{
		ID: "deferred-action", AccountID: "account-1", Action: InspectionActionDisable,
		Status: InspectionActionSucceeded, Source: OperationSourceInspection,
		CreatedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	}}
	app.inspection.mu.Unlock()

	unrelated := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/experiments",
	})
	if unrelated.StatusCode != http.StatusOK {
		t.Fatalf("unrelated response = %d", unrelated.StatusCode)
	}
	if got := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize}).Total; got != 0 {
		t.Fatalf("operations after unrelated request = %d, want 0", got)
	}

	listed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/operations",
	})
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("operation list response = %d", listed.StatusCode)
	}
	if got := app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize}).Total; got != 1 {
		t.Fatalf("operations after journal read = %d, want 1", got)
	}
}

func TestInspectionResultReadsDoNotReloadCPAAccounts(t *testing.T) {
	host := &fakeAuthHost{}
	app := NewApp(host, []byte("index"))
	defer app.Close()

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/inspection/results",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("inspection results response = %d", response.StatusCode)
	}
	host.mu.Lock()
	listCalls := host.listCalls
	host.mu.Unlock()
	if listCalls != 0 {
		t.Fatalf("CPA account list calls = %d, want 0 for a cached inspection read", listCalls)
	}
}

func accountDetailFixtures(count int) ([]cpaapi.HostAuthFileEntry, map[string]cpaapi.HostAuthGetResponse) {
	entries := make([]cpaapi.HostAuthFileEntry, count)
	details := make(map[string]cpaapi.HostAuthGetResponse, len(entries))
	for index := range entries {
		id := fmt.Sprintf("auth-%03d", index)
		path := fmt.Sprintf("/auth/%s.json", id)
		entries[index] = cpaapi.HostAuthFileEntry{
			AuthIndex: id, Name: id + ".json", Provider: "codex", Type: "codex", Source: "file", Path: path,
		}
		details[id] = cpaapi.HostAuthGetResponse{
			AuthIndex: id, Name: id + ".json", Path: path,
			JSON: json.RawMessage(`{"type":"codex","priority":1}`),
		}
	}
	return entries, details
}
