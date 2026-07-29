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

type tokenRefreshTestHost struct {
	*fakeAuthHost
	mu       sync.Mutex
	response cpaapi.HostAuthRefreshResponse
	err      error
	started  chan struct{}
	release  chan struct{}
	calls    int
}

func (h *tokenRefreshTestHost) RefreshHostAuth(ctx context.Context, authIndex string) (cpaapi.HostAuthRefreshResponse, error) {
	h.mu.Lock()
	h.calls++
	started := h.started
	release := h.release
	response := h.response
	errRefresh := h.err
	h.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-ctx.Done():
			return cpaapi.HostAuthRefreshResponse{}, ctx.Err()
		case <-release:
		}
	}
	if response.AuthIndex == "" {
		response.AuthIndex = authIndex
	}
	return response, errRefresh
}

func tokenRefreshFixture(t *testing.T) *fakeAuthHost {
	t.Helper()
	raw := json.RawMessage(`{"type":"codex","email":"operator@example.com","refresh_token":"not-returned"}`)
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "operator.json", Provider: "codex", Type: "codex",
			Email: "operator@example.com", Source: "file", Path: "/auths/operator.json",
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {AuthIndex: "auth-1", Name: "operator.json", Path: "/auths/operator.json", JSON: raw},
		},
	}
}

func TestAccountTokenRefreshUsesNativeHostCapabilityAndReturnsOnlyRedactedMetadata(t *testing.T) {
	refreshedAt := time.Date(2026, 7, 30, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	expiresAt := refreshedAt.Add(10 * 24 * time.Hour)
	host := &tokenRefreshTestHost{
		fakeAuthHost: tokenRefreshFixture(t),
		response: cpaapi.HostAuthRefreshResponse{
			Provider: "codex", RefreshedAt: refreshedAt, ExpiresAt: &expiresAt, RefreshTokenRotated: true,
		},
	}
	result, errRefresh := NewAccountTokenRefreshService(NewAccountService(host), host).Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if result.AccountID != "auth-1" || result.Provider != "codex" || !result.RefreshTokenRotated {
		t.Fatalf("Refresh() = %#v", result)
	}
	if !result.RefreshedAt.Equal(refreshedAt) || result.ExpiresAt == nil || !result.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("Refresh() timestamps = %#v", result)
	}
	encoded, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"not-returned", "access_token", `"refresh_token":`} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("result exposed credential material: %s", encoded)
		}
	}
}

func TestAccountTokenRefreshRejectsUnsupportedHostsWithoutCredentialFallback(t *testing.T) {
	host := tokenRefreshFixture(t)
	_, errRefresh := NewAccountTokenRefreshService(NewAccountService(host), host).Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if !errors.Is(errRefresh, ErrAccountTokenRefreshUnsupported) {
		t.Fatalf("Refresh() error = %v, want unsupported", errRefresh)
	}
	if len(host.saves) != 0 {
		t.Fatalf("unsupported refresh rewrote auth JSON: %#v", host.saves)
	}
}

func TestAccountTokenRefreshClassifiesSafeErrorsAndSerializesOneAccount(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "unsupported", err: errors.New("unsupported host callback host.auth.refresh"), want: ErrAccountTokenRefreshUnsupported},
		{name: "missing", err: errors.New("refresh token is missing"), want: ErrAccountTokenRefreshCredentialMissing},
		{name: "rejected", err: errors.New("oauth invalid_grant"), want: ErrAccountTokenRefreshRejected},
		{name: "sanitized fallback", err: errors.New("upstream included secret-value"), want: ErrAccountTokenRefreshFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyHostTokenRefreshError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("classifyHostTokenRefreshError() = %v, want %v", got, test.want)
			}
			if strings.Contains(got.Error(), "secret-value") {
				t.Fatalf("classified error exposed the host error: %v", got)
			}
		})
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	host := &tokenRefreshTestHost{
		fakeAuthHost: tokenRefreshFixture(t), started: started, release: release,
		response: cpaapi.HostAuthRefreshResponse{RefreshedAt: time.Now().UTC()},
	}
	service := NewAccountTokenRefreshService(NewAccountService(host), host)
	done := make(chan error, 1)
	go func() {
		_, errRefresh := service.Refresh(context.Background(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		done <- errRefresh
	}()
	<-started
	if _, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"}); !errors.Is(errRefresh, ErrAccountTokenRefreshBusy) {
		t.Fatalf("concurrent Refresh() error = %v, want busy", errRefresh)
	}
	close(release)
	if errRefresh := <-done; errRefresh != nil {
		t.Fatalf("first Refresh() error = %v", errRefresh)
	}
}

func TestAccountTokenRefreshManagementRouteRecordsSanitizedOutcome(t *testing.T) {
	host := &tokenRefreshTestHost{
		fakeAuthHost: tokenRefreshFixture(t),
		response:     cpaapi.HostAuthRefreshResponse{Provider: "codex", RefreshedAt: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)},
	}
	app := NewApp(host, nil)
	defer app.Close()
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	response := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/token/refresh",
		Body:   []byte(`{"account_id":"auth-1"}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", response.StatusCode, response.Body)
	}
	if bytes.Contains(response.Body, []byte("not-returned")) || bytes.Contains(response.Body, []byte(`"refresh_token":`)) {
		t.Fatalf("management response exposed a credential: %s", response.Body)
	}
	listed := app.operations.List(OperationQuery{Page: 1})
	if len(listed.Operations) != 1 {
		t.Fatalf("operations = %#v", listed.Operations)
	}
	entry := listed.Operations[0]
	if entry.Action != OperationActionTokenRefresh || entry.Status != OperationStatusSucceeded || entry.TargetID != "auth-1" {
		t.Fatalf("operation = %#v", entry)
	}
}
