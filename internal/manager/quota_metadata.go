package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	codexQuotaUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexQuotaResetConsumeURL = codexQuotaResetCreditsURL + "/consume"
	codexQuotaUserAgent       = "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"
	maxActiveResetCount       = 1_000_000
)

var (
	ErrQuotaMetadataAccountNotFound = errors.New("quota metadata account was not found")
	ErrQuotaMetadataUnsupported     = errors.New("quota metadata is only available for Codex accounts")
	ErrQuotaMetadataUnavailable     = errors.New("CPA quota metadata is unavailable")
	ErrActiveResetUnavailable       = errors.New("no active reset credit is available")
)

type QuotaMetadataRequest struct {
	AccountID string `json:"account_id"`
	Confirm   bool   `json:"confirm,omitempty"`
}

type QuotaMetadataResponse struct {
	AccountID        string    `json:"account_id"`
	PlanType         string    `json:"plan_type,omitempty"`
	ActiveResetCount *int      `json:"active_reset_count,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
	Warning          string    `json:"warning,omitempty"`
	ResetCreditUsed  bool      `json:"reset_credit_used,omitempty"`
}

type quotaMetadata struct {
	planType         string
	activeResetCount *int
	usage            *CodexUsageSnapshot
	warning          string
}

func (a *App) handleAccountQuotaMetadata(ctx context.Context, req cpaapi.ManagementRequest, consume bool) cpaapi.ManagementResponse {
	startedAt := time.Now().UTC()
	var request QuotaMetadataRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var response QuotaMetadataResponse
	var errOperation error
	if consume {
		response, errOperation = a.handleQuotaMetadataReset(ctx, request, managementKey)
	} else {
		response, errOperation = a.handleQuotaMetadataRefresh(ctx, request, managementKey)
	}
	managementKey = ""
	a.recordQuotaMetadataOperation(request.AccountID, consume, response, errOperation, startedAt)
	if errOperation == nil {
		return jsonResponse(http.StatusOK, response)
	}
	switch {
	case errors.Is(errOperation, ErrQuotaMetadataAccountNotFound):
		return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrQuotaMetadataAccountNotFound.Error()})
	case errors.Is(errOperation, ErrQuotaMetadataUnsupported):
		return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrQuotaMetadataUnsupported.Error()})
	case errors.Is(errOperation, ErrActiveResetUnavailable):
		return jsonResponse(http.StatusConflict, map[string]any{"error": ErrActiveResetUnavailable.Error()})
	case errors.Is(errOperation, ErrQuotaMetadataUnavailable):
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": ErrQuotaMetadataUnavailable.Error()})
	default:
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errOperation.Error()})
	}
}

func (a *App) recordQuotaMetadataOperation(accountID string, consume bool, response QuotaMetadataResponse, operationError error, startedAt time.Time) {
	action := OperationActionQuotaMetadataRefresh
	if consume {
		action = OperationActionActiveReset
	}
	status := OperationStatusSucceeded
	succeeded := 1
	failed := 0
	reason := "completed"
	if operationError != nil {
		status = OperationStatusFailed
		succeeded = 0
		failed = 1
		reason = "operation_failed"
	} else if response.Warning != "" {
		status = OperationStatusWarning
		reason = response.Warning
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: action, Status: status, Source: OperationSourceManual,
		Scope: OperationScopeSingle, TargetID: safeOperationIdentifier(accountID, 256), TargetCount: 1,
		Succeeded: succeeded, Failed: failed, StartedAt: startedAt, FinishedAt: time.Now().UTC(), ReasonCode: reason,
	})
}

func (a *App) handleQuotaMetadataRefresh(ctx context.Context, request QuotaMetadataRequest, managementKey string) (QuotaMetadataResponse, error) {
	account, client, errResolve := a.resolveQuotaMetadataTarget(ctx, request.AccountID, managementKey)
	if errResolve != nil {
		return QuotaMetadataResponse{}, errResolve
	}
	defer client.clearSecrets()

	metadata, errFetch := fetchQuotaMetadata(ctx, client, account, a.quotaAccountID(ctx, account))
	if errFetch != nil {
		return QuotaMetadataResponse{}, errFetch
	}
	return a.persistQuotaMetadata(account, metadata, false), nil
}

func (a *App) handleQuotaMetadataReset(ctx context.Context, request QuotaMetadataRequest, managementKey string) (QuotaMetadataResponse, error) {
	if !request.Confirm {
		return QuotaMetadataResponse{}, fmt.Errorf("active reset confirmation is required")
	}
	resetLock := a.quotaResetLock(request.AccountID)
	resetLock.Lock()
	defer resetLock.Unlock()
	account, client, errResolve := a.resolveQuotaMetadataTarget(ctx, request.AccountID, managementKey)
	if errResolve != nil {
		return QuotaMetadataResponse{}, errResolve
	}
	defer client.clearSecrets()
	chatGPTAccountID := a.quotaAccountID(ctx, account)

	before, errFetch := fetchQuotaMetadata(ctx, client, account, chatGPTAccountID)
	if errFetch != nil {
		return QuotaMetadataResponse{}, errFetch
	}
	if before.activeResetCount == nil || *before.activeResetCount <= 0 {
		a.persistQuotaMetadata(account, before, false)
		return QuotaMetadataResponse{}, ErrActiveResetUnavailable
	}
	if errConsume := consumeQuotaResetCredit(ctx, client, account, chatGPTAccountID); errConsume != nil {
		return QuotaMetadataResponse{}, errConsume
	}
	after, errRefresh := fetchQuotaMetadata(ctx, client, account, chatGPTAccountID)
	expectedMaximum := *before.activeResetCount - 1
	if errRefresh != nil {
		before.activeResetCount = &expectedMaximum
		before.warning = "quota_metadata_refresh_after_reset_unavailable"
		return a.persistQuotaMetadata(account, before, true), nil
	}
	if after.activeResetCount == nil || *after.activeResetCount > expectedMaximum {
		after.activeResetCount = &expectedMaximum
		after.warning = "quota_metadata_refresh_after_reset_unavailable"
	}
	return a.persistQuotaMetadata(account, after, true), nil
}

func (a *App) quotaResetLock(accountID string) *sync.Mutex {
	var hash uint32 = 2166136261
	for _, value := range []byte(strings.TrimSpace(accountID)) {
		hash ^= uint32(value)
		hash *= 16777619
	}
	return &a.quotaResetLocks[hash%uint32(len(a.quotaResetLocks))]
}

func (a *App) resolveQuotaMetadataTarget(ctx context.Context, accountID, managementKey string) (Account, *managementClient, error) {
	accountID = safeOperationIdentifier(accountID, 256)
	if accountID == "" {
		return Account{}, nil, ErrQuotaMetadataAccountNotFound
	}
	resolved, errResolve := a.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{accountID}})
	if errResolve != nil {
		return Account{}, nil, fmt.Errorf("resolve quota metadata account: %w", errResolve)
	}
	if len(resolved.Accounts) != 1 || len(resolved.MissingIDs) != 0 {
		return Account{}, nil, ErrQuotaMetadataAccountNotFound
	}
	account := resolved.Accounts[0]
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(account.Provider, account.Type)))
	if provider != "codex" && provider != agentIdentityProvider {
		return Account{}, nil, ErrQuotaMetadataUnsupported
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return Account{}, nil, ErrQuotaMetadataUnavailable
	}
	return account, client, nil
}

func (a *App) persistQuotaMetadata(account Account, metadata quotaMetadata, consumed bool) QuotaMetadataResponse {
	observedAt := time.Now().UTC()
	snapshot := cloneCodexUsage(metadata.usage)
	if snapshot == nil {
		snapshot = &CodexUsageSnapshot{}
	}
	snapshot.PlanType = metadata.planType
	snapshot.ActiveResetCount = cloneIntPointer(metadata.activeResetCount)
	snapshot.MetadataObservedAt = observedAt
	a.usage.ObserveCredentialUsage(account.ID, snapshot)
	return QuotaMetadataResponse{
		AccountID:        account.ID,
		PlanType:         firstNonEmpty(metadata.planType, account.PlanType),
		ActiveResetCount: cloneIntPointer(metadata.activeResetCount),
		ObservedAt:       observedAt,
		Warning:          metadata.warning,
		ResetCreditUsed:  consumed,
	}
}

func (a *App) runNewAccountQuotaMetadata(ctx context.Context, account Account, managementKey string) error {
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return ErrQuotaMetadataUnavailable
	}
	defer client.clearSecrets()
	metadata, errFetch := fetchQuotaMetadata(ctx, client, account, a.quotaAccountID(ctx, account))
	if errFetch != nil {
		return errFetch
	}
	a.persistQuotaMetadata(account, metadata, false)
	return nil
}

type accountObserverGroup []interface{ ObserveAccounts([]Account) }

func (group accountObserverGroup) ObserveAccounts(accounts []Account) {
	for _, observer := range group {
		if observer != nil {
			observer.ObserveAccounts(accounts)
		}
	}
}

func fetchQuotaMetadata(ctx context.Context, client *managementClient, account Account, chatGPTAccountID string) (quotaMetadata, error) {
	headers := codexQuotaHeaders(chatGPTAccountID)
	usageResponse, errUsage := client.APICall(ctx, managementAPICallRequest{
		AuthIndex: account.ID, Method: http.MethodGet, URL: codexQuotaUsageURL, Header: headers,
	})
	if errUsage != nil || usageResponse.StatusCode < http.StatusOK || usageResponse.StatusCode >= http.StatusMultipleChoices {
		return quotaMetadata{}, ErrQuotaMetadataUnavailable
	}
	planType, usageCount, validUsage := parseQuotaUsageMetadata(usageResponse.Body)
	if !validUsage {
		return quotaMetadata{}, ErrQuotaMetadataUnavailable
	}
	metadata := quotaMetadata{
		planType:         planType,
		activeResetCount: usageCount,
		usage:            codexUsageProbeSnapshot(usageResponse.Body, time.Now().UTC()),
	}

	resetHeaders := cloneStringMap(headers)
	resetHeaders["Accept"] = "application/json"
	resetHeaders["OpenAI-Beta"] = "codex-1"
	resetHeaders["Originator"] = "Codex Desktop"
	resetResponse, errReset := client.APICall(ctx, managementAPICallRequest{
		AuthIndex: account.ID, Method: http.MethodGet, URL: codexQuotaResetCreditsURL, Header: resetHeaders,
	})
	if errReset != nil || resetResponse.StatusCode < http.StatusOK || resetResponse.StatusCode >= http.StatusMultipleChoices {
		if metadata.activeResetCount == nil {
			metadata.warning = "active_reset_count_unavailable"
		}
		return metadata, nil
	}
	if count, validReset := parseQuotaResetCredits(resetResponse.Body); validReset {
		if count != nil {
			metadata.activeResetCount = count
		}
	} else if metadata.activeResetCount == nil {
		metadata.warning = "active_reset_count_unavailable"
	}
	return metadata, nil
}

func consumeQuotaResetCredit(ctx context.Context, client *managementClient, account Account, chatGPTAccountID string) error {
	requestID, errID := newQuotaResetRequestID()
	if errID != nil {
		return fmt.Errorf("create active reset request: %w", errID)
	}
	body, errMarshal := json.Marshal(map[string]string{"redeem_request_id": requestID})
	if errMarshal != nil {
		return fmt.Errorf("encode active reset request: %w", errMarshal)
	}
	response, errCall := client.APICall(ctx, managementAPICallRequest{
		AuthIndex: account.ID,
		Method:    http.MethodPost,
		URL:       codexQuotaResetConsumeURL,
		Header:    codexQuotaHeaders(chatGPTAccountID),
		Data:      string(body),
	})
	if errCall != nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("CPA active reset failed")
	}
	return nil
}

func codexQuotaHeaders(chatGPTAccountID string) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    codexQuotaUserAgent,
	}
	if chatGPTAccountID != "" {
		headers["Chatgpt-Account-Id"] = chatGPTAccountID
	}
	return headers
}

func parseQuotaUsageMetadata(raw []byte) (string, *int, bool) {
	object, ok := decodeQuotaObject(raw)
	if !ok {
		return "", nil, false
	}
	planType := quotaPlanType(firstMapValue(object, "plan_type", "planType"))
	credits, _ := firstMapValue(object, "rate_limit_reset_credits", "rateLimitResetCredits").(map[string]any)
	count := quotaCount(firstMapValue(credits, "available_count", "availableCount"))
	return planType, count, true
}

func parseQuotaResetCredits(raw []byte) (*int, bool) {
	object, ok := decodeQuotaObject(raw)
	if !ok {
		return nil, false
	}
	_, hasCredits := object["credits"]
	_, hasSnakeCount := object["available_count"]
	_, hasCamelCount := object["availableCount"]
	if !hasCredits && !hasSnakeCount && !hasCamelCount {
		return nil, false
	}
	if count := quotaCount(firstMapValue(object, "available_count", "availableCount")); count != nil {
		return count, true
	}
	credits, _ := object["credits"].([]any)
	valid := 0
	for _, candidate := range credits {
		credit, _ := candidate.(map[string]any)
		if quotaString(firstMapValue(credit, "reset_type", "resetType")) != "codex_rate_limits" ||
			quotaString(credit["status"]) != "available" ||
			quotaString(firstMapValue(credit, "expires_at", "expiresAt")) == "" {
			continue
		}
		valid++
		if valid > maxActiveResetCount {
			return nil, false
		}
	}
	if valid > 0 {
		return &valid, true
	}
	return nil, true
}

func decodeQuotaObject(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var object map[string]any
	if errDecode := decoder.Decode(&object); errDecode != nil || object == nil {
		return nil, false
	}
	return object, true
}

func quotaPlanType(value any) string {
	switch typed := value.(type) {
	case json.Number:
		if number, errNumber := typed.Float64(); errNumber == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
			return safeAccountPlanType(typed.String())
		}
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
			return safeAccountPlanType(strconv.FormatFloat(typed, 'f', -1, 64))
		}
	default:
		return safeAccountPlanType(value)
	}
	return ""
}

func quotaCount(value any) *int {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.TrimSpace(typed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return nil
		}
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return nil
	}
	parsed, errParse := strconv.ParseInt(text, 10, 32)
	if errParse != nil || parsed < 0 || parsed > maxActiveResetCount {
		return nil
	}
	count := int(parsed)
	return &count
}

func quotaString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func firstMapValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			return value
		}
	}
	return nil
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func newQuotaResetRequestID() (string, error) {
	raw := make([]byte, 16)
	if _, errRead := rand.Read(raw); errRead != nil {
		return "", errRead
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func (a *App) quotaAccountID(ctx context.Context, account Account) string {
	document, errDocument := a.accounts.CurrentAuthDocument(ctx, account)
	if errDocument != nil {
		return ""
	}
	metadata := document.Metadata
	if accountID := safeQuotaAccountID(firstMapValue(metadata, "account_id", "chatgpt_account_id")); accountID != "" {
		return accountID
	}
	for _, key := range []string{"id_token", "idToken", "access_token", "accessToken"} {
		claims := accountIdentityClaims(metadata[key])
		if accountID := quotaAccountIDFromClaims(claims); accountID != "" {
			return accountID
		}
	}
	return ""
}

func quotaAccountIDFromClaims(claims map[string]any) string {
	if accountID := safeQuotaAccountID(firstMapValue(claims, "chatgpt_account_id", "account_id")); accountID != "" {
		return accountID
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	return safeQuotaAccountID(firstMapValue(auth, "chatgpt_account_id", "account_id"))
}

func safeQuotaAccountID(value any) string {
	valueText, ok := value.(string)
	if !ok {
		return ""
	}
	valueText = strings.TrimSpace(valueText)
	if valueText == "" || len(valueText) > 256 {
		return ""
	}
	for _, char := range valueText {
		if char < 0x21 || char > 0x7e {
			return ""
		}
	}
	return valueText
}
