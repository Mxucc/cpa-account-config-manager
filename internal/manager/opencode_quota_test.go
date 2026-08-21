package manager

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
}

func TestOpenCodeQuotaServicePersistsAccounts(t *testing.T) {
	dataDir := t.TempDir()
	service := NewOpenCodeQuotaService()
	service.Configure(Config{DataDir: dataDir})
	firstID, errSave := service.SaveAccount("wrk_one", "cookie-secret-1")
	if errSave != nil {
		t.Fatalf("SaveAccount() error = %v", errSave)
	}
	if _, errSave := service.SaveAccount("wrk_two", "cookie-secret-2"); errSave != nil {
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

func TestOpenCodeStatusPageResourceServesHTMLAndSavesAccount(t *testing.T) {
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
		},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if contentType := response.Headers.Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	body := string(response.Body)
	if !strings.Contains(body, "OpenCode Go") || !strings.Contains(body, "wrk_status") {
		t.Fatalf("status page missing account content: %.2000s", body)
	}
	if strings.Contains(body, "status-cookie") {
		t.Fatalf("status page echoed the auth cookie")
	}
	views := app.opencode.ListAccounts()
	if len(views) != 1 || views[0].WorkspaceID != "wrk_status" {
		t.Fatalf("status page save did not bind account: %#v", views)
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
	if _, errSave := service.SaveAccount("wrk_leak", "cookie-leak-test"); errSave != nil {
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
	if _, errSave := service.SaveAccount("wrk_perm", "cookie"); errSave != nil {
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
