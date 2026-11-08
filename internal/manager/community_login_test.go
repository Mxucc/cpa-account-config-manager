package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An imported community provider is offered with the community flow, and its
// sign-in is run by this plugin. Without the package or the runtime it says why,
// instead of sending the operator to another application.
func TestCommunityLoginExplainsAMissingRuntime(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	packageDir := filepath.Join(app.providerPackageRoot(), "magpie-community__opencode-zcode-auth")
	if errMkdir := os.MkdirAll(packageDir, 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	if errWrite := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"name":"@magpie-community/opencode-zcode-auth"}`), 0o600); errWrite != nil {
		t.Fatalf("write manifest: %v", errWrite)
	}
	if _, errImport := app.providerImportStore().Import(
		[]byte(`{"name":"@magpie-community/opencode-zcode-auth","version":"0.1.57","main":"./opencode.mjs","providers":["zcode"]}`),
		"magpie", packageDir,
	); errImport != nil {
		t.Fatalf("Import: %v", errImport)
	}
	// No Bun in the test environment: the route must explain that, not crash.
	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/accounts/login/start",
		`{"provider":"zcode"}`, nil,
	))
	var decoded map[string]any
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, response.Body)
	}
	// Whether the runtime is missing or the package has no module, the route must
	// say why instead of failing silently.
	reason, _ := decoded["reason"].(string)
	message, _ := decoded["message"].(string)
	explained := reason + " " + message
	if strings.TrimSpace(explained) == "" {
		t.Fatalf("no reason was given: %s", response.Body)
	}
	if decoded["flow"] != "community" && decoded["error"] == nil {
		t.Fatalf("answer = %s", response.Body)
	}
}

// A provider that was never imported cannot start a community sign-in.
func TestCommunityLoginRequiresAnImportedProvider(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	if app.isImportedProvider("zcode") {
		t.Fatal("an unimported provider was reported as imported")
	}
	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/accounts/login/start",
		`{"provider":"zcode"}`, nil,
	))
	// It falls through to the host login path, which does not know the provider.
	if response.StatusCode == http.StatusOK && strings.Contains(string(response.Body), "community_sign_in") {
		t.Fatalf("body = %s", response.Body)
	}
}

// The runtime route lives under /providers, which the provider router owns. It must
// be answered before that router, or it reports an unknown provider route.
func TestProviderRuntimeRouteIsReachable(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	response := app.HandleManagement(context.Background(), accountLoginRequest(
		http.MethodGet, "/v0/management"+managementRoutePrefix+"/providers/runtime", "", nil,
	))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), "unknown provider route") {
		t.Fatalf("the provider router claimed the runtime route: %s", response.Body)
	}
	var decoded map[string]any
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if _, found := decoded["available"]; !found {
		t.Fatalf("body = %s", response.Body)
	}
}
