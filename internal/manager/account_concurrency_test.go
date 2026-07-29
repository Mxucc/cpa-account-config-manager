package manager

import (
	"context"
	"encoding/json"
	"errors"
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

func TestAccountConcurrencyRejectsOnlyTheSaturatedAccount(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	if errSet := service.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); errSet != nil {
		t.Fatalf("SetLimit(auth-a) error = %v", errSet)
	}
	if errSet := service.SetLimit(Account{ID: "index-b", AuthID: "auth-b"}, 1); errSet != nil {
		t.Fatalf("SetLimit(auth-b) error = %v", errSet)
	}

	if response, changed := service.InterceptRequest(concurrencyRequest("request-a-1", "auth-a")); changed || response.Terminate {
		t.Fatalf("first auth-a admission = %#v, changed %v", response, changed)
	}
	response, changed := service.InterceptRequest(concurrencyRequest("request-a-2", "auth-a"))
	if !changed || !response.Terminate || response.StatusCode != http.StatusTooManyRequests || response.ResponseHeaders.Get("Retry-After") != "1" {
		t.Fatalf("second auth-a admission = %#v, changed %v", response, changed)
	}
	if !json.Valid(response.ResponseBody) || !strings.Contains(string(response.ResponseBody), "account_concurrency_limit_reached") {
		t.Fatalf("rejection body = %q", response.ResponseBody)
	}
	if response, changed := service.InterceptRequest(concurrencyRequest("request-b-1", "auth-b")); changed || response.Terminate {
		t.Fatalf("auth-b admission was affected by auth-a = %#v, changed %v", response, changed)
	}
	if got := service.Summary("auth-a"); got.Active != 1 || got.Limit != 1 {
		t.Fatalf("auth-a summary = %#v", got)
	}
	if got := service.Summary("auth-b"); got.Active != 1 || got.Limit != 1 {
		t.Fatalf("auth-b summary = %#v", got)
	}
}

func TestAccountConcurrencyCompletionIsIdempotentForEveryOutcome(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
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

func TestAccountConcurrencyInactiveGateDoesNotAllocate(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	if service.RequestInterceptionActive() {
		t.Fatal("empty service is active")
	}
	if allocations := testing.AllocsPerRun(1000, func() { _ = service.RequestInterceptionActive() }); allocations != 0 {
		t.Fatalf("inactive gate allocations = %f", allocations)
	}
}

func TestAccountConcurrencyDynamicLimitAndClear(t *testing.T) {
	service := configuredConcurrencyService(t, cpaapi.SchemaVersion)
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
	if got := service.Summary("auth-a").Limit; got != 0 {
		t.Fatalf("cleared limit = %d", got)
	}
}

func TestAccountConcurrencyClearKeepsCompletionActiveUntilExistingLeaseEnds(t *testing.T) {
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
	if service.RequestInterceptionActive() {
		t.Fatal("completion callback remained active after the final lease ended")
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
