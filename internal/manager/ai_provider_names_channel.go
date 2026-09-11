package manager

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// AIProviderNameAssignment is one resolved channel record. Index and BaseURL let
// the caller verify the record against the entry it currently renders, Name is
// the plugin-stored label, and Identities lists the runtime usage identities that
// CPA has reported for this channel (including identities kept across a
// credential rotation), so usage history follows the channel instead of the
// volatile auth index.
type AIProviderNameAssignment struct {
	Kind       string   `json:"kind"`
	Index      int      `json:"index"`
	BaseURL    string   `json:"base_url,omitempty"`
	Name       string   `json:"name,omitempty"`
	Identities []string `json:"identities,omitempty"`
}

// aiProviderChannelIdentity is the precomputed identity of one live channel
// entry: the plugin digest keys plus the runtime identity CPA reports for it.
type aiProviderChannelIdentity struct {
	credentialKey string
	urlKey        string
	runtimeID     string
	baseURL       string
	provider      string
}

func (a *App) channelIdentityFor(kind string, entry map[string]any) aiProviderChannelIdentity {
	identity := aiProviderChannelIdentity{}
	if a == nil || a.aiProviderNames == nil {
		return identity
	}
	identity.baseURL = aiProviderChannelBaseURL(entry)
	identity.provider = aiProviderRuntimeProviderName(kind)
	identity.credentialKey = a.aiProviderNames.CredentialKey(kind, identity.baseURL, aiProviderChannelCredential(entry))
	identity.urlKey = a.aiProviderNames.URLKey(kind, identity.baseURL)
	identity.runtimeID = aiProviderRuntimeCredentialIdentity(identity.provider, aiProviderChannelCredential(entry))
	return identity
}

// aiProviderRuntimeProviderName maps a CPA channel kind to the provider name CPA
// reports in usage callbacks, which is the namespace the runtime tracker uses.
func aiProviderRuntimeProviderName(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "openai-compatibility", "openai-compatible":
		return "openai"
	case "gemini-api-key":
		return "gemini"
	case "interactions-api-key":
		return "interactions"
	case "claude-api-key":
		return "claude"
	case "codex-api-key":
		return "codex"
	case "xai-api-key":
		return "xai"
	case "vertex-api-key":
		return "vertex"
	default:
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(kind)), "-api-key")
	}
}

// aiProviderChannelCredential extracts the channel credential used for the
// channel digest. It never leaves this process: only the keyed digest is stored.
func aiProviderChannelCredential(entry map[string]any) string {
	if entry == nil {
		return ""
	}
	if raw, ok := entry["api-key-entries"]; ok {
		if list, isList := raw.([]any); isList {
			for _, item := range list {
				record, isRecord := item.(map[string]any)
				if !isRecord {
					continue
				}
				if key, ok := record["api-key"].(string); ok && strings.TrimSpace(key) != "" {
					return strings.TrimSpace(key)
				}
			}
		}
	}
	if key, ok := entry["api-key"].(string); ok {
		return strings.TrimSpace(key)
	}
	return ""
}

func aiProviderChannelBaseURL(entry map[string]any) string {
	if entry == nil {
		return ""
	}
	value, _ := entry["base-url"].(string)
	return strings.TrimSpace(value)
}

func (a *App) aiProviderChannelEntries(ctx context.Context, managementKey, kind string) ([]map[string]any, error) {
	if a == nil {
		return nil, fmt.Errorf("AI provider channel client is unavailable")
	}
	if !supportedAIProviderProxyKind(kind) {
		return nil, fmt.Errorf("AI provider channel %s is not supported", kind)
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return nil, fmt.Errorf("AI provider channel client is unavailable")
	}
	defer client.clearSecrets()
	entries, supported, errList := client.getAIProviderChannel(ctx, kind)
	if errList != nil {
		return nil, errList
	}
	if !supported {
		return nil, nil
	}
	return entries, nil
}

// syncAIProviderChannelBindings reconciles the stored channel records with one
// live channel list and returns the resolved assignments.
//
// Resolution rules:
//   - The credential digest (kind + base URL + API key) is the primary identity,
//     so changing the key produces a new binding instead of a stale match.
//   - When the new binding does not exist yet and exactly one live entry uses
//     that base URL, the previous record is adopted, which carries the label and
//     the usage history across the credential change.
//   - A base URL shared by several entries never adopts, so two channels cannot
//     trade identities.
func (a *App) syncAIProviderChannelBindings(kind string, entries []map[string]any) []AIProviderNameAssignment {
	if a == nil || a.aiProviderNames == nil {
		return nil
	}
	identities := make([]aiProviderChannelIdentity, len(entries))
	urlOwners := make(map[string]int, len(entries))
	runtimeOwners := make(map[string]int, len(entries))
	for index, entry := range entries {
		identities[index] = a.channelIdentityFor(kind, entry)
		urlOwners[identities[index].urlKey]++
		if identities[index].runtimeID != "" {
			runtimeOwners[identities[index].runtimeID]++
		}
	}

	assignments := make([]AIProviderNameAssignment, 0, len(entries))
	keep := make(map[string]struct{}, len(entries)*2)
	for index, entry := range entries {
		identity := identities[index]
		bindingKey := identity.credentialKey
		if aiProviderChannelCredential(entry) == "" {
			// A channel without a credential can only be identified by its URL.
			bindingKey = identity.urlKey
		}
		if _, exists := a.aiProviderNames.Binding(bindingKey); !exists && bindingKey != identity.urlKey && urlOwners[identity.urlKey] == 1 {
			// Adopt the URL-level record so a rotated credential keeps its label
			// and the usage identities observed before the change.
			_ = a.aiProviderNames.AdoptBinding(identity.urlKey, bindingKey)
		}
		// A usage identity is only attributed when exactly one live entry claims
		// it; otherwise the same credential would appear on several channels.
		runtimeID := identity.runtimeID
		if runtimeOwners[runtimeID] != 1 {
			runtimeID = ""
		}
		_ = a.aiProviderNames.UpsertBinding(bindingKey, identity.baseURL, identity.provider, runtimeID)
		// An OpenCode channel also records its CPA auth index so the session
		// router can attribute a request to OpenCode without guessing from a
		// model id that Zen may resell to other providers too.
		if authIdentity := openCodeChannelAuthIdentity(entry); authIdentity != "" {
			_ = a.aiProviderNames.UpsertBinding(bindingKey, identity.baseURL, identity.provider, authIdentity)
		}
		// Keep the URL-level alias in step so a later rotation can adopt again.
		if identity.urlKey != bindingKey {
			_ = a.aiProviderNames.UpsertBinding(identity.urlKey, identity.baseURL, identity.provider, runtimeID)
		}
		keep[bindingKey] = struct{}{}
		keep[identity.urlKey] = struct{}{}

		binding, _ := a.aiProviderNames.Binding(bindingKey)
		list := normalizeAIProviderIdentities(binding.Identities)
		if runtimeID != "" && !containsAIProviderIdentity(list, runtimeID) {
			list = append(list, runtimeID)
		}
		assignments = append(assignments, AIProviderNameAssignment{
			Kind:       kind,
			Index:      index,
			BaseURL:    identity.baseURL,
			Name:       strings.TrimSpace(binding.Name),
			Identities: list,
		})
	}
	// Drop records whose channel no longer exists, so a deleted channel cannot
	// hand its label or usage history to a future entry.
	if errPrune := a.aiProviderNames.PruneKind(kind, keep); errPrune != nil {
		a.aiProviderNames.noteStorageError("AI provider name state could not be persisted")
	}
	return assignments
}

// openCodeChannelAuthIdentity returns the binding identity that records a CPA
// auth index for an OpenCode channel. Non-OpenCode channels record nothing.
func openCodeChannelAuthIdentity(entry map[string]any) string {
	if !isOpenCodeGatewayBaseURL(aiProviderChannelBaseURL(entry)) {
		return ""
	}
	index, _ := entry["auth-index"].(string)
	index = strings.TrimSpace(index)
	if index == "" {
		return ""
	}
	return "auth-index:" + index
}

func containsAIProviderIdentity(values []string, identity string) bool {
	for _, value := range values {
		if value == identity {
			return true
		}
	}
	return false
}

// resolveAIProviderChannelNames revalidates every stored channel record against
// the live channel list and returns one assignment per live entry.
func (a *App) resolveAIProviderChannelNames(ctx context.Context, managementKey string) ([]AIProviderNameAssignment, string) {
	if a == nil || a.aiProviderNames == nil {
		return nil, ""
	}
	type kindResult struct {
		assignments []AIProviderNameAssignment
		failed      bool
	}
	results := make([]kindResult, len(aiProviderProxyPolicyKinds))
	var wait sync.WaitGroup
	for position, kind := range aiProviderProxyPolicyKinds {
		wait.Add(1)
		go func(position int, kind string) {
			defer wait.Done()
			entries, errList := a.aiProviderChannelEntries(ctx, managementKey, kind)
			if errList != nil {
				results[position] = kindResult{failed: true}
				return
			}
			results[position] = kindResult{assignments: a.syncAIProviderChannelBindings(kind, entries)}
		}(position, kind)
	}
	wait.Wait()

	assignments := make([]AIProviderNameAssignment, 0, len(aiProviderProxyPolicyKinds))
	failed := false
	for _, result := range results {
		assignments = append(assignments, result.assignments...)
		if result.failed {
			failed = true
		}
	}
	storageError := ""
	if failed {
		storageError = "AI provider channels could not be revalidated"
	}
	if errStorage := a.aiProviderNames.StorageError(); errStorage != "" {
		storageError = errStorage
	}
	return assignments, storageError
}

// assignAIProviderChannelName stores or clears one label. The live entry is
// re-read first so the digest is computed from CPA's current base URL and
// credential rather than from browser state.
func (a *App) assignAIProviderChannelName(ctx context.Context, managementKey, kind string, index int, baseURL, name string) error {
	if a == nil || a.aiProviderNames == nil {
		return ErrAIProviderNameStorageUnavailable
	}
	if !supportedAIProviderProxyKind(kind) {
		return fmt.Errorf("AI provider channel %s is not supported", kind)
	}
	entries, errList := a.aiProviderChannelEntries(ctx, managementKey, kind)
	if errList != nil {
		return fmt.Errorf("AI provider channels could not be read")
	}
	if index < 0 || index >= len(entries) {
		return errAIProviderNameEntryStale
	}
	entry := entries[index]
	if current := canonicalProviderBaseURL(aiProviderChannelBaseURL(entry)); current != canonicalProviderBaseURL(baseURL) {
		return errAIProviderNameEntryStale
	}
	// Reconcile first so the binding exists with its current identity, then write
	// the label onto both the credential and URL records.
	a.syncAIProviderChannelBindings(kind, entries)
	identity := a.channelIdentityFor(kind, entry)
	keys := []string{identity.credentialKey}
	if aiProviderChannelCredential(entry) == "" {
		keys = []string{identity.urlKey}
	} else {
		keys = append(keys, identity.urlKey)
	}
	_, errAssign := a.aiProviderNames.Assign(keys, name)
	return errAssign
}
