package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	CredentialExportFormatCPA          = "cpa"
	CredentialExportFormatSub2API      = "sub2api"
	CredentialExportFormatCockpit      = "cockpit"
	CredentialExportFormat9Router      = "9router"
	CredentialExportFormatCodex        = "codex"
	CredentialExportFormatAxonHub      = "axonhub"
	CredentialExportFormatCodexManager = "codexmanager"

	maxCredentialExportAccounts = 500
	maxCredentialExportEntry    = defaultImportMaxEntryBytes
	maxCredentialExportBytes    = defaultImportMaxExpandedBytes
)

var (
	ErrCredentialExportNoAccounts   = errors.New("no file-backed accounts match the current filters")
	ErrCredentialExportNoCompatible = errors.New("no compatible Codex OAuth accounts match the current filters")
	ErrCredentialExportTooLarge     = errors.New("credential export exceeds the configured limit")
)

type credentialExportSource struct {
	Account Account
	Object  map[string]any
}

type credentialExportCollection struct {
	Sources []credentialExportSource
	Skipped int
}

type credentialExportRecord struct {
	AccessToken      string
	RefreshToken     string
	IDToken          string
	InputIDToken     string
	Email            string
	AccountID        string
	ChatGPTAccountID string
	WorkspaceID      string
	UserID           string
	PlanType         string
	Name             string
	ExpiresAt        string
	AccessExpiresAt  int64
	LastRefresh      string
	AuthProvider     string
	AccountNote      string
	TestStatus       string
	Priority         int64
	IsActive         bool
	CreatedAt        string
	UpdatedAt        string
}

func credentialExportFormatFromValues(values map[string][]string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(firstQuery(values, "format")))
	switch format {
	case "":
		return "", fmt.Errorf("format is required: cpa, sub2api, cockpit, 9router, codex, axonhub, or codexmanager")
	case "codex-manager", "codex_manager":
		format = CredentialExportFormatCodexManager
	case "9-router", "nine-router", "ninerouter":
		format = CredentialExportFormat9Router
	}
	switch format {
	case CredentialExportFormatCPA, CredentialExportFormatSub2API, CredentialExportFormatCockpit,
		CredentialExportFormat9Router, CredentialExportFormatCodex, CredentialExportFormatAxonHub,
		CredentialExportFormatCodexManager:
		return format, nil
	default:
		return "", fmt.Errorf("format must be cpa, sub2api, cockpit, 9router, codex, axonhub, or codexmanager")
	}
}

func (s *AccountService) ExportCredentialSources(ctx context.Context, filters AccountFilters) (credentialExportCollection, error) {
	accounts, errAccounts := s.baseAccounts(ctx)
	if errAccounts != nil {
		return credentialExportCollection{}, errAccounts
	}
	accounts = filterAccounts(accounts, filters)
	sortAccounts(accounts)
	if len(accounts) > maxCredentialExportAccounts {
		return credentialExportCollection{}, ErrCredentialExportTooLarge
	}

	collection := credentialExportCollection{Sources: make([]credentialExportSource, 0, len(accounts))}
	var aggregateBytes int64
	for _, account := range accounts {
		if errContext := ctx.Err(); errContext != nil {
			return credentialExportCollection{}, errContext
		}
		if !account.Editable || account.ID == "" {
			collection.Skipped++
			continue
		}
		detail, errGet := s.host.GetAuth(ctx, account.ID)
		if errGet != nil || !credentialDetailMatchesAccount(detail.Name, detail.Path, account) {
			collection.Skipped++
			continue
		}
		raw := bytes.TrimSpace(detail.JSON)
		if len(raw) == 0 {
			collection.Skipped++
			continue
		}
		if int64(len(raw)) > maxCredentialExportEntry || aggregateBytes+int64(len(raw)) > maxCredentialExportBytes {
			return credentialExportCollection{}, ErrCredentialExportTooLarge
		}
		aggregateBytes += int64(len(raw))
		object, errDecode := decodeCredentialJSONObject(raw)
		if errDecode != nil {
			collection.Skipped++
			continue
		}
		collection.Sources = append(collection.Sources, credentialExportSource{
			Account: account,
			Object:  object,
		})
	}
	if len(collection.Sources) == 0 {
		return credentialExportCollection{}, ErrCredentialExportNoAccounts
	}
	return collection, nil
}

func credentialDetailMatchesAccount(name, path string, account Account) bool {
	if normalized := normalizedPath(path); normalized != "" && account.path != "" && normalized != account.path {
		return false
	}
	name = strings.TrimSpace(name)
	return name == "" || (safeAuthJSONName(name) && strings.EqualFold(name, account.Name))
}

func decodeCredentialJSONObject(raw []byte) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("auth JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if errDecode := decoder.Decode(&object); errDecode != nil || object == nil {
		return nil, fmt.Errorf("auth JSON must be an object")
	}
	var extra any
	if errExtra := decoder.Decode(&extra); !errors.Is(errExtra, io.EOF) {
		return nil, fmt.Errorf("auth JSON must contain one object")
	}
	return object, nil
}

func renderCredentialExport(format string, collection credentialExportCollection, now time.Time) (exportDownload, error) {
	now = now.UTC()
	if len(collection.Sources) == 0 {
		return exportDownload{}, ErrCredentialExportNoAccounts
	}
	if format == CredentialExportFormatCPA {
		return renderCPACredentialExport(collection, now)
	}

	records := make([]credentialExportRecord, 0, len(collection.Sources))
	skipped := collection.Skipped
	for _, source := range collection.Sources {
		if !isCodexCredentialSource(source) {
			skipped++
			continue
		}
		record, errConvert := normalizeCredentialExportRecord(source, now)
		if errConvert != nil {
			skipped++
			continue
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return exportDownload{}, ErrCredentialExportNoCompatible
	}

	document, filename, errBuild := buildCredentialTargetDocument(format, records, now)
	if errBuild != nil {
		return exportDownload{}, errBuild
	}
	body, errMarshal := json.MarshalIndent(document, "", "  ")
	if errMarshal != nil {
		return exportDownload{}, fmt.Errorf("encode credential export: %w", errMarshal)
	}
	body = append(body, '\n')
	if int64(len(body)) > maxCredentialExportBytes {
		return exportDownload{}, ErrCredentialExportTooLarge
	}
	return exportDownload{
		Filename: filename, ContentType: "application/json; charset=utf-8", Body: body,
		Credential: true, Exported: len(records), Skipped: skipped,
	}, nil
}

func isCodexCredentialSource(source credentialExportSource) bool {
	for _, value := range []string{
		source.Account.Provider,
		source.Account.Type,
		firstImportString(source.Object, []string{"type"}, []string{"provider"}),
	} {
		if strings.EqualFold(strings.TrimSpace(value), "codex") {
			return true
		}
	}
	return false
}

func renderCPACredentialExport(collection credentialExportCollection, now time.Time) (exportDownload, error) {
	type archiveEntry struct {
		name string
		body []byte
	}
	entries := make([]archiveEntry, 0, len(collection.Sources))
	usedNames := make(map[string]int, len(collection.Sources))
	var expandedBytes int64
	for index, source := range collection.Sources {
		body, errMarshal := json.MarshalIndent(source.Object, "", "  ")
		if errMarshal != nil {
			return exportDownload{}, fmt.Errorf("encode CPA credential export: %w", errMarshal)
		}
		body = append(body, '\n')
		expandedBytes += int64(len(body))
		if int64(len(body)) > maxCredentialExportEntry || expandedBytes > maxCredentialExportBytes {
			return exportDownload{}, ErrCredentialExportTooLarge
		}
		stem := credentialFileStem(source, index)
		name := uniqueCredentialFilename(stem, usedNames)
		entries = append(entries, archiveEntry{name: name, body: body})
	}
	if len(entries) == 1 {
		return exportDownload{
			Filename: entries[0].name, ContentType: "application/json; charset=utf-8", Body: entries[0].body,
			Credential: true, Exported: 1, Skipped: collection.Skipped,
		}, nil
	}

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetModTime(now)
		header.SetMode(0o600)
		handle, errCreate := writer.CreateHeader(header)
		if errCreate != nil {
			_ = writer.Close()
			return exportDownload{}, fmt.Errorf("create CPA ZIP entry: %w", errCreate)
		}
		if _, errWrite := handle.Write(entry.body); errWrite != nil {
			_ = writer.Close()
			return exportDownload{}, fmt.Errorf("write CPA ZIP entry: %w", errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return exportDownload{}, fmt.Errorf("finish CPA ZIP export: %w", errClose)
	}
	return exportDownload{
		Filename: "cpa-accounts.zip", ContentType: "application/zip", Body: buffer.Bytes(),
		Credential: true, Exported: len(entries), Skipped: collection.Skipped,
	}, nil
}

func normalizeCredentialExportRecord(source credentialExportSource, now time.Time) (credentialExportRecord, error) {
	record := make(map[string]any, len(source.Object)+3)
	for key, value := range source.Object {
		record[key] = value
	}
	if firstImportString(record, []string{"email"}) == "" && source.Account.Email != "" {
		record["email"] = source.Account.Email
	}
	if firstImportString(record, []string{"name"}) == "" {
		record["name"] = firstNonEmpty(source.Account.Label, source.Account.Name)
	}
	candidate, errConvert := convertImportRecord(record, source.Account.Name, "$", now)
	if errConvert != nil {
		return credentialExportRecord{}, errConvert
	}
	cpa, errDecode := decodeCredentialJSONObject(candidate.AuthJSON)
	if errDecode != nil {
		return credentialExportRecord{}, errDecode
	}
	accessToken := firstImportString(cpa, []string{"access_token"})
	refreshToken := firstImportString(cpa, []string{"refresh_token"})
	inputIDToken := firstImportString(record,
		[]string{"idToken"}, []string{"id_token"}, []string{"tokens", "idToken"}, []string{"tokens", "id_token"},
	)
	idToken := firstImportString(cpa, []string{"id_token"})
	accountID := firstImportString(cpa, []string{"account_id"}, []string{"chatgpt_account_id"})
	chatGPTAccountID := firstImportString(cpa, []string{"chatgpt_account_id"}, []string{"account_id"})
	accessPayload := parseImportJWTPayload(accessToken)
	idPayload := parseImportJWTPayload(idToken)
	accessAuth := importObjectAt(accessPayload, "https://api.openai.com/auth")
	idAuth := importObjectAt(idPayload, "https://api.openai.com/auth")
	userID := firstImportString(record,
		[]string{"user", "id"}, []string{"user_id"}, []string{"chatgptUserId"}, []string{"chatgpt_user_id"},
		[]string{"providerSpecificData", "chatgptUserId"}, []string{"providerSpecificData", "chatgpt_user_id"},
	)
	if userID == "" {
		userID = firstStringFromMaps([]map[string]any{accessAuth, idAuth}, "chatgpt_user_id", "user_id")
	}
	workspaceID := firstImportString(record,
		[]string{"account", "workspaceId"}, []string{"account", "workspace_id"},
		[]string{"workspaceId"}, []string{"workspace_id"}, []string{"meta", "workspaceId"}, []string{"meta", "workspace_id"},
		[]string{"providerSpecificData", "workspaceId"}, []string{"providerSpecificData", "workspace_id"},
		[]string{"credentials", "workspace_id"},
	)
	priority, hasPriority := importIntegerAt(record, "priority")
	if !hasPriority {
		priority = 9
	}
	isActive := true
	if active, ok := importBoolAt(record, "isActive"); ok {
		isActive = active
	} else if disabled, ok := importBoolAt(record, "disabled"); ok {
		isActive = !disabled
	}
	expiresAt := firstImportString(cpa, []string{"expired"})
	var accessExpiresAt int64
	if refreshToken == "" {
		accessExpiresAt, _ = importNumber(accessPayload["exp"])
	}
	lastRefresh := now.Format(time.RFC3339Nano)
	createdAt := firstImportTimestamp(record, []string{"createdAt"}, []string{"created_at"})
	if createdAt == "" {
		createdAt = lastRefresh
	}
	updatedAt := firstImportTimestamp(record, []string{"updatedAt"}, []string{"updated_at"})
	if updatedAt == "" {
		updatedAt = lastRefresh
	}
	return credentialExportRecord{
		AccessToken: accessToken, RefreshToken: refreshToken, IDToken: idToken, InputIDToken: inputIDToken,
		Email:     firstImportString(cpa, []string{"email"}),
		AccountID: accountID, ChatGPTAccountID: chatGPTAccountID, WorkspaceID: workspaceID, UserID: userID,
		PlanType:  firstImportString(cpa, []string{"plan_type"}, []string{"chatgpt_plan_type"}),
		Name:      firstNonEmpty(firstImportString(cpa, []string{"name"}), candidate.Name, candidate.Email, source.Account.Name),
		ExpiresAt: expiresAt, AccessExpiresAt: accessExpiresAt, LastRefresh: lastRefresh,
		AuthProvider: firstImportString(record, []string{"authProvider"}, []string{"auth_provider"}),
		AccountNote:  firstImportString(record, []string{"account_note"}, []string{"accountInfo"}, []string{"account_info"}, []string{"note"}, []string{"notes"}, []string{"remark"}),
		TestStatus:   firstNonEmptyImportString(firstImportString(record, []string{"testStatus"}, []string{"test_status"}), "active"),
		Priority:     priority, IsActive: isActive, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func buildCredentialTargetDocument(format string, records []credentialExportRecord, now time.Time) (any, string, error) {
	switch format {
	case CredentialExportFormatSub2API:
		accounts := make([]any, 0, len(records))
		for _, record := range records {
			accounts = append(accounts, sub2APIExportAccount(record, now))
		}
		return map[string]any{"exported_at": now.Format(time.RFC3339Nano), "proxies": []any{}, "accounts": accounts}, "cpa-accounts.sub2api.json", nil
	case CredentialExportFormatCockpit:
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, cockpitExportAccount(record))
		}
		return singleCredentialDocument(items), "cpa-accounts.cockpit.json", nil
	case CredentialExportFormat9Router:
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, nineRouterExportAccount(record, now))
		}
		return singleCredentialDocument(items), "cpa-accounts.9router.json", nil
	case CredentialExportFormatCodex:
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, codexExportAccount(record))
		}
		filename := "cpa-accounts.codex.json"
		if len(items) == 1 {
			filename = "auth.json"
		}
		return singleCredentialDocument(items), filename, nil
	case CredentialExportFormatAxonHub:
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, axonHubExportAccount(record, now))
		}
		return singleCredentialDocument(items), "cpa-accounts.axonhub.json", nil
	case CredentialExportFormatCodexManager:
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, codexManagerExportAccount(record))
		}
		return singleCredentialDocument(items), "cpa-accounts.codex-manager.json", nil
	default:
		return nil, "", fmt.Errorf("unsupported credential export format")
	}
}

func sub2APIExportAccount(record credentialExportRecord, now time.Time) map[string]any {
	credentials := map[string]any{"access_token": record.AccessToken}
	setExportString(credentials, "chatgpt_account_id", record.AccountID)
	setExportString(credentials, "chatgpt_user_id", record.UserID)
	setExportString(credentials, "email", record.Email)
	setExportString(credentials, "expires_at", record.ExpiresAt)
	if expiresIn, ok := credentialExpiresIn(record.ExpiresAt, now); ok {
		credentials["expires_in"] = expiresIn
	}
	setExportString(credentials, "plan_type", record.PlanType)
	extra := make(map[string]any)
	setExportString(extra, "email", record.Email)
	setExportString(extra, "email_key", credentialEmailKey(record.Email))
	setExportString(extra, "name", record.Name)
	setExportString(extra, "auth_provider", record.AuthProvider)
	extra["source"] = "cpa"
	extra["last_refresh"] = record.LastRefresh
	account := map[string]any{
		"name": record.Name, "platform": "openai", "type": "oauth", "concurrency": 10, "priority": 1,
		"credentials": credentials, "extra": extra,
	}
	if record.RefreshToken == "" && record.AccessExpiresAt > 0 {
		account["expires_at"] = record.AccessExpiresAt
		account["auto_pause_on_expired"] = true
	}
	return account
}

func cockpitExportAccount(record credentialExportRecord) map[string]any {
	account := map[string]any{
		"type": "codex", "id_token": record.IDToken, "access_token": record.AccessToken,
		"refresh_token": record.RefreshToken, "last_refresh": record.LastRefresh,
	}
	setExportString(account, "account_id", record.AccountID)
	setExportString(account, "email", record.Email)
	setExportString(account, "expired", record.ExpiresAt)
	setExportString(account, "account_note", record.AccountNote)
	return account
}

func nineRouterExportAccount(record credentialExportRecord, now time.Time) map[string]any {
	providerData := make(map[string]any)
	setExportString(providerData, "chatgptAccountId", record.AccountID)
	setExportString(providerData, "chatgptPlanType", record.PlanType)
	account := map[string]any{
		"accessToken": record.AccessToken, "testStatus": record.TestStatus, "provider": "codex", "authType": "oauth",
		"name": record.Name, "priority": record.Priority, "isActive": record.IsActive,
		"createdAt": record.CreatedAt, "updatedAt": record.UpdatedAt,
	}
	setExportString(account, "refreshToken", record.RefreshToken)
	setExportString(account, "expiresAt", record.ExpiresAt)
	if expiresIn, ok := credentialExpiresIn(record.ExpiresAt, now); ok {
		account["expiresIn"] = expiresIn
	}
	if len(providerData) > 0 {
		account["providerSpecificData"] = providerData
	}
	setExportString(account, "id", record.AccountID)
	setExportString(account, "email", record.Email)
	return account
}

func codexExportAccount(record credentialExportRecord) map[string]any {
	tokens := map[string]any{
		"id_token": record.IDToken, "access_token": record.AccessToken, "refresh_token": record.RefreshToken,
	}
	setExportString(tokens, "account_id", record.AccountID)
	return map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": tokens, "last_refresh": record.LastRefresh,
	}
}

func axonHubExportAccount(record credentialExportRecord, now time.Time) map[string]any {
	refreshToken := record.RefreshToken
	if refreshToken == "" {
		refreshToken = "__missing_refresh_token__"
	}
	account := map[string]any{
		"auth_mode": "chatgpt", "last_refresh": credentialAxonHubLastRefresh(record.ExpiresAt, now),
		"tokens": map[string]any{"access_token": record.AccessToken, "refresh_token": refreshToken, "id_token": record.IDToken},
	}
	if record.RefreshToken == "" {
		account["axonhub_refresh_token_placeholder"] = true
		account["axonhub_note"] = "refresh_token is a placeholder; access_token works only until it expires."
	}
	return account
}

func codexManagerExportAccount(record credentialExportRecord) map[string]any {
	tokens := map[string]any{
		"access_token": record.AccessToken, "refresh_token": record.RefreshToken, "id_token": record.InputIDToken,
	}
	setExportString(tokens, "account_id", record.AccountID)
	setExportString(tokens, "chatgpt_account_id", record.ChatGPTAccountID)
	meta := map[string]any{"label": record.Name, "note": "Exported from CPA Account Config Manager"}
	setExportString(meta, "workspace_id", record.WorkspaceID)
	setExportString(meta, "chatgpt_account_id", record.ChatGPTAccountID)
	return map[string]any{"tokens": tokens, "meta": meta}
}

func singleCredentialDocument(items []any) any {
	if len(items) == 1 {
		return items[0]
	}
	return items
}

func credentialExpiresIn(expiresAt string, now time.Time) (int64, bool) {
	parsed, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(expiresAt))
	if errParse != nil {
		return 0, false
	}
	seconds := int64(parsed.Sub(now).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return seconds, true
}

func credentialAxonHubLastRefresh(expiresAt string, now time.Time) string {
	parsed, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(expiresAt))
	if errParse != nil {
		return now.UTC().Format(time.RFC3339Nano)
	}
	return parsed.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
}

func setExportString(target map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		target[key] = value
	}
}

func credentialEmailKey(email string) string {
	var builder strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(email)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if separator && builder.Len() > 0 {
				builder.WriteByte('_')
			}
			builder.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
	}
	return builder.String()
}

func credentialFileStem(source credentialExportSource, index int) string {
	candidate := firstNonEmpty(
		firstImportString(source.Object, []string{"email"}, []string{"user", "email"}, []string{"credentials", "email"}),
		source.Account.Email, source.Account.Label, strings.TrimSuffix(source.Account.Name, filepath.Ext(source.Account.Name)),
	)
	return sanitizeCredentialFileStem(candidate, fmt.Sprintf("account-%03d", index+1))
}

func sanitizeCredentialFileStem(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for index := 0; index < len(value) && builder.Len() < 96; index++ {
		character := value[index]
		allowed := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("@._+-", rune(character))
		if allowed {
			builder.WriteByte(character)
			lastDash = false
		} else if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	stem := strings.Trim(builder.String(), " .-_")
	if stem == "" || isReservedCredentialStem(stem) {
		stem = fallback
	}
	return stem
}

func isReservedCredentialStem(stem string) bool {
	upper := strings.ToUpper(strings.SplitN(stem, ".", 2)[0])
	if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" {
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		_, errParse := strconv.Atoi(upper[3:])
		return errParse == nil
	}
	return false
}

func uniqueCredentialFilename(stem string, used map[string]int) string {
	for suffix := 1; ; suffix++ {
		candidate := stem
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", stem, suffix)
		}
		key := strings.ToLower(candidate + ".json")
		if used[key] == 0 {
			used[key] = 1
			return candidate + ".json"
		}
	}
}

func clearCredentialExportCollection(collection *credentialExportCollection) {
	if collection == nil {
		return
	}
	for index := range collection.Sources {
		collection.Sources[index].Object = nil
	}
	collection.Sources = nil
}
