package manager

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// convertNativeCPAImportRecord converts file-backed CPA provider records. The
// converter deliberately allow-lists fields because import input can be an
// arbitrary JSON document supplied by an untrusted user.
func convertNativeCPAImportRecord(record map[string]any, sourceName, sourcePath string) (importCandidate, bool, error) {
	rawProvider := firstImportString(record, []string{"provider"})
	explicitProvider := rawProvider != ""
	if rawProvider == "" {
		rawProvider = firstImportString(record, []string{"type"})
	}
	provider := normalizeNativeCPAProvider(rawProvider)
	if provider == "codex" {
		return importCandidate{}, false, nil
	}
	if provider == "" {
		if explicitProvider && nativeRecordHasCredentialMaterial(record) {
			return importCandidate{}, true, fmt.Errorf("unsupported CPA Auth provider %q", rawProvider)
		}
		return importCandidate{}, false, nil
	}

	credentials := importObjectAt(record, "credentials")
	if credentials == nil {
		credentials = importObjectAt(record, "credential")
	}
	token := importObjectAt(record, "token")
	accessToken := firstNativeString(record, credentials, token, "access_token", "accessToken")
	refreshToken := firstNativeString(record, credentials, token, "refresh_token", "refreshToken")
	email := firstNativeString(record, credentials, token, "email", "outlook_email")
	name := firstNativeString(record, credentials, token, "name", "label")
	accountID := firstNativeString(record, credentials, token, "account_id", "accountId", "chatgpt_account_id")
	serviceAccount, hasServiceAccount := firstNativeValue(record, credentials, nil, "service_account")
	if !hasServiceAccount {
		serviceAccount, hasServiceAccount = firstNativeValue(record, credentials, nil, "serviceAccount")
	}
	if provider == "vertex" {
		if serviceAccountObject, ok := serviceAccount.(map[string]any); ok {
			if email == "" {
				email = firstNativeString(serviceAccountObject, nil, nil, "client_email", "email")
			}
			if accountID == "" {
				accountID = firstNativeString(serviceAccountObject, nil, nil, "project_id")
			}
		}
	}
	if name == "" {
		name = firstNonEmptyImportString(email, accountID, strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName)), "Imported "+provider+" account")
	}

	document := map[string]any{"type": nativeOutputType(record, provider)}
	copyNativeString(document, "access_token", accessToken)
	copyNativeString(document, "refresh_token", refreshToken)
	copyNativeString(document, "email", email)
	copyNativeString(document, "name", name)
	copyNativeString(document, "account_id", accountID)
	copyNativeString(document, "id_token", firstNativeString(record, credentials, token, "id_token", "idToken"))
	copyNativeString(document, "last_refresh", firstNativeString(record, credentials, token, "last_refresh", "lastRefresh"))
	copyNativeString(document, "expired", firstNativeString(record, credentials, token, "expired", "expires_at", "expiresAt"))
	if provider == "vertex" {
		projectID := firstNativeString(record, credentials, nil, "project_id")
		if projectID == "" {
			if serviceAccountObject, ok := serviceAccount.(map[string]any); ok {
				projectID = firstNativeString(serviceAccountObject, nil, nil, "project_id")
			}
		}
		copyNativeString(document, "project_id", projectID)
	}

	for _, key := range nativeProviderFields(provider) {
		if value, ok := firstNativeValue(record, credentials, token, key); ok {
			document[key] = value
		}
	}
	if provider == "vertex" {
		if hasServiceAccount {
			document["service_account"] = serviceAccount
		}
	}
	if provider == "gemini-cli" && token != nil {
		document["token"] = selectGeminiTokenFields(token)
	}
	copyImportConfiguration(record, document)

	if errValidate := validateNativeCPARecord(provider, accessToken, record, credentials, document); errValidate != nil {
		return importCandidate{}, true, errValidate
	}
	warnings := []string(nil)
	if refreshToken == "" && provider != "vertex" {
		warnings = append(warnings, "refresh token is missing")
	}
	raw, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return importCandidate{}, true, fmt.Errorf("encode converted %s auth JSON: %w", provider, errMarshal)
	}
	identity := firstNonEmptyImportString(email, accountID, name)
	secret := firstNonEmptyImportString(accessToken, refreshToken, nativeServiceAccountFingerprint(document))
	fingerprintSum := sha256.Sum256([]byte(provider + "\x00" + identity + "\x00" + secret))
	return importCandidate{
		SourceName: sourceName, SourcePath: sourcePath, Provider: provider, CredentialType: provider,
		Email: email, AccountID: accountID, Name: name, Warnings: warnings, AuthJSON: raw,
		fingerprint: base64.RawURLEncoding.EncodeToString(fingerprintSum[:]),
	}, true, nil
}

func normalizeNativeCPAProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "anthropic":
		return "claude"
	case "antigravity":
		return "antigravity"
	case "kimi":
		return "kimi"
	case "xai", "grok":
		return "xai"
	case "vertex":
		return "vertex"
	case "gemini", "gemini-cli":
		return "gemini-cli"
	case "codex", "openai":
		return "codex"
	case "oauth", "account", "accounts", "credential", "credentials", "bearer", "sub2api", "json", "api_key", "apikey", "empty", "":
		return ""
	default:
		return ""
	}
}

func nativeOutputType(record map[string]any, provider string) string {
	if provider == "gemini-cli" && strings.EqualFold(firstImportString(record, []string{"type"}), "gemini") {
		return "gemini"
	}
	return provider
}

func nativeProviderFields(provider string) []string {
	switch provider {
	case "claude":
		return []string{"id_token", "last_refresh", "expired"}
	case "antigravity":
		return []string{"project_id", "expires_in", "timestamp", "expired"}
	case "kimi":
		return []string{"token_type", "scope", "device_id", "expired"}
	case "xai":
		return []string{"token_type", "expires_in", "sub", "base_url", "redirect_uri", "token_endpoint", "auth_kind", "last_refresh", "expired"}
	case "gemini-cli":
		return []string{"project_id", "project_ids", "token_type", "expiry", "expires_in", "timestamp"}
	case "vertex":
		return []string{"project_id", "email", "location", "prefix"}
	default:
		return nil
	}
}

func validateNativeCPARecord(provider, accessToken string, record, credentials, document map[string]any) error {
	if provider == "vertex" {
		serviceAccount, ok := document["service_account"].(map[string]any)
		if !ok || firstNativeString(serviceAccount, nil, nil, "project_id") == "" || firstNativeString(serviceAccount, nil, nil, "client_email") == "" || firstNativeString(serviceAccount, nil, nil, "private_key") == "" {
			return fmt.Errorf("vertex Auth record is missing service account project_id, client_email, or private_key")
		}
		return nil
	}
	if accessToken == "" {
		return fmt.Errorf("%s Auth record is missing access token", provider)
	}
	if provider == "antigravity" && firstNativeString(record, credentials, nil, "project_id") == "" {
		return fmt.Errorf("antigravity Auth record is missing project_id")
	}
	if provider == "gemini-cli" {
		if firstNativeString(record, credentials, document, "project_id") == "" {
			if _, ok := firstNativeValue(record, credentials, tokenFromDocument(document), "project_ids"); !ok {
				return fmt.Errorf("gemini-cli Auth record is missing project_id or project_ids")
			}
		}
	}
	return nil
}

func nativeRecordHasCredentialMaterial(record map[string]any) bool {
	if firstImportString(record, []string{"access_token"}, []string{"accessToken"}) != "" || importObjectAt(record, "service_account") != nil {
		return true
	}
	return importObjectAt(record, "credentials") != nil || importObjectAt(record, "credential") != nil || importObjectAt(record, "token") != nil
}

func firstNativeString(record, credentials, token map[string]any, keys ...string) string {
	for _, object := range []map[string]any{record, credentials, token} {
		if object == nil {
			continue
		}
		for _, key := range keys {
			if value := importScalarString(object[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func firstNativeValue(record, credentials, token map[string]any, key string) (any, bool) {
	for _, object := range []map[string]any{record, credentials, token} {
		if object == nil {
			continue
		}
		if value, ok := object[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func copyNativeString(destination map[string]any, key, value string) {
	setImportString(destination, key, value)
}

func selectGeminiTokenFields(token map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"access_token", "refresh_token", "token_type", "expiry"} {
		if value, ok := token[key]; ok {
			result[key] = value
		}
	}
	return result
}

func tokenFromDocument(document map[string]any) map[string]any {
	token, _ := document["token"].(map[string]any)
	return token
}

func nativeServiceAccountFingerprint(document map[string]any) string {
	serviceAccount, _ := document["service_account"].(map[string]any)
	if serviceAccount == nil {
		return ""
	}
	return firstNativeString(serviceAccount, nil, nil, "client_email", "private_key_id", "private_key")
}
