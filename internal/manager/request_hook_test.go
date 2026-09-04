package manager

import (
	"net/http"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestRequestHookWaitsForAccountBeforeObservingProvider(t *testing.T) {
	concurrency := configuredConcurrencyService(t, cpaapi.SchemaVersion)
	concurrency.maxWait = 2 * time.Second
	if errSet := concurrency.SetLimit(Account{ID: "index-a", AuthID: "auth-a"}, 1); errSet != nil {
		t.Fatalf("SetLimit() error = %v", errSet)
	}

	policies := NewQuotaPolicyService()
	policies.Configure(Config{DataDir: t.TempDir()})
	providerLimit, providerWindowLimit := 1, 1
	if errSet := policies.SetProviderPolicy(ProviderQuotaPolicy{
		Key:            "openai:auth-a",
		Concurrency:    &providerLimit,
		Concurrency15s: &providerWindowLimit,
	}); errSet != nil {
		t.Fatalf("SetProviderPolicy() error = %v", errSet)
	}

	tracker := NewProviderRuntimeTracker(nil)
	tracker.SetQuotaPolicies(policies)
	hook := NewRequestHook(concurrency, tracker)
	request := func(id string) cpaapi.RequestInterceptRequest {
		return cpaapi.RequestInterceptRequest{
			RequestID: id,
			ToFormat:  "codex",
			Metadata: map[string]any{
				"selected_auth_id":    "auth-a",
				"selected_auth_index": "auth-a",
				"provider":            "openai",
			},
		}
	}

	if response := hook.InterceptAfter(request("request-1")); response.Terminate || response.StatusCode != 0 {
		t.Fatalf("first request was rejected: %#v", response)
	}
	if got := tracker.Snapshot(); len(got) != 1 || got[0].Active != 1 || got[0].Used15s != 1 {
		t.Fatalf("first request was not observed after admission: %+v", got)
	}

	result := make(chan cpaapi.RequestInterceptResponse, 1)
	go func() {
		result <- hook.InterceptAfter(request("request-2"))
	}()

	deadline := time.Now().Add(time.Second)
	for concurrency.Summary("auth-a").Waiting != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("second request was not queued: %#v", concurrency.Summary("auth-a"))
		}
		time.Sleep(time.Millisecond)
	}
	if got := tracker.Snapshot(); len(got) != 1 || got[0].Active != 1 || got[0].Used15s != 1 {
		t.Fatalf("queued request polluted provider metrics: %+v", got)
	}

	concurrency.Complete(cpaapi.RequestCompletion{RequestID: "request-1"})
	tracker.Complete(cpaapi.RequestCompletion{RequestID: "request-1"})
	select {
	case response := <-result:
		if response.Terminate || response.StatusCode != 0 {
			t.Fatalf("waited request was rejected: %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("queued request was not admitted after completion")
	}

	if got := tracker.Snapshot(); len(got) != 1 || got[0].Active != 1 || got[0].Used15s != 2 || got[0].Used60s != 2 {
		t.Fatalf("admitted request was not observed exactly once: %+v", got)
	}
	concurrency.Complete(cpaapi.RequestCompletion{RequestID: "request-2"})
	tracker.Complete(cpaapi.RequestCompletion{RequestID: "request-2"})
}

func TestRequestHookPreservesExplicitTransformer429(t *testing.T) {
	transformer := requestTransformerFunc(func(cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
		return cpaapi.RequestInterceptResponse{Terminate: true, StatusCode: http.StatusTooManyRequests}, true
	})
	hook := NewRequestHook(transformer)
	response := hook.InterceptAfter(cpaapi.RequestInterceptRequest{RequestID: "request-429"})
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("explicit transformer 429 was not preserved: %#v", response)
	}
}

type requestTransformerFunc func(cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool)

func (f requestTransformerFunc) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	return f(request)
}
