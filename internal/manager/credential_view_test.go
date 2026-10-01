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

func newRuntimeCredentialHost(runtimeErr error) *runtimeCredentialHost {
	return &runtimeCredentialHost{
		fakeAuthHost: &fakeAuthHost{
			entries: []cpaapi.HostAuthFileEntry{{
				AuthIndex: "auth-index", ID: "cpa-id", Name: "account.json",
				Provider: "codex", Type: "codex", Email: "user@example.com", Source: "file", Path: "/auth/account.json",
			}},
			details: map[string]cpaapi.HostAuthGetResponse{"auth-index": {
				AuthIndex: "auth-index", Name: "account.json", Path: "/auth/account.json",
				JSON: json.RawMessage(`{"type":"codex","access_token":"access-secret","refresh_token":"refresh-secret"}`),
			}},
		},
		runtimeErr: runtimeErr,
		runtime: map[string]cpaapi.HostAuthFileEntry{"auth-index": {
			ID: "runtime-id", AuthIndex: "auth-index", Name: "account.json", Provider: "codex", Type: "codex",
			Account: "upstream-account-id", AccountType: "oauth", PlanType: "k12", Status: "active",
			Source: "file", Path: "/auth/account.json",
		}},
	}
}

// The credential view of the editable-config route takes its upstream identity
// from the runtime callback, and the serialized account never carries
// credential material.
func TestEditableConfigCredentialUsesRuntimeIdentityAndRedactsSecrets(t *testing.T) {
	host := newRuntimeCredentialHost(nil)
	config, errConfig := NewAccountService(host).EditableConfig(context.Background(), "auth-index")
	if errConfig != nil {
		t.Fatalf("EditableConfig() error = %v", errConfig)
	}
	if config.Credential == nil || !config.Credential.RuntimeLoaded ||
		config.Credential.AccountID != "upstream-account-id" || config.Credential.PlanType != "k12" {
		t.Fatalf("credential = %#v", config.Credential)
	}
	if config.Credential.AccountID == config.Credential.AuthID || config.Credential.AccountID == config.Credential.ID {
		t.Fatalf("credential identity conflated: %#v", config.Credential)
	}
	encoded, errEncode := json.Marshal(config)
	if errEncode != nil {
		t.Fatalf("encode config: %v", errEncode)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "access_token", "refresh_token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the credential view leaked %q: %s", secret, encoded)
		}
	}
	if host.runtimeCalls != 1 {
		t.Fatalf("runtime callback calls = %d, want 1", host.runtimeCalls)
	}
}

// An account-list request must not fan out into one runtime callback per
// account, and a host error must not echo credential-like text back.
func TestAccountListDoesNotCallTheRuntimeCredentialCallback(t *testing.T) {
	host := newRuntimeCredentialHost(errors.New("upstream token secret must not be echoed"))
	list, errList := NewAccountService(host).List(context.Background(), ListQuery{Page: 1, PageSize: 20})
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if host.runtimeCalls != 0 {
		t.Fatalf("List() runtime callback calls = %d, want 0", host.runtimeCalls)
	}
	if list.Accounts[0].Credential == nil || list.Accounts[0].Credential.AccountID != "" {
		t.Fatalf("list credential = %#v", list.Accounts[0].Credential)
	}

	config, errConfig := NewAccountService(host).EditableConfig(context.Background(), "auth-index")
	if errConfig != nil {
		t.Fatalf("EditableConfig() error = %v", errConfig)
	}
	if config.Credential == nil || config.Credential.RuntimeError != "runtime credential details unavailable" {
		t.Fatalf("runtime error = %#v", config.Credential)
	}
}
