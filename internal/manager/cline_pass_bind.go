package manager

import (
	"context"
	"strings"
)

// Cline Pass channel binding: Cline Pass is a separate product surface from
// OpenCode, with its own gateway base URL, identity headers and channel label. It
// binds through the shared OpenAI-compatible channel machinery in
// provider_channel_bind.go, and the row it writes belongs to a stored account so
// the CPA auth index of that row can attribute usage to it.

// bindClinePassChannel writes the Cline Pass channel. Cline Pass is
// OpenAI-compatible, so it uses the same channel shape as OpenCode with the
// Cline product-surface headers the gateway (including its free tier) requires.
// accountID is the stored account whose row this write owns: CPA assigns the
// auth-index of that row, and the re-read records it so a usage callback can be
// attributed to this account.
func (a *App) bindClinePassChannel(ctx context.Context, managementKey, accountID, baseURL, accessToken, label string, models []string, version string) (ProviderChannelBindingResult, error) {
	return a.bindOpenAICompatibleChannelForAccount(ctx, managementKey, accountID, clinePassChannelBaseURL(baseURL), accessToken, label, clinePassBoundChannelName, models, clinePassChannelHeaders(version), clinePassChannelModelAliases(models, a.clinePassStripModelPrefix()), a.clinePassChannelOwnership(accountID, label))
}

// clinePassChannelOwnership reports what proves that a channel row publishing this
// bind's label is this account's own row. Several accounts share one gateway base
// URL and the label is the only identity a CPA row carries, so a bind that cannot
// prove ownership publishes its own row instead of rewriting a sibling's - which is
// exactly how two accounts used to collapse into one row.
//
// The shared default name is renamed on this bind as well: a row that still carries it was
// written by a release that had no per-account label, so naming it after this account keeps
// its siblings' rows distinguishable. Such a row is only adopted when it does not currently
// carry another stored account's credential, which is what stops the migration from taking
// that account's routing away.
func (a *App) clinePassChannelOwnership(accountID, label string) ProviderChannelOwnership {
	ownership := ProviderChannelOwnership{
		ExclusiveLabel:           a.clinePassChannelLabelIsExclusive(accountID, label),
		RenameSharedDefaultLabel: true,
	}
	if a != nil && a.clinePass != nil {
		ownership.PublishedCredential = a.clinePass.RoutePublishedCredential(accountID)
		ownership.DigestCredential = clinePassChannelCredentialIdentity
		ownership.ForeignCredentials = a.clinePassForeignCredentialIdentities(accountID)
	}
	return ownership
}

// clinePassForeignCredentialIdentities digests the credentials every OTHER stored account
// publishes. The row of such an account is never adopted by this bind, however its label
// reads, so binding one account can never silently unbind another.
func (a *App) clinePassForeignCredentialIdentities(accountID string) []string {
	if a == nil || a.clinePass == nil {
		return nil
	}
	own := strings.TrimSpace(accountID)
	identities := make([]string, 0, 2)
	for _, account := range a.clinePass.ListAccounts() {
		if account.ID == own {
			continue
		}
		if digest := clinePassChannelCredentialIdentity(a.clinePass.accessToken(account.ID)); digest != "" {
			identities = append(identities, digest)
		}
	}
	return identities
}

// clinePassChannelLabelIsExclusive reports whether this account is the only stored
// account that publishes this label. Such a label can only have been written by this
// account, so the row carrying it may be adopted even when the row's credential is
// already unknown (a restart, or a credential rotated more than once).
func (a *App) clinePassChannelLabelIsExclusive(accountID, label string) bool {
	wanted := strings.TrimSpace(label)
	if wanted == "" || a == nil || a.clinePass == nil {
		return false
	}
	matches := 0
	for _, account := range a.clinePass.ListAccounts() {
		if !strings.EqualFold(a.clinePassChannelLabel(account.ID), wanted) {
			continue
		}
		matches++
		if matches > 1 {
			return false
		}
	}
	return matches == 1
}

// clinePassRouteAuthIndexes collects the CPA auth indexes the account's own row
// carries in a live channel list, using the same matching the bind itself uses:
// the canonical base URL plus either the credential this bind just wrote or the
// label it publishes. A row is only read when it already belongs to this bind's
// account, so a sibling account's index is never recorded for this one.
func clinePassRouteAuthIndexes(entries []map[string]any, baseURL, apiKey, label string) []string {
	channelBase := canonicalProviderBaseURL(baseURL)
	if channelBase == "" {
		return nil
	}
	credential := strings.TrimSpace(apiKey)
	wantedLabel := strings.TrimSpace(label)
	indexes := make([]string, 0, 2)
	for _, entry := range entries {
		if canonicalProviderBaseURL(aiProviderChannelBaseURL(entry)) != channelBase {
			continue
		}
		matched := credential != "" && providerChannelHoldsCredential(entry, credential)
		if !matched && wantedLabel != "" {
			matched = strings.EqualFold(strings.TrimSpace(aiProviderChannelName(entry)), wantedLabel)
		}
		if !matched {
			continue
		}
		indexes = append(indexes, clinePassChannelAuthIndexes(entry)...)
	}
	return indexes
}

// clinePassChannelAuthIndexes reads every CPA auth index one channel entry
// carries: the row's own index and the index of each weighted key entry. CPA
// assigns them when the row is written, and a usage callback can name either.
func clinePassChannelAuthIndexes(entry map[string]any) []string {
	indexes := make([]string, 0, 2)
	if index, isText := entry["auth-index"].(string); isText {
		if trimmed := strings.TrimSpace(index); trimmed != "" {
			indexes = append(indexes, trimmed)
		}
	}
	list, isList := entry["api-key-entries"].([]any)
	if !isList {
		return indexes
	}
	for _, item := range list {
		record, isRecord := item.(map[string]any)
		if !isRecord {
			continue
		}
		if index, isText := record["auth-index"].(string); isText {
			if trimmed := strings.TrimSpace(index); trimmed != "" {
				indexes = append(indexes, trimmed)
			}
		}
	}
	return indexes
}
