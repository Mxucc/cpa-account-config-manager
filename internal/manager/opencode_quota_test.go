package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestParseOpenCodeSSRDashboard(t *testing.T) {
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	html := `rollingUsage:$R[1]={usagePercent:25.5,resetInSec:3600} weeklyUsage:$R[2]={resetInSec:7200,usagePercent:40} monthlyUsage:$R[3]={usagePercent:50,resetInSec:86400}`
	rolling, weekly, monthly, source := parseOpenCodeDashboard(html, now)
	if source != "dashboard_ssr" {
		t.Fatalf("source=%s", source)
	}
	if rolling == nil || math.Abs(rolling.UsagePercent-25.5) > 0.001 || rolling.ResetInSec != 3600 {
		t.Fatalf("rolling=%+v", rolling)
	}
	if weekly == nil || math.Abs(weekly.UsagePercent-40) > 0.001 || weekly.ResetInSec != 7200 {
		t.Fatalf("weekly=%+v", weekly)
	}
	if monthly == nil || monthly.ResetInSec != 86400 {
		t.Fatalf("monthly=%+v", monthly)
	}
}

func TestParseOpenCodeDataSlotDashboard(t *testing.T) {
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	html := `
<div data-slot="usage-item"><span data-slot="usage-label">Rolling Usage</span><span data-slot="usage-value">31.2%</span><span data-slot="reset-time">Resets in 1 hour 30 minutes</span></div>
<div data-slot="usage-item"><span data-slot="usage-label">Weekly Usage</span><span data-slot="usage-value">44%</span><span data-slot="reset-time">Resets in 2 days 3 hours</span></div>`
	rolling, weekly, monthly, source := parseOpenCodeDashboard(html, now)
	if source != "dashboard_data_slot" {
		t.Fatalf("source=%s", source)
	}
	if rolling == nil || rolling.ResetInSec != 5400 {
		t.Fatalf("rolling=%+v", rolling)
	}
	if weekly == nil || weekly.ResetInSec != 183600 {
		t.Fatalf("weekly=%+v", weekly)
	}
	if monthly != nil {
		t.Fatalf("monthly should be nil: %+v", monthly)
	}
}

func TestRenderOpenCodeStatusPageLabels(t *testing.T) {
	snapshot := OpenCodeQuotaSnapshot{
		Accounts: []OpenCodeAccountView{{ID: "wrk_test_1", WorkspaceID: "wrk_test"}},
		Results: map[string]*OpenCodeQuotaResult{
			"wrk_test_1": {
				Success: true,
				Rolling: &OpenCodeWindowUsage{UsagePercent: 10, PercentRemaining: 90, ResetInSec: 100},
				Weekly:  &OpenCodeWindowUsage{UsagePercent: 20, PercentRemaining: 80, ResetInSec: 200},
				Monthly: &OpenCodeWindowUsage{UsagePercent: 30, PercentRemaining: 70, ResetInSec: 300},
			},
		},
	}
	body := renderOpenCodeStatusPage(snapshot, "")
	for _, label := range []string{"5 hours", "7 days", "30 days", "OpenCode Go"} {
		if !strings.Contains(body, label) {
			t.Fatalf("expected label %q in page", label)
		}
	}
	if strings.Contains(body, "super-secret-cookie-value") {
		t.Fatalf("page must not echo cookie material")
	}
	// The resource route is unauthenticated, so the page must not offer any form
	// that submits credentials or mutates state.
	for _, forbidden := range []string{"auth_cookie", "<form", `action="save"`, `action="remove"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("read-only status page contains %q", forbidden)
		}
	}
	if strings.Contains(body, "wrk_test") {
		t.Fatal("status page exposed the full workspace identifier")
	}
	if !strings.Contains(body, "****") {
		t.Fatal("status page did not mask the workspace identifier")
	}
}

func TestMaskOpenCodeWorkspaceIDKeepsOnlyASuffix(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"ab":          "**",
		"abcd":        "****",
		"wrk_abcdef":  "******cdef",
		"wrk_abc_def": "*******_def",
	}
	for input, want := range cases {
		if got := maskOpenCodeWorkspaceID(input); got != want {
			t.Fatalf("maskOpenCodeWorkspaceID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenCodeQuotaServicePersistsAccounts(t *testing.T) {
	dataDir := t.TempDir()
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: dataDir})
	firstID, errSave := service.SaveAccount("wrk_one", "cookie-secret-1", "")
	if errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	if _, errSave := service.SaveAccount("wrk_two", "cookie-secret-2", ""); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}

	raw, errRead := os.ReadFile(openCodeQuotaStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("store file missing: %v", errRead)
	}
	if !strings.Contains(string(raw), "cookie-secret-1") {
		t.Fatalf("private credential store must persist the cookie")
	}

	restored := NewOpenCodeQuotaService()
	restored.Configure(Config{DataDir: dataDir})
	views := restored.ListAccounts()
	if len(views) != 2 || views[0].WorkspaceID != "wrk_one" || views[1].WorkspaceID != "wrk_two" {
		t.Fatalf("restored accounts = %#v", views)
	}
	if rawViews, errMarshal := json.Marshal(views); errMarshal == nil && strings.Contains(string(rawViews), "cookie-secret") {
		t.Fatalf("redacted account view leaked cookie: %s", rawViews)
	}
	if errRemove := restored.RemoveAccount(firstID); errRemove != nil {
		t.Fatalf("RemoveAccount() error = %v", errRemove)
	}
	third := NewOpenCodeQuotaService()
	third.Configure(Config{DataDir: dataDir})
	if len(third.ListAccounts()) != 1 || third.ListAccounts()[0].WorkspaceID != "wrk_two" {
		t.Fatalf("removal was not persisted: %#v", third.ListAccounts())
	}
}

func TestOpenCodeManagementAccountsRoutesRedactAndPersist(t *testing.T) {
	dataDir := t.TempDir()
	host := &fakeAuthHost{}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	app.Configure([]byte("data_dir: " + dataDir + "\n"))

	saveResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/opencode/accounts",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"workspace_id":"wrk_login","auth_cookie":"super-secret-cookie"}`),
	})
	if saveResponse.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveResponse.StatusCode, saveResponse.Body)
	}
	if strings.Contains(string(saveResponse.Body), "super-secret-cookie") {
		t.Fatalf("save response leaked cookie: %s", saveResponse.Body)
	}

	listResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-account-config-manager/opencode/accounts",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
	})
	if listResponse.StatusCode != http.StatusOK || !strings.Contains(string(listResponse.Body), "wrk_login") || strings.Contains(string(listResponse.Body), "super-secret-cookie") {
		t.Fatalf("list response = %d %s", listResponse.StatusCode, listResponse.Body)
	}

	var accounts openCodeAccountsResponse
	if errDecode := json.Unmarshal(listResponse.Body, &accounts); errDecode != nil {
		t.Fatalf("decode accounts: %v", errDecode)
	}
	if len(accounts.Accounts) != 1 || accounts.Accounts[0].WorkspaceID != "wrk_login" {
		t.Fatalf("accounts = %#v", accounts.Accounts)
	}

	removeResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodDelete,
		Path:   "/v0/management/plugins/cpa-account-config-manager/opencode/accounts",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Query: map[string][]string{"account_id": {accounts.Accounts[0].ID}},
	})
	if removeResponse.StatusCode != http.StatusOK {
		t.Fatalf("remove status = %d body=%s", removeResponse.StatusCode, removeResponse.Body)
	}

	reloaded := NewApp(host, []byte("index"))
	defer reloaded.Close()
	reloaded.Configure([]byte("data_dir: " + dataDir + "\n"))
	if views := reloaded.opencode.ListAccounts(); len(views) != 0 {
		t.Fatalf("account removal was not persisted: %#v", views)
	}
}

// The status page is served on an unauthenticated resource route. Query
// parameters must never mutate state or carry a credential: a GET that saved a
// cookie could be triggered cross-site and would leak the credential into browser
// history, referrers, and server access logs.
func TestOpenCodeStatusPageResourceIsReadOnly(t *testing.T) {
	dataDir := t.TempDir()
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.Configure([]byte("data_dir: " + dataDir + "\n"))

	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/cpa-account-config-manager/opencode-status",
		Query: map[string][]string{
			"workspace_id": {"wrk_status"},
			"auth_cookie":  {"status-cookie"},
			"action":       {"save"},
			"account_id":   {"wrk_existing"},
		},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if contentType := response.Headers.Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	body := string(response.Body)
	if !strings.Contains(body, "OpenCode Go") {
		t.Fatalf("status page missing content: %.2000s", body)
	}
	if strings.Contains(body, "status-cookie") {
		t.Fatalf("status page echoed the auth cookie")
	}
	if views := app.opencode.ListAccounts(); len(views) != 0 {
		t.Fatalf("unauthenticated status page mutated state: %#v", views)
	}
}

func TestOpenCodeProbeRequiresCredentialAndRejectsEmpty(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/opencode/probe",
		Headers: http.Header{
			"Authorization": []string{"Bearer management-secret"},
		},
		Body: []byte(`{"workspace_id":"","auth_cookie":""}`),
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
}

func TestOpenCodeQuotaSnapshotNeverLeaksCookies(t *testing.T) {
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: t.TempDir()})
	if _, errSave := service.SaveAccount("wrk_leak", "cookie-leak-test", ""); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	raw, errMarshal := json.Marshal(service.Snapshot())
	if errMarshal != nil {
		t.Fatalf("marshal snapshot: %v", errMarshal)
	}
	if strings.Contains(string(raw), "cookie-leak-test") {
		t.Fatalf("snapshot leaked cookie: %s", raw)
	}
}

func TestOpenCodeStorePathIsPrivate(t *testing.T) {
	dataDir := t.TempDir()
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: dataDir})
	if _, errSave := service.SaveAccount("wrk_perm", "cookie", ""); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	info, errStat := os.Stat(filepath.Join(dataDir, "opencode-quota.json"))
	if errStat != nil {
		t.Fatalf("store missing: %v", errStat)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("store permissions too open: %v", info.Mode())
	}
}

func TestOpenCodeWriteRoutesRequireManagementKey(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	for _, route := range []string{
		"/v0/management/plugins/cpa-account-config-manager/opencode/accounts",
		"/v0/management/plugins/cpa-account-config-manager/opencode/refresh",
		"/v0/management/plugins/cpa-account-config-manager/opencode/refresh-account",
		"/v0/management/plugins/cpa-account-config-manager/opencode/probe",
	} {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodPost,
			Path:   route,
			Body:   []byte(`{}`),
		})
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("route %s status = %d body=%s", route, response.StatusCode, response.Body)
		}
	}
}

func TestOpenCodeQuotaConfigureRetriesCorruptStoreWithoutDroppingAccounts(t *testing.T) {
	firstDir := t.TempDir()
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: firstDir})
	if _, errSave := service.SaveAccount("wrk_existing", "cookie-existing", ""); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}

	secondDir := t.TempDir()
	storePath := openCodeQuotaStorePath(secondDir)
	if errWrite := os.WriteFile(storePath, []byte(`{"version":`), 0o600); errWrite != nil {
		t.Fatalf("write corrupt store: %v", errWrite)
	}
	service.Configure(Config{DataDir: secondDir})
	if got := service.ListAccounts(); len(got) != 1 || got[0].WorkspaceID != "wrk_existing" {
		t.Fatalf("corrupt store replaced live accounts: %+v", got)
	}
	if got := service.Snapshot().StorageError; got != "OpenCode quota state could not be loaded" {
		t.Fatalf("StorageError = %q", got)
	}

	persisted := openCodeQuotaPersisted{
		Version:        openCodeQuotaStoreVersion,
		Accounts:       []OpenCodeAccount{{ID: "restored", WorkspaceID: "wrk_restored", AuthCookie: "cookie-restored"}},
		TimeoutSeconds: openCodeQuotaDefaultTimeout,
	}
	if errSave := savePrivateJSON(storePath, persisted); errSave != nil {
		t.Fatalf("repair store: %v", errSave)
	}
	service.Configure(Config{DataDir: secondDir})
	if got := service.ListAccounts(); len(got) != 1 || got[0].WorkspaceID != "wrk_restored" {
		t.Fatalf("recovered accounts = %+v", got)
	}
	if got := service.Snapshot().StorageError; got != "" {
		t.Fatalf("StorageError after recovery = %q", got)
	}
}

func TestOpenCodeQuotaPersistenceFailureIsSanitized(t *testing.T) {
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blockingPath, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocker: %v", errWrite)
	}
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: blockingPath})
	_, errSave := service.SaveAccount("wrk_secret", "cookie-super-secret", "")
	if errSave == nil {
		t.Fatal("SaveAccount() error = nil")
	}
	if got := service.Snapshot().StorageError; got != "OpenCode quota state could not be persisted" {
		t.Fatalf("StorageError = %q", got)
	}
	if strings.Contains(service.Snapshot().StorageError, blockingPath) || strings.Contains(service.Snapshot().StorageError, "cookie-super-secret") {
		t.Fatalf("StorageError leaked sensitive details: %q", service.Snapshot().StorageError)
	}
}

// An account that was created before the workspace (or the key) was known must be
// completable in place: deleting and re-adding it would drop the quota history binding and
// the CPA channel that already references it.
func TestOpenCodeCredentialCanBeCompletedInPlace(t *testing.T) {
	dataDir := t.TempDir()
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: dataDir})
	accountID, errSave := service.SaveAccount("wrk_old", "cookie-secret", "")
	if errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	view, errView := service.accountView(accountID)
	if errView != nil {
		t.Fatalf("accountView() error = %v", errView)
	}
	if view.KeySet || !view.CookieSet {
		t.Fatalf("an account without a key must report cookie_set and no key_set: %#v", view)
	}

	// Add the missing API key only; the workspace and cookie stay untouched.
	updated, errUpdate := service.UpdateCredentials(accountID, OpenCodeCredentialPatch{APIKey: "sk-new-key"})
	if errUpdate != nil {
		t.Fatalf("UpdateCredentials() error = %v", errUpdate)
	}
	if !updated.KeySet || updated.WorkspaceID != "wrk_old" || !updated.CookieSet {
		t.Fatalf("partial update changed unrelated fields: %#v", updated)
	}
	if len(updated.Models) != 0 {
		t.Fatalf("a changed key must invalidate the cached catalog: %#v", updated.Models)
	}

	// Correct the workspace and the cookie in the same call.
	corrected, errCorrect := service.UpdateCredentials(accountID, OpenCodeCredentialPatch{
		WorkspaceID: "wrk_corrected",
		AuthCookie:  "auth=Fe26.2*rotated",
	})
	if errCorrect != nil {
		t.Fatalf("UpdateCredentials() error = %v", errCorrect)
	}
	if corrected.WorkspaceID != "wrk_corrected" {
		t.Fatalf("workspace = %q", corrected.WorkspaceID)
	}
	stored, errStored := service.accountView(accountID)
	if errStored != nil {
		t.Fatalf("accountView() error = %v", errStored)
	}
	if !stored.CookieSet || !stored.KeySet || stored.WorkspaceID != "wrk_corrected" {
		t.Fatalf("stored credential = %#v", stored)
	}
	// The "auth=" prefix is stripped exactly like SaveAccount does.
	encoded, errEncode := os.ReadFile(openCodeQuotaStorePath(dataDir))
	if errEncode != nil {
		t.Fatalf("read store: %v", errEncode)
	}
	if strings.Contains(string(encoded), "auth=Fe26.2*rotated") {
		t.Fatalf("the stored cookie kept its auth= prefix")
	}

	// Two accounts cannot claim the same workspace.
	if _, errOther := service.SaveAccount("wrk_other", "cookie-other", "sk-other"); errOther != nil {
		t.Fatalf("SaveAccount() error = %v", errOther)
	}
	if _, errClash := service.UpdateCredentials(accountID, OpenCodeCredentialPatch{WorkspaceID: "wrk_other"}); errClash == nil {
		t.Fatalf("a duplicate workspace must be rejected")
	}
	if _, errMissing := service.UpdateCredentials("missing-id", OpenCodeCredentialPatch{APIKey: "sk-x"}); errMissing == nil {
		t.Fatalf("an unknown account must be rejected")
	}
}

// A restart from another working directory leaves an implicit relative data directory empty,
// which used to make every stored credential look gone. The store is now adopted from a known
// location instead, and the UI can see where it came from.
func TestOpenCodeCredentialsSurviveAChangedWorkingDirectory(t *testing.T) {
	original := t.TempDir()
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: original})
	if _, errSave := service.SaveAccount("wrk_kept", "cookie-secret", "sk-key"); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}

	// The next start resolves a different (empty) directory, but the plugin knows where else
	// its state may live, exactly like a CPA restart from another working directory.
	restartedAt := t.TempDir()
	restarted := NewOpenCodeQuotaService()
	restarted.Configure(Config{DataDir: restartedAt, DataDirAlternates: []string{original}})
	accounts := restarted.ListAccounts()
	if len(accounts) != 1 || accounts[0].WorkspaceID != "wrk_kept" {
		t.Fatalf("the adopted store was not used: %#v", accounts)
	}
	storage := restarted.Storage()
	if storage.AdoptedFrom != original {
		t.Fatalf("adopted_from = %q, want %q", storage.AdoptedFrom, original)
	}
	if !storage.StoreExists || storage.StorePath != openCodeQuotaStorePath(original) {
		t.Fatalf("storage = %#v", storage)
	}
	// Later writes keep using the adopted file, so the credentials stay in one place.
	if _, errSave := restarted.SaveAccount("wrk_new", "cookie-2", "sk-2"); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	reread := NewOpenCodeQuotaService()
	reread.Configure(Config{DataDir: original})
	if len(reread.ListAccounts()) != 2 {
		t.Fatalf("the adopted store did not receive the new account: %#v", reread.ListAccounts())
	}

	// A directory that already has its own store always wins over the alternates.
	ownService := NewOpenCodeQuotaService()
	ownService.Configure(Config{DataDir: restartedAt})
	if _, errOwn := ownService.SaveAccount("wrk_own", "cookie-own", "sk-own"); errOwn != nil {
		t.Fatalf("SaveAccount() error = %v", errOwn)
	}
	preferred := NewOpenCodeQuotaService()
	preferred.Configure(Config{DataDir: original, DataDirAlternates: []string{restartedAt}})
	if accounts := preferred.ListAccounts(); len(accounts) != 2 {
		t.Fatalf("the primary store must win: %#v", accounts)
	}
}

// An unreadable store is copied aside before the plugin can write a new one, so a corrupt or
// newer-format file never destroys recoverable credentials.
func TestUnreadableOpenCodeStoreIsPreserved(t *testing.T) {
	dataDir := t.TempDir()
	store := openCodeQuotaStorePath(dataDir)
	if errWrite := os.WriteFile(store, []byte("{ this is not json"), 0o600); errWrite != nil {
		t.Fatalf("seed store: %v", errWrite)
	}
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: dataDir})
	if service.Storage().Accounts != 0 {
		t.Fatalf("an unreadable store must not report accounts")
	}
	preserved, errRead := os.ReadFile(store + ".unreadable")
	if errRead != nil || string(preserved) != "{ this is not json" {
		t.Fatalf("preserved copy = %q err=%v", preserved, errRead)
	}
}

// The storage report explains the suspicious states instead of leaving them implicit.
func TestOpenCodeStorageReportsMissingAndAdoptedStates(t *testing.T) {
	empty := NewOpenCodeQuotaService()
	empty.Configure(Config{DataDir: t.TempDir()})
	if got := empty.Storage(); got.StoreExists || got.Hint != "missing" {
		t.Fatalf("empty storage = %#v", got)
	}
}

// The OpenCode quota page is read-only for the operator, so a GET must be able to fill an
// empty or stale cache itself. Before this, the first load after a while served an empty
// cache and showed "no data" until someone pressed the refresh button by hand.

// openCodeQuotaGateway emulates the OpenCode Go dashboard scrape. It counts upstream calls
// so a test can prove how often a read reaches the upstream, and it can park a call so a
// concurrency test keeps one fetch in flight while another read arrives.
type openCodeQuotaGateway struct {
	mu      sync.Mutex
	calls   int
	cookies []string
	status  int
	body    string
	err     error

	started     chan struct{}
	startedOnce sync.Once
	hold        chan struct{}
}

func (gateway *openCodeQuotaGateway) Do(request *http.Request) (*http.Response, error) {
	gateway.mu.Lock()
	gateway.calls++
	gateway.cookies = append(gateway.cookies, request.Header.Get("Cookie"))
	status, body, errDo := gateway.status, gateway.body, gateway.err
	started, hold := gateway.started, gateway.hold
	gateway.mu.Unlock()

	if started != nil {
		gateway.startedOnce.Do(func() { close(started) })
		if hold != nil {
			<-hold
		}
	}
	if errDo != nil {
		return nil, errDo
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func (gateway *openCodeQuotaGateway) callCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.calls
}

func (gateway *openCodeQuotaGateway) lastCookie() string {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if len(gateway.cookies) == 0 {
		return ""
	}
	return gateway.cookies[len(gateway.cookies)-1]
}

func (gateway *openCodeQuotaGateway) setResponse(status int, body string, errDo error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.status, gateway.body, gateway.err = status, body, errDo
}

// openCodeQuotaDashboardBody is the SSR markup the live dashboard serves.
const openCodeQuotaDashboardBody = `rollingUsage:$R[1]={usagePercent:7.8,resetInSec:3600} ` +
	`weeklyUsage:$R[2]={resetInSec:7200,usagePercent:43.2} ` +
	`monthlyUsage:$R[3]={usagePercent:21.6,resetInSec:86400}`

// openCodeQuotaStaleDashboardBody is the same markup with different percentages, so a test
// can tell a fresh fetch apart from the values that were already cached.
const openCodeQuotaStaleDashboardBody = `rollingUsage:$R[1]={usagePercent:99,resetInSec:3600} ` +
	`weeklyUsage:$R[2]={resetInSec:7200,usagePercent:98} ` +
	`monthlyUsage:$R[3]={usagePercent:97,resetInSec:86400}`

// newOpenCodeQuotaReadApp binds one workspace behind the fake gateway.
func newOpenCodeQuotaReadApp(t *testing.T, gateway *openCodeQuotaGateway, workspaceID string) *App {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	t.Cleanup(app.Close)
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	app.opencode.doer = gateway
	if _, errSave := app.opencode.SaveAccount(workspaceID, "cookie-"+workspaceID, ""); errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	return app
}

// readOpenCodeQuota calls the management read route the OpenCode page uses.
func readOpenCodeQuota(app *App) (int, OpenCodeQuotaSnapshot, error) {
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/management/plugins/cpa-account-config-manager/opencode/quota",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	var snapshot OpenCodeQuotaSnapshot
	if errDecode := json.Unmarshal(response.Body, &snapshot); errDecode != nil {
		return response.StatusCode, snapshot, errDecode
	}
	return response.StatusCode, snapshot, nil
}

func singleOpenCodeQuotaResult(t *testing.T, snapshot OpenCodeQuotaSnapshot) *OpenCodeQuotaResult {
	t.Helper()
	if len(snapshot.Results) != 1 {
		t.Fatalf("results = %#v, want exactly one entry", snapshot.Results)
	}
	for _, result := range snapshot.Results {
		return result
	}
	return nil
}

// ageOpenCodeQuotaCache moves the cache timestamp into the past, the way a page kept open
// for a while would find it.
func ageOpenCodeQuotaCache(service *OpenCodeQuotaService, age time.Duration) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.fetchedAt = service.now().Add(-age)
}

func requireOpenCodeWindowPercents(t *testing.T, result *OpenCodeQuotaResult, rolling, weekly, monthly float64) {
	t.Helper()
	if result == nil || !result.Success {
		t.Fatalf("result = %+v, want a successful quota result", result)
	}
	if result.Rolling == nil || result.Weekly == nil || result.Monthly == nil {
		t.Fatalf("result = %+v, want rolling, weekly and monthly windows", result)
	}
	if math.Abs(result.Rolling.UsagePercent-rolling) > 0.001 ||
		math.Abs(result.Weekly.UsagePercent-weekly) > 0.001 ||
		math.Abs(result.Monthly.UsagePercent-monthly) > 0.001 {
		t.Fatalf("window percents = %v/%v/%v, want %v/%v/%v",
			result.Rolling.UsagePercent, result.Weekly.UsagePercent, result.Monthly.UsagePercent,
			rolling, weekly, monthly)
	}
}

// A read that finds an empty cache fetches once and answers with real window values.
func TestOpenCodeQuotaReadRefreshesAnEmptyCache(t *testing.T) {
	gateway := &openCodeQuotaGateway{body: openCodeQuotaDashboardBody}
	app := newOpenCodeQuotaReadApp(t, gateway, "wrk_read_empty")

	status, snapshot, errRead := readOpenCodeQuota(app)
	if errRead != nil {
		t.Fatalf("decode quota response: %v", errRead)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("upstream calls = %d, want exactly one for an empty cache", calls)
	}
	if snapshot.FetchedAt.IsZero() {
		t.Fatal("fetched_at is zero, so the read-triggered refresh did not stamp the cache")
	}
	if len(snapshot.Accounts) != 1 {
		t.Fatalf("accounts = %#v", snapshot.Accounts)
	}
	requireOpenCodeWindowPercents(t, singleOpenCodeQuotaResult(t, snapshot), 7.8, 43.2, 21.6)
	// The scrape still presents the stored session cookie upstream.
	if cookie := gateway.lastCookie(); cookie != "auth=cookie-wrk_read_empty" {
		t.Fatalf("upstream cookie = %q, want the stored auth cookie", cookie)
	}
}

// A fresh cache is answered without touching the upstream.
func TestOpenCodeQuotaReadLeavesAFreshCacheAlone(t *testing.T) {
	gateway := &openCodeQuotaGateway{body: openCodeQuotaDashboardBody}
	app := newOpenCodeQuotaReadApp(t, gateway, "wrk_read_fresh")

	if _, _, errRead := readOpenCodeQuota(app); errRead != nil {
		t.Fatalf("decode first quota response: %v", errRead)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("upstream calls after the first read = %d, want 1", calls)
	}

	// A different dashboard body would be visible if the second read fetched again.
	gateway.setResponse(http.StatusOK, openCodeQuotaStaleDashboardBody, nil)
	status, snapshot, errRead := readOpenCodeQuota(app)
	if errRead != nil {
		t.Fatalf("decode second quota response: %v", errRead)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("upstream calls after a read with a fresh cache = %d, want still 1", calls)
	}
	requireOpenCodeWindowPercents(t, singleOpenCodeQuotaResult(t, snapshot), 7.8, 43.2, 21.6)
}

// A cache older than the read window is refreshed by the read itself.
func TestOpenCodeQuotaReadRefreshesAStaleCache(t *testing.T) {
	gateway := &openCodeQuotaGateway{body: openCodeQuotaDashboardBody}
	app := newOpenCodeQuotaReadApp(t, gateway, "wrk_read_stale")

	if _, _, errRead := readOpenCodeQuota(app); errRead != nil {
		t.Fatalf("decode first quota response: %v", errRead)
	}
	ageOpenCodeQuotaCache(app.opencode, 2*openCodeQuotaReadStaleness)
	gateway.setResponse(http.StatusOK, openCodeQuotaStaleDashboardBody, nil)

	status, snapshot, errRead := readOpenCodeQuota(app)
	if errRead != nil {
		t.Fatalf("decode second quota response: %v", errRead)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if calls := gateway.callCount(); calls != 2 {
		t.Fatalf("upstream calls after a stale read = %d, want 2", calls)
	}
	requireOpenCodeWindowPercents(t, singleOpenCodeQuotaResult(t, snapshot), 99, 98, 97)
}

// A failing upstream must not turn the read into an error, and it must not erase the values
// the operator was already looking at.
func TestOpenCodeQuotaReadKeepsCachedValuesWhenTheUpstreamFails(t *testing.T) {
	gateway := &openCodeQuotaGateway{body: openCodeQuotaDashboardBody}
	app := newOpenCodeQuotaReadApp(t, gateway, "wrk_read_failing")

	if _, _, errRead := readOpenCodeQuota(app); errRead != nil {
		t.Fatalf("decode first quota response: %v", errRead)
	}

	for index, failure := range []struct {
		name   string
		status int
		errDo  error
	}{
		{name: "gateway rejects the cookie", status: http.StatusUnauthorized},
		{name: "upstream is unreachable", errDo: errors.New("dial tcp: connection refused")},
	} {
		ageOpenCodeQuotaCache(app.opencode, 2*openCodeQuotaReadStaleness)
		gateway.setResponse(failure.status, "login required", failure.errDo)

		status, snapshot, errRead := readOpenCodeQuota(app)
		if errRead != nil {
			t.Fatalf("%s: decode quota response: %v", failure.name, errRead)
		}
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 for a read", failure.name, status)
		}
		if calls := gateway.callCount(); calls != index+2 {
			t.Fatalf("%s: upstream calls = %d, want %d", failure.name, calls, index+2)
		}
		requireOpenCodeWindowPercents(t, singleOpenCodeQuotaResult(t, snapshot), 7.8, 43.2, 21.6)
	}
}

// A read whose very first fetch fails still answers 200 with the failed cache entry, because
// the read route reports quota and never the transport.
func TestOpenCodeQuotaReadAnswersWhenTheFirstFetchFails(t *testing.T) {
	gateway := &openCodeQuotaGateway{status: http.StatusUnauthorized, body: "login required"}
	app := newOpenCodeQuotaReadApp(t, gateway, "wrk_read_denied")

	status, snapshot, errRead := readOpenCodeQuota(app)
	if errRead != nil {
		t.Fatalf("decode quota response: %v", errRead)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	result := singleOpenCodeQuotaResult(t, snapshot)
	if result == nil || result.Success || result.Error == "" {
		t.Fatalf("result = %+v, want the sanitized failure in the cache", result)
	}
}

// Nothing is bound, so a read must answer instantly instead of scraping anything.
func TestOpenCodeQuotaReadWithoutBoundAccountsSkipsTheFetch(t *testing.T) {
	gateway := &openCodeQuotaGateway{body: openCodeQuotaDashboardBody}
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	t.Cleanup(app.Close)
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	app.opencode.doer = gateway

	status, snapshot, errRead := readOpenCodeQuota(app)
	if errRead != nil {
		t.Fatalf("decode quota response: %v", errRead)
	}
	if status != http.StatusOK || len(snapshot.Accounts) != 0 {
		t.Fatalf("status = %d accounts = %#v", status, snapshot.Accounts)
	}
	if calls := gateway.callCount(); calls != 0 {
		t.Fatalf("upstream calls without a bound account = %d, want 0", calls)
	}
}

// Several readers at once (tabs, a reload storm) must share one upstream fetch.
func TestConcurrentOpenCodeQuotaReadsPerformOneFetch(t *testing.T) {
	gateway := &openCodeQuotaGateway{
		body:    openCodeQuotaDashboardBody,
		started: make(chan struct{}),
		hold:    make(chan struct{}),
	}
	app := newOpenCodeQuotaReadApp(t, gateway, "wrk_read_race")

	type readOutcome struct {
		status   int
		snapshot OpenCodeQuotaSnapshot
		err      error
	}
	startReader := func() chan readOutcome {
		done := make(chan readOutcome, 1)
		go func() {
			status, snapshot, errRead := readOpenCodeQuota(app)
			done <- readOutcome{status: status, snapshot: snapshot, err: errRead}
		}()
		return done
	}

	first := startReader()
	<-gateway.started
	second := startReader()
	// The first fetch is parked inside the gateway, so the second reader is concurrent with
	// it: it either waits on the shared fetch lock or arrives to find the fresh cache.
	time.Sleep(50 * time.Millisecond)
	close(gateway.hold)

	for index, done := range []chan readOutcome{first, second} {
		outcome := <-done
		if outcome.err != nil {
			t.Fatalf("reader %d: decode quota response: %v", index+1, outcome.err)
		}
		if outcome.status != http.StatusOK {
			t.Fatalf("reader %d: status = %d, want 200", index+1, outcome.status)
		}
		requireOpenCodeWindowPercents(t, singleOpenCodeQuotaResult(t, outcome.snapshot), 7.8, 43.2, 21.6)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("upstream calls for two concurrent reads = %d, want exactly one", calls)
	}
}
