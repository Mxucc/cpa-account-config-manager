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
	"sync"
	"time"
	"unicode"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	defaultPageSize          = 50
	maxPageSize              = 1000
	accountDetailWorkers     = 8
	maxAccountPlanTypeLength = 64
)

type AuthHost interface {
	ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error)
	GetAuth(context.Context, string) (cpaapi.HostAuthGetResponse, error)
	SaveAuth(context.Context, string, json.RawMessage) (cpaapi.HostAuthSaveResponse, error)
}

type UsageSnapshotReader interface {
	Snapshot(string) *AccountUsageSnapshot
}

type UsageStorageDiscoverer interface {
	DiscoverAuthStorage([]cpaapi.HostAuthFileEntry)
}

type UsageIdentityReader interface {
	UsageIdentity(string) string
}

type AccountLifecycleReader interface {
	AccountLifecycle(string) AccountLifecycleSnapshot
}

type AccountService struct {
	host        AuthHost
	usage       UsageSnapshotReader
	concurrency *AccountConcurrencyService
	observer    interface{ ObserveAccounts([]Account) }
}

func (s *AccountService) SetObserver(observer interface{ ObserveAccounts([]Account) }) {
	if s == nil {
		return
	}
	s.observer = observer
}

func (s *AccountService) SetAccountConcurrency(concurrency *AccountConcurrencyService) {
	if s == nil {
		return
	}
	s.concurrency = concurrency
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
	if filtersRequireAccountDetail(query.Filters) {
		s.enrichAccountDetails(ctx, accounts)
	}
	accounts = filterAccounts(accounts, query.Filters)
	if sortRequiresAccountDetail(query.SortBy) && !filtersRequireAccountDetail(query.Filters) {
		s.enrichAccountDetails(ctx, accounts)
	}
	sortAccountsBy(accounts, query.SortBy, query.SortOrder)

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
	pageAccounts := append([]Account{}, accounts[start:end]...)
	s.enrichAccountDetails(ctx, pageAccounts)

	pages := 0
	if total > 0 {
		pages = (total + pageSize - 1) / pageSize
	}
	return ListResponse{
		Accounts:           pageAccounts,
		Total:              total,
		Page:               page,
		PageSize:           pageSize,
		Pages:              pages,
		AccountConcurrency: s.accountConcurrencyAvailability(),
	}, nil
}

func (s *AccountService) accountConcurrencyAvailability() AccountConcurrencyAvailability {
	if s == nil || s.concurrency == nil {
		return AccountConcurrencyAvailability{RequiredSchemaVersion: cpaapi.SchemaVersion, HostSchemaVersion: cpaapi.LegacySchemaVersion, Reason: "host_schema_v2_required"}
	}
	return s.concurrency.Availability()
}

func (s *AccountService) Export(ctx context.Context, filters AccountFilters) ([]Account, error) {
	accounts, errAccounts := s.baseAccounts(ctx)
	if errAccounts != nil {
		return nil, errAccounts
	}
	s.enrichAccountDetails(ctx, accounts)
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
		if filtersRequireAccountDetail(scope.Filters) {
			s.enrichAccountDetails(ctx, accounts)
		}
		resolved = filterAccounts(accounts, scope.Filters)
		sortAccounts(resolved)
	}

	s.enrichAccountDetails(ctx, resolved)
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
	document, errDocument := s.CurrentAuthDocument(ctx, account)
	if errDocument != nil {
		return "", errDocument
	}
	return document.Revision, nil
}

func (s *AccountService) CurrentAuthDocument(ctx context.Context, account Account) (currentAuthDocument, error) {
	if s == nil || s.host == nil {
		return currentAuthDocument{}, fmt.Errorf("auth host is unavailable")
	}
	detail, errGet := s.host.GetAuth(ctx, account.ID)
	if errGet != nil {
		return currentAuthDocument{}, fmt.Errorf("read physical auth file: %w", errGet)
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 || !json.Valid(raw) {
		return currentAuthDocument{}, fmt.Errorf("physical auth file is invalid")
	}
	if currentPath := normalizedPath(detail.Path); account.path != "" && currentPath != "" && currentPath != account.path {
		return currentAuthDocument{}, fmt.Errorf("physical auth source changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	metadata := make(map[string]any)
	if errDecode := decoder.Decode(&metadata); errDecode != nil {
		return currentAuthDocument{}, fmt.Errorf("decode physical auth file: %w", errDecode)
	}
	return currentAuthDocument{Revision: revisionFor(raw), Metadata: metadata}, nil
}

func (s *AccountService) baseAccounts(ctx context.Context) ([]Account, error) {
	if s == nil || s.host == nil {
		return nil, fmt.Errorf("auth host is unavailable")
	}
	entries, errList := s.host.ListAuth(ctx)
	if errList != nil {
		return nil, fmt.Errorf("list host auth records: %w", errList)
	}
	if discoverer, ok := s.usage.(UsageStorageDiscoverer); ok {
		discoverer.DiscoverAuthStorage(entries)
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
		account := projectHostEntry(entry, pathCounts, indexCounts, s.usage)
		if s.concurrency != nil {
			account.Concurrency = s.concurrency.Summary(account.AuthID)
		}
		accounts = append(accounts, account)
	}
	if s.observer != nil {
		s.observer.ObserveAccounts(accounts)
	}
	return accounts, nil
}

func (s *AccountService) enrichAccountDetails(ctx context.Context, accounts []Account) {
	if s == nil || s.host == nil || len(accounts) == 0 {
		return
	}
	workers := accountDetailWorkers
	if workers > len(accounts) {
		workers = len(accounts)
	}
	indexes := make(chan int, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for index := range indexes {
				if ctx.Err() != nil {
					continue
				}
				s.enrichAccountDetail(ctx, &accounts[index])
			}
		}()
	}
	for index := range accounts {
		if accounts[index].detailAuthIndex == "" || accounts[index].revision != "" {
			continue
		}
		indexes <- index
	}
	close(indexes)
	group.Wait()
}

func (s *AccountService) enrichAccountDetail(ctx context.Context, account *Account) {
	detail, errGet := s.host.GetAuth(ctx, account.detailAuthIndex)
	if errGet != nil {
		markAccountDetailUnavailable(account)
		return
	}
	if returnedIndex := strings.TrimSpace(detail.AuthIndex); returnedIndex != "" && returnedIndex != account.detailAuthIndex {
		markAccountDetailUnavailable(account)
		return
	}
	if detailPath := normalizedPath(detail.Path); account.path != "" && detailPath != "" && detailPath != account.path {
		markAccountDetailUnavailable(account)
		return
	}
	if errEnrich := enrichAccount(account, detail); errEnrich != nil && account.Editable {
		account.Editable = false
		account.ReadOnlyReason = "physical auth file is invalid"
	}
}

func markAccountDetailUnavailable(account *Account) {
	if account.Editable {
		account.Editable = false
		account.ReadOnlyReason = "physical auth file is unavailable"
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
	sortAccountsBy(accounts, AccountSortAccount, AccountSortAscending)
}

func sortAccountsBy(accounts []Account, field AccountSortField, order AccountSortOrder) {
	if !validAccountSortField(field) {
		field = AccountSortAccount
	}
	descending := order == AccountSortDescending
	sort.Slice(accounts, func(i, j int) bool {
		comparison, leftMissing, rightMissing := compareAccountSortValue(accounts[i], accounts[j], field)
		if leftMissing != rightMissing {
			return !leftMissing
		}
		if comparison != 0 {
			if descending {
				return comparison > 0
			}
			return comparison < 0
		}
		leftIdentity := normalizedAccountIdentity(accounts[i])
		rightIdentity := normalizedAccountIdentity(accounts[j])
		if leftIdentity != rightIdentity {
			return leftIdentity < rightIdentity
		}
		return accounts[i].ID < accounts[j].ID
	})
}

func compareAccountSortValue(left, right Account, field AccountSortField) (comparison int, leftMissing, rightMissing bool) {
	switch field {
	case AccountSortProvider:
		return compareOptionalStrings(left.Provider, right.Provider)
	case AccountSortType:
		return compareOptionalStrings(firstNonEmpty(left.PlanType, left.AccountType, left.Type), firstNonEmpty(right.PlanType, right.AccountType, right.Type))
	case AccountSortUsage:
		return compareOptionalInt64(accountUsageTotal(left), accountUsageTotal(right))
	case AccountSortActiveResetCount:
		return compareOptionalInts(accountActiveResetCount(left), accountActiveResetCount(right))
	case AccountSortConcurrency:
		if !left.Concurrency.Supported || !right.Concurrency.Supported {
			return 0, !left.Concurrency.Supported, !right.Concurrency.Supported
		}
		if comparison = compareSortInt(left.Concurrency.Active, right.Concurrency.Active); comparison == 0 {
			comparison = compareSortInt(left.Concurrency.Limit, right.Concurrency.Limit)
		}
		return comparison, false, false
	case AccountSortCreatedAt:
		return compareOptionalTimes(left.CreatedAt, right.CreatedAt)
	case AccountSortDisabledAt:
		return compareOptionalTimes(left.DisabledAt, right.DisabledAt)
	case AccountSortAccess:
		return strings.Compare(accountAccessSortValue(left), accountAccessSortValue(right)), false, false
	case AccountSortStatus:
		return strings.Compare(accountSortStatus(left), accountSortStatus(right)), false, false
	case AccountSortPriority:
		return compareOptionalInts(left.Priority, right.Priority)
	case AccountSortRouting:
		return strings.Compare(accountRoutingSortValue(left), accountRoutingSortValue(right)), false, false
	default:
		return strings.Compare(normalizedAccountIdentity(left), normalizedAccountIdentity(right)), false, false
	}
}

func normalizedAccountIdentity(account Account) string {
	return strings.ToLower(firstNonEmpty(account.Label, account.Email, account.Name, account.ID))
}

func accountUsageTotal(account Account) *int64 {
	if account.Usage == nil {
		return nil
	}
	value := account.Usage.TotalTokens
	return &value
}

func accountActiveResetCount(account Account) *int {
	if account.Usage == nil || account.Usage.Codex == nil {
		return nil
	}
	return account.Usage.Codex.ActiveResetCount
}

func accountSortStatus(account Account) string {
	switch {
	case account.Disabled:
		return "disabled"
	case account.Unavailable:
		return "unavailable"
	default:
		return strings.ToLower(firstNonEmpty(account.Status, "unknown"))
	}
}

func accountAccessSortValue(account Account) string {
	if account.Editable {
		return "editable"
	}
	return "read_only"
}

func accountRoutingSortValue(account Account) string {
	websockets := "0"
	if account.Websockets != nil && *account.Websockets {
		websockets = "1"
	}
	return strings.ToLower(fmt.Sprintf("%s\x00%t\x00%s\x00%08d", account.Prefix, account.ProxyConfigured, websockets, account.HeaderCount))
}

func compareOptionalStrings(left, right string) (int, bool, bool) {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	return strings.Compare(left, right), left == "", right == ""
}

func compareOptionalInt64(left, right *int64) (int, bool, bool) {
	if left == nil || right == nil {
		return 0, left == nil, right == nil
	}
	return compareSortInt64(*left, *right), false, false
}

func compareOptionalInts(left, right *int) (int, bool, bool) {
	if left == nil || right == nil {
		return 0, left == nil, right == nil
	}
	return compareSortInt(*left, *right), false, false
}

func compareOptionalTimes(left, right *time.Time) (int, bool, bool) {
	if left == nil || right == nil {
		return 0, left == nil, right == nil
	}
	if left.Before(*right) {
		return -1, false, false
	}
	if left.After(*right) {
		return 1, false, false
	}
	return 0, false, false
}

func compareSortInt(left, right int) int {
	return compareSortInt64(int64(left), int64(right))
}

func compareSortInt64(left, right int64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func validAccountSortField(field AccountSortField) bool {
	switch field {
	case AccountSortAccount, AccountSortProvider, AccountSortType, AccountSortUsage, AccountSortActiveResetCount, AccountSortConcurrency, AccountSortCreatedAt, AccountSortDisabledAt, AccountSortAccess, AccountSortStatus, AccountSortPriority, AccountSortRouting:
		return true
	default:
		return false
	}
}

func sortRequiresAccountDetail(field AccountSortField) bool {
	switch field {
	case AccountSortType, AccountSortAccess, AccountSortPriority, AccountSortRouting:
		return true
	default:
		return false
	}
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
		PlanType:      safeAccountPlanType(entry.IDToken.PlanType, entry.IDToken.ChatGPTPlanType, entry.PlanType, entry.ChatGPTPlanType),
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
	normalizeAgentIdentityNativeState(&account)
	if !entry.NextRetryAfter.IsZero() {
		nextRetryAfter := entry.NextRetryAfter.UTC()
		account.NextRetryAfter = &nextRetryAfter
	}
	if usage != nil && authIndex != "" {
		account.Usage = usage.Snapshot(authIndex)
		applyQuotaPlanType(&account)
		if lifecycle, ok := usage.(AccountLifecycleReader); ok {
			snapshot := lifecycle.AccountLifecycle(authIndex)
			account.CreatedAt = snapshot.CreatedAt
			account.DisabledAt = snapshot.DisabledAt
		}
		if identities, ok := usage.(UsageIdentityReader); ok {
			account.usageIdentity = identities.UsageIdentity(authIndex)
		}
	}
	if account.CreatedAt == nil && !entry.CreatedAt.IsZero() {
		createdAt := entry.CreatedAt.UTC()
		account.CreatedAt = &createdAt
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
	if !account.RuntimeOnly && account.path != "" && strings.EqualFold(account.Source, "file") && authIndex != "" && indexCounts[authIndex] == 1 {
		account.detailAuthIndex = authIndex
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

func normalizeAgentIdentityNativeState(account *Account) {
	if account == nil || !isAgentIdentityProvider(account.Provider) {
		return
	}
	status := strings.ToLower(strings.TrimSpace(account.Status))
	unsupported := account.Unavailable || status == "error" || status == "unavailable" || account.StatusMessage == "provider reported an account error"
	if !unsupported {
		return
	}
	account.Unavailable = false
	account.StatusMessage = ""
	if observedAccountSuccess(*account) {
		account.Status = "active"
	} else if status == "error" || status == "unavailable" {
		account.Status = ""
	}
}

func observedAccountSuccess(account Account) bool {
	if account.Success > 0 {
		return true
	}
	for _, item := range account.RecentRequests {
		if item.Success > 0 {
			return true
		}
	}
	return false
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
	if planType := accountPlanTypeFromMetadata(metadata); planType != "" {
		account.PlanType = planType
	}
	account.HeaderNames = safeHeaderNames(metadata["headers"])
	account.HeaderCount = len(account.HeaderNames)
	account.ModelPolicy = modelPolicySummary(metadata)
	applyQuotaPlanType(account)
	return nil
}

func applyQuotaPlanType(account *Account) {
	if account == nil || account.Usage == nil || account.Usage.Codex == nil {
		return
	}
	if planType := safeAccountPlanType(account.Usage.Codex.PlanType); planType != "" {
		account.PlanType = planType
	}
}

func matchesFilters(account Account, filters AccountFilters) bool {
	if value := strings.TrimSpace(filters.Provider); value != "" && !strings.EqualFold(account.Provider, value) {
		return false
	}
	if value := strings.TrimSpace(filters.Type); value != "" &&
		!strings.EqualFold(account.PlanType, value) &&
		!strings.EqualFold(account.AccountType, value) &&
		!strings.EqualFold(account.Type, value) {
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
		account.PlanType,
		account.Status,
		account.Note,
	}, "\n"))
	return strings.Contains(haystack, search)
}

func filtersRequireAccountDetail(filters AccountFilters) bool {
	return strings.TrimSpace(filters.Type) != "" ||
		strings.TrimSpace(filters.Editability) != "" ||
		strings.TrimSpace(filters.Search) != ""
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

func safeAccountPlanType(values ...any) string {
	for _, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		planType := strings.ToLower(strings.TrimSpace(raw))
		if planType == "" || len(planType) > maxAccountPlanTypeLength {
			continue
		}
		valid := true
		for _, char := range planType {
			if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
				continue
			}
			switch char {
			case '-', '_', '.':
				continue
			default:
				valid = false
			}
			break
		}
		if valid {
			return planType
		}
	}
	return ""
}

func accountPlanTypeFromMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range []string{"id_token", "idToken"} {
		claims := accountIdentityClaims(metadata[key])
		if planType := accountPlanTypeFromClaims(claims); planType != "" {
			return planType
		}
	}
	if planType := safeAccountPlanType(metadata["plan_type"], metadata["chatgpt_plan_type"]); planType != "" {
		return planType
	}
	for _, key := range []string{"access_token", "accessToken"} {
		claims := accountIdentityClaims(metadata[key])
		if planType := accountPlanTypeFromClaims(claims); planType != "" {
			return planType
		}
	}
	return ""
}

func accountIdentityClaims(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case string:
		return parseImportIdentityPayload(typed)
	default:
		return nil
	}
}

func accountPlanTypeFromClaims(claims map[string]any) string {
	if len(claims) == 0 {
		return ""
	}
	if planType := safeAccountPlanType(claims["plan_type"], claims["chatgpt_plan_type"]); planType != "" {
		return planType
	}
	authClaims, _ := claims["https://api.openai.com/auth"].(map[string]any)
	return safeAccountPlanType(authClaims["plan_type"], authClaims["chatgpt_plan_type"])
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
