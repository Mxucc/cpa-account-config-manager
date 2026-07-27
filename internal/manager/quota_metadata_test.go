package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestQuotaMetadataParsersPreserveZeroAndOfficialPrecedence(t *testing.T) {
	plan, usageCount, valid := parseQuotaUsageMetadata([]byte(`{"planType":"Free","rateLimitResetCredits":{"availableCount":"7"}}`))
	if !valid || plan != "free" || usageCount == nil || *usageCount != 7 {
		t.Fatalf("usage metadata = plan:%q count:%v valid:%t", plan, usageCount, valid)
	}
	count, valid := parseQuotaResetCredits([]byte(`{"available_count":0,"credits":[{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-07-28T00:00:00Z"}]}`))
	if !valid || count == nil || *count != 0 {
		t.Fatalf("reset count = %v valid:%t, want known zero", count, valid)
	}
	count, valid = parseQuotaResetCredits([]byte(`{"credits":[{"resetType":"codex_rate_limits","status":"available","expiresAt":"2026-07-28T00:00:00Z"},{"reset_type":"other","status":"available","expires_at":"2026-07-28T00:00:00Z"}]}`))
	if !valid || count == nil || *count != 1 {
		t.Fatalf("credit fallback = %v valid:%t, want one", count, valid)
	}
	for _, raw := range []string{
		`{"available_count":-1}`,
		`{"available_count":1.5}`,
		`{"available_count":1000001}`,
		`{"unexpected":"shape"}`,
	} {
		if parsed, ok := parseQuotaResetCredits([]byte(raw)); ok && parsed != nil {
			t.Fatalf("unsafe count %s parsed as %v", raw, *parsed)
		}
	}
}

func TestQuotaMetadataRefreshUsesCPAAPIAndPersistsPlanFirstType(t *testing.T) {
	dataDir := t.TempDir()
	host := quotaMetadataHost()
	var mu sync.Mutex
	requests := make([]managementAPICallRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v0/management/api-call" {
			t.Errorf("management request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			t.Errorf("management authorization was not forwarded")
		}
		var call managementAPICallRequest
		if errDecode := json.NewDecoder(request.Body).Decode(&call); errDecode != nil {
			t.Errorf("decode API call: %v", errDecode)
		}
		mu.Lock()
		requests = append(requests, call)
		mu.Unlock()
		body := `{"available_count":0,"credits":[]}`
		if call.URL == codexQuotaUsageURL {
			body = `{"plan_type":"Free","rate_limit_reset_credits":{"available_count":6},"rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_after_seconds":3600}}}`
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"status_code": 200, "header": map[string][]string{}, "body": body})
	}))
	defer server.Close()

	app := NewApp(host, []byte("index"))
	app.Configure([]byte(fmt.Sprintf("data_dir: %q\nmanagement_base_url: %q\n", dataDir, server.URL)))
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/accounts/quota-metadata/refresh",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
		Body:    []byte(`{"account_id":"auth-1"}`),
	})
	if response.StatusCode != http.StatusOK {
		app.Close()
		t.Fatalf("refresh = %d %s", response.StatusCode, response.Body)
	}
	var refreshed QuotaMetadataResponse
	if errDecode := json.Unmarshal(response.Body, &refreshed); errDecode != nil {
		app.Close()
		t.Fatalf("decode response: %v", errDecode)
	}
	if refreshed.PlanType != "free" || refreshed.ActiveResetCount == nil || *refreshed.ActiveResetCount != 0 {
		app.Close()
		t.Fatalf("refresh result = %#v", refreshed)
	}
	listed, errList := app.accounts.List(context.Background(), ListQuery{Page: 1, PageSize: 50})
	if errList != nil || len(listed.Accounts) != 1 || listed.Accounts[0].PlanType != "free" || listed.Accounts[0].Usage == nil || listed.Accounts[0].Usage.Codex == nil || listed.Accounts[0].Usage.Codex.ActiveResetCount == nil || *listed.Accounts[0].Usage.Codex.ActiveResetCount != 0 {
		app.Close()
		t.Fatalf("listed account = %#v err=%v", listed.Accounts, errList)
	}
	app.Close()

	mu.Lock()
	captured := append([]managementAPICallRequest(nil), requests...)
	mu.Unlock()
	if len(captured) != 2 || captured[0].URL != codexQuotaUsageURL || captured[1].URL != codexQuotaResetCreditsURL {
		t.Fatalf("API calls = %#v", captured)
	}
	for _, call := range captured {
		if call.AuthIndex != "auth-1" || call.Header["Authorization"] != "Bearer $TOKEN$" || call.Header["Chatgpt-Account-Id"] != "chatgpt-account-1" {
			t.Fatalf("unsafe or incomplete API call = %#v", call)
		}
	}

	restarted := NewApp(host, []byte("index"))
	restarted.Configure([]byte(fmt.Sprintf("data_dir: %q\nmanagement_base_url: %q\n", dataDir, server.URL)))
	reloaded, errReload := restarted.accounts.List(context.Background(), ListQuery{Page: 1, PageSize: 50})
	if errReload != nil || len(reloaded.Accounts) != 1 || reloaded.Accounts[0].PlanType != "free" || reloaded.Accounts[0].Usage == nil || reloaded.Accounts[0].Usage.Codex == nil || reloaded.Accounts[0].Usage.Codex.ActiveResetCount == nil || *reloaded.Accounts[0].Usage.Codex.ActiveResetCount != 0 {
		restarted.Close()
		t.Fatalf("reloaded account = %#v err=%v", reloaded.Accounts, errReload)
	}
	restarted.Close()
	raw, errRead := os.ReadFile(usageStorePath(dataDir))
	if errRead != nil {
		t.Fatalf("read usage store: %v", errRead)
	}
	for _, forbidden := range []string{"management-secret", "Bearer $TOKEN$", "chatgpt-account-1", "rate_limit_reset_credits", "primary_window"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("usage store leaked %q: %s", forbidden, raw)
		}
	}
}

func TestQuotaMetadataResetRequiresConfirmationAndRefreshesRemainingCount(t *testing.T) {
	host := quotaMetadataHost()
	var mu sync.Mutex
	resetReads := 0
	consumeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call managementAPICallRequest
		_ = json.NewDecoder(request.Body).Decode(&call)
		body := `{"plan_type":"plus"}`
		mu.Lock()
		switch call.URL {
		case codexQuotaResetCreditsURL:
			resetReads++
			count := 1
			body = fmt.Sprintf(`{"available_count":%d,"credits":[]}`, count)
		case codexQuotaResetConsumeURL:
			consumeCalls++
			if call.Method != http.MethodPost || !strings.Contains(call.Data, "redeem_request_id") {
				t.Errorf("consume call = %#v", call)
			}
			body = `{}`
		}
		mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]any{"status_code": 200, "header": map[string][]string{}, "body": body})
	}))
	defer server.Close()
	app := NewApp(host, []byte("index"))
	defer app.Close()
	app.Configure([]byte(fmt.Sprintf("data_dir: %q\nmanagement_base_url: %q\n", t.TempDir(), server.URL)))

	request := cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/accounts/quota-metadata/reset",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}}, Body: []byte(`{"account_id":"auth-1"}`),
	}
	denied := app.HandleManagement(context.Background(), request)
	if denied.StatusCode != http.StatusBadRequest {
		t.Fatalf("unconfirmed reset = %d %s", denied.StatusCode, denied.Body)
	}
	mu.Lock()
	if consumeCalls != 0 || resetReads != 0 {
		mu.Unlock()
		t.Fatalf("unconfirmed request made upstream calls: reset=%d consume=%d", resetReads, consumeCalls)
	}
	mu.Unlock()

	request.Body = []byte(`{"account_id":"auth-1","confirm":true}`)
	response := app.HandleManagement(context.Background(), request)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confirmed reset = %d %s", response.StatusCode, response.Body)
	}
	var result QuotaMetadataResponse
	_ = json.Unmarshal(response.Body, &result)
	if !result.ResetCreditUsed || result.ActiveResetCount == nil || *result.ActiveResetCount != 0 || result.Warning != "quota_metadata_refresh_after_reset_unavailable" {
		t.Fatalf("reset result = %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if consumeCalls != 1 || resetReads != 2 {
		t.Fatalf("upstream calls: reset=%d consume=%d", resetReads, consumeCalls)
	}
}

func quotaMetadataHost() *fakeAuthHost {
	path := filepath.Join(os.TempDir(), "auth-1.json")
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "auth-1.json", Provider: "codex", Type: "codex", AccountType: "oauth",
			PlanType: "k12", Email: "quota@example.com", Source: "file", Path: path,
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {AuthIndex: "auth-1", Name: "auth-1.json", Path: path, JSON: json.RawMessage(`{"email":"quota@example.com","id_token":{"chatgpt_account_id":"chatgpt-account-1","plan_type":"k12"}}`)},
		},
	}
}

func TestQuotaMetadataPersistenceKeepsAccountsSeparatedByIdentity(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.persistDelay = time.Hour
	defer tracker.Close()
	tracker.bindUsageAccounts([]cpaapi.HostAuthFileEntry{
		{AuthIndex: "left", Email: "left@example.com"},
		{AuthIndex: "right", Email: "right@example.com"},
	})
	zero := 0
	tracker.ObserveCredentialUsage("left", &CodexUsageSnapshot{PlanType: "free", ActiveResetCount: &zero})
	if right := tracker.Snapshot("right"); right != nil {
		t.Fatalf("right account received left metadata: %#v", right)
	}
	observedAt := time.Now().UTC()
	tracker.ObserveCredentialUsage("left", &CodexUsageSnapshot{MetadataObservedAt: observedAt})
	left := tracker.Snapshot("left")
	if left == nil || left.Codex == nil || left.Codex.PlanType != "" || left.Codex.ActiveResetCount != nil {
		t.Fatalf("explicit metadata refresh retained stale values: %#v", left)
	}
}
