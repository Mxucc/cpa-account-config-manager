package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

type runtimeCredentialHost struct {
	*fakeAuthHost
	runtime      map[string]cpaapi.HostAuthFileEntry
	runtimeErr   error
	runtimeCalls int
}

func (h *runtimeCredentialHost) GetAuthRuntime(_ context.Context, authIndex string) (cpaapi.HostAuthFileEntry, error) {
	h.runtimeCalls++
	if h.runtimeErr != nil {
		return cpaapi.HostAuthFileEntry{}, h.runtimeErr
	}
	entry, ok := h.runtime[authIndex]
	if !ok {
		return cpaapi.HostAuthFileEntry{}, errors.New("runtime credential not found")
	}
	return entry, nil
}

func TestCredentialSummaryRedactsSecretsAndUsesRuntimeIdentity(t *testing.T) {
	host := &runtimeCredentialHost{
		fakeAuthHost: &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{AuthIndex: "auth-index", ID: "cpa-id", Name: "account.json", Provider: "codex", Type: "codex", Email: "user@example.com", Source: "file", Path: "/auth/account.json"}}, details: map[string]cpaapi.HostAuthGetResponse{"auth-index": {AuthIndex: "auth-index", Path: "/auth/account.json", JSON: json.RawMessage(`{"type":"codex","access_token":"access-secret","refresh_token":"refresh-secret"}`)}}},
		runtime:      map[string]cpaapi.HostAuthFileEntry{"auth-index": {ID: "runtime-id", AuthIndex: "auth-index", Name: "account.json", Provider: "codex", Type: "codex", Account: "upstream-account-id", AccountType: "oauth", PlanType: "k12", Status: "active", Source: "file", Path: "/auth/account.json"}},
	}
	summary, err := NewAccountService(host).CredentialSummary(t.Context(), "auth-index")
	if err != nil {
		t.Fatalf("CredentialSummary() error = %v", err)
	}
	if !summary.RuntimeLoaded || summary.AccountID != "upstream-account-id" || summary.PlanType != "k12" {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.AccountID == summary.AuthID || summary.AccountID == summary.ID {
		t.Fatalf("credential identity conflated: %#v", summary)
	}
	encoded, _ := json.Marshal(summary)
	text := string(encoded)
	for _, secret := range []string{"access-secret", "refresh-secret", "access_token", "refresh_token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("summary leaked %q: %s", secret, text)
		}
	}
	if host.runtimeCalls != 1 {
		t.Fatalf("runtime callback calls = %d, want 1", host.runtimeCalls)
	}
}

func TestCredentialSummaryRuntimeFailureIsNonBlockingAndListDoesNotCallRuntime(t *testing.T) {
	host := &runtimeCredentialHost{fakeAuthHost: &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{AuthIndex: "auth-index", ID: "cpa-id", Name: "account.json", Provider: "codex", Source: "file", Path: "/auth/account.json"}}, details: map[string]cpaapi.HostAuthGetResponse{"auth-index": {AuthIndex: "auth-index", Path: "/auth/account.json", JSON: json.RawMessage(`{"type":"codex"}`)}}}, runtimeErr: errors.New("upstream token secret must not be echoed")}
	list, err := NewAccountService(host).List(t.Context(), ListQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if host.runtimeCalls != 0 {
		t.Fatalf("List() runtime callback calls = %d, want 0", host.runtimeCalls)
	}
	if list.Accounts[0].Credential == nil || list.Accounts[0].Credential.AccountID != "" {
		t.Fatalf("list credential = %#v", list.Accounts[0].Credential)
	}
	summary, err := NewAccountService(host).CredentialSummary(t.Context(), "auth-index")
	if err != nil {
		t.Fatalf("CredentialSummary() error = %v", err)
	}
	if summary.RuntimeError != "运行时凭据详情不可用" {
		t.Fatalf("runtime error = %q", summary.RuntimeError)
	}
}
