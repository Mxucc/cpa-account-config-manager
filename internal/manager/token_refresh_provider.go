package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	codexTokenRefreshEndpoint      = "https://auth.openai.com/oauth/token"
	codexTokenRefreshClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexTokenRefreshScopes        = "openid profile email"
	tokenRefreshRequestTimeout     = 20 * time.Second
	maxTokenRefreshResponseBytes   = 64 << 10
	maxTokenRefreshCredentialBytes = 64 << 10
)

type tokenRefreshExchangeInput struct {
	Provider     string
	RefreshToken string
	ClientID     string
	ProxyURL     string
}

type tokenRefreshExchangeOutput struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	Scope        string
	ExpiresIn    time.Duration
}

type tokenRefreshExchanger interface {
	Exchange(context.Context, tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error)
}

type codexTokenRefreshExchanger struct {
	endpoint string
	doer     HTTPDoer
}

func newCodexTokenRefreshExchanger() *codexTokenRefreshExchanger {
	return &codexTokenRefreshExchanger{endpoint: codexTokenRefreshEndpoint}
}

func (e *codexTokenRefreshExchanger) Exchange(ctx context.Context, input tokenRefreshExchangeInput) (tokenRefreshExchangeOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if normalizeTokenRefreshProvider(input.Provider) != "codex" {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshProviderUnsupported
	}
	refreshToken := strings.TrimSpace(input.RefreshToken)
	if refreshToken == "" {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshCredentialMissing
	}
	if len(refreshToken) > maxTokenRefreshCredentialBytes {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID == "" || len(clientID) > 512 || hasUnsafeControl(clientID, false) {
		clientID = codexTokenRefreshClientID
	}
	endpoint := strings.TrimSpace(e.endpoint)
	if endpoint == "" {
		endpoint = codexTokenRefreshEndpoint
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	form.Set("scope", codexTokenRefreshScopes)
	requestCtx, cancel := context.WithTimeout(ctx, tokenRefreshRequestTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if errRequest != nil {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ensureCodexIdentityHeaders(request.Header)

	doer := e.doer
	if doer == nil {
		var errClient error
		doer, errClient = newTokenRefreshHTTPClient(input.ProxyURL)
		if errClient != nil {
			return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
		}
	}
	response, errDo := doer.Do(request)
	if errDo != nil {
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) || requestCtx.Err() != nil {
			return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
		}
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	if response == nil || response.Body == nil {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, maxTokenRefreshResponseBytes+1))
	if errRead != nil || len(body) > maxTokenRefreshResponseBytes {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	if response.StatusCode != http.StatusOK {
		return tokenRefreshExchangeOutput{}, classifyTokenExchangeResponse(response.StatusCode, body)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	if errTrailing := decoder.Decode(&struct{}{}); errTrailing != io.EOF {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	if strings.TrimSpace(payload.AccessToken) == "" || len(payload.AccessToken) > maxTokenRefreshCredentialBytes ||
		len(payload.RefreshToken) > maxTokenRefreshCredentialBytes || len(payload.IDToken) > maxTokenRefreshCredentialBytes ||
		payload.ExpiresIn <= 0 || payload.ExpiresIn > int64((30*24*time.Hour)/time.Second) {
		return tokenRefreshExchangeOutput{}, ErrAccountTokenRefreshFailed
	}
	return tokenRefreshExchangeOutput{
		AccessToken: strings.TrimSpace(payload.AccessToken), RefreshToken: strings.TrimSpace(payload.RefreshToken),
		IDToken: strings.TrimSpace(payload.IDToken), TokenType: strings.TrimSpace(payload.TokenType),
		Scope: strings.TrimSpace(payload.Scope), ExpiresIn: time.Duration(payload.ExpiresIn) * time.Second,
	}, nil
}

func newTokenRefreshHTTPClient(rawProxyURL string) (*http.Client, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok && defaultTransport != nil {
		transport = defaultTransport.Clone()
	}
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 2
	proxyURL := strings.TrimSpace(rawProxyURL)
	switch {
	case proxyURL == "":
		// Preserve ProxyFromEnvironment from the cloned default transport.
	case strings.EqualFold(proxyURL, "direct"), strings.EqualFold(proxyURL, "none"):
		transport.Proxy = nil
	default:
		parsed, errParse := url.Parse(proxyURL)
		if errParse != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Fragment != "" {
			return nil, fmt.Errorf("token refresh proxy is unsupported")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   tokenRefreshRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func classifyTokenExchangeResponse(statusCode int, body []byte) error {
	if statusCode == http.StatusUnauthorized {
		return ErrAccountTokenRefreshRejected
	}
	var payload struct {
		Error any    `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(body, &payload)
	errorCode := strings.ToLower(strings.TrimSpace(payload.Code))
	switch value := payload.Error.(type) {
	case string:
		errorCode += " " + strings.ToLower(strings.TrimSpace(value))
	case map[string]any:
		for _, key := range []string{"code", "type"} {
			if text, ok := value[key].(string); ok {
				errorCode += " " + strings.ToLower(strings.TrimSpace(text))
			}
		}
	}
	if statusCode == http.StatusBadRequest && (strings.Contains(errorCode, "invalid_grant") ||
		strings.Contains(errorCode, "refresh_token_reused") || strings.Contains(errorCode, "refresh_token_expired")) {
		return ErrAccountTokenRefreshRejected
	}
	return ErrAccountTokenRefreshFailed
}

func normalizeTokenRefreshProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "codex", "openai":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
