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
	return a.bindOpenAICompatibleChannelForAccount(ctx, managementKey, accountID, clinePassChannelBaseURL(baseURL), accessToken, label, clinePassBoundChannelName, models, clinePassChannelHeaders(version), clinePassChannelModelAliases(models, a.clinePassStripModelPrefix()))
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
