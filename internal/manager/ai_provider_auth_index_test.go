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
	app.managementDoer = providerAuthIndexTestChannelDoer(entries)
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
	// CPA names this channel by its own provider key ("openai-compatible-" plus the
	// lower-cased row name), which is the namespace the tracker and the dashboard use.
	wantIdentity := aiProviderRuntimeCredentialIdentity("openai-compatible-provider channel", apiKey)
	if snapshot.Identity != wantIdentity || !snapshot.CredentialBacked {
		t.Fatalf("snapshot identity = %+v, want credential-backed %q", snapshot, wantIdentity)
	}
	if snapshot.Provider != "openai-compatible-provider channel" || snapshot.AuthIndex != "key-index-a" {
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

// TestAppPublishesTheUsageIdentityTheDashboardMatchesBy pins the other half of the
// attribution fix. The dashboard matches a runtime snapshot to a channel row by the
// identity published on that row's binding, so the binding has to carry the same
// identity the tracker records usage under - CPA's per-channel provider key plus the
// credential. A binding that carried the kind-derived name instead left every row
// without an auth index unable to reach its own usage ("暂无用量" with traffic on it).
func TestAppPublishesTheUsageIdentityTheDashboardMatchesBy(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	const cpaProvider = "openai-compatible-provider channel"
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("row-index-a", apiKey, "key-index-a"),
	})
	// Read it the way the dashboard does, so the test covers what the UI receives.
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management" + managementRoutePrefix + "/ai-provider-names",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("names status = %d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Names []AIProviderNameAssignment `json:"names"`
	}
	if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
		t.Fatalf("decode names: %v", errDecode)
	}
	if len(payload.Names) != 1 {
		t.Fatalf("names = %+v, want the one channel row", payload.Names)
	}
	want := aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey)
	found := false
	for _, identity := range payload.Names[0].Identities {
		if identity == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("binding identities = %v, want the usage identity %q among them", payload.Names[0].Identities, want)
	}
}

// TestAppRepairsHistoryStrandedByTheKindDerivedProviderName pins the production bug
// this fix closes. The channel index used to register the KIND-derived provider name
// ("openai") while a usage callback for an OpenAI-compatible channel carries CPA's
// per-channel key ("openai-compatible-<name>"). Two consequences followed: the record
// could not be attributed to a credential, and the orphan repair compared two
// spellings of one channel, found no candidate and silently did nothing - which is
// exactly why the operator's 40.3M stranded tokens never moved. With the index keyed
// by CPA's name, the next channel read folds that history into the channel credential.
func TestAppRepairsHistoryStrandedByTheKindDerivedProviderName(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	const cpaProvider = "openai-compatible-provider channel"
	app := providerAuthIndexTestApp(t, []map[string]any{
		providerAuthIndexChannelEntry("row-index-a", apiKey, "key-index-a"),
	})
	// History recorded before the channel's row was known: the volatile identity.
	// CPA sends AuthType api_key for a provider channel, which is what classifies the
	// record as provider traffic; the auth index is the only identifier it carries.
	app.HandleUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: "stale-row-index", Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{InputTokens: 40_000_000, OutputTokens: 300_000, TotalTokens: 40_300_000},
	})
	app.HandleUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: "another-stale-index", Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 2_300_000},
	})
	if snapshots := app.providerRuntime.Snapshot(); len(snapshots) != 2 {
		t.Fatalf("stranded snapshots = %+v, want the two volatile aggregates", snapshots)
	}

	// A channel read is what explains them, which is what a page load performs.
	if _, storageErr := app.resolveAIProviderChannelNames(context.Background(), "management-secret"); storageErr != "" {
		t.Fatalf("channel read failed: %s", storageErr)
	}
	snapshots := app.providerRuntime.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots after the repair = %+v, want one channel aggregate", snapshots)
	}
	snapshot := snapshots[0]
	if want := aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey); snapshot.Identity != want || !snapshot.CredentialBacked {
		t.Fatalf("recovered snapshot = %+v, want credential-backed %q", snapshot, want)
	}
	if snapshot.Provider != cpaProvider || snapshot.AuthIndex != "row-index-a" {
		t.Fatalf("recovered snapshot provider/index = %+v", snapshot)
	}
	// The counters merge by maximum, so the largest stranded total survives the move.
	if snapshot.TotalTokens != 40_300_000 {
		t.Fatalf("recovered tokens = %d, want the largest stranded total", snapshot.TotalTokens)
	}
}

// TestAIProviderUsageProviderNameMatchesCPAShape pins the provider name CPA sends in
// a usage callback for each channel row, including its fallback for a nameless
// OpenAI-compatible row and a name that already carries the prefix.
func TestAIProviderUsageProviderNameMatchesCPAShape(t *testing.T) {
	for _, testCase := range []struct {
		scenario string
		kind     string
		entry    map[string]any
		want     string
	}{
		{"named compatible row", "openai-compatibility", map[string]any{"name": "Cline Pass"}, "openai-compatible-cline pass"},
		{"name with surrounding space", "openai-compatibility", map[string]any{"name": "  Warrior.ikitten@gmail.com "}, "openai-compatible-warrior.ikitten@gmail.com"},
		{"nameless compatible row", "openai-compatibility", map[string]any{}, "openai-compatibility"},
		{"name already carrying the prefix", "openai-compatibility", map[string]any{"name": "OpenAI-Compatible-Bare"}, "openai-compatible-bare"},
		{"kind CPA names directly", "codex-api-key", map[string]any{"name": "ignored-by-cpa"}, "codex"},
	} {
		if got := aiProviderUsageProviderName(testCase.kind, testCase.entry); got != testCase.want {
			t.Fatalf("%s: provider name = %q, want %q", testCase.scenario, got, testCase.want)
		}
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
	identity := aiProviderRuntimeCredentialIdentity("openai-compatible-provider channel", apiKey)
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
	// The app fills the record's key from the index before the tracker sees it, and the
	// index names the channel the way CPA does.
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: "row-index-a", APIKey: apiKey, Kind: "openai-compatibility", Provider: "openai-compatible-provider channel", Primary: true,
	}})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai-compatible-provider channel", AuthIndex: "row-index-a", AuthType: "api_key", APIKey: apiKey,
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

// providerAuthIndexTestChannelDoer serves the same openai-compatibility rows on
// every read, so a test can drive more than one channel list read. Every other kind
// comes back empty: one row belongs to one kind, and the tracker keys its channel
// index by auth index alone.
func providerAuthIndexTestChannelDoer(entries []map[string]any) HTTPDoer {
	return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
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
}

// TestProviderRuntimeFoldsLiveAuthIndexShellIntoTheCredentialAggregate pins the
// operator's live case 1: 997M tokens sit on an aggregate whose identity is still
// the volatile auth index of a channel row that also has the credential-backed
// aggregate new traffic writes to. That index is live, which is exactly why the
// earlier repair skipped the shell, but the existing credential-backed aggregate
// proves the live path settled elsewhere and can never write to the shell again, so
// its history folds in. Counters merge by maximum, so the legacy total both survives
// the move and cannot be counted twice by a later pass.
func TestProviderRuntimeFoldsLiveAuthIndexShellIntoTheCredentialAggregate(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	const cpaProvider = "openai-compatible-warrior.ikitten@gmail.com"
	const liveIndex = "26a4a73ced59cc49"
	tracker := NewProviderRuntimeTracker(nil)
	// The legacy shell: recorded before the row was known, so only CPA's auth index
	// identified the record and it carries no credential identity.
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{InputTokens: 900_000_000, OutputTokens: 97_194_890, TotalTokens: 997_194_890},
	})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: liveIndex, APIKey: apiKey, BaseURL: "https://opencode.ai/zen/v1",
		Kind: "openai-compatibility", Provider: cpaProvider, Primary: true,
	}})
	// No credential-backed aggregate exists yet, so nothing proves the live path
	// settled: the shell has to stay exactly where it is.
	if repair := tracker.RepairOrphanedAuthIndexAggregates(); repair.Adopted != 0 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair without a credential aggregate = %+v, want the shell left alone", repair)
	}
	if snapshots := tracker.Snapshot(); len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want the untouched shell", snapshots)
	}
	// New traffic reaches the row's credential aggregate through the channel index.
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", APIKey: apiKey, AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 2_025_570},
	})
	if snapshots := tracker.Snapshot(); len(snapshots) != 2 {
		t.Fatalf("snapshots = %+v, want the shell and the credential aggregate", snapshots)
	}
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 1 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair = %+v, want the live-index shell adopted once", repair)
	}
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots after the repair = %+v, want one channel aggregate", snapshots)
	}
	snapshot := snapshots[0]
	if want := aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey); snapshot.Identity != want || !snapshot.CredentialBacked {
		t.Fatalf("recovered aggregate = %+v, want credential-backed %q", snapshot, want)
	}
	if snapshot.Provider != cpaProvider || snapshot.AuthIndex != liveIndex {
		t.Fatalf("recovered aggregate = %+v, want the live row and its index", snapshot)
	}
	if snapshot.TotalTokens != 997_194_890 {
		t.Fatalf("recovered tokens = %d, want the legacy total to survive", snapshot.TotalTokens)
	}
	if again := tracker.RepairOrphanedAuthIndexAggregates(); again.Adopted != 0 || len(again.Ambiguous) != 0 {
		t.Fatalf("a second repair pass = %+v, want nothing adopted", again)
	}
}

// TestProviderRuntimeLeavesLiveAuthIndexShellWithoutACredentialAggregate pins the
// safety rule of that case (the operator's live case 3 shape): with a live index but
// no credential-backed aggregate for the row, nothing proves the live path settled,
// so the shell may still be receiving records. It is left alone, and the repair does
// not create the target it is supposed to only find.
func TestProviderRuntimeLeavesLiveAuthIndexShellWithoutACredentialAggregate(t *testing.T) {
	const apiKey = "sk-cline-pass-secret"
	const cpaProvider = "openai-compatible-cline pass"
	const liveIndex = "5eb8ef87c6abb929"
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 413_881},
	})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: liveIndex, APIKey: apiKey, BaseURL: "https://opencode.ai/zen/v1",
		Kind: "openai-compatibility", Provider: cpaProvider, Primary: true,
	}})
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 0 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair = %+v, want the shell left alone", repair)
	}
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want the untouched shell", snapshots)
	}
	if snapshots[0].Identity != "auth-index:"+liveIndex || snapshots[0].CredentialBacked || snapshots[0].TotalTokens != 413_881 {
		t.Fatalf("the shell was rewritten: %+v", snapshots[0])
	}
	tracker.mu.RLock()
	_, created := tracker.aggregates[runtimeAggregateKey(cpaProvider, aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey))]
	tracker.mu.RUnlock()
	if created {
		t.Fatal("the repair created the credential aggregate it may only find")
	}
}

// TestProviderRuntimeFoldsLegacyKindNamedCredentialAggregate pins the operator's
// live case 2: 135M tokens were written by the previous release under the KIND-derived
// provider name ("openai") with a credential identity, while the row is served under
// CPA's per-channel provider name. The aggregate names a live row index and the row's
// own credential aggregate exists, so the legacy copy folds into it by auth index.
func TestProviderRuntimeFoldsLegacyKindNamedCredentialAggregate(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	const cpaProvider = "openai-compatible-warrior.ikitten@gmail.com"
	const liveIndex = "26a4a73ced59cc49"
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai", AuthType: "api_key", APIKey: apiKey, AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 135_344_883},
	})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: liveIndex, APIKey: apiKey, Kind: "openai-compatibility", Provider: cpaProvider, Primary: true,
	}})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", APIKey: apiKey, AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 2_025_570},
	})
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 1 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair = %+v, want the legacy kind-named aggregate adopted", repair)
	}
	snapshots := tracker.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want one channel aggregate", snapshots)
	}
	snapshot := snapshots[0]
	if want := aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey); snapshot.Identity != want || snapshot.Provider != cpaProvider || !snapshot.CredentialBacked {
		t.Fatalf("recovered aggregate = %+v, want the row's provider and credential %q", snapshot, want)
	}
	if snapshot.AuthIndex != liveIndex || snapshot.TotalTokens != 135_344_883 {
		t.Fatalf("recovered aggregate = %+v, want the legacy history under the live index", snapshot)
	}
	if again := tracker.RepairOrphanedAuthIndexAggregates(); again.Adopted != 0 {
		t.Fatalf("a second repair pass adopted %d aggregates", again.Adopted)
	}
}

// TestProviderRuntimeRepairReportsAmbiguousLegacyKindNamedAggregate pins the guard on
// the legacy-name rule: the kind's name is shared by every channel of that kind, so
// with two distinct credentials for the row's provider the auth index cannot be
// trusted to identify the owner. The aggregate is reported, never moved.
func TestProviderRuntimeRepairReportsAmbiguousLegacyKindNamedAggregate(t *testing.T) {
	const cpaProvider = "openai-compatible-provider channel"
	const liveIndex = "row-index-a"
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: "openai", AuthType: "api_key", APIKey: "sk-first", AuthIndex: liveIndex,
		Detail: cpaapi.UsageDetail{TotalTokens: 135_344_883},
	})
	// Two weighted key entries of one row: two distinct credentials for one provider.
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{
		{AuthIndex: liveIndex, APIKey: "sk-first", Kind: "openai-compatibility", Provider: cpaProvider, Primary: true},
		{AuthIndex: "key-index-b", APIKey: "sk-second", Kind: "openai-compatibility", Provider: cpaProvider},
	})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", APIKey: "sk-first", AuthIndex: liveIndex,
		Detail: cpaapi.UsageDetail{TotalTokens: 2_025_570},
	})
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 0 {
		t.Fatalf("repair adopted %d aggregates for an ambiguous provider", repair.Adopted)
	}
	if len(repair.Ambiguous) != 1 || repair.Ambiguous[0] != cpaProvider {
		t.Fatalf("repair ambiguity report = %+v, want the row's provider %q", repair.Ambiguous, cpaProvider)
	}
	tracker.mu.RLock()
	legacy := tracker.aggregates[runtimeAggregateKey("openai", aiProviderRuntimeCredentialIdentity("openai", "sk-first"))]
	tracker.mu.RUnlock()
	if legacy == nil || legacy.TotalTokens != 135_344_883 {
		t.Fatalf("the legacy aggregate was moved or dropped: %+v", legacy)
	}
}

// TestProviderRuntimeRepairNeverTouchesAnAccountOwnedAuthIndex pins that a native
// account always wins CPA's auth-index namespace. Once the host auth listing claims the
// index, the provider channel entry for it is dropped and the volatile shell is pruned,
// so the repair has nothing to fold and must not attribute that history to the row's
// credential aggregate either.
func TestProviderRuntimeRepairNeverTouchesAnAccountOwnedAuthIndex(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	const cpaProvider = "openai-compatible-warrior.ikitten@gmail.com"
	const sharedIndex = "26a4a73ced59cc49"
	tracker := NewProviderRuntimeTracker(nil)
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: sharedIndex,
		Detail: cpaapi.UsageDetail{TotalTokens: 997_194_890},
	})
	tracker.SetProviderChannelCredentials("openai-compatibility", []providerChannelCredential{{
		AuthIndex: sharedIndex, APIKey: apiKey, Kind: "openai-compatibility", Provider: cpaProvider, Primary: true,
	}})
	tracker.ObserveUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", APIKey: apiKey, AuthIndex: sharedIndex,
		Detail: cpaapi.UsageDetail{TotalTokens: 2_025_570},
	})
	// CPA can hand an index it read from a channel row to a native account later.
	tracker.DiscoverAuthStorage([]cpaapi.HostAuthFileEntry{{
		AuthIndex: sharedIndex, ID: "native-account-id", Name: "account.json", Provider: "codex", Type: "oauth",
	}})
	if key, _ := tracker.ProviderCredentialForAuthIndex(sharedIndex); key != "" {
		t.Fatal("an account-owned auth index still resolved a provider credential")
	}
	repair := tracker.RepairOrphanedAuthIndexAggregates()
	if repair.Adopted != 0 || len(repair.Ambiguous) != 0 {
		t.Fatalf("repair = %+v, want account-owned history untouched", repair)
	}
	tracker.mu.RLock()
	_, shellSurvives := tracker.aggregates[runtimeAggregateKey(cpaProvider, "auth-index:"+sharedIndex)]
	target := tracker.aggregates[runtimeAggregateKey(cpaProvider, aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey))]
	tracker.mu.RUnlock()
	if shellSurvives {
		t.Fatal("an account-owned auth index was rebuilt as a provider aggregate")
	}
	if target == nil || target.TotalTokens != 2_025_570 {
		t.Fatalf("the row's credential aggregate = %+v, want only its own traffic", target)
	}
}

// TestAppFoldsTheLiveIndexShellAfterTheChannelListIsRead drives the production path
// end to end: the legacy record arrives before any channel list was read, new traffic
// reaches the row's credential aggregate through the index the app publishes, and the
// next channel read (what a dashboard page load performs) folds the shell.
func TestAppFoldsTheLiveIndexShellAfterTheChannelListIsRead(t *testing.T) {
	const apiKey = "sk-provider-channel-secret"
	const cpaProvider = "openai-compatible-warrior.ikitten@gmail.com"
	const liveIndex = "26a4a73ced59cc49"
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	t.Cleanup(app.Close)
	// The row's name is what CPA builds its per-channel provider key from, so the row
	// under test has to be the channel the legacy record was written for.
	channelEntry := providerAuthIndexChannelEntry(liveIndex, apiKey, "key-index-a")
	channelEntry["name"] = "Warrior.ikitten@gmail.com"
	app.managementDoer = providerAuthIndexTestChannelDoer([]map[string]any{channelEntry})
	app.HandleUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 997_194_890},
	})
	// The first read publishes the index. Without the row's credential aggregate the
	// shell stays, which is what the second read then finds superseded.
	if _, storageErr := app.resolveAIProviderChannelNames(context.Background(), "management-secret"); storageErr != "" {
		t.Fatalf("channel read failed: %s", storageErr)
	}
	if snapshots := app.providerRuntime.Snapshot(); len(snapshots) != 1 || snapshots[0].Identity != "auth-index:"+liveIndex {
		t.Fatalf("snapshots = %+v, want the untouched shell after the first read", snapshots)
	}
	// CPA names the auth index and no key; the app fills the key from the channel
	// index, so the record lands on the row's credential aggregate.
	app.HandleUsage(cpaapi.UsageRecord{
		Provider: cpaProvider, AuthType: "api_key", AuthIndex: liveIndex, Model: "gpt-5.5",
		Detail: cpaapi.UsageDetail{TotalTokens: 2_025_570},
	})
	if _, storageErr := app.resolveAIProviderChannelNames(context.Background(), "management-secret"); storageErr != "" {
		t.Fatalf("second channel read failed: %s", storageErr)
	}
	snapshots := app.providerRuntime.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots after the second read = %+v, want one channel aggregate", snapshots)
	}
	snapshot := snapshots[0]
	if want := aiProviderRuntimeCredentialIdentity(cpaProvider, apiKey); snapshot.Identity != want || !snapshot.CredentialBacked {
		t.Fatalf("recovered aggregate = %+v, want credential-backed %q", snapshot, want)
	}
	if snapshot.Provider != cpaProvider || snapshot.AuthIndex != liveIndex || snapshot.TotalTokens != 997_194_890 {
		t.Fatalf("recovered aggregate = %+v, want the legacy history on the live row", snapshot)
	}
}
