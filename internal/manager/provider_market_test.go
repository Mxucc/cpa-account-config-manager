package manager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/provider"
)

// stubMarketDoer answers the market's fixed URLs from a table, so the route tests
// never reach GitHub or npm.
type stubMarketDoer struct {
	body map[string]string
	fail map[string]error
}

func (s stubMarketDoer) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	for key, errInject := range s.fail {
		if strings.Contains(url, key) {
			return nil, errInject
		}
	}
	body := ""
	found := false
	for key, value := range s.body {
		if strings.Contains(url, key) {
			body, found = value, true
			break
		}
	}
	status := http.StatusOK
	if !found {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

const marketRegistryFixture = `{"version":1,"plugins":[
  {"package":"@magpie-community/opencode-cline-auth","name":"Cline","providers":["cline"],"community":true,
   "summary":{"en":"Cline sign-in","zh":"Cline 登录"}},
  {"package":"@magpie-community/gateway-respire-memory","name":"respire-memory","kind":"middleware","community":true,
   "summary":{"en":"Memory","zh":"记忆"}}
]}`

const marketSearchFixture = `{"objects":[
  {"package":{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14"}},
  {"package":{"name":"@magpie-community/gateway-respire-memory","version":"0.3.0"}}
]}`

const marketPackumentFixture = `{
  "dist-tags":{"latest":"0.1.14"},
  "versions":{"0.1.14":{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14",
    "main":"./opencode.mjs","keywords":["magpie","cline"],"magpie":{"name":{"zh":"Cline"}}}}
}`

func newProviderMarketApp(t *testing.T, doer provider.MarketDoer) *App {
	t.Helper()
	app := newProviderTestApp(t)
	app.SetProviderMarket(provider.NewMarket(doer))
	return app
}

// The market page needs one listing call that already says what is importable and
// never leaves the operator to paste a package.json.
func TestProviderMarketRouteListsCommunityPlugins(t *testing.T) {
	app := newProviderMarketApp(t, stubMarketDoer{body: map[string]string{
		"registry.json": marketRegistryFixture,
		"v1/search":     marketSearchFixture,
	}})

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/market",
		Query:  map[string][]string{"locale": {"zh-CN"}},
	})
	decoded := decodeProviderResponse(t, response)
	if decoded["available"] != true || decoded["source"] != "magpie-community" {
		t.Fatalf("market = %#v", decoded)
	}
	plugins, ok := decoded["plugins"].([]any)
	// The market lists provider plugins only: the middleware entry in the same
	// fixture is counted as hidden instead of being offered as a provider.
	if !ok || len(plugins) != 1 {
		t.Fatalf("plugins = %#v", decoded["plugins"])
	}
	if decoded["hidden"] != float64(1) {
		t.Fatalf("hidden = %#v, want the middleware entry", decoded["hidden"])
	}
	first := plugins[0].(map[string]any)
	if first["id"] != "@magpie-community/opencode-cline-auth" || first["name"] != "Cline" {
		t.Fatalf("first entry = %#v", first)
	}
	if first["version"] != "0.1.14" {
		t.Fatalf("version = %#v, want the published version", first["version"])
	}
	if first["summary"] != "Cline 登录" {
		t.Fatalf("summary = %#v, want the localized summary", first["summary"])
	}
	if first["imported"] != false {
		t.Fatalf("imported = %#v", first["imported"])
	}
}

// A package that is already registered is marked, so the page does not offer the
// same import twice without saying so.
func TestProviderMarketRouteMarksAnAlreadyImportedPlugin(t *testing.T) {
	app := newProviderMarketApp(t, stubMarketDoer{body: map[string]string{
		"registry.json": marketRegistryFixture,
		"v1/search":     marketSearchFixture,
	}})
	manifest := `{"name":"@magpie-community/opencode-cline-auth","version":"0.1.14","main":"./opencode.mjs","keywords":["magpie","cline"],"magpie":{"name":{"zh":"Cline"}}}`
	body, errMarshal := json.Marshal(map[string]any{"manifest": manifest, "source": "magpie"})
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	if response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   body,
	}); response.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d body = %s", response.StatusCode, string(response.Body))
	}

	decoded := decodeProviderResponse(t, app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/market",
	}))
	for _, raw := range decoded["plugins"].([]any) {
		entry := raw.(map[string]any)
		if entry["id"] != "@magpie-community/opencode-cline-auth" {
			continue
		}
		if entry["imported"] != true || entry["imported_version"] != "0.1.14" {
			t.Fatalf("entry = %#v, want it marked as imported", entry)
		}
		return
	}
	t.Fatal("the imported plugin is missing from the market listing")
}

// One click must be enough: the plugin fetches the published manifest itself.
func TestProviderImportByMarketIDFetchesTheManifest(t *testing.T) {
	app := newProviderMarketApp(t, stubMarketDoer{body: map[string]string{
		"registry.npmjs.org/@magpie-community%2Fopencode-cline-auth": marketPackumentFixture,
	}})
	body, errMarshal := json.Marshal(map[string]any{"id": "@magpie-community/opencode-cline-auth"})
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
	if entry["id"] != "cline" || entry["version"] != "0.1.14" {
		t.Fatalf("entry = %#v", entry)
	}
	if entry["source"] != "magpie" {
		t.Fatalf("source = %#v, want the market as the origin", entry["source"])
	}
	if entry["hook_execution"] != false {
		t.Fatal("a market import must not claim hook execution")
	}
}

func TestProviderImportByMarketIDReportsAFailedFetch(t *testing.T) {
	app := newProviderMarketApp(t, stubMarketDoer{fail: map[string]error{"registry.npmjs.org": http.ErrHandlerTimeout}})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   []byte(`{"id":"@magpie-community/opencode-cline-auth"}`),
	})
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
	var decoded map[string]any
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode body: %v", errDecode)
	}
	if decoded["error"] != "market_manifest_unavailable" || decoded["id"] != "@magpie-community/opencode-cline-auth" {
		t.Fatalf("body = %#v", decoded)
	}
	if catalog := decodeProviderResponse(t, app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/catalog",
	})); len(catalog["imported"].([]any)) != 0 {
		t.Fatalf("a failed fetch still imported %#v", catalog["imported"])
	}
}

func TestProviderImportByMarketIDRejectsANonPackageID(t *testing.T) {
	app := newProviderMarketApp(t, stubMarketDoer{})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   providerRoutePrefix + "/import",
		Body:   []byte(`{"id":"../../etc/passwd"}`),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.StatusCode, string(response.Body))
	}
}

// An unreachable registry is a normal answer the page can explain, not a 500.
func TestProviderMarketRouteReportsAnUnreachableRegistry(t *testing.T) {
	app := newProviderMarketApp(t, stubMarketDoer{fail: map[string]error{"registry.json": http.ErrHandlerTimeout}})
	decoded := decodeProviderResponse(t, app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   providerRoutePrefix + "/market",
	}))
	if decoded["available"] != false {
		t.Fatalf("available = %#v", decoded["available"])
	}
	if decoded["reason"] != "market_unreachable" {
		t.Fatalf("reason = %#v", decoded["reason"])
	}
	if plugins, ok := decoded["plugins"].([]any); !ok || len(plugins) != 0 {
		t.Fatalf("plugins = %#v, want an empty list", decoded["plugins"])
	}
}
