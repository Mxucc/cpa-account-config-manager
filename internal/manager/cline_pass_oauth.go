package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cline Pass authenticates with a WorkOS device authorization that is exchanged
// for Cline tokens, exactly like the reference implementation
// github.com/fifidayone/pi-clinepass. The endpoints below are the ones the
// reference documents; the client id is the public WorkOS application the Cline
// CLI uses.
const (
	clinePassWorkOSDeviceEndpoint = "https://api.workos.com/user_management/authorize/device"
	clinePassWorkOSAuthEndpoint   = "https://api.workos.com/user_management/authenticate"
	clinePassWorkOSClientID       = "client_01K3A541FN8TA3EPPHTD2325AR"
	clinePassWorkOSTokenPrefix    = "workos:"
	clinePassRefreshEndpointPath  = "/auth/refresh"
	clinePassRegisterEndpointPath = "/auth/register"
	clinePassMaxTokenBytes        = 64 << 10
	clinePassMaxLoginBytes        = 64 << 10
)

// clinePassEnsureWorkOSPrefix makes sure a gateway access token carries the
// prefix the Cline API expects. The reference implementation shows the API
// returning a bare JWT that must be prefixed before it is used as a bearer token.
func clinePassEnsureWorkOSPrefix(token string) string {
	trimmed := strings.TrimSpace(token)
	if strings.HasPrefix(trimmed, clinePassWorkOSTokenPrefix) {
		return trimmed
	}
	return clinePassWorkOSTokenPrefix + trimmed
}

// clinePassIsWorkOSToken reports whether a stored token is a rotating OAuth
// access token rather than a static API key.
func clinePassIsWorkOSToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), clinePassWorkOSTokenPrefix)
}

// clinePassDeviceAuthorization is one pending device-code authorization.
type clinePassDeviceAuthorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresInSeconds        int
	IntervalSeconds         int
}

// startClinePassDeviceAuthorization begins the WorkOS device flow.
func startClinePassDeviceAuthorization(ctx context.Context, doer HTTPDoer) (clinePassDeviceAuthorization, error) {
	form := url.Values{}
	form.Set("client_id", clinePassWorkOSClientID)
	requestCtx, cancel := context.WithTimeout(ctx, clinePassDeviceTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, clinePassWorkOSDeviceEndpoint, strings.NewReader(form.Encode()))
	if errRequest != nil {
		return clinePassDeviceAuthorization{}, fmt.Errorf("the Cline Pass device request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: clinePassDeviceTimeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return clinePassDeviceAuthorization{}, fmt.Errorf("Cline Pass device authorization failed: %s", sanitizeClinePassError(errDo.Error()))
	}
	if response == nil || response.Body == nil {
		return clinePassDeviceAuthorization{}, fmt.Errorf("Cline Pass device authorization returned an empty response")
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, clinePassMaxLoginBytes))
	if errRead != nil {
		return clinePassDeviceAuthorization{}, fmt.Errorf("Cline Pass device authorization could not be read")
	}
	var payload struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return clinePassDeviceAuthorization{}, fmt.Errorf("Cline Pass device authorization returned an unrecognized response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.DeviceCode == "" || payload.UserCode == "" || payload.VerificationURI == "" {
		detail := payload.ErrorDescription
		if detail == "" {
			detail = payload.Error
		}
		if detail == "" {
			detail = response.Status
		}
		return clinePassDeviceAuthorization{}, fmt.Errorf("Cline Pass device authorization failed: %s", sanitizeClinePassError(detail))
	}
	interval := payload.Interval
	if interval < 1 {
		interval = 5
	}
	expiresIn := payload.ExpiresIn
	if expiresIn < 30 {
		expiresIn = 300
	}
	return clinePassDeviceAuthorization{
		DeviceCode:              payload.DeviceCode,
		UserCode:                payload.UserCode,
		VerificationURI:         payload.VerificationURI,
		VerificationURIComplete: payload.VerificationURIComplete,
		ExpiresInSeconds:        expiresIn,
		IntervalSeconds:         interval,
	}, nil
}

// pollResult is the outcome of one device-code poll.
type clinePassPollResult struct {
	AccessToken  string
	RefreshToken string
	Pending      bool
	SlowDown     bool
}

// pollClinePassDeviceAuthorization performs exactly one poll. Waiting between
// polls is the caller's job so a management request never blocks on the device
// flow deadline.
func pollClinePassDeviceAuthorization(ctx context.Context, deviceCode string, doer HTTPDoer) (clinePassPollResult, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	form.Set("client_id", clinePassWorkOSClientID)
	requestCtx, cancel := context.WithTimeout(ctx, clinePassDeviceTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, clinePassWorkOSAuthEndpoint, strings.NewReader(form.Encode()))
	if errRequest != nil {
		return clinePassPollResult{}, fmt.Errorf("the Cline Pass authorization request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: clinePassDeviceTimeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return clinePassPollResult{}, fmt.Errorf("Cline Pass authorization failed: %s", sanitizeClinePassError(errDo.Error()))
	}
	if response == nil || response.Body == nil {
		return clinePassPollResult{}, fmt.Errorf("Cline Pass authorization returned an empty response")
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, clinePassMaxLoginBytes))
	if errRead != nil {
		return clinePassPollResult{}, fmt.Errorf("Cline Pass authorization could not be read")
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return clinePassPollResult{}, fmt.Errorf("Cline Pass authorization returned an unrecognized response")
	}
	if response.StatusCode == http.StatusOK && payload.AccessToken != "" && payload.RefreshToken != "" {
		return clinePassPollResult{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken}, nil
	}
	switch payload.Error {
	case "authorization_pending":
		return clinePassPollResult{Pending: true}, nil
	case "slow_down":
		// RFC 8628: the client must increase its polling interval.
		return clinePassPollResult{Pending: true, SlowDown: true}, nil
	}
	detail := payload.ErrorDescription
	if detail == "" {
		detail = payload.Error
	}
	if detail == "" {
		detail = response.Status
	}
	return clinePassPollResult{}, fmt.Errorf("Cline Pass authorization failed: %s", sanitizeClinePassError(detail))
}

// registerClinePassWorkOSTokens exchanges WorkOS tokens for Cline tokens. The
// response carries the authoritative expiry, so it is used as-is.
func registerClinePassWorkOSTokens(ctx context.Context, baseURL, accessToken, refreshToken string, doer HTTPDoer) (string, string, time.Time, error) {
	base := normalizeClinePassBaseURL(baseURL)
	if !validClinePassBaseURL(base) {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass base URL is invalid")
	}
	payload, errMarshal := json.Marshal(map[string]string{
		"accessToken":  strings.TrimSpace(accessToken),
		"refreshToken": strings.TrimSpace(refreshToken),
	})
	if errMarshal != nil {
		return "", "", time.Time{}, fmt.Errorf("the Cline Pass registration request could not be encoded")
	}
	requestCtx, cancel := context.WithTimeout(ctx, clinePassRequestTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, base+clinePassRegisterEndpointPath, bytes.NewReader(payload))
	if errRequest != nil {
		return "", "", time.Time{}, fmt.Errorf("the Cline Pass registration request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "cpa-account-config-manager/cline-pass")
	client := &http.Client{Timeout: clinePassRequestTimeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token registration failed: %s", sanitizeClinePassError(errDo.Error()))
	}
	if response == nil || response.Body == nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token registration returned an empty response")
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, clinePassMaxTokenBytes))
	if errRead != nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token registration could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token registration failed (HTTP %d): %s", response.StatusCode, sanitizeClinePassError(string(body)))
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(body, &envelope); errDecode != nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token registration returned an unrecognized response")
	}
	if !envelope.Success || strings.TrimSpace(envelope.Data.AccessToken) == "" {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token registration was rejected")
	}
	registeredRefresh := strings.TrimSpace(envelope.Data.RefreshToken)
	if registeredRefresh == "" {
		registeredRefresh = strings.TrimSpace(refreshToken)
	}
	expiresAt := time.Time{}
	if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(envelope.Data.ExpiresAt)); errParse == nil {
		expiresAt = parsed.UTC()
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(clinePassTokenLifetime)
	}
	return clinePassEnsureWorkOSPrefix(envelope.Data.AccessToken), registeredRefresh, expiresAt, nil
}

// exchangeClinePassRefreshToken rotates a Cline Pass access token server-side.
// The gateway may rotate the refresh token, so the returned pair always replaces
// the stored one.
func exchangeClinePassRefreshToken(ctx context.Context, baseURL, refreshToken string, doer HTTPDoer) (string, string, time.Time, error) {
	base := normalizeClinePassBaseURL(baseURL)
	if !validClinePassBaseURL(base) {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass base URL is invalid")
	}
	trimmedRefresh := strings.TrimSpace(refreshToken)
	if trimmedRefresh == "" {
		return "", "", time.Time{}, fmt.Errorf("the Cline Pass refresh token is missing; sign in again")
	}
	payload, errMarshal := json.Marshal(map[string]string{
		"granttype":    "refresh_token",
		"refreshToken": trimmedRefresh,
	})
	if errMarshal != nil {
		return "", "", time.Time{}, fmt.Errorf("the Cline Pass refresh request could not be encoded")
	}
	requestCtx, cancel := context.WithTimeout(ctx, clinePassRefreshTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodPost, base+clinePassRefreshEndpointPath, bytes.NewReader(payload))
	if errRequest != nil {
		return "", "", time.Time{}, fmt.Errorf("the Cline Pass refresh request could not be created")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "cpa-account-config-manager/cline-pass")
	client := &http.Client{Timeout: clinePassRefreshTimeout, Transport: doerTransport(doer)}
	response, errDo := client.Do(request)
	if errDo != nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token refresh failed: %s", sanitizeClinePassError(errDo.Error()))
	}
	if response == nil || response.Body == nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token refresh returned an empty response")
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, clinePassMaxTokenBytes))
	if errRead != nil {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token refresh could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token refresh failed (HTTP %d): %s", response.StatusCode, sanitizeClinePassError(string(body)))
	}
	access, refresh, expiresAt, ok := parseClinePassTokenPair(body)
	if !ok {
		return "", "", time.Time{}, fmt.Errorf("Cline Pass token refresh returned an unrecognized response")
	}
	if strings.TrimSpace(refresh) == "" {
		refresh = trimmedRefresh
	}
	return access, refresh, expiresAt, nil
}

// parseClinePassTokenPair accepts the nested and flat token shapes the Cline API
// returns, together with the expiry the answer carries when it carries one.
//
// The expiry matters: the plugin used to assume a fixed lifetime for every rotated
// token, so a gateway that hands out shorter-lived tokens left the stored one looking
// valid long after the gateway had stopped accepting it - and every routed request then
// failed with an authorization error the plugin only learned about from the failure.
func parseClinePassTokenPair(body []byte) (string, string, time.Time, bool) {
	var nested struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
			ExpiresIn    int    `json:"expiresIn"`
		} `json:"data"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    string `json:"expiresAt"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	if errDecode := json.Unmarshal(body, &nested); errDecode != nil {
		return "", "", time.Time{}, false
	}
	access := strings.TrimSpace(nested.Data.AccessToken)
	refresh := strings.TrimSpace(nested.Data.RefreshToken)
	expiresAt := clinePassTokenExpiry(nested.Data.ExpiresAt, nested.Data.ExpiresIn)
	if access == "" {
		access = strings.TrimSpace(nested.AccessToken)
		refresh = strings.TrimSpace(nested.RefreshToken)
		expiresAt = clinePassTokenExpiry(nested.ExpiresAt, nested.ExpiresIn)
	}
	if access == "" {
		return "", "", time.Time{}, false
	}
	return access, refresh, expiresAt, true
}

// clinePassTokenExpiry reads the expiry a token answer carries: an absolute timestamp, a
// lifetime in seconds, or nothing. A nonsense value is treated as absent rather than trusted, so
// a gateway that answers with a stray number cannot park an account behind a made-up lifetime.
func clinePassTokenExpiry(rawExpiresAt string, expiresIn int) time.Time {
	if trimmed := strings.TrimSpace(rawExpiresAt); trimmed != "" {
		if parsed, errParse := time.Parse(time.RFC3339, trimmed); errParse == nil {
			return parsed.UTC()
		}
	}
	switch {
	case expiresIn > 0 && expiresIn <= int(clinePassMaxTokenLifetime/time.Second):
		return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	case expiresIn > int(clinePassMaxTokenLifetime/time.Second):
		return time.Now().UTC().Add(clinePassMaxTokenLifetime)
	}
	return time.Time{}
}

// refreshAccountToken rotates the stored tokens of one OAuth account and
// persists the result. A persist failure does not fail the refresh: the rotated
// refresh token is single-use, so keeping the live credential usable and
// surfacing the storage error is safer than discarding the new token.
func (s *ClinePassService) refreshAccountToken(ctx context.Context, id string) (ClinePassAccount, error) {
	account, found, _ := s.accountByID(strings.TrimSpace(id))
	if !found {
		return ClinePassAccount{}, fmt.Errorf("Cline Pass account was not found")
	}
	if normalizeClinePassAuthMethod(account.AuthMethod) == clinePassAuthMethodAPIKey {
		return account, nil
	}
	access, refresh, expiresAt, errExchange := exchangeClinePassRefreshToken(ctx, account.BaseURL, account.RefreshToken, s.httpDoer())
	if errExchange != nil {
		return ClinePassAccount{}, errExchange
	}
	if expiresAt.IsZero() {
		// The gateway did not name a window, so the documented lifetime is used - discounted by
		// the refresh margin, because the token is rotated before it runs out.
		expiresAt = s.now().UTC().Add(clinePassTokenLifetime - clinePassTokenRefreshMargin)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stillPresent := s.accountLocked(account.ID); !stillPresent {
		return ClinePassAccount{}, fmt.Errorf("Cline Pass account was not found")
	}
	s.updateAccountTokensLocked(account.ID, clinePassEnsureWorkOSPrefix(access), refresh, expiresAt)
	_ = s.persistLocked()
	updated, _ := s.accountLocked(account.ID)
	return updated, nil
}

// StartDeviceLogin begins a browser (device-code) sign-in and returns the code
// the operator must enter. Polling is a separate, bounded call.
func (s *ClinePassService) StartDeviceLogin(ctx context.Context, name string) (ClinePassLoginView, error) {
	if s == nil {
		return ClinePassLoginView{}, fmt.Errorf("Cline Pass service is unavailable")
	}
	device, errDevice := startClinePassDeviceAuthorization(ctx, s.httpDoer())
	if errDevice != nil {
		return ClinePassLoginView{}, errDevice
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLoginsLocked()
	if len(s.logins) >= clinePassMaxLoginSessions {
		return ClinePassLoginView{}, fmt.Errorf("too many pending Cline Pass sign-ins")
	}
	now := s.now().UTC()
	session := &clinePassLoginSession{
		ID:                      "clinelogin_" + clinePassRandomID(),
		Method:                  clinePassAuthMethodOAuth,
		Name:                    strings.TrimSpace(name),
		DeviceCode:              device.DeviceCode,
		UserCode:                device.UserCode,
		VerificationURI:         device.VerificationURI,
		VerificationURIComplete: device.VerificationURIComplete,
		IntervalSeconds:         device.IntervalSeconds,
		ExpiresAt:               now.Add(time.Duration(device.ExpiresInSeconds) * time.Second),
		NextPollAt:              now,
		Status:                  clinePassLoginPending,
	}
	s.logins[session.ID] = session
	return s.loginViewLocked(session), nil
}

// PollDeviceLogin performs one poll of a pending sign-in. Pending states are
// reported through the view so the UI can keep polling; only an unusable request
// (an unknown session) is an error.
func (s *ClinePassService) PollDeviceLogin(ctx context.Context, sessionID string) (ClinePassLoginView, error) {
	if s == nil {
		return ClinePassLoginView{}, fmt.Errorf("Cline Pass service is unavailable")
	}
	// The transport is captured before the lock is taken: httpDoer() acquires the
	// same mutex, so reading it while a write lock is held would deadlock.
	doer := s.httpDoer()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ClinePassLoginView{}, fmt.Errorf("session_id is required")
	}
	s.mu.Lock()
	session, found := s.logins[sessionID]
	if !found {
		s.mu.Unlock()
		return ClinePassLoginView{}, fmt.Errorf("Cline Pass login session was not found")
	}
	if session.Status != clinePassLoginPending {
		view := s.loginViewLocked(session)
		s.mu.Unlock()
		return view, nil
	}
	now := s.now().UTC()
	if now.After(session.ExpiresAt) {
		session.Status = clinePassLoginExpired
		view := s.loginViewLocked(session)
		s.mu.Unlock()
		return view, nil
	}
	if now.Before(session.NextPollAt) {
		view := s.loginViewLocked(session)
		s.mu.Unlock()
		return view, nil
	}
	deviceCode := session.DeviceCode
	name := session.Name
	s.mu.Unlock()

	result, errPoll := pollClinePassDeviceAuthorization(ctx, deviceCode, doer)

	s.mu.Lock()
	defer s.mu.Unlock()
	session, found = s.logins[sessionID]
	if !found || session.Status != clinePassLoginPending {
		if !found {
			return ClinePassLoginView{SessionID: sessionID, Status: clinePassLoginCancelled}, nil
		}
		return s.loginViewLocked(session), nil
	}
	now = s.now().UTC()
	if errPoll != nil {
		session.Status = clinePassLoginFailed
		session.Error = sanitizeClinePassError(errPoll.Error())
		return s.loginViewLocked(session), nil
	}
	if result.Pending {
		if result.SlowDown {
			session.IntervalSeconds += 5
		}
		interval := session.IntervalSeconds
		if interval < 1 {
			interval = 5
		}
		session.NextPollAt = now.Add(time.Duration(interval) * time.Second)
		return s.loginViewLocked(session), nil
	}
	accountID, errComplete := s.completeLoginLocked(ctx, name, result.AccessToken, result.RefreshToken, doer)
	if errComplete != nil {
		session.Status = clinePassLoginFailed
		session.Error = sanitizeClinePassError(errComplete.Error())
		return s.loginViewLocked(session), nil
	}
	session.Status = clinePassLoginCompleted
	session.AccountID = accountID
	return s.loginViewLocked(session), nil
}

// CompleteClineCLILogin reuses a sign-in the Cline CLI already stored on this
// host instead of starting a new browser flow.
func (s *ClinePassService) CompleteClineCLILogin(ctx context.Context, name string) (ClinePassLoginView, error) {
	if s == nil {
		return ClinePassLoginView{}, fmt.Errorf("Cline Pass service is unavailable")
	}
	access, refresh, expiresAt, ok := resolveClineCLICredential()
	if !ok {
		return ClinePassLoginView{}, fmt.Errorf("no Cline CLI sign-in was found on this host")
	}
	if expiresAt.IsZero() || !expiresAt.After(s.now().UTC().Add(clinePassTokenRefreshMargin)) {
		rotatedAccess, rotatedRefresh, rotatedExpiry, errRotate := exchangeClinePassRefreshToken(ctx, clinePassDefaultBaseURL, refresh, s.httpDoer())
		if errRotate == nil {
			access, refresh = rotatedAccess, rotatedRefresh
			expiresAt = rotatedExpiry
			if expiresAt.IsZero() {
				expiresAt = s.now().UTC().Add(clinePassTokenLifetime - clinePassTokenRefreshMargin)
			}
		} else if expiresAt.IsZero() || !expiresAt.After(s.now().UTC()) {
			return ClinePassLoginView{}, errRotate
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accountID, errSave := s.saveOAuthAccountLocked(name, clinePassDefaultBaseURL, access, refresh, expiresAt, clinePassAuthMethodCLI)
	if errSave != nil {
		return ClinePassLoginView{}, errSave
	}
	view := ClinePassLoginView{Method: clinePassAuthMethodCLI, Status: clinePassLoginCompleted}
	if account, found := s.accountLocked(accountID); found {
		accountView := s.clinePassViewOfLocked(account)
		view.Account = &accountView
	}
	return view, nil
}

// CancelDeviceLogin abandons a pending sign-in.
func (s *ClinePassService) CancelDeviceLogin(sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, found := s.logins[strings.TrimSpace(sessionID)]
	if !found {
		return false
	}
	if session.Status == clinePassLoginPending {
		session.Status = clinePassLoginCancelled
	}
	return true
}

// completeLoginLocked exchanges the WorkOS tokens for Cline tokens and stores
// the account. It is called with the write lock held and never performs a
// blocking network call outside the token exchange timeout.
func (s *ClinePassService) completeLoginLocked(ctx context.Context, name, accessToken, refreshToken string, doer HTTPDoer) (string, error) {
	baseURL := clinePassDefaultBaseURL
	registeredAccess, registeredRefresh, expiresAt, errRegister := registerClinePassWorkOSTokens(ctx, baseURL, accessToken, refreshToken, doer)
	if errRegister != nil {
		return "", errRegister
	}
	return s.saveOAuthAccountLocked(name, baseURL, registeredAccess, registeredRefresh, expiresAt, clinePassAuthMethodOAuth)
}

// saveOAuthAccountLocked appends an authorized account and persists it.
func (s *ClinePassService) saveOAuthAccountLocked(name, baseURL, accessToken, refreshToken string, expiresAt time.Time, authMethod string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("the authorization did not return an access token")
	}
	normalizedBase := normalizeClinePassBaseURL(baseURL)
	if !validClinePassBaseURL(normalizedBase) {
		return "", fmt.Errorf("base_url must be a valid http(s) URL")
	}
	if strings.TrimSpace(refreshToken) == "" {
		refreshToken = accessToken
	}
	if len(s.accounts) >= clinePassMaxAccounts {
		return "", fmt.Errorf("Cline Pass account limit reached")
	}
	now := s.now().UTC()
	account := ClinePassAccount{
		ID:           fmt.Sprintf("cline_%d", s.now().UnixNano()),
		Name:         strings.TrimSpace(name),
		BaseURL:      normalizedBase,
		AuthMethod:   normalizeClinePassAuthMethod(authMethod),
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(refreshToken),
		ExpiresAt:    expiresAt.UTC(),
		Models:       normalizeClinePassModels(clinePassCatalogIDs()),
		CreatedAt:    now,
	}
	s.accounts = append(s.accounts, account)
	if errPersist := s.persistLocked(); errPersist != nil {
		s.accounts = s.accounts[:len(s.accounts)-1]
		return "", errPersist
	}
	return account.ID, nil
}

// loginViewLocked builds the public view of one login session.
func (s *ClinePassService) loginViewLocked(session *clinePassLoginSession) ClinePassLoginView {
	view := ClinePassLoginView{
		SessionID:               session.ID,
		Method:                  session.Method,
		Status:                  session.Status,
		UserCode:                session.UserCode,
		VerificationURI:         session.VerificationURI,
		VerificationURIComplete: session.VerificationURIComplete,
		IntervalSeconds:         session.IntervalSeconds,
		Error:                   session.Error,
	}
	if !session.ExpiresAt.IsZero() {
		if remaining := int(session.ExpiresAt.Sub(s.now().UTC()).Seconds()); remaining > 0 {
			view.ExpiresInSeconds = remaining
		}
	}
	if session.AccountID != "" {
		if account, found := s.accountLocked(session.AccountID); found {
			accountView := s.clinePassViewOfLocked(account)
			view.Account = &accountView
		}
	}
	return view
}

// pruneLoginsLocked drops finished or expired sessions so the map stays bounded.
func (s *ClinePassService) pruneLoginsLocked() {
	now := s.now().UTC()
	for id, session := range s.logins {
		if session.Status == clinePassLoginPending && now.Before(session.ExpiresAt) {
			continue
		}
		delete(s.logins, id)
	}
}

// cancelLoginsForAccountLocked marks sessions that produced one account as
// cancelled so a removed account cannot be re-reported as a live sign-in.
func (s *ClinePassService) cancelLoginsForAccountLocked(accountID string) {
	for _, session := range s.logins {
		if session.AccountID == accountID && session.Status == clinePassLoginPending {
			session.Status = clinePassLoginCancelled
		}
	}
}

// clinePassRandomID mints a non-secret session identifier.
func clinePassRandomID() string {
	raw := make([]byte, 16)
	if _, errRead := rand.Read(raw); errRead != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

// resolveClineCLICredential reads the sign-in the Cline CLI left on this host
// (`~/.cline/data/settings/providers.json`). It never reads pi's own auth store:
// only a Cline CLI sign-in is a reusable identity.
func resolveClineCLICredential() (access, refresh string, expiresAt time.Time, ok bool) {
	home, errHome := os.UserHomeDir()
	if errHome != nil || strings.TrimSpace(home) == "" {
		return "", "", time.Time{}, false
	}
	raw, errRead := os.ReadFile(filepath.Join(home, ".cline", "data", "settings", "providers.json"))
	if errRead != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return "", "", time.Time{}, false
	}
	var parsed struct {
		Providers map[string]struct {
			Settings struct {
				Auth struct {
					AccessToken  string          `json:"accessToken"`
					RefreshToken string          `json:"refreshToken"`
					ExpiresAt    json.RawMessage `json:"expiresAt"`
				} `json:"auth"`
			} `json:"settings"`
		} `json:"providers"`
	}
	if errDecode := json.Unmarshal(raw, &parsed); errDecode != nil {
		return "", "", time.Time{}, false
	}
	for _, key := range []string{"cline-pass", "cline"} {
		provider, exists := parsed.Providers[key]
		if !exists {
			continue
		}
		auth := provider.Settings.Auth
		accessToken := strings.TrimSpace(auth.AccessToken)
		if accessToken == "" || !clinePassIsWorkOSToken(accessToken) {
			continue
		}
		refreshToken := strings.TrimSpace(auth.RefreshToken)
		if refreshToken == "" {
			refreshToken = accessToken
		}
		return accessToken, refreshToken, clinePassParseExpiresAt(auth.ExpiresAt), true
	}
	return "", "", time.Time{}, false
}

// clinePassParseExpiresAt accepts the epoch-millisecond and RFC3339 shapes the
// Cline CLI store uses.
func clinePassParseExpiresAt(raw json.RawMessage) time.Time {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return time.Time{}
	}
	var asNumber float64
	if errNumber := json.Unmarshal([]byte(trimmed), &asNumber); errNumber == nil && asNumber > 0 {
		millis := int64(asNumber)
		if millis < 1_000_000_000_000 {
			return time.Unix(millis, 0).UTC()
		}
		return time.UnixMilli(millis).UTC()
	}
	var asText string
	if errText := json.Unmarshal([]byte(trimmed), &asText); errText == nil {
		if parsed, errParse := time.Parse(time.RFC3339, strings.TrimSpace(asText)); errParse == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
