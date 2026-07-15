package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

type AuthHost interface {
	ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error)
	GetAuth(context.Context, string) (cpaapi.HostAuthGetResponse, error)
	SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error)
}

type UsageSnapshotReader interface {
	Snapshot(string) *AccountUsageSnapshot
}

type AccountService struct {
	host  AuthHost
	usage UsageSnapshotReader
}

type ResolvedTargets struct {
	Accounts      []Account
	MissingIDs    []string
	PhysicalFiles int
}

func NewAccountService(host AuthHost, usage ...UsageSnapshotReader) *AccountService {
	service := &AccountService{host: host}
	if len(usage) > 0 {
		service.usage = usage[0]
	}
	return service
}

func (s *AccountService) List(ctx context.Context, query ListQuery) (ListResponse, error) {
	accounts, errAccounts := s.baseAccounts(ctx)
	if errAccounts != nil {
		return ListResponse{}, errAccounts
	}
	if strings.TrimSpace(query.Filters.Editability) != "" {
		s.enrichEditableAccounts(ctx, accounts)
	}
	accounts = filterAccounts(accounts, query.Filters)
	sortAccounts(accounts)

	page, pageSize := normalizePage(query.Page, query.PageSize)
	total := len(accounts)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageAccounts := append([]Account(nil), accounts[start:end]...)
	s.enrichEditableAccounts(ctx, pageAccounts)

	pages := 0
	if total > 0 {
		pages = (total + pageSize - 1) / pageSize
	}
	return ListResponse{
		Accounts: pageAccounts,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Pages:    pages,
	}, nil
}

func (s *AccountService) Export(ctx context.Context, filters AccountFilters) ([]Account, error) {
	accounts, errAccounts := s.baseAccounts(ctx)
	if errAccounts != nil {
		return nil, errAccounts
	}
	s.enrichEditableAccounts(ctx, accounts)
	accounts = filterAccounts(accounts, filters)
	sortAccounts(accounts)
	return accounts, nil
}

func (s *AccountService) ResolveTargets(ctx context.Context, scope TargetScope) (ResolvedTargets, error) {
	accounts, errAccounts := s.baseAccounts(ctx)
	if errAccounts != nil {
		return ResolvedTargets{}, errAccounts
	}

	resolved := make([]Account, 0, len(accounts))
	missing := make([]string, 0)
	if scope.Mode == "selected" {
		byID := make(map[string]Account, len(accounts))
		for _, account := range accounts {
			byID[account.ID] = account
		}
		for _, id := range scope.IDs {
			account, exists := byID[id]
			if !exists {
				missing = append(missing, id)
				continue
			}
			resolved = append(resolved, account)
		}
	} else {
		if strings.TrimSpace(scope.Filters.Editability) != "" {
			s.enrichEditableAccounts(ctx, accounts)
		}
		resolved = filterAccounts(accounts, scope.Filters)
		sortAccounts(resolved)
	}

	s.enrichEditableAccounts(ctx, resolved)
	paths := make(map[string]struct{}, len(resolved))
	for index := range resolved {
		account := &resolved[index]
		if !account.Editable || account.path == "" {
			continue
		}
		if _, duplicate := paths[account.path]; duplicate {
			account.Editable = false
			account.ReadOnlyReason = "target resolves to a duplicate physical auth file"
			continue
		}
		paths[account.path] = struct{}{}
	}
	return ResolvedTargets{
		Accounts:      resolved,
		MissingIDs:    missing,
		PhysicalFiles: len(paths),
	}, nil
}

func (s *AccountService) CurrentRevision(ctx context.Context, account Account) (string, error) {
	if s == nil || s.host == nil {
		return "", fmt.Errorf("auth host is unavailable")
	}
	detail, errGet := s.host.GetAuth(ctx, account.ID)
	if errGet != nil {
		return "", fmt.Errorf("read physical auth file: %w", errGet)
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 || !json.Valid(raw) {
		return "", fmt.Errorf("physical auth file is invalid")
	}
	if currentPath := normalizedPath(detail.Path); account.path != "" && currentPath != "" && currentPath != account.path {
		return "", fmt.Errorf("physical auth source changed")
	}
	return revisionFor(raw), nil
}

func (s *AccountService) baseAccounts(ctx context.Context) ([]Account, error) {
	if s == nil || s.host == nil {
		return nil, fmt.Errorf("auth host is unavailable")
	}
	entries, errList := s.host.ListAuth(ctx)
	if errList != nil {
		return nil, fmt.Errorf("list host auth records: %w", errList)
	}
	pathCounts := make(map[string]int)
	indexCounts := make(map[string]int)
	for _, entry := range entries {
		if path := normalizedPath(entry.Path); path != "" {
			pathCounts[path]++
		}
		if authIndex := strings.TrimSpace(entry.AuthIndex); authIndex != "" {
			indexCounts[authIndex]++
		}
	}
	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		accounts = append(accounts, projectHostEntry(entry, pathCounts, indexCounts, s.usage))
	}
	return accounts, nil
}

func (s *AccountService) enrichEditableAccounts(ctx context.Context, accounts []Account) {
	for index := range accounts {
		if !accounts[index].Editable || accounts[index].revision != "" {
			continue
		}
		detail, errGet := s.host.GetAuth(ctx, accounts[index].ID)
		if errGet != nil {
			accounts[index].Editable = false
			accounts[index].ReadOnlyReason = "physical auth file is unavailable"
			continue
		}
		if errEnrich := enrichAccount(&accounts[index], detail); errEnrich != nil {
			accounts[index].Editable = false
			accounts[index].ReadOnlyReason = "physical auth file is invalid"
		}
	}
}

func filterAccounts(accounts []Account, filters AccountFilters) []Account {
	filtered := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if matchesFilters(account, filters) {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

func sortAccounts(accounts []Account) {
	sort.Slice(accounts, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(accounts[i].Label, accounts[i].Email, accounts[i].Name, accounts[i].ID))
		right := strings.ToLower(firstNonEmpty(accounts[j].Label, accounts[j].Email, accounts[j].Name, accounts[j].ID))
		if left == right {
			return accounts[i].ID < accounts[j].ID
		}
		return left < right
	})
}

func projectHostEntry(entry cpaapi.HostAuthFileEntry, pathCounts, indexCounts map[string]int, usage UsageSnapshotReader) Account {
	provider := strings.TrimSpace(firstNonEmpty(entry.Provider, entry.Type))
	authIndex := strings.TrimSpace(entry.AuthIndex)
	account := Account{
		ID:            authIndex,
		AuthID:        strings.TrimSpace(entry.ID),
		Name:          strings.TrimSpace(entry.Name),
		Provider:      provider,
		Type:          strings.TrimSpace(entry.Type),
		Label:         strings.TrimSpace(entry.Label),
		Email:         strings.TrimSpace(entry.Email),
		ProjectID:     strings.TrimSpace(entry.ProjectID),
		AccountType:   strings.TrimSpace(entry.AccountType),
		Status:        strings.TrimSpace(entry.Status),
		StatusMessage: safeStatusMessage(entry.StatusMessage),
		Disabled:      entry.Disabled,
		Unavailable:   entry.Unavailable,
		RuntimeOnly:   entry.RuntimeOnly,
		Source:        strings.TrimSpace(entry.Source),
		Note:          strings.TrimSpace(entry.Note),
		Success:       entry.Success,
		Failed:        entry.Failed,
		path:          normalizedPath(entry.Path),
	}
	if len(entry.RecentRequests) > 0 {
		account.RecentRequests = make([]RecentRequestEntry, 0, len(entry.RecentRequests))
		for _, recent := range entry.RecentRequests {
			account.RecentRequests = append(account.RecentRequests, RecentRequestEntry{
				Time:    strings.TrimSpace(recent.Time),
				Success: recent.Success,
				Failed:  recent.Failed,
			})
		}
	}
	if !entry.NextRetryAfter.IsZero() {
		nextRetryAfter := entry.NextRetryAfter.UTC()
		account.NextRetryAfter = &nextRetryAfter
	}
	if usage != nil && authIndex != "" {
		account.Usage = usage.Snapshot(authIndex)
	}
	if !entry.UpdatedAt.IsZero() {
		updatedAt := entry.UpdatedAt
		account.UpdatedAt = &updatedAt
	}
	if !entry.LastRefresh.IsZero() {
		lastRefresh := entry.LastRefresh
		account.LastRefresh = &lastRefresh
	}
	if account.ID == "" {
		account.ID = firstNonEmpty(account.AuthID, account.Name)
	}
	if entry.Priority != 0 {
		priority := entry.Priority
		account.Priority = &priority
	}
	websockets := entry.Websockets
	if entry.Websockets {
		account.Websockets = &websockets
	}

	switch {
	case account.RuntimeOnly:
		account.ReadOnlyReason = "runtime-only account has no physical auth file"
	case account.path == "" || !strings.EqualFold(account.Source, "file"):
		account.ReadOnlyReason = "account is not backed by an editable auth file"
	case authIndex == "":
		account.ReadOnlyReason = "account has no stable auth index"
	case indexCounts[authIndex] > 1:
		account.ReadOnlyReason = "multiple runtime accounts share this auth index"
	case !safeAuthJSONName(account.Name):
		account.ReadOnlyReason = "backing auth file name is invalid"
	case pathCounts[account.path] > 1:
		account.ReadOnlyReason = "multiple runtime accounts share this source file"
	default:
		account.Editable = true
	}
	return account
}

func enrichAccount(account *Account, detail cpaapi.HostAuthGetResponse) error {
	if account == nil {
		return fmt.Errorf("account is nil")
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 {
		return fmt.Errorf("auth json is empty")
	}
	if !json.Valid(raw) {
		return fmt.Errorf("auth json is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	metadata := make(map[string]any)
	if errDecode := decoder.Decode(&metadata); errDecode != nil {
		return fmt.Errorf("decode auth json: %w", errDecode)
	}
	account.path = normalizedPath(firstNonEmpty(detail.Path, account.path))
	account.revision = revisionFor(raw)
	if prefix, ok := metadata["prefix"].(string); ok {
		account.Prefix = strings.TrimSpace(prefix)
	}
	if proxyURL, ok := metadata["proxy_url"].(string); ok && strings.TrimSpace(proxyURL) != "" {
		account.ProxyConfigured = true
		account.Proxy = redactProxyURL(proxyURL)
	}
	if priority, ok := intValue(metadata["priority"]); ok {
		account.Priority = &priority
	}
	if note, ok := metadata["note"].(string); ok {
		account.Note = strings.TrimSpace(note)
	}
	if websockets, ok := boolValue(metadata["websockets"]); ok {
		account.Websockets = &websockets
	}
	account.HeaderNames = safeHeaderNames(metadata["headers"])
	account.HeaderCount = len(account.HeaderNames)
	return nil
}

func matchesFilters(account Account, filters AccountFilters) bool {
	if value := strings.TrimSpace(filters.Provider); value != "" && !strings.EqualFold(account.Provider, value) {
		return false
	}
	if value := strings.TrimSpace(filters.Type); value != "" && !strings.EqualFold(account.Type, value) && !strings.EqualFold(account.AccountType, value) {
		return false
	}
	if value := strings.TrimSpace(filters.Status); value != "" && !strings.EqualFold(account.Status, value) {
		return false
	}
	if filters.Disabled != nil && account.Disabled != *filters.Disabled {
		return false
	}
	if value := strings.TrimSpace(filters.Source); value != "" && !strings.EqualFold(account.Source, value) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(filters.Editability)) {
	case "editable":
		if !account.Editable {
			return false
		}
	case "read_only", "readonly":
		if account.Editable {
			return false
		}
	}
	search := strings.ToLower(strings.TrimSpace(filters.Search))
	if search == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		account.ID,
		account.Name,
		account.Provider,
		account.Type,
		account.Label,
		account.Email,
		account.ProjectID,
		account.AccountType,
		account.Status,
		account.Note,
	}, "\n"))
	return strings.Contains(haystack, search)
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func normalizedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func safeAuthJSONName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && filepath.Base(name) == name && strings.EqualFold(filepath.Ext(name), ".json")
}

func revisionFor(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func redactProxyURL(raw string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "configured"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String()
}

func safeStatusMessage(raw string) string {
	message := strings.ToLower(strings.TrimSpace(raw))
	if message == "" {
		return ""
	}
	switch message {
	case "unauthorized",
		"payment_required",
		"not_found",
		"quota exhausted",
		"transient upstream error",
		"request failed",
		"cloudflare challenge",
		"invalid_grant",
		"disabled via management api",
		"removed via management api",
		"upstream temporarily unavailable":
		return message
	default:
		return "provider reported an account error"
	}
}

func safeHeaderNames(value any) []string {
	metadata, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(metadata))
	for rawName := range metadata {
		name := strings.TrimSpace(rawName)
		if validHeaderName(name) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if char > unicode.MaxASCII {
			return false
		}
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		switch char {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, errParse := typed.Int64()
		return int(parsed), errParse == nil
	case string:
		parsed, errParse := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, errParse == nil
	default:
		return 0, false
	}
}

func boolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, errParse == nil
	default:
		return false, false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
