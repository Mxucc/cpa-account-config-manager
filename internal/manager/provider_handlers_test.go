package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func newProviderTestApp(t *testing.T) *App {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, nil)
	app.ConfigureHost([]byte("data_dir: "+t.TempDir()), cpaapi.MaxSchemaVersion)
	t.Cleanup(app.Close)
	return app
}

func decodeProviderResponse(t *testing.T, response cpaapi.ManagementResponse) map[string]any {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
	var decoded map[string]any
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode body: %v", errDecode)
	}
	return decoded
}

func TestProviderRegistryRouteListsEveryDefinition(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix,
	})
	decoded := decodeProviderResponse(t, response)
	providers, ok := decoded["providers"].([]any)
	if !ok || len(providers) != 5 {
		t.Fatalf("providers = %#v", decoded["providers"])
	}
	seen := map[string]bool{}
	for _, item := range providers {
		entry := item.(map[string]any)
		seen[entry["id"].(string)] = true
		if _, hasCapabilities := entry["capabilities"]; !hasCapabilities {
			t.Fatalf("provider %v has no capabilities", entry["id"])
		}
	}
	for _, id := range []string{"codex", "opencode-go", "opencode-zen", "cline-pass", "ai-provider"} {
		if !seen[id] {
			t.Errorf("provider %q missing from the registry", id)
		}
	}
}

func TestProviderAccountsRouteStaysExactPathWithQuery(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/accounts",
		Query:  url.Values{"provider": []string{"codex"}},
	})
	decoded := decodeProviderResponse(t, response)
	if decoded["total"].(float64) != 0 {
		t.Fatalf("total = %v, want 0 with no credentials", decoded["total"])
	}
	if _, ok := decoded["accounts"].([]any); !ok {
		t.Fatalf("accounts = %#v", decoded["accounts"])
	}
}

func TestProviderOverviewCountsEveryStatus(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/overview",
	})
	decoded := decodeProviderResponse(t, response)
	if _, ok := decoded["by_status"].(map[string]any); !ok {
		t.Fatalf("by_status = %#v", decoded["by_status"])
	}
	if decoded["providers"].(float64) != 5 {
		t.Fatalf("providers = %v", decoded["providers"])
	}
}

func TestQuotaDescribeAdvertisesManagedProvidersOnly(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleQuotaDescribe(context.Background(), cpaapi.QuotaDescribeRequest{})
	if response.SupportsReset {
		t.Fatalf("this plugin does not reset quotas")
	}
	wanted := map[string]bool{"codex": false, "opencode-go": false, "opencode-zen": false, "cline-pass": false}
	for _, providerID := range response.SupportedProviders {
		if _, ok := wanted[providerID]; !ok {
			t.Errorf("unexpected supported provider %q", providerID)
		}
		wanted[providerID] = true
	}
	for providerID, seen := range wanted {
		if !seen {
			t.Errorf("provider %q is not advertised", providerID)
		}
	}
}

func TestQuotaFetchForAnUnknownCredentialIsEmptyNotAnError(t *testing.T) {
	app := newProviderTestApp(t)
	response, errFetch := app.HandleQuotaFetch(context.Background(), cpaapi.QuotaFetchRequest{AuthIndex: "missing"})
	if errFetch != nil {
		t.Fatalf("HandleQuotaFetch() error = %v", errFetch)
	}
	if response.Subscription != nil || len(response.Groups) != 0 || len(response.Summary) != 0 {
		t.Fatalf("an unknown credential must answer empty, got %#v", response)
	}
}

func TestProviderActionRejectsUnknownProvider(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/account/refresh",
		Query:  url.Values{"provider": []string{"nope"}, "id": []string{"x"}},
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
}

func TestProviderRefreshForCodexIsExplicitlyUnsupported(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/account/refresh",
		Query:  url.Values{"provider": []string{"codex"}, "id": []string{"x"}},
	})
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
	var decoded map[string]any
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if decoded["error"] != "codex_refresh_unsupported" {
		t.Fatalf("error = %v", decoded["error"])
	}
}

func TestProviderImportRecordsAMagpiePluginWithoutExecutingIt(t *testing.T) {
	app := newProviderTestApp(t)
	manifest := `{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14","main":"./opencode.mjs","keywords":["magpie","cline"],"magpie":{"name":{"zh":"Cline"}}}`
	body, errMarshal := json.Marshal(map[string]any{"manifest": manifest, "source": "magpie"})
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   body,
	})
	decoded := decodeProviderResponse(t, response)
	entry, ok := decoded["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider = %#v", decoded["provider"])
	}
	if entry["id"] != "cline" {
		t.Fatalf("id = %v, want the package's provider id", entry["id"])
	}
	if entry["hook_execution"] != false {
		t.Fatalf("an import must never claim hook execution in this build")
	}
	if entry["name"] != "Cline" {
		t.Fatalf("name = %v", entry["name"])
	}

	catalog := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/catalog",
	})
	catalogDecoded := decodeProviderResponse(t, catalog)
	if imported, ok := catalogDecoded["imported"].([]any); !ok || len(imported) != 1 {
		t.Fatalf("imported = %#v", catalogDecoded["imported"])
	}
	if presets, ok := catalogDecoded["presets"].([]any); !ok || len(presets) < 10 {
		t.Fatalf("presets = %#v", catalogDecoded["presets"])
	}

	// The imported provider appears in the registry beside the built-ins.
	providers := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: providerRoutePrefix})
	registry := decodeProviderResponse(t, providers)
	found := false
	for _, item := range registry["providers"].([]any) {
		if item.(map[string]any)["id"] == "cline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("imported provider is missing from the registry")
	}
}

func TestProviderImportRejectsAManifestWithoutAName(t *testing.T) {
	app := newProviderTestApp(t)
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   []byte(`{"version":"1.0.0"}`),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
}
