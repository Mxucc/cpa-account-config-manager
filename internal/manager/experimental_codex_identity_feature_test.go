package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

type codexGateAccountProviderFunc struct {
	list                func(context.Context, ListQuery) (ListResponse, error)
	currentAuthDocument func(context.Context, Account) (currentAuthDocument, error)
}

type aiProviderCodexIdentityHost struct {
	detail cpaapi.HostAuthGetResponse
}

func (h aiProviderCodexIdentityHost) CurrentAuthDocument(_ context.Context, _ Account) (currentAuthDocument, error) {
	return currentAuthDocument{Metadata: map[string]any{"codex_fingerprint_seed": "33333333-3333-4333-8333-333333333333"}}, nil
}

func (h aiProviderCodexIdentityHost) List(context.Context, ListQuery) (ListResponse, error) {
	return ListResponse{Accounts: []Account{{ID: "provider-auth", Provider: "codex-api-key", Type: "codex"}}}, nil
}

func (h aiProviderCodexIdentityHost) GetAuth(context.Context, string) (cpaapi.HostAuthGetResponse, error) {
	return h.detail, nil
}

func (h aiProviderCodexIdentityHost) SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	return cpaapi.HostAuthSaveResponse{}, errors.New("AI-provider credentials must not be rewritten")
}

func (h aiProviderCodexIdentityHost) ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	return []cpaapi.HostAuthFileEntry{{
		AuthIndex: "provider-auth", ID: "credential-auth", Name: "codex-api.json",
		Provider: "codex-api-key", Type: "codex", Path: "/auths/codex-api.json",
	}}, nil
}

func TestAIProviderCodexFingerprintConvergesProbeHeaders(t *testing.T) {
	settings := ExperimentalCodexIdentitySettings{
		OutboundConvergenceEnabled: true,
		ConvergenceMode:            "full",
	}
	experiment := NewCodexIdentityExperiment(stubCodexSettings{settings}, NewAccountService(aiProviderCodexIdentityHost{
		detail: cpaapi.HostAuthGetResponse{
			AuthIndex: "provider-auth",
			Name:      "codex-api.json",
			Path:      "/auths/codex-api.json",
			JSON:      json.RawMessage(`{"type":"codex","account_type":"oauth","codex_fingerprint_seed":"33333333-3333-4333-8333-333333333333"}`),
		},
	}))
	app := &App{requestHooks: NewRequestHook(experiment)}
	headers := http.Header{}
	app.applyAIProviderCodexFingerprint(headers, "Codex-API-Key", "provider-auth")
	if headers.Get("Session-Id") == "" || headers.Get("Thread-Id") == "" ||
		headers.Get("X-Codex-Installation-Id") != "d1ca6921-e7c7-4bb4-aae4-9ea7f2d15163" ||
		!strings.Contains(headers.Get(codexFingerprintHeader), `"installation_id":"d1ca6921-e7c7-4bb4-aae4-9ea7f2d15163"`) {
		t.Fatalf("AI provider Codex probe was not converged: %#v", headers)
	}
}

func (p codexGateAccountProviderFunc) List(ctx context.Context, query ListQuery) (ListResponse, error) {
	return p.list(ctx, query)
}

func (p codexGateAccountProviderFunc) CurrentAuthDocument(ctx context.Context, account Account) (currentAuthDocument, error) {
	return p.currentAuthDocument(ctx, account)
}

func TestCodexIdentityRestrictedAccountsRequireIngressScan(t *testing.T) {
	restricted := currentAuthDocument{Metadata: map[string]any{"codex_cli_only": true}}
	tests := []struct {
		name string
		list func(context.Context, ListQuery) (ListResponse, error)
		read func(context.Context, Account) (currentAuthDocument, error)
		want bool
	}{
		{name: "restricted account", want: true, list: func(context.Context, ListQuery) (ListResponse, error) {
			return ListResponse{Accounts: []Account{{ID: "restricted"}}}, nil
		}, read: func(context.Context, Account) (currentAuthDocument, error) { return restricted, nil }},
		{name: "unrestricted accounts", list: func(context.Context, ListQuery) (ListResponse, error) {
			return ListResponse{Accounts: []Account{{ID: "one"}, {ID: "two"}}}, nil
		}, read: func(context.Context, Account) (currentAuthDocument, error) {
			return currentAuthDocument{}, nil
		}},
		{name: "unreadable account fails closed", want: true, list: func(context.Context, ListQuery) (ListResponse, error) {
			return ListResponse{Accounts: []Account{{ID: "one"}}}, nil
		}, read: func(context.Context, Account) (currentAuthDocument, error) {
			return currentAuthDocument{}, errors.New("read failed")
		}},
		{name: "list failure fails closed", want: true, list: func(context.Context, ListQuery) (ListResponse, error) {
			return ListResponse{}, errors.New("list failed")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			experiment := NewCodexIdentityExperiment(codexPolicyProvider(nil), codexGateAccountProviderFunc{list: test.list, currentAuthDocument: test.read})
			if got := experiment.accountRequiresIngressGate(context.Background()); got != test.want {
				t.Fatalf("accountRequiresIngressGate() = %t, want %t", got, test.want)
			}
		})
	}
}
