package manager

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// AIProviderNameAssignment is one resolved operator label. Index and BaseURL
// let the caller verify the label against the entry it currently renders, so a
// stale browser list cannot attach a name to the wrong provider.
type AIProviderNameAssignment struct {
	Kind    string `json:"kind"`
	Index   int    `json:"index"`
	BaseURL string `json:"base_url,omitempty"`
	Name    string `json:"name"`
}

// aiProviderChannelCredential extracts the channel credential used for the
// name digest. It never leaves this process: only the keyed digest is stored.
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

// nameKeysForEntry returns the digest keys that identify one live channel
// entry. The credential key is primary; the URL-only key is the fallback for
// entries that carry no credential at all.
func (a *App) nameKeysForEntry(kind string, entry map[string]any) []string {
	if a == nil || a.aiProviderNames == nil {
		return nil
	}
	baseURL := aiProviderChannelBaseURL(entry)
	if credential := aiProviderChannelCredential(entry); credential != "" {
		return []string{a.aiProviderNames.CredentialKey(kind, baseURL, credential)}
	}
	return []string{a.aiProviderNames.URLKey(kind, baseURL)}
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

// resolveAIProviderChannelNames revalidates every stored label against the live
// channel list: a label is returned only when the recomputed digest of the
// entry's base URL and credential matches, and a URL-only label is dropped when
// more than one entry shares that base URL.
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
			assignments := make([]AIProviderNameAssignment, 0, len(entries))
			urlOwners := make(map[string]int, len(entries))
			entryKeys := make([][]string, len(entries))
			for index, entry := range entries {
				keys := a.nameKeysForEntry(kind, entry)
				entryKeys[index] = keys
				for _, key := range keys {
					if strings.Contains(key, ":url:") {
						urlOwners[key]++
					}
				}
			}
			for index, entry := range entries {
				name := ""
				for _, key := range entryKeys[index] {
					if strings.Contains(key, ":url:") && urlOwners[key] != 1 {
						// An ambiguous base URL must never claim a label.
						continue
					}
					if value, ok := a.aiProviderNames.Name(key); ok {
						name = value
						break
					}
				}
				if name == "" {
					continue
				}
				assignments = append(assignments, AIProviderNameAssignment{
					Kind:    kind,
					Index:   index,
					BaseURL: aiProviderChannelBaseURL(entry),
					Name:    name,
				})
			}
			results[position] = kindResult{assignments: assignments}
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
	keys := a.nameKeysForEntry(kind, entry)
	if _, errAssign := a.aiProviderNames.Assign(keys, name); errAssign != nil {
		return errAssign
	}
	keep := make(map[string]struct{}, len(entries))
	for _, candidate := range entries {
		for _, key := range a.nameKeysForEntry(kind, candidate) {
			keep[key] = struct{}{}
		}
	}
	return a.aiProviderNames.PruneKind(kind, keep)
}
