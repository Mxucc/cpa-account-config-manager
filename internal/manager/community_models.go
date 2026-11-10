package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cpa-account-config-manager/internal/magpiecompat"
)

// A community provider's model list.
//
// The catalog a package publishes is what CPA can route, and CPA routes exactly
// the models a channel row lists. Until now the page could only show how many
// models there were: an operator could not see the ids, could not choose which
// ones to publish, and had no way to publish a change. This file answers the
// three questions that need — what is available, what is published, and write the
// selection back onto the channel this plugin owns.

// communityModelRow is one model in the catalog with its published state.
type communityModelRow struct {
	ID        string `json:"id"`
	Published bool   `json:"published"`
}

// communityAccountStatus is what the plugin answers for the signed-in account: its
// own catalog, the provider's name for the account, its plan and its allowance. It
// is one sidecar call, cached with the probe cache so a page load does not run the
// same plugin twice.
func (a *App) communityAccountStatus(ctx context.Context, providerID string) (magpiecompat.AccountModels, bool) {
	if a == nil || !a.pluginRuntimeAvailable() {
		return magpiecompat.AccountModels{}, false
	}
	key := strings.ToLower(strings.TrimSpace(providerID)) + "|status"
	communityProbeCache.mu.Lock()
	if cached, found := communityProbeCache.statuses[key]; found && time.Since(cached.at) < communityProbeCacheTTL {
		communityProbeCache.mu.Unlock()
		return cached.value, true
	}
	communityProbeCache.mu.Unlock()
	directory, _, errDirectory := a.importedProviderDirectory(ctx, providerID)
	if errDirectory != nil || strings.TrimSpace(directory) == "" {
		return magpiecompat.AccountModels{}, false
	}
	_, credential, errChannel := a.communityOwnChannelCredential(providerID)
	if errChannel != nil || strings.TrimSpace(credential) == "" {
		return magpiecompat.AccountModels{}, false
	}
	runner := magpiecompat.New("", "", magpiecompat.Options{})
	if runner == nil || !runner.Available() {
		return magpiecompat.AccountModels{}, false
	}
	answer, errStatus := runner.Models(ctx, directory, magpiecompat.LoginCredential{Access: credential})
	if errStatus != nil {
		return magpiecompat.AccountModels{}, false
	}
	communityProbeCache.mu.Lock()
	if communityProbeCache.statuses == nil {
		communityProbeCache.statuses = map[string]communityStatusEntry{}
	}
	communityProbeCache.statuses[key] = communityStatusEntry{value: answer, at: time.Now()}
	communityProbeCache.mu.Unlock()
	return answer, true
}

// communityAccountModels asks the plugin for the catalog the signed-in account
// actually serves. The package's static list is only the default for when nobody is
// signed in, so this is what answers "are these really all the models?".
func (a *App) communityAccountModels(ctx context.Context, providerID string) ([]string, string) {
	if a == nil || !a.pluginRuntimeAvailable() {
		return nil, ""
	}
	directory, _, errDirectory := a.importedProviderDirectory(ctx, providerID)
	if errDirectory != nil || strings.TrimSpace(directory) == "" {
		return nil, ""
	}
	_, credential, errChannel := a.communityOwnChannelCredential(providerID)
	if errChannel != nil || strings.TrimSpace(credential) == "" {
		return nil, ""
	}
	runner := magpiecompat.New("", "", magpiecompat.Options{})
	if runner == nil || !runner.Available() {
		return nil, ""
	}
	answer, errModels := runner.Models(ctx, directory, magpiecompat.LoginCredential{Access: credential})
	if errModels != nil {
		return nil, "the plugin could not list this account's models"
	}
	if len(answer.Models) == 0 {
		if answer.Reason != "" {
			return nil, answer.Reason
		}
		return nil, ""
	}
	return answer.Models, ""
}

// communityModelCatalog lists what the provider's package publishes, in catalog
// order, with the ids the operator can select.
func (a *App) communityModelCatalog(providerID string) []string {
	return a.communityModelCatalogWithReason(providerID, "")
}

func (a *App) communityModelCatalogWithReason(providerID, fallbackReason string) []string {
	probe := a.communityProbe(providerID)
	out := make([]string, 0, len(probe.models))
	seen := map[string]bool{}
	for _, model := range probe.models {
		id := strings.TrimSpace(model)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// communityChannelModelIDs reads the model ids one channel row publishes.
func communityChannelModelIDs(entry map[string]any) []string {
	raw, found := entry["models"]
	if !found {
		return nil
	}
	out := make([]string, 0)
	switch list := raw.(type) {
	case []any:
		for _, item := range list {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if name, ok := record["name"].(string); ok && strings.TrimSpace(name) != "" {
				out = append(out, strings.TrimSpace(name))
			}
		}
	case []map[string]any:
		for _, record := range list {
			if name, ok := record["name"].(string); ok && strings.TrimSpace(name) != "" {
				out = append(out, strings.TrimSpace(name))
			}
		}
	}
	return out
}

// communityOwnChannelRows reports, for every live channel row, whether it is one
// this plugin published for the provider. A row qualifies when it publishes a
// credential this plugin recorded for that provider or when it points at the base
// the provider declares; an operator's own row therefore never matches.
func (a *App) communityOwnChannelRows(providerID string, entries []map[string]any) []bool {
	known := map[string]bool{}
	for _, digest := range a.communityChannelCredentialsFor(providerID) {
		known[digest] = true
	}
	baseURL := strings.TrimRight(strings.TrimSpace(a.communityChannelBase(providerID)), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(a.communityProviderDeclaredBase(providerID)), "/")
	}
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(communityProviderBaseURL(providerID)), "/")
	}
	own := make([]bool, len(entries))
	for index, entry := range entries {
		if credential := aiProviderChannelCredential(entry); credential != "" && len(known) > 0 {
			if _, found := known[communityCredentialDigest(credential)]; found {
				own[index] = true
				continue
			}
		}
		if baseURL != "" {
			if live := strings.TrimRight(strings.TrimSpace(aiProviderChannelBaseURL(entry)), "/"); live == baseURL {
				own[index] = true
			}
		}
	}
	return own
}

// communityPublishedModels reads what the provider's channel currently routes.
func (a *App) communityPublishedModels(providerID string) ([]string, error) {
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" {
		return nil, fmt.Errorf("a management key is required to read the channel")
	}
	client, errClient := a.newWriteManagementClient(managementKey)
	if errClient != nil {
		return nil, errClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entries, supported, errRead := client.getAIProviderChannel(ctx, "openai-compatibility")
	if errRead != nil {
		return nil, fmt.Errorf("read the channel list: %w", errRead)
	}
	if !supported {
		return nil, fmt.Errorf("this host does not accept OpenAI-compatible channels")
	}
	out := make([]string, 0)
	seen := map[string]bool{}
	own := a.communityOwnChannelRows(providerID, entries)
	for index, entry := range entries {
		if !own[index] {
			continue
		}
		for _, id := range communityChannelModelIDs(entry) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// writeCommunityChannelModels replaces the model list of the channels this plugin
// owns for one provider. The selection is what CPA then routes: a model left out
// stops being reachable, which is the point of choosing.
func (a *App) writeCommunityChannelModels(providerID string, models []string) (int, int, error) {
	wanted := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		wanted = append(wanted, id)
	}
	if len(wanted) == 0 {
		return 0, 0, fmt.Errorf("select at least one model, or remove the channel instead")
	}
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" {
		return 0, 0, fmt.Errorf("a management key is required to update the channel")
	}
	client, errClient := a.newWriteManagementClient(managementKey)
	if errClient != nil {
		return 0, 0, errClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const kind = "openai-compatibility"
	entries, supported, errRead := client.getAIProviderChannel(ctx, kind)
	if errRead != nil {
		return 0, 0, fmt.Errorf("read the channel list: %w", errRead)
	}
	if !supported {
		return 0, 0, fmt.Errorf("this host does not accept OpenAI-compatible channels")
	}
	rows := make([]map[string]any, 0, len(wanted))
	for _, id := range wanted {
		rows = append(rows, map[string]any{"name": id, "alias": id})
	}
	own := a.communityOwnChannelRows(providerID, entries)
	updated := 0
	for index := range entries {
		if !own[index] {
			continue
		}
		entries[index]["models"] = rows
		// The prefix is part of publishing, not an extra: without it the model ids
		// compete with every other channel that publishes the same id.
		entries[index]["prefix"] = communityChannelPrefix(providerID)
		updated++
	}
	if updated == 0 {
		return 0, 0, fmt.Errorf("no channel of this provider was found; sign in once to create it")
	}
	if errWrite := client.putAIProviderChannel(ctx, kind, entries); errWrite != nil {
		return 0, 0, fmt.Errorf("the channel list could not be saved: %w", errWrite)
	}
	return updated, len(wanted), nil
}

// communityModelsView is the answer both model routes return.
type communityModelsView struct {
	Provider  string              `json:"provider"`
	Available []string            `json:"available"`
	Published []string            `json:"published"`
	Rows      []communityModelRow `json:"rows"`
	Channels  int                 `json:"channels"`
	Stale     []string            `json:"stale,omitempty"`
	Warning   string              `json:"warning,omitempty"`
	// Source says whose list this is: "account" when the plugin answered with the
	// signed-in plan's own catalog, "package" when it is the offline default.
	Source string `json:"source,omitempty"`
}

// mergeModelIDs returns the first list followed by the entries of the second that
// it does not already hold, without duplicates. It keeps the answered list usable
// even when a read has caught up with only part of a write.
func mergeModelIDs(primary, secondary []string) []string {
	out := make([]string, 0, len(primary)+len(secondary))
	seen := map[string]bool{}
	for _, list := range [][]string{primary, secondary} {
		for _, id := range list {
			trimmed := strings.TrimSpace(id)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			out = append(out, trimmed)
		}
	}
	return out
}

// containsID reports whether a list already holds one id.
func containsID(list []string, id string) bool {
	for _, entry := range list {
		if entry == id {
			return true
		}
	}
	return false
}
