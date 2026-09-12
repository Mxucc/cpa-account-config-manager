package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Operators configure OpenCode both here and on the AI providers page, so the
// workspace reads the CPA AI-provider channels that already point at an OpenCode
// gateway and can import their credential instead of asking for it twice.
//
// A channel is treated as OpenCode when its base URL is on the OpenCode gateway,
// or when its label names OpenCode: a self-hosted opencode-cc bridge on an
// arbitrary host cannot be recognized by URL, and the plugin's own bind action
// labels the channels it writes.

const (
	openCodeChannelSourceAIProvider = "ai_provider"

	openCodeImportActionCreateZen = "create_zen"
	openCodeImportActionAttachKey = "attach_key"
)

// ErrOpenCodeImportNeedsWorkspace reports that a Go channel was found, but the
// plugin still needs the Workspace ID and auth Cookie that only the operator can
// provide, because quota scraping and the subscription allowance depend on them.
var ErrOpenCodeImportNeedsWorkspace = errors.New("add the Workspace ID and auth Cookie for this Go workspace, then import the API key")

// OpenCodeChannelView is one existing CPA channel that looks like OpenCode. The
// credential itself is never included, only whether one is stored.
type OpenCodeChannelView struct {
	Kind        string `json:"kind"`
	Name        string `json:"name,omitempty"`
	BaseURL     string `json:"base_url"`
	KeySet      bool   `json:"key_set"`
	Models      int    `json:"models"`
	Source      string `json:"source"`
	Imported    bool   `json:"imported"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// OpenCodeImportResult reports what an import changed.
type OpenCodeImportResult struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	AccountID string `json:"account_id"`
	Name      string `json:"name,omitempty"`
	BaseURL   string `json:"base_url"`
}

// openCodeChannelKindForEntry classifies one AI-provider channel entry.
func openCodeChannelKindForEntry(entry map[string]any) (string, bool) {
	if entry == nil {
		return "", false
	}
	baseURL := aiProviderChannelBaseURL(entry)
	if isOpenCodeGatewayBaseURL(baseURL) {
		return openCodeGatewayKind(baseURL), true
	}
	if strings.Contains(strings.ToLower(aiProviderChannelName(entry)), "opencode") {
		return openCodeKindZenValue, true
	}
	return "", false
}

func aiProviderChannelName(entry map[string]any) string {
	if entry == nil {
		return ""
	}
	value, _ := entry["name"].(string)
	return strings.TrimSpace(value)
}

func aiProviderChannelModelCount(entry map[string]any) int {
	list, ok := entry["models"].([]any)
	if !ok {
		return 0
	}
	return len(list)
}

// openCodeCompareBase normalizes a base URL for equality checks. A Go or Zen
// account may store the gateway root while the channel stores the OpenAI-compatible
// "/v1" endpoint, and both describe the same upstream.
func openCodeCompareBase(base, fallback string) string {
	value := strings.TrimSpace(base)
	if value == "" {
		value = fallback
	}
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return canonicalProviderBaseURL(openCodeAPIBase(value))
}

// openCodeWorkspaceFromChannelName recovers the workspace id the bind action wrote
// into the channel label ("OpenCode Go wrk_...").
func openCodeWorkspaceFromChannelName(name string) string {
	for _, field := range strings.Fields(name) {
		trimmed := strings.Trim(field, "()[],:;")
		if strings.HasPrefix(trimmed, "wrk_") && len(trimmed) > len("wrk_") {
			return trimmed
		}
	}
	return ""
}

// listOpenCodeChannels reports the AI-provider channels that belong to OpenCode,
// with whether each one is already represented in this workspace.
func (a *App) listOpenCodeChannels(ctx context.Context, managementKey string) ([]OpenCodeChannelView, error) {
	if a == nil {
		return nil, fmt.Errorf("OpenCode channel service is unavailable")
	}
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errRead != nil {
		return nil, fmt.Errorf("AI provider channels could not be read")
	}
	views := make([]OpenCodeChannelView, 0, len(entries))
	for _, entry := range entries {
		kind, ok := openCodeChannelKindForEntry(entry)
		if !ok {
			continue
		}
		baseURL := aiProviderChannelBaseURL(entry)
		view := OpenCodeChannelView{
			Kind:    kind,
			Name:    aiProviderChannelName(entry),
			BaseURL: baseURL,
			KeySet:  aiProviderChannelCredential(entry) != "",
			Models:  aiProviderChannelModelCount(entry),
			Source:  openCodeChannelSourceAIProvider,
		}
		if kind == openCodeKindGoValue {
			view.WorkspaceID = openCodeWorkspaceFromChannelName(view.Name)
			view.Imported = a.openCodeGoChannelImported(baseURL, view.WorkspaceID)
		} else {
			view.Imported = a.openCodeZenChannelImported(baseURL)
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *App) openCodeGoChannelImported(baseURL, workspaceID string) bool {
	if a == nil || a.opencode == nil {
		return false
	}
	target := openCodeCompareBase(baseURL, "")
	for _, account := range a.opencode.ListAccounts() {
		if workspaceID != "" && account.WorkspaceID == workspaceID {
			return true
		}
		if target != "" && openCodeCompareBase(account.BaseURL, openCodeGoDefaultBaseURL) == target {
			return true
		}
	}
	return false
}

func (a *App) openCodeZenChannelImported(baseURL string) bool {
	if a == nil || a.opencodeZen == nil {
		return false
	}
	target := openCodeCompareBase(baseURL, openCodeZenDefaultBaseURL)
	if target == "" {
		return false
	}
	for _, account := range a.opencodeZen.ListAccounts() {
		if openCodeCompareBase(account.BaseURL, openCodeZenDefaultBaseURL) == target {
			return true
		}
	}
	return false
}

// importOpenCodeChannel copies the credential of one existing AI-provider channel
// into the OpenCode workspace. A Zen channel (including a self-hosted bridge)
// becomes a Zen account. A Go channel attaches its API key to the workspace
// account that already holds the Workspace ID and auth Cookie, because the key
// alone cannot drive the subscription allowance.
func (a *App) importOpenCodeChannel(ctx context.Context, managementKey, baseURL string) (OpenCodeImportResult, error) {
	result := OpenCodeImportResult{}
	if a == nil {
		return result, fmt.Errorf("OpenCode channel service is unavailable")
	}
	target := openCodeCompareBase(baseURL, "")
	if target == "" {
		return result, fmt.Errorf("a channel base URL is required")
	}
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errRead != nil {
		return result, fmt.Errorf("AI provider channels could not be read")
	}
	var matched map[string]any
	for _, entry := range entries {
		if openCodeCompareBase(aiProviderChannelBaseURL(entry), "") == target {
			matched = entry
			break
		}
	}
	if matched == nil {
		return result, fmt.Errorf("the AI provider channel was not found")
	}
	kind, ok := openCodeChannelKindForEntry(matched)
	if !ok {
		return result, fmt.Errorf("the AI provider channel does not point at an OpenCode gateway")
	}
	apiKey := aiProviderChannelCredential(matched)
	if apiKey == "" {
		return result, fmt.Errorf("the AI provider channel stores no API key to import")
	}
	name := aiProviderChannelName(matched)
	result = OpenCodeImportResult{Kind: kind, BaseURL: aiProviderChannelBaseURL(matched)}

	if kind == openCodeKindGoValue {
		accountID := a.openCodeGoImportTarget(name)
		if accountID == "" {
			return OpenCodeImportResult{}, ErrOpenCodeImportNeedsWorkspace
		}
		view, errAttach := a.opencode.SetAPIKey(accountID, apiKey)
		if errAttach != nil {
			return OpenCodeImportResult{}, errAttach
		}
		result.Action = openCodeImportActionAttachKey
		result.AccountID = view.ID
		result.Name = view.WorkspaceID
		// The stored catalog belongs to the previous credential.
		_, _ = a.opencode.RefreshModels(ctx, view.ID, openCodeBindCatalogTimeoutSeconds)
		return result, nil
	}

	accountID, errSave := a.opencodeZen.SaveAccount("", name, openCodeAPIBase(result.BaseURL), apiKey)
	if errSave != nil {
		return OpenCodeImportResult{}, errSave
	}
	result.Action = openCodeImportActionCreateZen
	result.AccountID = accountID
	result.Name = name
	return result, nil
}

// openCodeGoImportTarget chooses the Go account that should receive an imported
// channel key: the account whose workspace the channel label names, or the only
// account that still has no usable credential key.
func (a *App) openCodeGoImportTarget(channelName string) string {
	if a == nil || a.opencode == nil {
		return ""
	}
	accounts := a.opencode.ListAccounts()
	if workspace := openCodeWorkspaceFromChannelName(channelName); workspace != "" {
		for _, account := range accounts {
			if account.WorkspaceID == workspace {
				return account.ID
			}
		}
	}
	keyless := ""
	for _, account := range accounts {
		if account.KeySet {
			continue
		}
		if keyless != "" {
			return ""
		}
		keyless = account.ID
	}
	return keyless
}
