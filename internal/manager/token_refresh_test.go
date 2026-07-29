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

type tokenRefreshVerificationHost struct {
	*fakeAuthHost
	afterSave func()
}

func (h *tokenRefreshVerificationHost) SaveAuth(ctx context.Context, name string, rawJSON json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	response, errSave := h.fakeAuthHost.SaveAuth(ctx, name, rawJSON)
	if errSave == nil && h.afterSave != nil {
		h.afterSave()
	}
	return response, errSave
}

type tokenRefreshExchangeFunc func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error)

func (f tokenRefreshExchangeFunc) Exchange(ctx context.Context, input tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
	return f(ctx, input)
}

func successfulTokenRefreshExchange() tokenRefreshExchangeFunc {
	return func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
		return tokenRefreshExchangeOutput{
			AccessToken: "new-access", IDToken: "new.id.token", TokenType: "Bearer",
			ExpiresIn: time.Hour,
		}, nil
	}
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
	if result.RefreshSource != "cpa_native" {
		t.Fatalf("RefreshSource = %q, want cpa_native", result.RefreshSource)
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

func TestAccountTokenRefreshFallsBackWhenNativeHostCapabilityIsUnsupported(t *testing.T) {
	host := tokenRefreshFixture(t)
	service := NewAccountTokenRefreshService(NewAccountService(host), host)
	service.exchanger = tokenRefreshExchangeFunc(func(_ context.Context, input tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
		if input.Provider != "codex" || input.RefreshToken != "not-returned" || input.ClientID != "" {
			t.Fatalf("exchange input = %#v", input)
		}
		return tokenRefreshExchangeOutput{
			AccessToken: "new-access", RefreshToken: "rotated-refresh", IDToken: "new.id.token",
			TokenType: "Bearer", Scope: "openid profile email", ExpiresIn: time.Hour,
		}, nil
	})
	service.now = func() time.Time { return time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC) }
	result, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if result.RefreshSource != "plugin_codex" || !result.RefreshTokenRotated || result.ExpiresAt == nil {
		t.Fatalf("Refresh() = %#v", result)
	}
	if len(host.saves) != 1 {
		t.Fatalf("fallback saves = %d, want 1", len(host.saves))
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(host.saves[0].JSON, &saved); errDecode != nil {
		t.Fatalf("decode saved Auth: %v", errDecode)
	}
	if saved["access_token"] != "new-access" || saved["refresh_token"] != "rotated-refresh" || saved["id_token"] != "new.id.token" ||
		saved["email"] != "operator@example.com" || saved["type"] != "codex" || saved["last_refresh"] != "2026-07-30T01:02:03Z" {
		t.Fatalf("saved Auth = %#v", saved)
	}
}

func TestAccountTokenRefreshFallsBackOnlyForUnsupportedNativeCapability(t *testing.T) {
	t.Run("unsupported callback", func(t *testing.T) {
		host := &tokenRefreshTestHost{fakeAuthHost: tokenRefreshFixture(t), err: errors.New("unsupported host callback host.auth.refresh")}
		service := NewAccountTokenRefreshService(NewAccountService(host), host)
		service.exchanger = successfulTokenRefreshExchange()
		result, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		if errRefresh != nil || result.RefreshSource != "plugin_codex" || host.calls != 1 || len(host.saves) != 1 {
			t.Fatalf("fallback result=%#v error=%v native_calls=%d saves=%d", result, errRefresh, host.calls, len(host.saves))
		}
	})

	t.Run("native rejection", func(t *testing.T) {
		host := &tokenRefreshTestHost{fakeAuthHost: tokenRefreshFixture(t), err: errors.New("oauth invalid_grant refresh-secret")}
		service := NewAccountTokenRefreshService(NewAccountService(host), host)
		called := false
		service.exchanger = tokenRefreshExchangeFunc(func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
			called = true
			return tokenRefreshExchangeOutput{}, nil
		})
		_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		if !errors.Is(errRefresh, ErrAccountTokenRefreshRejected) || called || len(host.saves) != 0 {
			t.Fatalf("native rejection error=%v fallback_called=%t saves=%d", errRefresh, called, len(host.saves))
		}
	})
}

func TestAccountTokenRefreshFallbackPreservesLatestUnrelatedFieldsAndOldRefreshToken(t *testing.T) {
	host := tokenRefreshFixture(t)
	host.details["auth-1"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "auth-1", Name: "operator.json", Path: "/auths/operator.json",
		JSON: json.RawMessage(`{"type":"codex","email":"operator@example.com","refresh_token":"old-refresh","access_token":"old-access","disabled":false,"priority":2,"websockets":true,"cpa_account_config_manager":{"model_policy":{"mode":"allow_only"}}}`),
	}
	service := NewAccountTokenRefreshService(NewAccountService(host), host)
	service.exchanger = tokenRefreshExchangeFunc(func(_ context.Context, _ tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
		host.mu.Lock()
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"codex","email":"operator@example.com","refresh_token":"old-refresh","access_token":"old-access","disabled":true,"priority":9,"websockets":false,"note":"changed while refreshing","cpa_account_config_manager":{"model_policy":{"mode":"deny_only"}}}`)
		host.details["auth-1"] = detail
		host.mu.Unlock()
		return tokenRefreshExchangeOutput{AccessToken: "new-access", IDToken: "new.id.token", ExpiresIn: time.Hour}, nil
	})
	result, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if result.RefreshTokenRotated {
		t.Fatal("RefreshTokenRotated = true when provider omitted a replacement")
	}
	var saved map[string]any
	if errDecode := json.Unmarshal(host.saves[0].JSON, &saved); errDecode != nil {
		t.Fatalf("decode saved Auth: %v", errDecode)
	}
	policy := saved["cpa_account_config_manager"].(map[string]any)["model_policy"].(map[string]any)
	if saved["refresh_token"] != "old-refresh" || saved["disabled"] != true || saved["priority"] != float64(9) ||
		saved["websockets"] != false || saved["note"] != "changed while refreshing" || policy["mode"] != "deny_only" {
		t.Fatalf("latest unrelated fields were not preserved: %#v", saved)
	}
}

func TestAccountTokenRefreshFallbackRejectsMissingUnsupportedAndChangedCredentials(t *testing.T) {
	t.Run("missing refresh token", func(t *testing.T) {
		host := tokenRefreshFixture(t)
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"codex","access_token":"access-only"}`)
		host.details["auth-1"] = detail
		service := NewAccountTokenRefreshService(NewAccountService(host), host)
		called := false
		service.exchanger = tokenRefreshExchangeFunc(func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
			called = true
			return tokenRefreshExchangeOutput{}, nil
		})
		_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		if !errors.Is(errRefresh, ErrAccountTokenRefreshCredentialMissing) || called || len(host.saves) != 0 {
			t.Fatalf("missing refresh result error=%v called=%t saves=%d", errRefresh, called, len(host.saves))
		}
	})

	t.Run("unsupported provider", func(t *testing.T) {
		host := tokenRefreshFixture(t)
		host.entries[0].Provider = "gemini"
		host.entries[0].Type = "gemini"
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"gemini","refresh_token":"gemini-refresh"}`)
		host.details["auth-1"] = detail
		service := NewAccountTokenRefreshService(NewAccountService(host), host)
		_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		if !errors.Is(errRefresh, ErrAccountTokenRefreshProviderUnsupported) || len(host.saves) != 0 {
			t.Fatalf("unsupported provider error=%v saves=%d", errRefresh, len(host.saves))
		}
	})

	t.Run("agent identity auth mode", func(t *testing.T) {
		host := tokenRefreshFixture(t)
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"codex-agent-identity","auth_mode":"agent_identity","refresh_token":"identity-refresh"}`)
		host.details["auth-1"] = detail
		service := NewAccountTokenRefreshService(NewAccountService(host), host)
		_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		if !errors.Is(errRefresh, ErrAccountTokenRefreshProviderUnsupported) || len(host.saves) != 0 {
			t.Fatalf("agent identity error=%v saves=%d", errRefresh, len(host.saves))
		}
	})

	t.Run("credential conflict", func(t *testing.T) {
		host := tokenRefreshFixture(t)
		service := NewAccountTokenRefreshService(NewAccountService(host), host)
		service.exchanger = tokenRefreshExchangeFunc(func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
			host.mu.Lock()
			detail := host.details["auth-1"]
			detail.JSON = json.RawMessage(`{"type":"codex","email":"operator@example.com","refresh_token":"newer-refresh","access_token":"newer-access"}`)
			host.details["auth-1"] = detail
			host.mu.Unlock()
			return tokenRefreshExchangeOutput{AccessToken: "stale-access", RefreshToken: "stale-refresh", ExpiresIn: time.Hour}, nil
		})
		_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
		if !errors.Is(errRefresh, ErrAccountTokenRefreshConflict) || len(host.saves) != 0 {
			t.Fatalf("conflict error=%v saves=%d", errRefresh, len(host.saves))
		}
	})
}

func TestAccountTokenRefreshFallbackDoesNotExposeProviderErrorsOrClaimFailedSaves(t *testing.T) {
	host := tokenRefreshFixture(t)
	service := NewAccountTokenRefreshService(NewAccountService(host), host)
	service.exchanger = tokenRefreshExchangeFunc(func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
		return tokenRefreshExchangeOutput{}, errors.New("invalid_grant included refresh-secret")
	})
	_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if !errors.Is(errRefresh, ErrAccountTokenRefreshFailed) || strings.Contains(errRefresh.Error(), "refresh-secret") {
		t.Fatalf("provider error was not sanitized: %v", errRefresh)
	}

	host = tokenRefreshFixture(t)
	host.saveErrors = map[string]error{"operator.json": errors.New("disk error with access-secret")}
	service = NewAccountTokenRefreshService(NewAccountService(host), host)
	service.exchanger = successfulTokenRefreshExchange()
	_, errRefresh = service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if !errors.Is(errRefresh, ErrAccountTokenRefreshFailed) || strings.Contains(errRefresh.Error(), "access-secret") {
		t.Fatalf("save error was not sanitized: %v", errRefresh)
	}
}

func TestAccountTokenRefreshFallbackVerifiesPersistedCredentialState(t *testing.T) {
	base := tokenRefreshFixture(t)
	host := &tokenRefreshVerificationHost{fakeAuthHost: base}
	host.afterSave = func() {
		host.mu.Lock()
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"codex","refresh_token":"concurrent-refresh","access_token":"concurrent-access"}`)
		host.details["auth-1"] = detail
		host.mu.Unlock()
	}
	service := NewAccountTokenRefreshService(NewAccountService(host), host)
	service.exchanger = successfulTokenRefreshExchange()
	_, errRefresh := service.Refresh(t.Context(), AccountTokenRefreshRequest{AccountID: "auth-1"})
	if !errors.Is(errRefresh, ErrAccountTokenRefreshVerification) || len(host.saves) != 1 {
		t.Fatalf("verification mismatch error=%v saves=%d", errRefresh, len(host.saves))
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
	if entry.Action != OperationActionTokenRefresh || entry.Status != OperationStatusSucceeded || entry.TargetID != "auth-1" || entry.ReasonCode != "token_refreshed_native" {
		t.Fatalf("operation = %#v", entry)
	}
}

func TestAccountTokenRefreshManagementRouteReportsFallbackConflictWithoutSecrets(t *testing.T) {
	host := tokenRefreshFixture(t)
	app := NewApp(host, nil)
	defer app.Close()
	app.Configure([]byte("data_dir: " + t.TempDir() + "\n"))
	app.tokenRefresh.exchanger = tokenRefreshExchangeFunc(func(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
		host.mu.Lock()
		detail := host.details["auth-1"]
		detail.JSON = json.RawMessage(`{"type":"codex","email":"operator@example.com","refresh_token":"rotated-elsewhere","access_token":"concurrent-access-secret"}`)
		host.details["auth-1"] = detail
		host.mu.Unlock()
		return tokenRefreshExchangeOutput{AccessToken: "stale-access-secret", RefreshToken: "stale-refresh-secret", ExpiresIn: time.Hour}, nil
	})
	response := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/token/refresh",
		Body:   []byte(`{"account_id":"auth-1"}`),
	})
	if response.StatusCode != http.StatusConflict || !bytes.Contains(response.Body, []byte(ErrAccountTokenRefreshConflict.Error())) {
		t.Fatalf("conflict status=%d body=%s", response.StatusCode, response.Body)
	}
	for _, secret := range []string{"rotated-elsewhere", "concurrent-access-secret", "stale-access-secret", "stale-refresh-secret", "not-returned"} {
		if bytes.Contains(response.Body, []byte(secret)) {
			t.Fatalf("management response exposed secret %q: %s", secret, response.Body)
		}
	}
	listed := app.operations.List(OperationQuery{Page: 1})
	if len(listed.Operations) != 1 || listed.Operations[0].ReasonCode != "refresh_conflict" || listed.Operations[0].Status != OperationStatusFailed {
		t.Fatalf("conflict operation = %#v", listed.Operations)
	}
}
