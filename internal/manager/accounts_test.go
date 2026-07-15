package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

type fakeAuthHost struct {
	entries []cpaapi.HostAuthFileEntry
	details map[string]cpaapi.HostAuthGetResponse
	errors  map[string]error
}

func (f *fakeAuthHost) ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	return append([]cpaapi.HostAuthFileEntry(nil), f.entries...), nil
}

func (f *fakeAuthHost) GetAuth(_ context.Context, authIndex string) (cpaapi.HostAuthGetResponse, error) {
	if err := f.errors[authIndex]; err != nil {
		return cpaapi.HostAuthGetResponse{}, err
	}
	return f.details[authIndex], nil
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
		details: map[string]cpaapi.HostAuthGetResponse{},
		errors:  map[string]error{"missing": errors.New("not found")},
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
