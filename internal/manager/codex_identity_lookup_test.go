package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

type identityLookupHost struct {
	*fakeAuthHost
	runtimeCalls  int
	runtimeError  error
	cancelRuntime context.CancelFunc
}

func (h *identityLookupHost) GetAuthRuntime(ctx context.Context, index string) (cpaapi.HostAuthFileEntry, error) {
	h.runtimeCalls++
	if h.cancelRuntime != nil {
		h.cancelRuntime()
	}
	if err := ctx.Err(); err != nil {
		return cpaapi.HostAuthFileEntry{}, err
	}
	if h.runtimeError != nil {
		return cpaapi.HostAuthFileEntry{}, h.runtimeError
	}
	for _, entry := range h.entries {
		if entry.AuthIndex == index {
			return entry, nil
		}
	}
	return cpaapi.HostAuthFileEntry{}, fmt.Errorf("unknown fixture index")
}

func newIdentityLookupHost(size int) *identityLookupHost {
	host := &identityLookupHost{fakeAuthHost: &fakeAuthHost{details: make(map[string]cpaapi.HostAuthGetResponse)}}
	for i := 0; i < size; i++ {
		index := fmt.Sprintf("auth-%04d", i)
		name := fmt.Sprintf("account-%04d.json", i)
		host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
			AuthIndex: index, ID: name, Name: name, Provider: "codex", Type: "codex",
			AccountType: "oauth", Source: "file", Path: "/auths/" + name,
		})
		host.details[index] = cpaapi.HostAuthGetResponse{
			AuthIndex: index, Name: name, Path: "/auths/" + name,
			JSON: json.RawMessage(`{"type":"codex","codex_cli_only":true,"codex_cli_only_allow_app_server":true,"device_id":"fixture-device"}`),
		}
	}
	return host
}

func TestCodexIdentityGateReadsOnlySelectedAccount(t *testing.T) {
	for _, size := range []int{1, 2000} {
		t.Run(fmt.Sprintf("accounts_%d", size), func(t *testing.T) {
			host := newIdentityLookupHost(size)
			experiment := NewCodexIdentityExperiment(nil, NewAccountService(host))
			index := fmt.Sprintf("auth-%04d", size-1)
			gate := experiment.accountGate(context.Background(), index)
			if gate.account == nil || gate.account.ID != index || !gate.codexCLIOnly || !gate.codexCLIOnlyAppServer {
				t.Errorf("selected account and restrictions were not resolved")
			}
			if gate.account != nil && gate.account.DeviceID != "fixture-device" {
				t.Errorf("device identity was not preserved")
			}
			totalGets := 0
			for _, calls := range host.getCalls {
				totalGets += calls
			}
			t.Logf("runtime=%d lists=%d physical reads=%d", host.runtimeCalls, host.listCalls, totalGets)
			if host.listCalls != 0 || host.runtimeCalls != 1 || totalGets != 1 || host.getCalls[index] != 1 {
				t.Errorf("identity gate must read only the selected runtime record and document")
			}
		})
	}
}

func TestCodexIdentityGateLegacyHostReadsOnlySelectedDocument(t *testing.T) {
	host := newIdentityLookupHost(2000).fakeAuthHost
	experiment := NewCodexIdentityExperiment(nil, NewAccountService(host))
	gate := experiment.accountGate(context.Background(), "account-1999.json")
	if gate.account == nil || gate.account.ID != "auth-1999" || !gate.codexCLIOnly {
		t.Errorf("credential ID beyond the first page did not resolve")
	}
	if host.listCalls != 1 || len(host.getCalls) != 1 || host.getCalls["auth-1999"] != 1 {
		t.Errorf("legacy lookup listed %d times and read %d distinct documents", host.listCalls, len(host.getCalls))
	}
}

func TestIdentityAccountRuntimeFallbackAndAliases(t *testing.T) {
	for _, tc := range []struct {
		name, identity string
		runtimeError   error
	}{
		{name: "credential ID", identity: "account-0001.json"},
		{name: "older host callback", identity: "auth-0001", runtimeError: errors.New("unknown host method")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newIdentityLookupHost(2)
			host.runtimeError = tc.runtimeError
			account, document, err := NewAccountService(host).identityAccount(context.Background(), tc.identity)
			if err != nil || account.ID != "auth-0001" || account.AuthID != "account-0001.json" || !codexExtraBool(document.Metadata["codex_cli_only"]) {
				t.Fatalf("fallback did not preserve selected account identity and policy: %v", err)
			}
			if host.listCalls != 1 || len(host.getCalls) != 1 || host.getCalls[account.ID] != 1 {
				t.Fatal("fallback fetched unrelated documents")
			}
		})
	}
}

func TestIdentityAccountCancellationStopsHostWork(t *testing.T) {
	for _, duringRuntime := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_runtime_%t", duringRuntime), func(t *testing.T) {
			host := newIdentityLookupHost(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantRuntimeCalls := 0
			if duringRuntime {
				host.cancelRuntime = cancel
				wantRuntimeCalls = 1
			} else {
				cancel()
			}
			_, _, err := NewAccountService(host).identityAccount(ctx, "auth-0000")
			if !errors.Is(err, context.Canceled) || host.runtimeCalls != wantRuntimeCalls || host.listCalls != 0 || len(host.getCalls) != 0 {
				t.Fatalf("canceled lookup continued host work: error=%v runtime=%d list=%d gets=%d", err, host.runtimeCalls, host.listCalls, len(host.getCalls))
			}
		})
	}
}

func TestIdentityAccountRejectsInvalidSources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*identityLookupHost)
	}{
		{name: "missing account", mutate: func(h *identityLookupHost) { h.entries = nil }},
		{name: "plugin state", mutate: func(h *identityLookupHost) { h.entries[0].Path = "/auths/.cpa-account-config-manager/state.json" }},
		{name: "changed file", mutate: func(h *identityLookupHost) {
			detail := h.details["auth-0000"]
			detail.Path = "/auths/replaced.json"
			h.details["auth-0000"] = detail
		}},
		{name: "invalid document", mutate: func(h *identityLookupHost) {
			detail := h.details["auth-0000"]
			detail.JSON = json.RawMessage(`invalid`)
			h.details["auth-0000"] = detail
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newIdentityLookupHost(1)
			tc.mutate(host)
			if _, _, err := NewAccountService(host).identityAccount(context.Background(), "auth-0000"); err == nil {
				t.Fatal("invalid source was accepted")
			}
		})
	}
}

// The provider fallback used to read one capped page, so a selected credential
// past the first page resolved to no gate at all.
func TestCodexIdentityGateFallbackResolvesAccountPastTheFirstPage(t *testing.T) {
	const size = maxPageSize + 200
	fixture := newIdentityLookupHost(size)
	lastIndex := fmt.Sprintf("auth-%04d", size-1)
	listCalls := 0
	provider := codexGateAccountProviderFunc{
		list: func(_ context.Context, query ListQuery) (ListResponse, error) {
			listCalls++
			page, pageSize := normalizePage(query.Page, query.PageSize)
			start := (page - 1) * pageSize
			if start > len(fixture.entries) {
				start = len(fixture.entries)
			}
			end := start + pageSize
			if end > len(fixture.entries) {
				end = len(fixture.entries)
			}
			accounts := make([]Account, 0, end-start)
			for _, entry := range fixture.entries[start:end] {
				accounts = append(accounts, projectHostEntry(entry, nil, nil, nil))
			}
			return ListResponse{
				Accounts: accounts, Total: len(fixture.entries),
				Page: page, PageSize: pageSize, Pages: (len(fixture.entries)-1)/pageSize + 1,
			}, nil
		},
		currentAuthDocument: func(context.Context, Account) (currentAuthDocument, error) {
			return currentAuthDocument{Metadata: map[string]any{"codex_cli_only": true}}, nil
		},
	}
	gate := NewCodexIdentityExperiment(nil, provider).accountGate(context.Background(), lastIndex)
	if gate.account == nil || gate.account.ID != lastIndex || !gate.codexCLIOnly {
		t.Fatalf("the credential past the first page did not resolve: %#v", gate.account)
	}
	if listCalls < 2 {
		t.Fatalf("the fallback read %d page(s), want a walk past the first page", listCalls)
	}
}

// One intercepted request must not read the same credential more than once: the
// gate already read the document, and identity resolution reuses it instead of
// reading the credential again for the identity namespace and the seed.
func TestCodexIdentityRequestReadsTheCredentialOnce(t *testing.T) {
	for _, test := range []struct {
		name string
		host func() (AuthHost, *fakeAuthHost)
	}{
		{name: "runtime host", host: func() (AuthHost, *fakeAuthHost) {
			host := newIdentityLookupHost(1)
			return host, host.fakeAuthHost
		}},
		{name: "legacy host", host: func() (AuthHost, *fakeAuthHost) {
			host := newIdentityLookupHost(1).fakeAuthHost
			return host, host
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			host, fake := test.host()
			experiment := NewCodexIdentityExperiment(
				stubCodexSettings{settings: ExperimentalCodexIdentitySettings{OutboundConvergenceEnabled: true}},
				NewAccountService(host),
			)
			request := cpaapi.RequestInterceptRequest{RequestID: "r1", ToFormat: "codex", Metadata: map[string]any{"auth_index": "auth-0000"}}
			// The first request may create the persisted per-account seed.
			if _, changed := experiment.InterceptRequest(request); !changed {
				t.Fatal("the experiment did not rewrite the request")
			}
			fake.mu.Lock()
			warm := fake.getCalls["auth-0000"]
			saves := len(fake.saves)
			fake.mu.Unlock()
			if saves != 1 {
				t.Fatalf("seed writes = %d, want 1", saves)
			}
			if _, changed := experiment.InterceptRequest(request); !changed {
				t.Fatal("the experiment did not rewrite the second request")
			}
			fake.mu.Lock()
			after := fake.getCalls["auth-0000"]
			fake.mu.Unlock()
			if delta := after - warm; delta != 1 {
				t.Fatalf("one intercepted request read the credential %d times, want 1", delta)
			}
		})
	}
}

func BenchmarkCodexIdentityGate(b *testing.B) {
	for _, size := range []int{1, 100, 2000} {
		b.Run(fmt.Sprintf("accounts_%d", size), func(b *testing.B) {
			host := newIdentityLookupHost(size)
			experiment := NewCodexIdentityExperiment(nil, NewAccountService(host))
			index := fmt.Sprintf("auth-%04d", size-1)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if experiment.accountGate(context.Background(), index).account == nil {
					b.Fatal("selected account is missing")
				}
			}
		})
	}
}
