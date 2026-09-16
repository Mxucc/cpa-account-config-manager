package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// providerAuthIndexChannelEntry builds one CPA OpenAI-compatible channel row with
// the auth indexes CPA assigns to it: the row-level index a usage callback names,
// and one index for the weighted key entry that carries the credential.
func providerAuthIndexChannelEntry(rowAuthIndex, apiKey, keyAuthIndex string) map[string]any {
	keyRow := map[string]any{"api-key": apiKey}
	if keyAuthIndex != "" {
		keyRow["auth-index"] = keyAuthIndex
	}
	entry := map[string]any{
		"name":            "Provider channel",
		"base-url":        "https://opencode.ai/zen/v1",
		"api-key-entries": []any{keyRow},
	}
	if rowAuthIndex != "" {
		entry["auth-index"] = rowAuthIndex
	}
	return entry
}

// providerAuthIndexTestApp wires an app whose CPA channel read returns the given
// openai-compatibility rows (and an empty list for every other kind), then reads
// the channel list once, which is how the running plugin learns the auth index of
// each channel row.
func providerAuthIndexTestApp(t *testing.T, entries []map[string]any) *App {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	t.Cleanup(app.Close)
	app.managementDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		kind := strings.TrimPrefix(request.URL.Path, "/v0/management/")
		var list []map[string]any
		if kind == "openai-compatibility" {
			list = entries
		}
		payload, errEncode := json.Marshal(map[string]any{kind: list})
		if errEncode != nil {
			return nil, errEncode
		}
		return jsonHTTPResponse(http.StatusOK, string(payload)), nil
	})
	if _, storageErr := app.resolveAIProviderChannelNames(context.Background(), "management-secret"); storageErr != "" {
		t.Fatalf("channel read failed: %s", storageErr)
	}
	return app
}

// TestAppAttributesAuthIndexOnlyUsageToChannelCredential pins the reported bug:
// CPA's callback for an openai-compatibility channel names the auth index of the
// channel row but carries no API key, so the record must still be attributed to
// that row's credential identity. The dashboard refuses a snapshot that is not
// credential backed, and pricing resolves the channel by credential identity.
func TestAppAttributesAuthIndexOnlyUsageToChannelCredential(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("row-index-a", apiKey, "key-index-a"),
	})

	app.HandleUsage(cpaapi.UsageRecord{
		AuthIndex: "row-index-a",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{InputTokens: 40_000_000, OutputTokens: 300_000, TotalTokens: 40_300_000},
	})
	// The weighted key entry of the same row carries its own index; both belong to
	// one credential, so both records must land in one aggregate.
	app.HandleUsage(cpaapi.UsageRecord{
		AuthIndex: "key-index-a",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{TotalTokens: 2_300_000},
	})

	snapshots := app.providerRuntime.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("provider snapshots = %+v, want exactly the channel row", snapshots)
	}
	snapshot := snapshots[0]
	wantIdentity := aiProviderRuntimeCredentialIdentity("openai", apiKey)
	if snapshot.Identity != wantIdentity || !snapshot.CredentialBacked {
		t.Fatalf("snapshot identity = %+v, want credential-backed %q", snapshot, wantIdentity)
	}
	if snapshot.Provider != "openai" || snapshot.AuthIndex != "key-index-a" {
		t.Fatalf("snapshot provider/index = %+v", snapshot)
	}
	if snapshot.TotalTokens != 42_600_000 {
		t.Fatalf("snapshot tokens = %d, want both records counted", snapshot.TotalTokens)
	}
	if got := app.usage.Snapshot("row-index-a"); got != nil {
		t.Fatalf("provider usage entered the account store: %+v", got)
	}
}

// TestAppLeavesUsageWithUnindexedAuthIndexUnattributed pins that an auth index no
// channel row owns keeps today's behaviour: no provider attribution at all.
func TestAppLeavesUsageWithUnindexedAuthIndexUnattributed(t *testing.T) {
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("row-index-a", "sk-provider-channel-secret", "key-index-a"),
	})
	app.HandleUsage(cpaapi.UsageRecord{
		AuthIndex: "an-account-index",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{TotalTokens: 25},
	})
	if snapshots := app.providerRuntime.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("an unindexed auth index entered provider metrics: %+v", snapshots)
	}
	if got := app.usage.Snapshot("an-account-index"); got == nil || got.TotalTokens != 25 {
		t.Fatalf("the record did not follow the previous account path: %+v", got)
	}
}

// TestAppKeepsUsageRecordsThatCarryAKey pins that a callback that does carry an
// API key behaves exactly as before: its own credential wins over the channel
// index, and the record is never rewritten.
func TestAppKeepsUsageRecordsThatCarryAKey(t *testing.T) {
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("row-index-a", "sk-channel-secret", "key-index-a"),
	})
	app.HandleUsage(cpaapi.UsageRecord{
		Provider:  "codex",
		AuthIndex: "row-index-a",
		AuthType:  "api_key",
		APIKey:    "sk-callback-secret",
		Model:     "gpt-5.3-codex",
		Detail:    cpaapi.UsageDetail{TotalTokens: 25},
	})
	snapshots := app.providerRuntime.Snapshot()
	if len(snapshots) != 1 || snapshots[0].TotalTokens != 25 {
		t.Fatalf("provider snapshots = %+v", snapshots)
	}
	if want := aiProviderRuntimeCredentialIdentity("codex", "sk-callback-secret"); snapshots[0].Identity != want {
		t.Fatalf("identity = %q, want the callback's own credential %q", snapshots[0].Identity, want)
	}
}

// TestAppKeepsAccountOwnedAuthIndexesOutOfProviderMetrics pins that CPA can hand
// an auth index the plugin read from a channel row to a native account later: the
// account wins, and no provider credential is resolved for it.
func TestAppKeepsAccountOwnedAuthIndexesOutOfProviderMetrics(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("shared-index", apiKey, "key-index-a"),
	})
	app.providerRuntime.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: "shared-index", ID: "native-account-id", Name: "account.json", Provider: "codex", Type: "oauth",
	}})
	if key, _ := app.providerRuntime.ProviderCredentialForAuthIndex("shared-index"); key != "" {
		t.Fatal("an account-owned auth index still resolved a provider credential")
	}
	app.HandleUsage(cpaapi.UsageRecord{
		AuthIndex: "shared-index",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{TotalTokens: 100},
	})
	if snapshots := app.providerRuntime.Snapshot(); len(snapshots) != 0 {
		t.Fatalf("account-indexed usage entered provider metrics: %+v", snapshots)
	}
}

// TestProviderRuntimeRepairsStrandedAuthIndexAggregates pins the recovery of the
// history an earlier release stranded under the volatile "auth-index:" identity:
// one channel row for the provider, so every orphan of it belongs to that row.
func TestProviderRuntimeRepairsStrandedAuthIndexAggregates(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	tracker := NewProviderRuntimeTracker(nil)
	// 40.3M tokens stranded on an index CPA no longer reports, and 2.3M on another.
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "stale-index-b", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{TotalTokens: 2_300_000}})
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "stale-index-a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{TotalTokens: 40_300_000}})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: "row-index-a", APIKey: apiKey, BaseURL: "https://opencode.ai/zen/v1",
		Kind: "openai-compatibility", Provider: "openai", Primary: true,
	}})

	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 2 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair = %+v, want both stranded aggregates adopted", repair)
	}
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want one collapsed channel aggregate", snapshots)
	}
	snapshot := snapshots[0]
	wantIdentity := aiProviderRuntimeCredentialIdentity("openai", apiKey)
	if snapshot.Identity != wantIdentity || !snapshot.CredentialBacked {
		t.Fatalf("recovered snapshot = %+v, want credential-backed %q", snapshot, wantIdentity)
	}
	if snapshot.AuthIndex != "row-index-a" {
		t.Fatalf("recovered snapshot kept a stale auth index: %+v", snapshot)
	}
	// Counters merge by maximum (the store merge convention) so a repeated pass
	// can never double count the same history.
	if snapshot.TotalTokens != 40_300_000 {
		t.Fatalf("recovered tokens = %d, want the largest stranded total", snapshot.TotalTokens)
	}
	if again := tracker.RepairOrphanedAuthIndexAggregates(); again.Adopted != 0 {
		t.Fatalf("a second repair pass adopted %d aggregates", again.Adopted)
	}
}

// TestProviderRuntimeRepairRefusesAmbiguousProviderRows pins the bound on the
// repair: with two channel rows for the provider the orphan's owner is unknown,
// so it is reported instead of being guessed onto either row.
func TestProviderRuntimeRepairRefusesAmbiguousProviderRows(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "stale-index-a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{TotalTokens: 40_300_000}})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{
		{AuthIndex: "row-index-a", APIKey: "sk-row-a", Kind: "openai-compatibility", Provider: "openai", Primary: true},
		{AuthIndex: "row-index-b", APIKey: "sk-row-b", Kind: "openai-compatibility", Provider: "openai", Primary: true},
	})
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 0 {
		t.Fatalf("repair adopted %d aggregates for an ambiguous provider", repair.Adopted)
	}
	if len(repair.Ambiguous) != 1 || repair.Ambiguous[0] != "openai" {
		t.Fatalf("repair ambiguity report = %+v", repair.Ambiguous)
	}
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Identity != "auth-index:stale-index-a" || snapshots[0].CredentialBacked {
		t.Fatalf("stranded history was moved without proof: %+v", snapshots)
	}
}

// TestProviderRuntimeRepairLeavesLiveAuthIndexAggregates pins that an index a
// channel row currently reports is not an orphan: its history migrates through
// the existing credential path when a record with the key arrives.
func TestProviderRuntimeRepairLeavesLiveAuthIndexAggregates(t *testing.T) {
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{Provider: "openai", AuthIndex: "row-index-a", Model: "gpt-5.5", Detail: cpaapi.UsageDetail{TotalTokens: 25}})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: "row-index-a", APIKey: "sk-row", Kind: "openai-compatibility", Provider: "openai", Primary: true,
	}})
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 0 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair touched a live channel index: %+v", repair)
	}
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 || snapshots[0].TotalTokens != 25 {
		t.Fatalf("live history was disturbed: %+v", snapshots)
	}
}

// TestAIProviderChannelAuthCredentialsReadsHostSpellings pins both spellings CPA
// uses and the priority of the row-level index over a key-entry index.
func TestAIProviderChannelAuthCredentialsReadsHostSpellings(t *testing.T) {
	entry := map[string]any{
		"base-url":   "https://gateway.example/v1",
		"auth-index": "row-index",
		"api-key-entries": []any{
			map[string]any{"api-key": "sk-first", "auth-index": "key-index-a"},
			map[string]any{"api_key": "sk-second", "auth_index": "key-index-b"},
			map[string]any{"api-key": "sk-third"},
		},
	}
	credentials := aiProviderChannelAuthCredentials("codex-api-key", entry)
	got := make(map[string]providerChannelCredential, len(credentials))
	for _, credential := range credentials {
		got[credential.AuthIndex] = credential
	}
	for authIndex, wantKey := range map[string]string{
		"key-index-a": "sk-first",
		"key-index-b": "sk-second",
		"row-index":   "sk-first",
	} {
		credential, ok := got[authIndex]
		if !ok || credential.APIKey != wantKey {
			t.Fatalf("auth index %q = %+v, want key %q", authIndex, credential, wantKey)
		}
		if credential.Provider != "codex" || credential.Kind != "codex-api-key" || credential.BaseURL != "https://gateway.example/v1" {
			t.Fatalf("auth index %q resolved the wrong channel: %+v", authIndex, credential)
		}
	}
	if !got["row-index"].Primary {
		t.Fatalf("the row-level index was not marked primary: %+v", got["row-index"])
	}
	if got["key-index-a"].Primary {
		t.Fatalf("a key-entry index was marked primary: %+v", got["key-index-a"])
	}
}

// TestProviderAuthIndexNeverExposesTheChannelKey pins that the resolved channel
// key stays inside this process: it is absent from the runtime response and from
// everything the tracker persists, while the redacted identity is present.
func TestProviderAuthIndexNeverExposesTheChannelKey(t *testing.T) {
	const apiKey = "sk-must-not-be-persisted"
	identity := aiProviderRuntimeCredentialIdentity("openai", apiKey)
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("row-index-a", apiKey, "key-index-a"),
	})
	app.HandleUsage(cpaapi.UsageRecord{
		AuthIndex: "row-index-a",
		Model:     "gpt-5.5",
		Detail:    cpaapi.UsageDetail{TotalTokens: 25},
	})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management" + managementRoutePrefix + "/ai-providers/runtime",
		Headers: http.Header{"Authorization": []string{"Bearer management-key"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("runtime status = %d body=%s", response.StatusCode, response.Body)
	}
	if bytes.Contains(response.Body, []byte(apiKey)) {
		t.Fatalf("the runtime response leaked the channel key: %s", response.Body)
	}
	if !bytes.Contains(response.Body, []byte(identity)) {
		t.Fatalf("the runtime response lost the credential identity: %s", response.Body)
	}

	dataDir := t.TempDir()
	tracker := NewProviderRuntimeTracker(nil)
	tracker.Configure(Config{DataDir: dataDir})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: "row-index-a", APIKey: apiKey, Kind: "openai-compatibility", Provider: "openai", Primary: true,
	}})
	// The app fills the record's key from the index before the tracker sees it.
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai", AuthIndex: "row-index-a", AuthType: "api_key", APIKey: apiKey,
		Model: "gpt-5.5", Detail: cpaapi.UsageDetail{TotalTokens: 25},
	})
	tracker.Shutdown()
	raw, errRead := os.ReadFile(filepath.Join(dataDir, providerRuntimeStoreFileName))
	if errRead != nil {
		t.Fatalf("read persisted runtime state: %v", errRead)
	}
	if bytes.Contains(raw, []byte(apiKey)) {
		t.Fatalf("the persisted runtime state leaked the channel key: %s", raw)
	}
	if !bytes.Contains(raw, []byte(identity)) {
		t.Fatalf("the persisted runtime state lost the credential identity: %s", raw)
	}
}
