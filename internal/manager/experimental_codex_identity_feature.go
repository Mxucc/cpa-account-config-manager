package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// CodexIdentityExperiment is the plugin-local adapter for the reference
// fork's two Codex protections: outbound identity convergence and an inbound
// official-client gate backed by blacklist, whitelist, version bounds, and
// engine fingerprints.
type CodexIdentityExperiment struct {
	settings codexPolicyProvider
	accounts codexAccountGateProvider
	seeds    fingerprintSeedStore
}

type codexAccountGateProvider interface {
	CurrentAuthDocument(context.Context, Account) (currentAuthDocument, error)
	List(context.Context, ListQuery) (ListResponse, error)
}

type fingerprintSeedStore interface {
	fingerprintAccountStore
}

type restrictedCodexAccountProvider struct {
	accounts codexAccountGateProvider
}

func NewCodexIdentityExperiment(settings codexPolicyProvider, accounts codexAccountGateProvider) *CodexIdentityExperiment {
	experiment := &CodexIdentityExperiment{settings: settings, accounts: accounts}
	if store, ok := accounts.(fingerprintSeedStore); ok {
		experiment.seeds = store
	}
	return experiment
}

// RequestInterceptionActive keeps the CPA interceptor enabled only when one of
// the two Codex identity modes is configured.
func (e *CodexIdentityExperiment) RequestInterceptionActive() bool {
	if e == nil || e.settings == nil {
		return false
	}
	settings := e.settings.CodexIdentity()
	return settings.OutboundConvergenceEnabled || settings.IngressGateEnabled
}

// RequestInterceptionAcceptsFormat intentionally covers every format. The
// transform itself only changes Codex-native requests, while ingress account
// selection is provider-agnostic because the selected auth is resolved first.
func (e *CodexIdentityExperiment) RequestInterceptionAcceptsFormat(string) bool {
	return true
}

// InterceptRequest applies the inbound gate first so rejected requests never
// reach upstream or receive converged identity headers.
func (e *CodexIdentityExperiment) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if !e.RequestInterceptionActive() {
		return cpaapi.RequestInterceptResponse{}, false
	}
	requestContext := context.Background()
	authIndex := overdraftAuthIndexFromMetadata(request.Metadata)
	gate := e.accountGate(requestContext, authIndex)
	policy := e.currentPolicy()

	if policy.IngressGateEnabled {
		if result := detectCodexClientRestriction(gate.codexAccountGateState, policy, headerValue(request.Headers, "User-Agent"), headerValue(request.Headers, "Originator"), request.Headers, request.Body); !result.Matched {
			return e.reject(result), true
		}
	}

	// The host can pass a nil header map when the original request had no
	// outbound headers. Treat that as an empty map so Codex-native requests
	// still receive the official-client identity and body convergence.
	headers := request.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	enforceCodexIdentityHeaders(headers)

	body := request.Body
	// Account identity isolation is independent from fingerprint convergence:
	// preserve each client's ID cardinality, but never send one client's IDs to
	// a different upstream account during failover. Convergence, when enabled,
	// then overwrites these values with its account-stable fingerprint.
	if gate.account != nil {
		namespace := e.resolveCodexAccountIdentityNamespace(requestContext, *gate.account)
		if namespace != "" {
			applyCodexAccountIdentityHeaders(headers, namespace)
			if decoded, errDecode := decodeJSONObjectBody(body); errDecode == nil &&
				applyCodexAccountIdentityClientMetadata(decoded, namespace) {
				if encoded, errEncode := json.Marshal(decoded); errEncode == nil {
					body = encoded
				}
			}
		}
	}

	var fingerprint *codexFingerprintIDs
	if e.seeds != nil && gate.account != nil {
		settings := e.settings.codexIdentitySnapshot()
		mode := effectiveCodexFingerprintMode(settings.ConvergenceMode)
		if seed, ok := resolveCodexFingerprintSeed(requestContext, e.seeds, *gate.account); ok {
			fingerprint = resolveCodexFingerprintIDs(
				*gate.account, seed, extractClientSessionID(request.Headers), mode,
			)
		}
	}
	if fingerprint != nil {
		applyCodexFingerprintHeaders(headers, fingerprint)
		if decoded, errDecode := decodeJSONObjectBody(body); errDecode == nil &&
			applyCodexFingerprintClientMetadata(decoded, fingerprint) {
			if encoded, errEncode := json.Marshal(decoded); errEncode == nil {
				body = encoded
			}
		}
	}

	response := cpaapi.RequestInterceptResponse{Headers: headers.Clone(), Body: body}
	for _, name := range []string{
		"User-Agent", "Originator", "Version", "OpenAI-Beta",
		"X-Codex-Installation-Id", "X-Codex-Window-Id", "X-Client-Request-Id",
		"Session-Id", "Session_Id", "Thread-Id", codexFingerprintHeader,
	} {
		if values := headers.Values(name); len(values) > 0 {
			response.Headers[name] = append([]string(nil), values...)
		}
	}
	if len(response.Headers) == 0 {
		response.Headers = nil
	}
	return response, true
}

func decodeJSONObjectBody(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, fmt.Errorf("request body is not valid JSON")
	}
	var decoded map[string]any
	if errDecode := json.Unmarshal(raw, &decoded); errDecode != nil || decoded == nil {
		return nil, fmt.Errorf("request body is not a JSON object")
	}
	return decoded, nil
}

// resolveCodexAccountIdentityNamespace derives the reference fork's stable
// upstream-account scope. Prefer immutable ChatGPT identifiers, then the
// persisted fingerprint seed. Setup tokens and API keys use irreversible
// fingerprints of their stable host identity; they never leak credential bytes.
func (e *CodexIdentityExperiment) resolveCodexAccountIdentityNamespace(ctx context.Context, account Account) string {
	if e == nil || e.accounts == nil {
		return ""
	}
	// The persisted seed is authoritative for OAuth-like credentials: unlike a
	// host row identifier it remains stable across duplicate physical files and
	// cannot be reused accidentally across deployments.
	if isCodexOAuthLikeAccount(account) && e.seeds != nil {
		if seed, ok := resolveCodexFingerprintSeed(ctx, e.seeds, account); ok {
			return "seed:" + seed
		}
	}
	document, errRead := e.accounts.CurrentAuthDocument(ctx, account)
	if errRead == nil && document.Metadata != nil {
		accountID := safeQuotaAccountID(firstMapValue(document.Metadata, "account_id", "chatgpt_account_id"))
		userID := safeQuotaAccountID(firstMapValue(document.Metadata, "chatgpt_user_id", "user_id"))
		if accountID != "" {
			return "chatgpt:" + accountID + ":user:" + userID
		}
		for _, key := range []string{"id_token", "idToken", "access_token", "accessToken"} {
			claims := accountIdentityClaims(document.Metadata[key])
			accountID = safeQuotaAccountID(firstMapValue(claims, "chatgpt_account_id", "account_id"))
			if userID == "" {
				userID = safeQuotaAccountID(firstMapValue(claims, "chatgpt_user_id", "user_id"))
			}
			if accountID != "" {
				return "chatgpt:" + accountID + ":user:" + userID
			}
		}
	}
	if isCodexOAuthLikeAccount(account) {
		return ""
	}
	identity := strings.TrimSpace(firstNonEmpty(account.ID, account.AuthID))
	if identity == "" || strings.ContainsAny(identity, "/\\") || strings.EqualFold(identity, "unknown") {
		return ""
	}
	return "host:" + identity
}

var codexAccountIdentityFields = []struct {
	name string
	kind string
}{
	{name: "installation_id", kind: "installation"},
	{name: "x-codex-installation-id", kind: "installation"},
	{name: "session_id", kind: "session"},
	{name: "session-id", kind: "session"},
	{name: "thread_id", kind: "thread"},
	{name: "thread-id", kind: "thread"},
	{name: "turn_id", kind: "turn"},
	{name: "turn-id", kind: "turn"},
	{name: "window_id", kind: "window"},
	{name: "x-codex-window-id", kind: "window"},
	{name: "x-client-request-id", kind: "request"},
}

func codexAccountIdentityValue(namespace, kind, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.TrimSpace(namespace) == "" {
		return raw
	}
	return deriveStableUUIDv4("cpa:codex-account-identity:v1:" + namespace + ":kind:" + kind + ":value:" + raw)
}

func applyCodexAccountIdentityFields(values map[string]any, namespace string) bool {
	if values == nil || namespace == "" {
		return false
	}
	modified := false
	for _, field := range codexAccountIdentityFields {
		raw, ok := values[field.name].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		next := codexAccountIdentityValue(namespace, field.kind, raw)
		if next != raw {
			values[field.name] = next
			modified = true
		}
	}
	return modified
}

func applyCodexAccountIdentityEmbeddedMetadata(values map[string]any, namespace string) bool {
	raw, _ := values[codexFingerprintHeader].(string)
	if strings.TrimSpace(raw) == "" {
		return false
	}
	metadata := make(map[string]any)
	if errDecode := json.Unmarshal([]byte(raw), &metadata); errDecode != nil || metadata == nil {
		return false
	}
	if !applyCodexAccountIdentityFields(metadata, namespace) {
		return false
	}
	rebuilt, errEncode := json.Marshal(metadata)
	if errEncode != nil {
		return false
	}
	values[codexFingerprintHeader] = string(rebuilt)
	return true
}

func applyCodexAccountIdentityHeaders(h http.Header, namespace string) {
	if h == nil || namespace == "" {
		return
	}
	for _, field := range codexAccountIdentityFields {
		if raw := strings.TrimSpace(h.Get(field.name)); raw != "" {
			h.Set(field.name, codexAccountIdentityValue(namespace, field.kind, raw))
		}
	}
	if raw := strings.TrimSpace(h.Get(codexFingerprintHeader)); raw != "" {
		metadata := make(map[string]any)
		if errDecode := json.Unmarshal([]byte(raw), &metadata); errDecode == nil && metadata != nil &&
			applyCodexAccountIdentityFields(metadata, namespace) {
			if rebuilt, errEncode := json.Marshal(metadata); errEncode == nil {
				h.Set(codexFingerprintHeader, string(rebuilt))
			}
		}
	}
}

func applyCodexAccountIdentityClientMetadata(reqBody map[string]any, namespace string) bool {
	if reqBody == nil || namespace == "" {
		return false
	}
	clientMetadata, _ := reqBody["client_metadata"].(map[string]any)
	originalSessionID := ""
	modified := false
	if clientMetadata != nil {
		originalSessionID, _ = clientMetadata["session_id"].(string)
		if applyCodexAccountIdentityFields(clientMetadata, namespace) {
			modified = true
		}
		if applyCodexAccountIdentityEmbeddedMetadata(clientMetadata, namespace) {
			modified = true
		}
	}
	raw, _ := reqBody["prompt_cache_key"].(string)
	if strings.TrimSpace(raw) != "" {
		kind := "prompt-cache"
		if strings.TrimSpace(originalSessionID) != "" && raw == originalSessionID {
			kind = "session"
		}
		if next := codexAccountIdentityValue(namespace, kind, raw); next != raw {
			reqBody["prompt_cache_key"] = next
			modified = true
		}
	}
	return modified
}

func (e *CodexIdentityExperiment) currentPolicy() codexRestrictionPolicy {
	if e == nil || e.settings == nil {
		return codexRestrictionPolicy{}
	}
	settings := e.settings.CodexIdentity()
	policy := codexRestrictionPolicyFromSettings(settings)
	policy.OutboundConvergenceEnabled = settings.OutboundConvergenceEnabled
	policy.IngressGateEnabled = settings.IngressGateEnabled
	return policy
}

func (e *CodexIdentityExperiment) accountGate(ctx context.Context, authIndex string) codexAccountWithMetadata {
	if e == nil || e.accounts == nil || strings.TrimSpace(authIndex) == "" {
		return codexAccountWithMetadata{}
	}
	response, err := e.accounts.List(ctx, ListQuery{Page: 1, PageSize: maxPageSize, Filters: AccountFilters{}})
	if err != nil {
		return codexAccountWithMetadata{}
	}
	for _, account := range response.Accounts {
		if account.AuthID != authIndex && account.ID != authIndex {
			continue
		}
		metadata, err := e.accounts.CurrentAuthDocument(ctx, account)
		if err != nil {
			return codexAccountWithMetadata{}
		}
		selected := account
		return codexAccountWithMetadata{
			account: &selected,
			codexAccountGateState: codexAccountGateState{
				codexCLIOnly:          codexExtraBool(metadata.Metadata["codex_cli_only"]),
				codexCLIOnlyAppServer: codexExtraBool(metadata.Metadata["codex_cli_only_allow_app_server"]),
			},
		}
	}
	return codexAccountWithMetadata{}
}

func (e *CodexIdentityExperiment) accountRequiresIngressGate(ctx context.Context) bool {
	if e == nil || e.accounts == nil {
		return false
	}
	provider := restrictedCodexAccountProvider{accounts: e.accounts}
	return provider.requiresIngressGate(ctx)
}

func (p restrictedCodexAccountProvider) requiresIngressGate(ctx context.Context) bool {
	response, err := p.accounts.List(ctx, ListQuery{Page: 1, PageSize: maxPageSize})
	if err != nil {
		return true
	}
	for _, account := range response.Accounts {
		metadata, err := p.accounts.CurrentAuthDocument(ctx, account)
		if err != nil {
			return true
		}
		if codexExtraBool(metadata.Metadata["codex_cli_only"]) || codexExtraBool(metadata.Metadata["codex_cli_only_allow_app_server"]) {
			return true
		}
	}
	return false
}

// codexExtraBool accepts only JSON booleans. String aliases are intentionally
// rejected so an accidental credential field cannot enable a security gate.
func codexExtraBool(value any) bool {
	enabled, ok := value.(bool)
	return ok && enabled
}

func (e *CodexIdentityExperiment) reject(result codexRestrictionDetectionResult) cpaapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": codexRestrictionMessage(result)}})
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusForbidden,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    body,
	}
}

// applyAIProviderProbeFingerprint converges the AI Providers page's Codex
// endpoint test with the real forwarded request. Probe requests are GETs and
// therefore have no client session/thread; only device identity applies.
func (a *App) applyAIProviderCodexFingerprint(headers http.Header, kind, authID string) {
	if a == nil || headers == nil || normalizeAIProviderKind(kind) != "codex-api-key" ||
		a.requestHooks == nil || !a.requestHooks.Active() {
		return
	}
	codexIdentity := findCodexIdentityTransformer(a.requestHooks)
	if codexIdentity == nil {
		return
	}
	settings := codexIdentity.settings.codexIdentitySnapshot()
	if !settings.OutboundConvergenceEnabled {
		return
	}
	mode := effectiveCodexFingerprintMode(settings.ConvergenceMode)
	if mode == codexFingerprintOff || strings.TrimSpace(authID) == "" {
		return
	}
	account := Account{ID: strings.TrimSpace(authID), Provider: normalizeAIProviderKind(kind)}
	if seed, ok := resolveCodexFingerprintSeed(context.Background(), a.accounts, account); ok {
		applyCodexFingerprintHeaders(headers, resolveCodexFingerprintIDs(account, seed, "", mode))
	}
}

func findCodexIdentityTransformer(hook *RequestHook) *CodexIdentityExperiment {
	if hook == nil {
		return nil
	}
	hook.mu.RLock()
	defer hook.mu.RUnlock()
	for _, transformer := range hook.transformers {
		if experiment, ok := transformer.(*CodexIdentityExperiment); ok {
			return experiment
		}
	}
	return nil
}
