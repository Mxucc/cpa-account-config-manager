package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxDeduplicationAccounts = 10000
	deduplicationWorkers     = 8
)

var ErrDeduplicationTooLarge = errors.New("account deduplication supports at most 10000 accounts")

type AccountDeduplicationMember struct {
	ID                string     `json:"id"`
	Name              string     `json:"name,omitempty"`
	Email             string     `json:"email,omitempty"`
	Provider          string     `json:"provider,omitempty"`
	Type              string     `json:"type,omitempty"`
	PlanType          string     `json:"plan_type,omitempty"`
	Status            string     `json:"status,omitempty"`
	Disabled          bool       `json:"disabled"`
	Unavailable       bool       `json:"unavailable"`
	Editable          bool       `json:"editable"`
	ReadOnlyReason    string     `json:"read_only_reason,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
	LastRefresh       *time.Time `json:"last_refresh,omitempty"`
	RecommendedAction string     `json:"recommended_action"`
}

type AccountDeduplicationGroup struct {
	ID            string                       `json:"id"`
	Provider      string                       `json:"provider"`
	MatchedBy     string                       `json:"matched_by"`
	IdentityLabel string                       `json:"identity_label"`
	KeepID        string                       `json:"keep_id"`
	KeepReason    string                       `json:"keep_reason"`
	Members       []AccountDeduplicationMember `json:"members"`
}

type AccountDeduplicationPreview struct {
	ScannedCredentials    int                         `json:"scanned_credentials"`
	IdentifiedCredentials int                         `json:"identified_credentials"`
	DuplicateGroups       int                         `json:"duplicate_groups"`
	DuplicateCredentials  int                         `json:"duplicate_credentials"`
	ProposedDeletions     int                         `json:"proposed_deletions"`
	ReadOnlySkipped       int                         `json:"read_only_skipped"`
	MissingIdentity       int                         `json:"missing_identity"`
	Groups                []AccountDeduplicationGroup `json:"groups"`
}

type AccountDeduplicationService struct {
	accounts *AccountService
}

type deduplicationIdentity struct {
	account      Account
	provider     string
	accountIDs   []string
	emails       []string
	completeness int
}

func NewAccountDeduplicationService(accounts *AccountService) *AccountDeduplicationService {
	return &AccountDeduplicationService{accounts: accounts}
}

func (s *AccountDeduplicationService) Preview(ctx context.Context) (AccountDeduplicationPreview, error) {
	if s == nil || s.accounts == nil || s.accounts.host == nil {
		return AccountDeduplicationPreview{}, fmt.Errorf("account service is unavailable")
	}
	accounts, errAccounts := s.accounts.baseAccounts(ctx)
	if errAccounts != nil {
		return AccountDeduplicationPreview{}, errAccounts
	}
	if len(accounts) > maxDeduplicationAccounts {
		return AccountDeduplicationPreview{}, ErrDeduplicationTooLarge
	}

	identities, errLoad := s.loadIdentities(ctx, accounts)
	if errLoad != nil {
		return AccountDeduplicationPreview{}, errLoad
	}
	preview := buildDeduplicationPreview(identities)
	preview.ScannedCredentials = len(accounts)
	preview.MissingIdentity = len(accounts) - preview.IdentifiedCredentials
	return preview, nil
}

func (s *AccountDeduplicationService) loadIdentities(ctx context.Context, accounts []Account) ([]deduplicationIdentity, error) {
	identities := make([]deduplicationIdentity, len(accounts))
	jobs := make(chan int)
	workers := deduplicationWorkers
	if len(accounts) < workers {
		workers = len(accounts)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				identities[index] = s.loadIdentity(ctx, accounts[index])
			}
		}()
	}
	for index := range accounts {
		select {
		case <-ctx.Done():
			close(jobs)
			wait.Wait()
			return nil, ctx.Err()
		case jobs <- index:
		}
	}
	close(jobs)
	wait.Wait()
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	return identities, nil
}

func (s *AccountDeduplicationService) loadIdentity(ctx context.Context, account Account) deduplicationIdentity {
	identity := deduplicationIdentity{
		account:  account,
		provider: deduplicationProviderFamily(firstNonEmpty(account.Provider, account.Type)),
		emails:   uniqueDeduplicationValues(normalizeDeduplicationEmail(account.Email)),
	}
	if account.detailAuthIndex == "" {
		return identity
	}
	detail, errGet := s.accounts.host.GetAuth(ctx, account.detailAuthIndex)
	if errGet != nil || (strings.TrimSpace(detail.AuthIndex) != "" && strings.TrimSpace(detail.AuthIndex) != account.detailAuthIndex) {
		return identity
	}
	object, errDecode := decodeCredentialJSONObject(detail.JSON)
	if errDecode != nil {
		return identity
	}
	if family := deduplicationProviderFamily(firstNonEmpty(
		account.Provider,
		firstImportString(object, []string{"provider"}, []string{"type"}),
		account.Type,
	)); family != "" {
		identity.provider = family
	}
	identity.accountIDs = deduplicationAccountIDs(object)
	identity.emails = uniqueDeduplicationValues(append(identity.emails, deduplicationEmails(object)...)...)
	identity.completeness = deduplicationCredentialCompleteness(object)
	return identity
}

func buildDeduplicationPreview(identities []deduplicationIdentity) AccountDeduplicationPreview {
	preview := AccountDeduplicationPreview{Groups: make([]AccountDeduplicationGroup, 0)}
	parents := make([]int, len(identities))
	for index := range parents {
		parents[index] = index
	}
	var find func(int) int
	find = func(index int) int {
		if parents[index] != index {
			parents[index] = find(parents[index])
		}
		return parents[index]
	}
	union := func(left, right int) {
		leftRoot, rightRoot := find(left), find(right)
		if leftRoot != rightRoot {
			parents[rightRoot] = leftRoot
		}
	}
	seen := make(map[string]int)
	identified := make([]bool, len(identities))
	for index, identity := range identities {
		if identity.provider == "" || len(identity.accountIDs)+len(identity.emails) == 0 {
			continue
		}
		identified[index] = true
		preview.IdentifiedCredentials++
		for _, accountID := range identity.accountIDs {
			key := identity.provider + "\x00id:" + accountID
			if previous, exists := seen[key]; exists {
				union(index, previous)
			} else {
				seen[key] = index
			}
		}
		for _, email := range identity.emails {
			key := identity.provider + "\x00email:" + email
			if previous, exists := seen[key]; exists {
				union(index, previous)
			} else {
				seen[key] = index
			}
		}
	}

	components := make(map[int][]int)
	for index := range identities {
		if identified[index] {
			root := find(index)
			components[root] = append(components[root], index)
		}
	}
	for _, indexes := range components {
		if len(indexes) < 2 {
			continue
		}
		group := buildDeduplicationGroup(identities, indexes)
		preview.Groups = append(preview.Groups, group)
		preview.DuplicateCredentials += len(group.Members) - 1
		for _, member := range group.Members {
			switch member.RecommendedAction {
			case "delete":
				preview.ProposedDeletions++
			case "skip":
				preview.ReadOnlySkipped++
			}
		}
	}
	sort.Slice(preview.Groups, func(i, j int) bool {
		if preview.Groups[i].Provider != preview.Groups[j].Provider {
			return preview.Groups[i].Provider < preview.Groups[j].Provider
		}
		return preview.Groups[i].ID < preview.Groups[j].ID
	})
	preview.DuplicateGroups = len(preview.Groups)
	return preview
}

func buildDeduplicationGroup(identities []deduplicationIdentity, indexes []int) AccountDeduplicationGroup {
	sort.Slice(indexes, func(i, j int) bool {
		return identities[indexes[i]].account.ID < identities[indexes[j]].account.ID
	})
	keep := indexes[0]
	for _, index := range indexes[1:] {
		if deduplicationIdentityBetter(identities[index], identities[keep]) {
			keep = index
		}
	}
	matchedBy, identityLabel := deduplicationMatchSummary(identities, indexes)
	memberIDs := make([]string, 0, len(indexes))
	members := make([]AccountDeduplicationMember, 0, len(indexes))
	for _, index := range indexes {
		identity := identities[index]
		account := identity.account
		action := "skip"
		if index == keep {
			action = "keep"
		} else if account.Editable {
			action = "delete"
		}
		memberIDs = append(memberIDs, account.ID)
		members = append(members, AccountDeduplicationMember{
			ID: account.ID, Name: account.Name, Email: firstNonEmpty(account.Email, firstDeduplicationValue(identity.emails)),
			Provider: account.Provider, Type: account.Type, PlanType: account.PlanType, Status: account.Status,
			Disabled: account.Disabled, Unavailable: account.Unavailable, Editable: account.Editable,
			ReadOnlyReason: account.ReadOnlyReason, UpdatedAt: account.UpdatedAt, LastRefresh: account.LastRefresh,
			RecommendedAction: action,
		})
	}
	return AccountDeduplicationGroup{
		ID: deduplicationFingerprint(strings.Join(memberIDs, "\x00")), Provider: identities[indexes[0]].provider,
		MatchedBy: matchedBy, IdentityLabel: identityLabel, KeepID: identities[keep].account.ID,
		KeepReason: deduplicationKeepReason(identities[keep], identities, indexes), Members: members,
	}
}

func deduplicationIdentityBetter(left, right deduplicationIdentity) bool {
	comparisons := [][2]int{
		{boolRank(left.account.Editable), boolRank(right.account.Editable)},
		{boolRank(!left.account.Disabled), boolRank(!right.account.Disabled)},
		{boolRank(!left.account.Unavailable), boolRank(!right.account.Unavailable)},
		{deduplicationHealthRank(left.account), deduplicationHealthRank(right.account)},
		{boolRank(observedAccountSuccess(left.account)), boolRank(observedAccountSuccess(right.account))},
	}
	for _, comparison := range comparisons {
		if comparison[0] != comparison[1] {
			return comparison[0] > comparison[1]
		}
	}
	leftTime, rightTime := deduplicationEvidenceTime(left.account), deduplicationEvidenceTime(right.account)
	if !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	if left.completeness != right.completeness {
		return left.completeness > right.completeness
	}
	return left.account.ID < right.account.ID
}

func deduplicationKeepReason(keep deduplicationIdentity, identities []deduplicationIdentity, indexes []int) string {
	for _, index := range indexes {
		other := identities[index]
		if other.account.ID == keep.account.ID {
			continue
		}
		switch {
		case keep.account.Editable != other.account.Editable:
			return "editable_physical_file"
		case keep.account.Disabled != other.account.Disabled:
			return "enabled_account"
		case keep.account.Unavailable != other.account.Unavailable || deduplicationHealthRank(keep.account) != deduplicationHealthRank(other.account):
			return "healthier_account"
		case !deduplicationEvidenceTime(keep.account).Equal(deduplicationEvidenceTime(other.account)):
			return "newer_evidence"
		case keep.completeness != other.completeness:
			return "more_complete_credential"
		}
	}
	return "deterministic_order"
}

func deduplicationMatchSummary(identities []deduplicationIdentity, indexes []int) (string, string) {
	idCounts := make(map[string]int)
	emailCounts := make(map[string]int)
	for _, index := range indexes {
		for _, value := range identities[index].accountIDs {
			idCounts[value]++
		}
		for _, value := range identities[index].emails {
			emailCounts[value]++
		}
	}
	sharedID, sharedEmail := "", ""
	for value, count := range idCounts {
		if count > 1 && (sharedID == "" || value < sharedID) {
			sharedID = value
		}
	}
	for value, count := range emailCounts {
		if count > 1 && (sharedEmail == "" || value < sharedEmail) {
			sharedEmail = value
		}
	}
	switch {
	case sharedID != "" && sharedEmail != "":
		return "multiple", sharedEmail
	case sharedID != "":
		return "account_id", "ID #" + deduplicationFingerprint(sharedID)
	default:
		return "email", sharedEmail
	}
}

func deduplicationAccountIDs(object map[string]any) []string {
	values := make([]string, 0, 4)
	if value := modelTestResolveAccountID([]map[string]any{object}); value != "" {
		values = append(values, normalizeDeduplicationAccountID(value))
	}
	for _, key := range []string{"agent_identity", "id_token", "idToken"} {
		payload := parseImportIdentityPayload(firstImportString(object, []string{key}))
		if value := modelTestAccountIDCandidate(payload, 0); value != "" {
			values = append(values, normalizeDeduplicationAccountID(value))
		}
	}
	return uniqueDeduplicationValues(values...)
}

func deduplicationEmails(object map[string]any) []string {
	values := []string{firstImportString(object,
		[]string{"email"}, []string{"user", "email"}, []string{"profile", "email"},
		[]string{"credentials", "email"}, []string{"credential", "email"},
		[]string{"providerSpecificData", "email"},
	)}
	for _, key := range []string{"agent_identity", "id_token", "idToken", "access_token", "accessToken"} {
		payload := parseImportIdentityPayload(firstImportString(object, []string{key}))
		values = append(values, firstImportString(payload, []string{"email"}, []string{"https://api.openai.com/profile", "email"}))
	}
	for index := range values {
		values[index] = normalizeDeduplicationEmail(values[index])
	}
	return uniqueDeduplicationValues(values...)
}

func deduplicationCredentialCompleteness(object map[string]any) int {
	count := 0
	for _, paths := range [][][]string{
		{{"account_id"}, {"chatgpt_account_id"}},
		{{"email"}, {"user", "email"}},
		{{"refresh_token"}, {"refreshToken"}},
		{{"id_token"}, {"idToken"}},
		{{"access_token"}, {"accessToken"}},
		{{"plan_type"}, {"chatgpt_plan_type"}},
		{{"agent_identity"}},
	} {
		if firstImportString(object, paths...) != "" {
			count++
		}
	}
	return count
}

func deduplicationProviderFamily(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	provider = strings.ReplaceAll(provider, "_", "-")
	switch provider {
	case "codex", agentIdentityProvider:
		return "codex"
	case "":
		return ""
	default:
		return provider
	}
}

func normalizeDeduplicationAccountID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 512 {
		return ""
	}
	return value
}

func normalizeDeduplicationEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 512 {
		return ""
	}
	address, errParse := mail.ParseAddress(value)
	if errParse != nil || !strings.EqualFold(strings.TrimSpace(address.Address), value) || !strings.Contains(value, "@") {
		return ""
	}
	return value
}

func uniqueDeduplicationValues(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func firstDeduplicationValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func deduplicationFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func deduplicationHealthRank(account Account) int {
	switch strings.ToLower(strings.TrimSpace(account.Status)) {
	case "active", "ready", "healthy", "normal", "ok":
		return 2
	case "error", "unavailable", "invalid":
		return 0
	default:
		return 1
	}
}

func deduplicationEvidenceTime(account Account) time.Time {
	latest := time.Time{}
	for _, candidate := range []*time.Time{account.LastRefresh, account.UpdatedAt} {
		if candidate != nil && candidate.After(latest) {
			latest = candidate.UTC()
		}
	}
	return latest
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}
