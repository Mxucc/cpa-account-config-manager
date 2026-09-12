package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func configuredConcurrencyService(t *testing.T, schema uint32) *AccountConcurrencyService {
	t.Helper()
	service := NewAccountConcurrencyService()
	service.Configure(Config{DataDir: t.TempDir()}, schema)
	return service
}

func concurrencyRequest(requestID, authID string) cpaapi.RequestInterceptRequest {
	return cpaapi.RequestInterceptRequest{
		RequestID: requestID,
		Metadata:  map[string]any{selectedAuthMetadataKey: authID},
	}
}

func TestAccountConcurrencyWaitsForSaturatedAccountWithout429(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	service.maxWait = 2 * time.Second
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	accountA := Account{ID: "index-a", AuthID: "auth-a"}
	accountB := Account{ID: "index-b", AuthID: "auth-b"}
	if errSet := service.SetLimit(accountA, 1); errSet != nil {
		t.Fatalf("SetLimit(auth-a) error = %v", errSet)
	}
	if errSet := service.SetLimit(accountB, 1); errSet != nil {
		t.Fatalf("SetLimit(auth-b) error = %v", errSet)
	}
	if response, changed := service.InterceptRequest(concurrencyRequest("request-a-1", "auth-a")); changed || response.Terminate {
		t.Fatalf("first auth-a admission = %#v, changed %v", response, changed)
	}

	type admissionResult struct {
		response cpaapi.RequestInterceptResponse
		changed  bool
	}
	result := make(chan admissionResult, 1)
	go func() {
		response, changed := service.InterceptRequest(concurrencyRequest("request-a-2", "auth-a"))
		result <- admissionResult{response: response, changed: changed}
	}()
	deadline := time.Now().Add(time.Second)
	for service.Summary("auth-a").Waiting != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("waiter was not registered: %#v", service.Summary("auth-a"))
		}
		time.Sleep(time.Millisecond)
	}
	if response, changed := service.InterceptRequest(concurrencyRequest("request-b-1", "auth-b")); changed || response.Terminate {
		t.Fatalf("auth-b admission was affected by auth-a = %#v, changed %v", response, changed)
	}
	if got := service.Summary("auth-a"); got.Active != 1 || got.Waiting != 1 || got.Limit != 1 {
		t.Fatalf("auth-a saturated summary = %#v", got)
	}

	// Completion of the in-flight request wakes the queue and admits the waiter.
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-a-1"})
	select {
	case outcome := <-result:
		if outcome.changed || outcome.response.Terminate {
			t.Fatalf("waited admission = %#v, changed %v", outcome.response, outcome.changed)
		}
	case <-time.After(time.Second):
		t.Fatal("saturated request was not admitted after the active slot was released")
	}
	if got := service.Summary("auth-a"); got.Active != 1 || got.Waiting != 0 {
		t.Fatalf("auth-a admitted summary = %#v", got)
	}
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-a-2"})
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-b-1"})
}

func TestAccountConcurrencyEnforcesConfiguredRequestWindow(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	service.maxWait = 0
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	activeLimit, requestLimit, windowSeconds := 3, 2, 15
	account := Account{ID: "index-a", AuthID: "auth-a"}
	if errSet := service.SetLimits(account, &activeLimit, &requestLimit); errSet != nil {
		t.Fatalf("SetLimits() error = %v", errSet)
	}
	if errSet := service.SetRequestWindowSeconds(account, windowSeconds); errSet != nil {
		t.Fatalf("SetRequestWindowSeconds() error = %v", errSet)
	}
	for _, requestID := range []string{"request-1", "request-2"} {
		if response, changed := service.InterceptRequest(concurrencyRequest(requestID, "auth-a")); changed || response.Terminate {
			t.Fatalf("%s rejected: %#v changed=%v", requestID, response, changed)
		}
		service.Complete(cpaapi.RequestCompletion{RequestID: requestID})
	}
	if got := service.Summary("auth-a"); got.Active != 0 || got.UsedRequests != 2 || got.RequestLimit != 2 || got.RequestWindowSeconds != 15 || got.Limit != 3 || got.Waiting != 0 {
		t.Fatalf("summary after first window = %#v", got)
	}
	response, changed := service.InterceptRequest(concurrencyRequest("request-3", "auth-a"))
	if !changed || !response.Terminate || response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(response.ResponseBody), "account_concurrency_wait_timeout") {
		t.Fatalf("request-window saturated request did not fail safely: %#v changed=%v", response, changed)
	}
	now = now.Add(16 * time.Second)
	if response, changed = service.InterceptRequest(concurrencyRequest("request-3", "auth-a")); changed || response.Terminate {
		t.Fatalf("request after configured window expiry rejected: %#v", response)
	}
	if got := service.Summary("auth-a"); got.UsedRequests != 1 || got.RequestWindowSeconds != 15 {
		t.Fatalf("summary after configured window expiry = %#v", got)
	}
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-3"})
}

func TestAccountConcurrencyUpdatingOneSettingPreservesTheOthers(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	account := Account{ID: "index-a", AuthID: "auth-a"}
	activeLimit, requestLimit := 10, 3
	if errSet := service.SetLimits(account, &activeLimit, &requestLimit); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := service.SetRequestWindowSeconds(account, 30); errSet != nil {
		t.Fatal(errSet)
	}
	updatedActive := 8
	if errSet := service.SetLimits(account, &updatedActive, nil); errSet != nil {
		t.Fatal(errSet)
	}
	if got := service.Summary("auth-a"); got.Limit != 8 || got.RequestLimit != 3 || got.RequestWindowSeconds != 30 {
		t.Fatalf("active update changed request settings: %#v", got)
	}
	clearRequestLimit := 0
	if errSet := service.SetLimits(account, nil, &clearRequestLimit); errSet != nil {
		t.Fatal(errSet)
	}
	if got := service.Summary("auth-a"); got.Limit != 8 || got.RequestLimit != 0 || got.RequestWindowSeconds != 30 {
		t.Fatalf("request-limit clear changed active/window settings: %#v", got)
	}
	if errSet := service.SetRequestWindowSeconds(account, 45); errSet != nil {
		t.Fatal(errSet)
	}
	if got := service.Summary("auth-a"); got.Limit != 8 || got.RequestLimit != 0 || got.RequestWindowSeconds != 45 {
		t.Fatalf("window update changed other settings: %#v", got)
	}
}

func TestAccountConcurrencyCompletionIsIdempotentForEveryOutcome(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	service.maxWait = 0
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	for _, outcome := range []string{"succeeded", "failed", "rejected", "canceled"} {
		requestID := "request-" + outcome
		service.InterceptRequest(concurrencyRequest(requestID, "auth-a"))
		service.Complete(cpaapi.RequestCompletion{RequestID: requestID, Outcome: outcome, Error: "must not be persisted"})
		service.Complete(cpaapi.RequestCompletion{RequestID: requestID, Outcome: outcome})
		if got := service.Summary("auth-a").Active; got != 0 {
			t.Fatalf("active after %s completion = %d", outcome, got)
		}
	}
}

func TestAccountConcurrencyDuplicateAdmissionAndLostCompletionCleanup(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 2); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	service.InterceptRequest(concurrencyRequest("request-a", "auth-a"))
	service.InterceptRequest(concurrencyRequest("request-a", "auth-a"))
	if got := service.Summary("auth-a").Active; got != 1 {
		t.Fatalf("active after duplicate admission = %d", got)
	}
	now = now.Add(accountConcurrencyLeaseTTL + time.Second)
	service.InterceptRequest(concurrencyRequest("request-b", "auth-a"))
	if got := service.Summary("auth-a").Active; got != 1 {
		t.Fatalf("active after expired lease cleanup = %d", got)
	}
}

func TestAccountConcurrencyMovesARequestSlotWhenCPAFailsOverAccounts(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	for _, account := range []Account{{ID: "index-a", AuthID: "auth-a"}, {ID: "index-b", AuthID: "auth-b"}} {
		if errSet := service.SetLimit(account, 1); errSet != nil {
			t.Fatalf("SetLimit(%s) error = %v", account.AuthID, errSet)
		}
	}
	service.InterceptRequest(concurrencyRequest("request-1", "auth-a"))
	service.InterceptRequest(concurrencyRequest("request-1", "auth-b"))
	if got := service.Summary("auth-a").Active; got != 0 {
		t.Fatalf("old auth active after failover = %d", got)
	}
	if got := service.Summary("auth-b").Active; got != 1 {
		t.Fatalf("new auth active after failover = %d", got)
	}
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-1", Outcome: "failed"})
	if got := service.Summary("auth-b").Active; got != 0 {
		t.Fatalf("new auth active after completion = %d", got)
	}
}

func TestAccountConcurrencyTracksUnlimitedAccounts(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	if !service.RequestInterceptionActive() {
		t.Fatal("supported service did not activate lifecycle observation")
	}
	for index, requestID := range []string{"request-1", "request-2", "request-3"} {
		if response, changed := service.InterceptRequest(concurrencyRequest(requestID, "auth-unlimited")); changed || response.Terminate {
			t.Fatalf("unlimited admission %d = %#v, changed %v", index+1, response, changed)
		}
	}
	if got := service.Summary("auth-unlimited"); !got.Supported || got.Limit != 0 || got.Active != 3 {
		t.Fatalf("unlimited summary = %#v", got)
	}
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-2", Outcome: "succeeded"})
	if got := service.Summary("auth-unlimited").Active; got != 2 {
		t.Fatalf("active after unlimited completion = %d", got)
	}
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-1", Outcome: "failed"})
	service.Complete(cpaapi.RequestCompletion{RequestID: "request-3", Outcome: "canceled"})
	if got := service.Summary("auth-unlimited").Active; got != 0 {
		t.Fatalf("active after all unlimited completions = %d", got)
	}
	if allocations := testing.AllocsPerRun(1000, func() { _ = service.RequestInterceptionActive() }); allocations != 0 {
		t.Fatalf("active gate allocations = %f", allocations)
	}
}

func TestAccountConcurrencyDynamicLimitAndClear(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	service.maxWait = 0
	account := Account{ID: "index-a", AuthID: "auth-a"}
	if errSet := service.SetLimit(account, 2); errSet != nil {
		t.Fatalf("SetLimit(2) error = %v", errSet)
	}
	service.InterceptRequest(concurrencyRequest("request-1", "auth-a"))
	service.InterceptRequest(concurrencyRequest("request-2", "auth-a"))
	if errSet := service.SetLimit(account, 1); errSet != nil {
		t.Fatalf("SetLimit(1) error = %v", errSet)
	}
	if response, changed := service.InterceptRequest(concurrencyRequest("request-3", "auth-a")); !changed || !response.Terminate {
		t.Fatalf("lowered limit accepted a new request: %#v, changed %v", response, changed)
	}
	if errSet := service.SetLimit(account, 0); errSet != nil {
		t.Fatalf("SetLimit(0) error = %v", errSet)
	}
	if response, changed := service.InterceptRequest(concurrencyRequest("request-4", "auth-a")); changed || response.Terminate {
		t.Fatalf("cleared limit rejected a request: %#v, changed %v", response, changed)
	}
	if got := service.Summary("auth-a"); got.Limit != 0 || got.Active != 3 {
		t.Fatalf("summary after clearing limit = %#v", got)
	}
}

func TestAccountConcurrencyClearKeepsLifecycleObservationActive(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	account := Account{ID: "index-a", AuthID: "auth-a"}
	if errSet := service.SetLimit(account, 1); errSet != nil {
		t.Fatalf("SetLimit(1) error = %v", errSet)
	}
	service.InterceptRequest(concurrencyRequest("request-1", "auth-a"))
	if errSet := service.SetLimit(account, 0); errSet != nil {
		t.Fatalf("SetLimit(0) error = %v", errSet)
	}
	if !service.RequestInterceptionActive() {
		t.Fatal("completion callback was disabled with an outstanding lease")
	}

	service.Complete(cpaapi.RequestCompletion{RequestID: "request-1", Outcome: "succeeded"})
	if !service.RequestInterceptionActive() {
		t.Fatal("supported lifecycle observation stopped after the final lease ended")
	}
	if got := service.Summary("auth-a"); got.Active != 0 || got.Limit != 0 {
		t.Fatalf("summary after clear and completion = %#v", got)
	}
}

func TestAccountConcurrencyPersistsWithoutSecrets(t *testing.T) {
	dataDir := t.TempDir()
	first := NewAccountConcurrencyService()
	first.Configure(Config{DataDir: dataDir}, cpaapi.SchemaVersion)
	if errSet := first.SetLimit(Account{ID: "index-a", AuthID: "auth-a", Email: "secret@example.com"}, 7); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}

	restored := NewAccountConcurrencyService()
	restored.Configure(Config{DataDir: dataDir}, cpaapi.SchemaVersion)
	if got := restored.Summary("auth-a"); got.Limit != 7 || got.Active != 0 {
		t.Fatalf("restored summary = %#v", got)
	}
	raw, errRead := os.ReadFile(accountConcurrencyStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if strings.Contains(string(raw), "secret@example.com") || strings.Contains(string(raw), "access_token") {
		t.Fatalf("persisted state contains account secrets: %s", raw)
	}
}

func TestAccountConcurrencyIsUnavailableOnLegacyCPA(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.LegacySchemaVersion)
	availability := service.Availability()
	if availability.Supported || availability.HostSchemaVersion != 1 || availability.RequiredSchemaVersion != 2 || availability.Reason != "host_schema_v2_required" {
		t.Fatalf("availability = %#v", availability)
	}
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); !errors.Is(errSet, ErrAccountConcurrencyUnsupported) {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	if service.RequestInterceptionActive() {
		t.Fatal("legacy host activated request interception")
	}
}

func TestAccountConcurrencyFailOpenForMalformedIdentity(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	for _, request := range []cpaapi.RequestInterceptRequest{
		{RequestID: ""},
		{RequestID: "missing-metadata"},
		{RequestID: "wrong-type", Metadata: map[string]any{selectedAuthMetadataKey: 42}},
		{RequestID: "unlimited", Metadata: map[string]any{selectedAuthMetadataKey: "auth-b"}},
	} {
		if response, changed := service.InterceptRequest(request); changed || response.Terminate {
			t.Fatalf("malformed request did not fail open: %#v, changed %v", response, changed)
		}
	}
}

func TestAccountConcurrencyPatchValidationAndSummary(t *testing.T) {
	limit := 3
	patch, errValidate := (BatchPatch{ConcurrencyLimit: &limit}).Validate()
	if errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	if patch.Empty() || !patch.HasPluginUpdates() || patch.HasFieldUpdates() {
		t.Fatalf("validated patch flags = empty %v plugin %v fields %v", patch.Empty(), patch.HasPluginUpdates(), patch.HasFieldUpdates())
	}
	if fields := patch.Summary().Fields; len(fields) != 1 || fields[0] != "concurrency_limit" {
		t.Fatalf("summary fields = %v", fields)
	}
	invalid := MaxAccountConcurrencyLimit + 1
	if _, errValidate = (BatchPatch{ConcurrencyLimit: &invalid}).Validate(); errValidate == nil {
		t.Fatal("Validate() accepted an excessive limit")
	}
}

func TestJobEngineAppliesPluginConcurrencyOnceAlongsideCPAFields(t *testing.T) {
	raw := json.RawMessage(`{"type":"codex","email":"operator@example.com"}`)
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{
		"index-a": {AuthIndex: "index-a", Name: "account.json", Path: "/auths/account.json", JSON: raw},
	}}
	accounts := NewAccountService(host)
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	engine := NewJobEngine(accounts)
	engine.SetAccountConcurrency(service)
	writer := &concurrencyTrackingWriter{}
	limit := 4
	priority := 7
	result := engine.applyAccount(context.Background(), Account{
		ID: "index-a", AuthID: "auth-a", Name: "account.json", path: "/auths/account.json", revision: revisionFor(raw),
	}, BatchOperationPatch, BatchPatch{ConcurrencyLimit: &limit, Priority: &priority}, writer)
	if result.Status != ResultSucceeded || len(result.AppliedFields) != 2 || result.AppliedFields[0] != "priority" || result.AppliedFields[1] != "concurrency_limit" {
		t.Fatalf("applyAccount() = %#v", result)
	}
	if writer.fieldCalls != 1 || writer.disabledCalls != 0 {
		t.Fatalf("CPA writer calls = fields %d disabled %d", writer.fieldCalls, writer.disabledCalls)
	}
	if got := service.Summary("auth-a").Limit; got != limit {
		t.Fatalf("stored limit = %d, want %d", got, limit)
	}
}

type concurrencyTrackingWriter struct {
	fieldCalls    int
	disabledCalls int
}

func (w *concurrencyTrackingWriter) PatchFields(context.Context, string, BatchPatch) error {
	w.fieldCalls++
	return nil
}

func (w *concurrencyTrackingWriter) PatchDisabled(context.Context, string, bool) error {
	w.disabledCalls++
	return nil
}

func (*concurrencyTrackingWriter) DeleteAuthFile(context.Context, string) error { return nil }

func TestRegistrationNegotiatesRequestLifecycleSchema(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	defer app.Close()
	app.ConfigureHost([]byte("data_dir: "+t.TempDir()), cpaapi.LegacySchemaVersion)
	legacy := app.Registration()
	if legacy.SchemaVersion != cpaapi.LegacySchemaVersion || legacy.Capabilities.RequestLifecyclePlugin {
		t.Fatalf("legacy registration = %#v", legacy)
	}
	app.ConfigureHost([]byte("data_dir: "+t.TempDir()), cpaapi.SchemaVersion)
	current := app.Registration()
	if current.SchemaVersion != cpaapi.SchemaVersion || !current.Capabilities.RequestLifecyclePlugin {
		t.Fatalf("current registration = %#v", current)
	}
}

func TestAccountConcurrencyConfigureSurfacesAndRecoversFromCorruptStore(t *testing.T) {
	dataDir := t.TempDir()
	storePath := accountConcurrencyStorePath(dataDir)
	if errWrite := os.WriteFile(storePath, []byte("{"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	service := NewAccountConcurrencyService()
	service.Configure(Config{DataDir: dataDir}, cpaapi.SchemaVersion)
	if got := service.Availability().StorageError; got != "account concurrency state could not be loaded" {
		t.Fatalf("storage_error = %q", got)
	}

	if errWrite := saveAccountConcurrency(storePath, map[string]accountConcurrencyRecord{
		"auth-one": {AuthID: "auth-one", AccountID: "one", Limit: 7},
	}); errWrite != nil {
		t.Fatalf("saveAccountConcurrency() error = %v", errWrite)
	}
	service.Configure(Config{DataDir: dataDir}, cpaapi.SchemaVersion)
	if got := service.Availability().StorageError; got != "" {
		t.Fatalf("storage_error after recovery = %q", got)
	}
	if got := service.Summary("auth-one").Limit; got != 7 {
		t.Fatalf("recovered limit = %d, want 7", got)
	}
}

func TestAccountConcurrencySummaryPrunesExpiredLeases(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	clock := time.Now().UTC()
	service.mu.Lock()
	service.now = func() time.Time { return clock }
	service.mu.Unlock()
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	service.InterceptRequest(concurrencyRequest("request-a", "auth-a"))
	if got := service.Summary("auth-a"); got.Active != 1 {
		t.Fatalf("active before expiry = %d, want 1", got.Active)
	}
	clock = clock.Add(accountConcurrencyLeaseTTL + time.Second)
	if got := service.Summary("auth-a"); got.Active != 0 {
		t.Fatalf("active after expiry = %d, want 0", got.Active)
	}
	if _, changed := service.InterceptRequest(concurrencyRequest("request-b", "auth-a")); changed {
		t.Fatal("expired lease continued to reject a new request")
	}
}

func TestAccountConcurrencyConfigureClearsLeasesAcrossRuntimeChange(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	service := NewAccountConcurrencyService()
	service.Configure(Config{DataDir: firstDir}, cpaapi.SchemaVersion)
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}
	service.InterceptRequest(concurrencyRequest("request-a", "auth-a"))
	if got := service.Summary("auth-a"); got.Active != 1 {
		t.Fatalf("active before reconfigure = %d, want 1", got.Active)
	}

	service.Configure(Config{DataDir: secondDir}, cpaapi.SchemaVersion)
	if got := service.Summary("auth-a"); got.Active != 0 {
		t.Fatalf("active after store change = %d, want 0", got.Active)
	}
	if _, changed := service.InterceptRequest(concurrencyRequest("request-b", "auth-a")); changed {
		t.Fatal("stale lease from previous store rejected a request")
	}
}

func schedulerCandidates(authIDs ...string) []cpaapi.SchedulerAuthCandidate {
	candidates := make([]cpaapi.SchedulerAuthCandidate, 0, len(authIDs))
	for _, authID := range authIDs {
		candidates = append(candidates, cpaapi.SchedulerAuthCandidate{ID: authID, Provider: "codex"})
	}
	return candidates
}

func configureSchedulerLimit(t *testing.T, service *AccountConcurrencyService, authID string, concurrency, request int) {
	t.Helper()
	if errSet := service.SetLimits(Account{ID: authID, AuthID: authID}, &concurrency, &request); errSet != nil {
		t.Fatalf("SetLimits(%s) error = %v", authID, errSet)
	}
}

func TestAccountConcurrencySchedulerMovesPressureToIdleAccount(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-idle", 10, 3)
	configureSchedulerLimit(t, service, "auth-busy", 10, 3)

	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.mu.Lock()
	service.active["auth-busy"] = 1
	service.waiting["auth-busy"] = 8
	service.events["auth-busy"] = []time.Time{now.Add(-3 * time.Second), now.Add(-2 * time.Second), now.Add(-time.Second)}
	service.mu.Unlock()

	response := service.PickAuth(cpaapi.SchedulerPickRequest{Provider: "codex", Candidates: schedulerCandidates("auth-busy", "auth-idle")})
	if !response.Handled || response.AuthID != "auth-idle" {
		t.Fatalf("scheduler response = %#v, want idle auth", response)
	}
}

func TestAccountConcurrencySchedulerBalancesBurstBeforeAnyAdmission(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 3)
	configureSchedulerLimit(t, service, "auth-b", 10, 3)

	counts := map[string]int{}
	for range 10 {
		response := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")})
		if !response.Handled || response.AuthID == "" {
			t.Fatalf("idle burst scheduler response = %#v, want a handled account", response)
		}
		counts[response.AuthID]++
	}
	if counts["auth-a"] != 5 || counts["auth-b"] != 5 {
		t.Fatalf("idle burst distribution = %#v, want 5 picks per account", counts)
	}
}

func TestAccountConcurrencySchedulerUsesRoundRobinForEqualPressure(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 3)
	configureSchedulerLimit(t, service, "auth-b", 10, 3)
	service.mu.Lock()
	service.active["auth-a"] = 1
	service.active["auth-b"] = 1
	service.mu.Unlock()

	first := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")})
	second := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")})
	if !first.Handled || !second.Handled || first.AuthID == second.AuthID {
		t.Fatalf("equal-pressure picks = %#v, %#v; want alternating accounts", first, second)
	}
}

func TestAccountConcurrencySchedulerReservationIsConsumedAfterAuth(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 3)
	configureSchedulerLimit(t, service, "auth-b", 10, 3)
	if response, changed := service.InterceptRequest(concurrencyRequest("busy", "auth-b")); changed || response.Terminate {
		t.Fatalf("busy admission terminated: %#v", response)
	}

	response := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")})
	if !response.Handled || response.AuthID != "auth-a" {
		t.Fatalf("scheduler response = %#v, want auth-a", response)
	}
	if got := len(service.reservations["auth-a"]); got != 1 {
		t.Fatalf("reservation count before after-auth = %d, want 1", got)
	}
	if admission, changed := service.InterceptRequest(concurrencyRequest("selected", "auth-a")); changed || admission.Terminate {
		t.Fatalf("selected admission = %#v, changed %v", admission, changed)
	}
	if got := len(service.reservations["auth-a"]); got != 0 {
		t.Fatalf("reservation count after after-auth = %d, want 0", got)
	}
}

func TestAccountConcurrencySchedulerFallsBackForUnmanagedOrSingleCandidate(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-managed", 10, 3)
	service.mu.Lock()
	service.active["auth-managed"] = 1
	service.mu.Unlock()

	if response := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-managed")}); response.Handled {
		t.Fatalf("single-candidate scheduler response = %#v, want unhandled", response)
	}
	if response := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-managed", "auth-unmanaged")}); response.Handled {
		t.Fatalf("mixed managed scheduler response = %#v, want unhandled", response)
	}
}

func TestAccountConcurrencySchedulerReservationExpiryAndReconfigure(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 3)
	configureSchedulerLimit(t, service, "auth-b", 10, 3)
	clock := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	service.mu.Lock()
	service.active["auth-b"] = 1
	service.mu.Unlock()
	if response := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")}); !response.Handled {
		t.Fatalf("scheduler did not create reservation: %#v", response)
	}
	clock = clock.Add(accountConcurrencySchedulerReserveTTL + time.Second)
	service.mu.Lock()
	service.pruneSchedulerReservationsLocked(clock)
	if len(service.reservations) != 0 {
		t.Fatalf("expired reservations = %#v", service.reservations)
	}
	service.mu.Unlock()

	if response := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")}); !response.Handled {
		t.Fatalf("scheduler did not create second reservation: %#v", response)
	}
	service.Configure(Config{DataDir: service.store}, cpaapi.SchemaVersion)
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.reservations) != 0 {
		t.Fatalf("reservations survived reconfigure: %#v", service.reservations)
	}
}

func schedulerSessionRequest(sessionID string, authIDs ...string) cpaapi.SchedulerPickRequest {
	return cpaapi.SchedulerPickRequest{
		Provider:   "codex",
		Candidates: schedulerCandidates(authIDs...),
		Options:    cpaapi.SchedulerOptions{Headers: map[string][]string{"x-opencode-session": {sessionID}}},
	}
}

func schedulerMetadataRequest(metadata map[string]any, authIDs ...string) cpaapi.SchedulerPickRequest {
	return cpaapi.SchedulerPickRequest{
		Provider:   "codex",
		Candidates: schedulerCandidates(authIDs...),
		Options:    cpaapi.SchedulerOptions{Metadata: metadata},
	}
}

func TestAccountConcurrencySchedulerKeepsSessionOnOneAccount(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 10)
	configureSchedulerLimit(t, service, "auth-b", 10, 10)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	first := service.PickAuth(schedulerSessionRequest("session-a", "auth-a", "auth-b"))
	if !first.Handled || first.AuthID == "" {
		t.Fatalf("first session pick = %#v", first)
	}
	second := service.PickAuth(schedulerSessionRequest("session-a", "auth-a", "auth-b"))
	if !second.Handled || second.AuthID != first.AuthID {
		t.Fatalf("sticky picks = %#v, %#v; want the same account", first, second)
	}

	third := service.PickAuth(schedulerSessionRequest("session-b", "auth-a", "auth-b"))
	if !third.Handled || third.AuthID == first.AuthID {
		t.Fatalf("second session pick = %#v, want a different account than %q", third, first.AuthID)
	}
	fourth := service.PickAuth(schedulerSessionRequest("session-b", "auth-a", "auth-b"))
	if !fourth.Handled || fourth.AuthID != third.AuthID {
		t.Fatalf("second session sticky picks = %#v, %#v; want the same account", third, fourth)
	}
	if snapshot := service.SessionStickiness(); snapshot.Tracked != 2 || snapshot.Bound != 0 {
		t.Fatalf("stickiness snapshot = %#v, want 2 tracked and 0 dropped", snapshot)
	}

	// A sticky short-circuit must not advance the shared round-robin cursor.
	service.mu.Lock()
	service.schedulerCursor = 7
	service.mu.Unlock()
	fifth := service.PickAuth(schedulerSessionRequest("session-a", "auth-a", "auth-b"))
	if !fifth.Handled || fifth.AuthID != first.AuthID {
		t.Fatalf("sticky pick = %#v, want the mapped account %q", fifth, first.AuthID)
	}
	service.mu.Lock()
	cursor := service.schedulerCursor
	service.mu.Unlock()
	if cursor != 7 {
		t.Fatalf("scheduler cursor after a sticky pick = %d, want 7", cursor)
	}
}

func TestAccountConcurrencySchedulerWithoutSessionKeepsLeastLoadedSelection(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 10)
	configureSchedulerLimit(t, service, "auth-b", 10, 10)
	service.now = func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }

	first := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")})
	second := service.PickAuth(cpaapi.SchedulerPickRequest{Candidates: schedulerCandidates("auth-a", "auth-b")})
	if !first.Handled || !second.Handled || first.AuthID == second.AuthID {
		t.Fatalf("session-less picks = %#v, %#v; want alternating accounts", first, second)
	}
	if snapshot := service.SessionStickiness(); snapshot.Tracked != 0 || snapshot.Bound != 0 {
		t.Fatalf("session-less stickiness snapshot = %#v", snapshot)
	}
}

func TestAccountConcurrencySchedulerAbandonsSaturatedSessionAccount(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 10)
	configureSchedulerLimit(t, service, "auth-b", 10, 10)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.mu.Lock()
	service.active["auth-b"] = 1
	service.mu.Unlock()

	// The idle account wins the first pick and becomes the session account.
	first := service.PickAuth(schedulerSessionRequest("session-saturated", "auth-a", "auth-b"))
	if !first.Handled || first.AuthID != "auth-a" {
		t.Fatalf("first session pick = %#v, want auth-a", first)
	}

	service.mu.Lock()
	service.active["auth-a"] = 10
	service.mu.Unlock()
	second := service.PickAuth(schedulerSessionRequest("session-saturated", "auth-a", "auth-b"))
	if !second.Handled || second.AuthID != "auth-b" {
		t.Fatalf("pick with a saturated session account = %#v, want auth-b", second)
	}

	// The saturated account is abandoned for the request, but the mapping follows the
	// account that served it: once auth-a is idle again a session without a mapping would
	// prefer auth-a, while the remapped session stays on auth-b.
	service.mu.Lock()
	service.active["auth-a"] = 0
	service.mu.Unlock()
	third := service.PickAuth(schedulerSessionRequest("session-saturated", "auth-a", "auth-b"))
	if !third.Handled || third.AuthID != "auth-b" {
		t.Fatalf("pick after the session account recovered = %#v, want the remapped auth-b", third)
	}
}

func TestAccountConcurrencySchedulerRemapsWhenSessionAccountLeavesPool(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	for _, authID := range []string{"auth-a", "auth-b", "auth-c"} {
		configureSchedulerLimit(t, service, authID, 10, 10)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.mu.Lock()
	service.active["auth-b"] = 1
	service.mu.Unlock()

	first := service.PickAuth(schedulerSessionRequest("session-gone", "auth-a", "auth-b"))
	if !first.Handled || first.AuthID != "auth-a" {
		t.Fatalf("first session pick = %#v, want auth-a", first)
	}
	second := service.PickAuth(schedulerSessionRequest("session-gone", "auth-b", "auth-c"))
	if !second.Handled || second.AuthID != "auth-c" {
		t.Fatalf("pick without the mapped account = %#v, want the idle auth-c", second)
	}
	third := service.PickAuth(schedulerSessionRequest("session-gone", "auth-b", "auth-c"))
	if !third.Handled || third.AuthID != second.AuthID {
		t.Fatalf("remapped session picks = %#v, %#v; want the remapped account", second, third)
	}
	service.mu.Lock()
	mapped := service.sessions["session-gone"].AuthID
	service.mu.Unlock()
	if mapped != second.AuthID {
		t.Fatalf("stored session mapping = %q, want %q", mapped, second.AuthID)
	}
}

func TestAccountConcurrencySchedulerBoundsSessionMappings(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 1000)
	configureSchedulerLimit(t, service, "auth-b", 10, 1000)
	service.sessionCap = 4
	service.now = func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }

	for index := range 10 {
		request := schedulerSessionRequest(fmt.Sprintf("session-%d", index), "auth-a", "auth-b")
		if response := service.PickAuth(request); !response.Handled || response.AuthID == "" {
			t.Fatalf("session %d pick = %#v", index, response)
		}
		if snapshot := service.SessionStickiness(); snapshot.Tracked > service.sessionCap {
			t.Fatalf("tracked session mappings = %d, want <= %d", snapshot.Tracked, service.sessionCap)
		}
	}
	if snapshot := service.SessionStickiness(); snapshot.Tracked != 4 || snapshot.Bound != 6 {
		t.Fatalf("bounded stickiness snapshot = %#v, want 4 tracked and 6 evicted", snapshot)
	}
}

func TestAccountConcurrencySchedulerExpiresIdleSessionMapping(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 10)
	configureSchedulerLimit(t, service, "auth-b", 10, 10)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.mu.Lock()
	service.active["auth-b"] = 1
	service.mu.Unlock()

	first := service.PickAuth(schedulerSessionRequest("session-ttl", "auth-a", "auth-b"))
	if !first.Handled || first.AuthID != "auth-a" {
		t.Fatalf("first session pick = %#v, want auth-a", first)
	}

	// A stale mapping must not win even though auth-a stays below saturation: the expired
	// session falls back to the idle account and rebuilds its mapping there.
	now = now.Add(accountConcurrencySessionTTL + time.Second)
	service.mu.Lock()
	service.active["auth-a"] = 5
	service.mu.Unlock()
	second := service.PickAuth(schedulerSessionRequest("session-ttl", "auth-a", "auth-b"))
	if !second.Handled || second.AuthID != "auth-b" {
		t.Fatalf("pick after session TTL = %#v, want the idle auth-b", second)
	}
	if snapshot := service.SessionStickiness(); snapshot.Tracked != 1 || snapshot.Bound != 1 {
		t.Fatalf("stickiness snapshot after TTL = %#v, want 1 tracked and 1 expired", snapshot)
	}
}

func TestAccountConcurrencySchedulerIgnoresMalformedSessionValues(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"too_long":  strings.Repeat("s", accountConcurrencySessionKeyMaxLen+1),
		"control":   "session\nvalue",
		"nul":       "session\x00value",
		"non_ascii": "sess\u00e3o",
		"blank":     "   ",
	}
	sources := map[string]func(string, ...string) cpaapi.SchedulerPickRequest{
		"header": schedulerSessionRequest,
		"metadata": func(sessionID string, authIDs ...string) cpaapi.SchedulerPickRequest {
			return schedulerMetadataRequest(map[string]any{"session_id": sessionID}, authIDs...)
		},
	}
	for name, value := range cases {
		for source, request := range sources {
			t.Run(name+"_"+source, func(t *testing.T) {
				service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
				configureSchedulerLimit(t, service, "auth-a", 10, 10)
				configureSchedulerLimit(t, service, "auth-b", 10, 10)
				service.now = func() time.Time { return now }
				first := service.PickAuth(request(value, "auth-a", "auth-b"))
				second := service.PickAuth(request(value, "auth-a", "auth-b"))
				if !first.Handled || !second.Handled || first.AuthID == second.AuthID {
					t.Fatalf("malformed session %q (%s) changed selection: %#v, %#v", value, source, first, second)
				}
				if snapshot := service.SessionStickiness(); snapshot.Tracked != 0 || snapshot.Bound != 0 {
					t.Fatalf("malformed session %q (%s) was tracked: %#v", value, source, snapshot)
				}
			})
		}
	}
}

func TestAccountConcurrencySchedulerIgnoresNonStringSessionMetadata(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 10)
	configureSchedulerLimit(t, service, "auth-b", 10, 10)
	service.now = func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }
	request := schedulerMetadataRequest(map[string]any{"session_id": 42, "conversation_id": true, "thread_id": nil}, "auth-a", "auth-b")

	first := service.PickAuth(request)
	second := service.PickAuth(request)
	if !first.Handled || !second.Handled || first.AuthID == second.AuthID {
		t.Fatalf("non-string metadata picks = %#v, %#v; want alternating accounts", first, second)
	}
	if snapshot := service.SessionStickiness(); snapshot.Tracked != 0 {
		t.Fatalf("non-string session metadata was tracked: %#v", snapshot)
	}
}

func TestAccountConcurrencySchedulerSessionKeyPrecedence(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	configureSchedulerLimit(t, service, "auth-a", 10, 10)
	configureSchedulerLimit(t, service, "auth-b", 10, 10)
	service.now = func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }

	// Header lookup is case-insensitive and outranks every metadata key.
	request := cpaapi.SchedulerPickRequest{
		Candidates: schedulerCandidates("auth-a", "auth-b"),
		Options: cpaapi.SchedulerOptions{
			Headers:  map[string][]string{"X-OpenCode-Session": {"header-session"}},
			Metadata: map[string]any{"session_id": "metadata-session", "thread_id": "thread-1"},
		},
	}
	if response := service.PickAuth(request); !response.Handled {
		t.Fatalf("precedence pick = %#v", response)
	}
	service.mu.Lock()
	_, headerKeyed := service.sessions["header-session"]
	_, metadataKeyed := service.sessions["metadata-session"]
	service.mu.Unlock()
	if !headerKeyed || metadataKeyed {
		t.Fatalf("session keys = header %v metadata %v, want the header key only", headerKeyed, metadataKeyed)
	}

	// Metadata-only sessions are sticky too, per key.
	metadataRequest := schedulerMetadataRequest(map[string]any{"conversation_id": "metadata-session"}, "auth-a", "auth-b")
	first := service.PickAuth(metadataRequest)
	second := service.PickAuth(metadataRequest)
	if !first.Handled || !second.Handled || first.AuthID != second.AuthID {
		t.Fatalf("metadata session picks = %#v, %#v; want the same account", first, second)
	}
}
