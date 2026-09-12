package manager

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// writeAuthEntryWithDir creates one real auth file so the directory discovery accepts it.
func writeAuthEntryWithDir(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"token"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}
	return path
}

// Plugin state must not live in a directory that moves with CPA's working directory. When the
// data directory is implicit it follows the discovered CPA auth directory, and state that was
// written to the old working-directory default is adopted instead of abandoned.
func TestPluginStateFollowsTheAuthDirectory(t *testing.T) {
	authDir := t.TempDir()
	authPath := writeAuthEntryWithDir(t, authDir, "codex-one.json")

	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		Name: "codex-one.json", Path: authPath, Source: "file",
	}}}
	app := NewApp(host, nil)
	// An implicit data directory, exactly like an installation that never configured data_dir.
	app.ConfigureHost([]byte(""), cpaapi.SchemaVersion)
	app.DiscoverAuthStorage(host.entries)

	storage := app.opencode.Storage()
	// macOS reports /private/var for a temporary directory created as /var, so compare resolved
	// paths instead of the raw strings.
	wantDir := resolvePathForTest(t, filepath.Join(authDir, usageDurableDirName))
	if got := resolvePathForTest(t, storage.DataDir); got != wantDir {
		t.Fatalf("state dir = %q, want %q", got, wantDir)
	}
	// A credential saved now lands beside the auth files, so a later restart resolves the same
	// directory no matter which working directory CPA was started from.
	if _, errSave := app.opencode.SaveAccount("wrk_kept", "cookie-secret", "sk-kept"); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	storePath := filepath.Join(wantDir, opencodeQuotaStoreFileName)
	if _, errStat := os.Stat(storePath); errStat != nil {
		t.Fatalf("state was not written beside the auth directory: %v", errStat)
	}
	// A fresh instance configured the same way reads the same credentials back.
	restarted := NewOpenCodeQuotaService()
	restarted.Configure(Config{DataDir: wantDir})
	accounts := restarted.ListAccounts()
	if len(accounts) != 1 || accounts[0].WorkspaceID != "wrk_kept" {
		t.Fatalf("accounts = %#v", accounts)
	}
	// The legacy working-directory default stays a fallback, so state written by an older release
	// is adopted when it is still reachable.
	if !containsAnyPath(app.opencode.Storage(), filepath.Join("data", "cpa-account-config-manager")) {
		t.Fatalf("the working-directory default must remain a fallback")
	}
}

// containsAnyPath reports whether the state directory or one of its fallbacks matches.
func containsAnyPath(info OpenCodeStorageInfo, want string) bool {
	if strings.Contains(info.DataDir, want) || strings.Contains(info.AdoptedFrom, want) {
		return true
	}
	for _, fallback := range info.Fallbacks {
		if strings.Contains(fallback, want) {
			return true
		}
	}
	return false
}

// An operator-pinned data_dir always wins, so an explicit deployment layout is never overridden.
func TestExplicitDataDirIsNotMovedToTheAuthDirectory(t *testing.T) {
	authDir := t.TempDir()
	authPath := writeAuthEntryWithDir(t, authDir, "codex-one.json")
	pinned := t.TempDir()

	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		Name: "codex-one.json", Path: authPath, Source: "file",
	}}}
	app := NewApp(host, nil)
	app.ConfigureHost([]byte("data_dir: "+pinned), cpaapi.SchemaVersion)
	app.DiscoverAuthStorage(host.entries)

	if got := app.opencode.Storage().DataDir; got != pinned {
		t.Fatalf("pinned data dir = %q, want %q", got, pinned)
	}
}

// The storage report names the effective directory, which is what an operator needs when
// credentials appear to be missing.
func TestStateDirectoryIsReportedThroughTheRoute(t *testing.T) {
	authDir := t.TempDir()
	authPath := writeAuthEntryWithDir(t, authDir, "codex-one.json")
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		Name: "codex-one.json", Path: authPath, Source: "file",
	}}}
	app := NewApp(host, nil)
	app.ConfigureHost([]byte(""), cpaapi.SchemaVersion)
	app.DiscoverAuthStorage(host.entries)

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/storage",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if !strings.Contains(string(response.Body), filepath.Join(authDir, usageDurableDirName)) {
		t.Fatalf("body = %s", response.Body)
	}
}

// resolvePathForTest follows symlinks so two spellings of the same directory compare equal.
func resolvePathForTest(t *testing.T, path string) string {
	t.Helper()
	if resolved, errResolve := filepath.EvalSymlinks(path); errResolve == nil {
		return resolved
	}
	return path
}
