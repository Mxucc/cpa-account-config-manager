package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestHandleAccountConfigReturnsFreshAllowListedFieldsWithoutSecrets(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{
			AuthIndex: "auth-1", Name: "operator.json", Provider: "codex", Type: "codex",
			Source: "file", Path: "/auths/operator.json", Disabled: true, ModTime: time.Now().UTC(),
		}},
		details: map[string]cpaapi.HostAuthGetResponse{
			"auth-1": {
				AuthIndex: "auth-1", Name: "operator.json", Path: "/auths/operator.json",
				JSON: json.RawMessage(`{
					"type":"codex",
					"access_token":"account-token-secret",
					"priority":8,
					"note":"primary pool",
					"prefix":"team-a",
					"proxy_url":"http://proxy-user:proxy-password@proxy.internal:8080/private?token=proxy-query-secret",
					"websockets":false,
					"headers":{"Authorization":"Bearer header-secret","X-Team":"team-secret"},
					"cpa_account_config_manager":{"model_policy":{"schema":1,"mode":"allow_only","models":["gpt-5.5"],"managed_excluded_models":["gpt-5.6-sol"]}}
				}`),
			},
		},
	}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	body, _ := json.Marshal(AccountConfigRequest{AccountID: "auth-1"})
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/config",
		Body:   body,
	})
	if response.StatusCode != http.StatusOK || response.Headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d headers=%#v body=%s", response.StatusCode, response.Headers, response.Body)
	}
	for _, secret := range []string{"account-token-secret", "proxy-user", "proxy-password", "proxy-query-secret", "header-secret", "team-secret"} {
		if bytes.Contains(response.Body, []byte(secret)) {
			t.Fatalf("account configuration leaked %q: %s", secret, response.Body)
		}
	}
	var config AccountEditableConfig
	if errDecode := json.Unmarshal(response.Body, &config); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if config.AccountID != "auth-1" || !config.Disabled || config.Priority == nil || *config.Priority != 8 || config.Note != "primary pool" || config.Prefix != "team-a" {
		t.Fatalf("configuration = %#v", config)
	}
	if !config.ProxyConfigured || config.Proxy != "http://proxy.internal:8080" || config.Websockets == nil || *config.Websockets {
		t.Fatalf("routing configuration = %#v", config)
	}
	if strings.Join(config.HeaderNames, ",") != "Authorization,X-Team" || config.ModelPolicy == nil || config.ModelPolicy.Mode != ModelPolicyModeAllowOnly || strings.Join(config.ModelPolicy.Models, ",") != "gpt-5.5" || config.ModelPolicy.ExcludedCount != 1 {
		t.Fatalf("plugin configuration = %#v", config)
	}
}

func TestHandleAccountConfigRejectsMissingAndReadOnlyAccounts(t *testing.T) {
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "runtime", Name: "runtime.json", Provider: "codex", Type: "codex", RuntimeOnly: true,
	}}}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	for _, test := range []struct {
		accountID string
		status    int
	}{
		{accountID: "missing", status: http.StatusNotFound},
		{accountID: "runtime", status: http.StatusConflict},
	} {
		body, _ := json.Marshal(AccountConfigRequest{AccountID: test.accountID})
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodPost,
			Path:   "/v0/management/plugins/cpa-account-config-manager/accounts/config",
			Body:   body,
		})
		if response.StatusCode != test.status {
			t.Fatalf("account %q response = %d %s", test.accountID, response.StatusCode, response.Body)
		}
	}
}
