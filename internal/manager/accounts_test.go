package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type fakeAuthHost struct {
	mu         sync.Mutex
	entries    []cpaapi.HostAuthFileEntry
	details    map[string]cpaapi.HostAuthGetResponse
	listError  error
	errors     map[string]error
	saveErrors map[string]error
	saveCalls  map[string]int
	saves      []cpaapi.HostAuthSaveRequest
}

func (f *fakeAuthHost) ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listError != nil {
		return nil, f.listError
	}
	return append([]cpaapi.HostAuthFileEntry(nil), f.entries...), nil
}

func (f *fakeAuthHost) GetAuth(_ context.Context, authIndex string) (cpaapi.HostAuthGetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errors[authIndex]; err != nil {
		return cpaapi.HostAuthGetResponse{}, err
	}
	detail := f.details[authIndex]
	detail.JSON = append(json.RawMessage(nil), detail.JSON...)
	return detail, nil
}

func (f *fakeAuthHost) SaveAuth(_ context.Context, name string, rawJSON json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveCalls == nil {
		f.saveCalls = make(map[string]int)
	}
	f.saveCalls[name]++
	if err := f.saveErrors[name]; err != nil {
		return cpaapi.HostAuthSaveResponse{}, err
	}
	request := cpaapi.HostAuthSaveRequest{Name: name, JSON: append(json.RawMessage(nil), rawJSON...)}
	f.saves = append(f.saves, request)
	for authIndex, detail := range f.details {
		if detail.Name != name {
			continue
		}
		detail.JSON = append(json.RawMessage(nil), rawJSON...)
		f.details[authIndex] = detail
	}
	for index := range f.entries {
		if f.entries[index].Name != name {
			continue
		}
		f.entries[index].Size = int64(len(rawJSON))
		f.entries[index].ModTime = time.Now().UTC()
	}
	return cpaapi.HostAuthSaveResponse{Name: name, Path: "/auths/" + name}, nil
}

func TestAccountServiceListRedactsSensitiveConfig(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			ID:            "auth-file-id",
			AuthIndex:     "auth-index-1",
			Name:          "codex-user.json",
			Provider:      "codex",
			Type:          "codex",
			Label:         "operator@example.com",
			Email:         "operator@example.com",
			AccountType:   "api_key",
			Account:       "sk-secret-account-value",
			StatusMessage: "Bearer status-secret",
			Source:        "file",
			Path:          "/auths/codex-user.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-index-1": {
				AuthIndex: "auth-index-1",
				Name:      "codex-user.json",
				Path:      "/auths/codex-user.json",
				JSON: json.RawMessage(`{
					"type":"codex",
					"api_key":"sk-secret",
					"access_token":"token-secret",
					"prefix":"team-a",
					"proxy_url":"http://user:password@127.0.0.1:7890?token=secret",
					"headers":{"Authorization":"Bearer secret","X-Team":"alpha"},
					"priority":7,
					"plan_type":"K12",
					"websockets":false
				}`),
			},
		},
	}

	response, errList := NewAccountService(host).List(context.Background(), ListQuery{Page: 1, PageSize: 20})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(response.Accounts) != 1 {
		t.Fatalf("accounts len = %d, want 1", len(response.Accounts))
	}
	account := response.Accounts[0]
	if account.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q, want redacted host", account.Proxy)
	}
	if account.HeaderCount != 2 || len(account.HeaderNames) != 2 {
		t.Fatalf("header summary = %d %#v", account.HeaderCount, account.HeaderNames)
	}
	if account.PlanType != "k12" {
		t.Fatalf("plan type = %q, want k12", account.PlanType)
	}
	encoded, errMarshal := json.Marshal(account)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if account.StatusMessage != "provider reported an account error" {
		t.Fatalf("status message = %q", account.StatusMessage)
	}
	for _, secret := range []string{"sk-secret", "token-secret", "password", "Bearer secret", "sk-secret-account-value", "status-secret"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("public account leaked %q: %s", secret, encoded)
		}
	}
	for _, emptyTime := range []string{"updated_at", "last_refresh", "0001-01-01"} {
		if bytes.Contains(encoded, []byte(emptyTime)) {
			t.Fatalf("public account contained empty timestamp %q: %s", emptyTime, encoded)
		}
	}
}

func TestAccountServicePlanTypeProjectionAndFilteringStayConsistent(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "k12", Name: "k12.json", Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file", Path: "/auths/k12.json"},
			{AuthIndex: "plus", Name: "plus.json", Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file", Path: "/auths/plus.json"},
			{AuthIndex: "unsafe", Name: "unsafe.json", Provider: "codex", Type: "codex", AccountType: "oauth", Source: "file", Path: "/auths/unsafe.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"k12": {
				AuthIndex: "k12", Name: "k12.json", Path: "/auths/k12.json",
				JSON: json.RawMessage(`{"type":"codex","plan_type":"K12","access_token":"secret-k12"}`),
			},
			"plus": {
				AuthIndex: "plus", Name: "plus.json", Path: "/auths/plus.json",
				JSON: json.RawMessage(`{"type":"codex","chatgpt_plan_type":"plus","access_token":"secret-plus"}`),
			},
			"unsafe": {
				AuthIndex: "unsafe", Name: "unsafe.json", Path: "/auths/unsafe.json",
				JSON: json.RawMessage(`{"type":"codex","plan_type":"Bearer secret-plan-value","access_token":"secret-unsafe"}`),
			},
		},
	}
	service := NewAccountService(host)

	listed, errList := service.List(t.Context(), ListQuery{Page: 1, PageSize: 20, Filters: AccountFilters{Type: "k12"}})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if listed.Total != 1 || len(listed.Accounts) != 1 || listed.Accounts[0].ID != "k12" || listed.Accounts[0].PlanType != "k12" {
		t.Fatalf("plan-filtered list = %#v", listed)
	}

	searched, errSearch := service.List(t.Context(), ListQuery{Page: 1, PageSize: 20, Filters: AccountFilters{Search: "plus"}})
	if errSearch != nil {
		t.Fatalf("search List() error = %v", errSearch)
	}
	if searched.Total != 1 || searched.Accounts[0].ID != "plus" || searched.Accounts[0].PlanType != "plus" {
		t.Fatalf("plan-searched list = %#v", searched)
	}

	exported, errExport := service.Export(t.Context(), AccountFilters{Type: "plus"})
	if errExport != nil {
		t.Fatalf("Export() error = %v", errExport)
	}
	if len(exported) != 1 || exported[0].ID != "plus" {
		t.Fatalf("plan-filtered export = %#v", exported)
	}

	resolved, errResolve := service.ResolveTargets(t.Context(), TargetScope{Mode: "filtered", Filters: AccountFilters{Type: "k12"}})
	if errResolve != nil {
		t.Fatalf("ResolveTargets() error = %v", errResolve)
	}
	if len(resolved.Accounts) != 1 || resolved.Accounts[0].ID != "k12" {
		t.Fatalf("plan-filtered targets = %#v", resolved)
	}

	credentials, errCredentials := service.ExportCredentialSources(t.Context(), AccountFilters{Type: "k12"})
	if errCredentials != nil {
		t.Fatalf("ExportCredentialSources() error = %v", errCredentials)
	}
	defer clearCredentialExportCollection(&credentials)
	if len(credentials.Sources) != 1 || credentials.Sources[0].Account.ID != "k12" {
		t.Fatalf("plan-filtered credential sources = %#v", credentials)
	}

	all, errAll := service.List(t.Context(), ListQuery{Page: 1, PageSize: 20})
	if errAll != nil {
		t.Fatalf("unfiltered List() error = %v", errAll)
	}
	for _, account := range all.Accounts {
		if account.ID == "unsafe" && account.PlanType != "" {
			t.Fatalf("unsafe plan type was projected: %q", account.PlanType)
		}
	}
	encoded, errMarshal := json.Marshal(all)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if bytes.Contains(encoded, []byte("secret-plan-value")) || bytes.Contains(encoded, []byte("secret-unsafe")) {
		t.Fatalf("account projection leaked unsafe metadata: %s", encoded)
	}
}

func TestAccountServiceListJoinsSanitizedUsageAndNativeRequestActivity(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	tracker := NewUsageTracker()
	defer tracker.Close()
	tracker.now = func() time.Time { return now }
	tracker.persistDelay = time.Hour
	tracker.Configure(Config{DataDir: t.TempDir()})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "auth-index-usage",
		AuthID:    "runtime-secret-id",
		APIKey:    "sk-client-secret",
		Failure:   cpaapi.UsageFailure{Body: "upstream-secret-body"},
		Detail:    cpaapi.UsageDetail{InputTokens: 80, OutputTokens: 20, TotalTokens: 100},
		ResponseHeaders: http.Header{
			"Authorization":                         []string{"Bearer header-secret"},
			"Set-Cookie":                            []string{"session=cookie-secret"},
			"X-Codex-Primary-Used-Percent":          []string{"64"},
			"X-Codex-Primary-Reset-After-Seconds":   []string{"604800"},
			"X-Codex-Primary-Window-Minutes":        []string{"10080"},
			"X-Codex-Secondary-Used-Percent":        []string{"18"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{"1800"},
			"X-Codex-Secondary-Window-Minutes":      []string{"300"},
		},
	})
	tracker.Observe(cpaapi.UsageRecord{
		AuthIndex: "fallback-auth-id",
		Detail:    cpaapi.UsageDetail{TotalTokens: 999},
	})

	nextRetryAfter := now.Add(10 * time.Minute)
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{
				ID:             "runtime-id",
				AuthIndex:      "auth-index-usage",
				Name:           "usage.json",
				Provider:       "codex",
				Source:         "file",
				Path:           "/auths/usage.json",
				Success:        12,
				Failed:         2,
				NextRetryAfter: nextRetryAfter,
				RecentRequests: []cpaapi.HostRecentRequestEntry{{Time: "2026-07-15T12:00:00Z", Success: 3, Failed: 1}},
			},
			{
				ID:       "fallback-auth-id",
				Name:     "missing-index.json",
				Provider: "codex",
				Source:   "file",
				Path:     "/auths/missing-index.json",
			},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-index-usage": {AuthIndex: "auth-index-usage", Path: "/auths/usage.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}

	response, errList := NewAccountService(host, tracker).List(context.Background(), ListQuery{Page: 1, PageSize: 20})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(response.Accounts) != 2 {
		t.Fatalf("accounts len = %d, want 2", len(response.Accounts))
	}
	var usageAccount, missingIndexAccount Account
	for _, account := range response.Accounts {
		switch account.ID {
		case "auth-index-usage":
			usageAccount = account
		case "fallback-auth-id":
			missingIndexAccount = account
		}
	}
	if usageAccount.Usage == nil || usageAccount.Usage.TotalTokens != 100 || usageAccount.Usage.Codex == nil {
		t.Fatalf("usage snapshot = %#v", usageAccount.Usage)
	}
	if usageAccount.Success != 12 || usageAccount.Failed != 2 || len(usageAccount.RecentRequests) != 1 {
		t.Fatalf("request activity = success:%d failed:%d recent:%#v", usageAccount.Success, usageAccount.Failed, usageAccount.RecentRequests)
	}
	if usageAccount.NextRetryAfter == nil || !usageAccount.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("next retry = %v, want %v", usageAccount.NextRetryAfter, nextRetryAfter)
	}
	if missingIndexAccount.Usage != nil {
		t.Fatalf("account without AuthIndex joined fallback usage: %#v", missingIndexAccount.Usage)
	}
	encoded, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"runtime-secret-id", "sk-client-secret", "upstream-secret-body", "header-secret", "cookie-secret", "Authorization", "Set-Cookie"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("public account response leaked %q: %s", secret, encoded)
		}
	}
}

func TestAccountServiceListFiltersAndPaginates(t *testing.T) {
	disabled := true
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Type: "codex", Email: "alpha@example.com", Source: "file", Path: "/auths/a.json", Disabled: disabled},
			{AuthIndex: "b", Name: "b.json", Provider: "gemini", Type: "gemini", Email: "beta@example.com", Source: "file", Path: "/auths/b.json"},
			{AuthIndex: "c", Name: "c.json", Provider: "codex", Type: "codex", Email: "charlie@example.com", Source: "file", Path: "/auths/c.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex"}`)},
			"b": {AuthIndex: "b", Path: "/auths/b.json", JSON: json.RawMessage(`{"type":"gemini"}`)},
			"c": {AuthIndex: "c", Path: "/auths/c.json", JSON: json.RawMessage(`{"type":"codex"}`)},
		},
	}

	response, errList := NewAccountService(host).List(context.Background(), ListQuery{
		Page:     1,
		PageSize: 1,
		Filters: AccountFilters{
			Provider: "codex",
			Search:   "example.com",
		},
	})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if response.Total != 2 || response.Pages != 2 || len(response.Accounts) != 1 {
		t.Fatalf("response = %#v", response)
	}
}

func TestAccountServiceMarksSharedSourceAndMissingFileReadOnly(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "virtual-1", Name: "source.json", Provider: "sample", Source: "file", Path: "/auths/source.json"},
			{AuthIndex: "virtual-2", Name: "source.json", Provider: "sample", Source: "file", Path: "/auths/source.json"},
			{AuthIndex: "missing", Name: "missing.json", Provider: "codex", Source: "file", Path: "/auths/missing.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"virtual-1": {AuthIndex: "virtual-1", Path: "/auths/source.json", JSON: json.RawMessage(`{"type":"codex","plan_type":"k12"}`)},
			"virtual-2": {AuthIndex: "virtual-2", Path: "/auths/source.json", JSON: json.RawMessage(`{"type":"codex","chatgpt_plan_type":"k12"}`)},
		},
		errors: map[string]error{"missing": errors.New("not found")},
	}

	response, errList := NewAccountService(host).List(context.Background(), ListQuery{Page: 1, PageSize: 20})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	for _, account := range response.Accounts {
		if account.Editable {
			t.Fatalf("account %s unexpectedly editable", account.ID)
		}
		if account.ReadOnlyReason == "" {
			t.Fatalf("account %s missing read-only reason", account.ID)
		}
		if strings.HasPrefix(account.ID, "virtual-") && account.PlanType != "k12" {
			t.Fatalf("read-only account %s plan type = %q, want k12", account.ID, account.PlanType)
		}
	}

	filtered, errFiltered := NewAccountService(host).List(context.Background(), ListQuery{
		Page:     1,
		PageSize: 20,
		Filters:  AccountFilters{Type: "k12"},
	})
	if errFiltered != nil {
		t.Fatalf("filtered List() error = %v", errFiltered)
	}
	if filtered.Total != 2 || len(filtered.Accounts) != 2 {
		t.Fatalf("read-only plan-filtered list = %#v", filtered)
	}
}

func TestAccountServiceRejectsAmbiguousAuthIndexesAndUnsafeFileNames(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "duplicate", Name: "first.json", Source: "file", Path: "/auths/first.json"},
			{AuthIndex: "duplicate", Name: "second.json", Source: "file", Path: "/auths/second.json"},
			{AuthIndex: "unsafe", Name: "../escape.json", Source: "file", Path: "/auths/escape.json"},
			{Name: "missing-index.json", Source: "file", Path: "/auths/missing-index.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{},
	}

	response, errList := NewAccountService(host).List(context.Background(), ListQuery{Page: 1, PageSize: 20})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(response.Accounts) != 4 {
		t.Fatalf("accounts len = %d, want 4", len(response.Accounts))
	}
	wantReason := map[string]string{
		"first.json":         "auth index",
		"second.json":        "auth index",
		"../escape.json":     "name is invalid",
		"missing-index.json": "stable auth index",
	}
	for _, account := range response.Accounts {
		if account.Editable {
			t.Fatalf("account %s unexpectedly editable", account.Name)
		}
		if !strings.Contains(account.ReadOnlyReason, wantReason[account.Name]) {
			t.Fatalf("account %s reason = %q", account.Name, account.ReadOnlyReason)
		}
	}
}

func TestAccountServiceResolvesFilteredReadOnlyScopeAfterPhysicalPreflight(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "missing",
			Name:      "missing.json",
			Provider:  "codex",
			Source:    "file",
			Path:      "/auths/missing.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{},
		errors:  map[string]error{"missing": errors.New("not found")},
	}
	resolved, errResolve := NewAccountService(host).ResolveTargets(context.Background(), TargetScope{
		Mode: "filtered",
		Filters: AccountFilters{
			Editability: "read_only",
		},
	})
	if errResolve != nil {
		t.Fatalf("ResolveTargets() error = %v", errResolve)
	}
	if len(resolved.Accounts) != 1 || resolved.Accounts[0].Editable || resolved.Accounts[0].ReadOnlyReason == "" {
		t.Fatalf("resolved = %#v", resolved)
	}
}
