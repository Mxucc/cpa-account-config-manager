package manager

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type tokenRefreshHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f tokenRefreshHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCodexTokenRefreshExchangerUsesVerifiedProtocolAndParsesBoundedResponse(t *testing.T) {
	exchanger := &codexTokenRefreshExchanger{
		endpoint: codexTokenRefreshEndpoint,
		doer: tokenRefreshHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost || request.URL.String() != codexTokenRefreshEndpoint {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || request.Header.Get("User-Agent") != "codex-cli/0.91.0" {
				t.Fatalf("request headers = %#v", request.Header)
			}
			body, errRead := io.ReadAll(request.Body)
			if errRead != nil {
				t.Fatalf("read request body: %v", errRead)
			}
			form, errParse := url.ParseQuery(string(body))
			if errParse != nil {
				t.Fatalf("parse request form: %v", errParse)
			}
			if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-refresh" ||
				form.Get("client_id") != codexTokenRefreshClientID || form.Get("scope") != codexTokenRefreshScopes {
				t.Fatalf("request form = %#v", form)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"new-id","token_type":"Bearer","scope":"openid profile email","expires_in":3600,"provider_extension":true}`)),
			}, nil
		}),
	}
	result, errExchange := exchanger.Exchange(t.Context(), tokenRefreshExchangeInput{Provider: "openai", RefreshToken: "old-refresh"})
	if errExchange != nil {
		t.Fatalf("Exchange() error = %v", errExchange)
	}
	if result.AccessToken != "new-access" || result.RefreshToken != "new-refresh" || result.IDToken != "new-id" || result.ExpiresIn.Hours() != 1 {
		t.Fatalf("Exchange() = %#v", result)
	}
}

func TestCodexTokenRefreshExchangerRejectsUnsafeResponsesWithoutLeakingBody(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       error
	}{
		{name: "invalid grant", statusCode: http.StatusBadRequest, body: `{"error":"invalid_grant","error_description":"refresh-secret"}`, want: ErrAccountTokenRefreshRejected},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, body: `{"detail":"access-secret"}`, want: ErrAccountTokenRefreshRejected},
		{name: "malformed", statusCode: http.StatusOK, body: `{"access_token":`, want: ErrAccountTokenRefreshFailed},
		{name: "trailing json", statusCode: http.StatusOK, body: `{"access_token":"new-access","expires_in":3600}{}`, want: ErrAccountTokenRefreshFailed},
		{name: "missing access token", statusCode: http.StatusOK, body: `{"refresh_token":"rotated","expires_in":3600}`, want: ErrAccountTokenRefreshFailed},
		{name: "oversized", statusCode: http.StatusOK, body: strings.Repeat("x", maxTokenRefreshResponseBytes+1), want: ErrAccountTokenRefreshFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exchanger := &codexTokenRefreshExchanger{
				endpoint: codexTokenRefreshEndpoint,
				doer: tokenRefreshHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: test.statusCode, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}),
			}
			_, errExchange := exchanger.Exchange(context.Background(), tokenRefreshExchangeInput{Provider: "codex", RefreshToken: "refresh-secret"})
			if !errors.Is(errExchange, test.want) {
				t.Fatalf("Exchange() error = %v, want %v", errExchange, test.want)
			}
			if strings.Contains(errExchange.Error(), "refresh-secret") || strings.Contains(errExchange.Error(), "access-secret") {
				t.Fatalf("Exchange() exposed response body: %v", errExchange)
			}
		})
	}
}

func TestTokenRefreshHTTPClientDisablesRedirectsAndRejectsUnsupportedProxy(t *testing.T) {
	client, errClient := newTokenRefreshHTTPClient("direct")
	if errClient != nil {
		t.Fatalf("newTokenRefreshHTTPClient() error = %v", errClient)
	}
	request, _ := http.NewRequest(http.MethodPost, codexTokenRefreshEndpoint, nil)
	if errRedirect := client.CheckRedirect(request, nil); !errors.Is(errRedirect, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() = %v", errRedirect)
	}
	if _, errClient = newTokenRefreshHTTPClient("socks5://user:proxy-secret@127.0.0.1:1080"); errClient == nil || strings.Contains(errClient.Error(), "proxy-secret") {
		t.Fatalf("unsupported proxy error = %v", errClient)
	}
}
