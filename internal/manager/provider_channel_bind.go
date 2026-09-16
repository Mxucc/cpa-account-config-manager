package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Shared AI-provider channel binding: OpenCode and Cline Pass are two product
// surfaces that both speak the OpenAI-compatible contract, so CPA can route their
// models natively. A bind writes (or updates) one CPA OpenAI-compatible channel
// that points at the gateway base URL with the account credential and the
// product's client headers, which is what makes the models reachable through CPA
// without a separate proxy process. The machinery in this file is product-neutral;
// the per-product entry points live in opencode_bind.go and cline_pass_bind.go.
//
// One exception is deliberate: a bind whose row belongs to a stored account (the
// accountID argument) records the CPA auth indexes that row carries, which is what
// attributes a usage callback. Only Cline Pass stores such accounts today, so the
// hook checks for that service; an OpenCode bind passes no account id and skips it.

// ProviderChannelBindingResult reports the CPA channel this account was bound to.
type ProviderChannelBindingResult struct {
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url"`
	Index      int    `json:"index"`
	Created    bool   `json:"created"`
	ChannelKey string `json:"channel_key"`
	Models     int    `json:"models"`
}

// bindOpenAICompatibleChannel upserts one OpenAI-compatible CPA channel that
// points at an upstream base URL with a credential and its client headers, then
// publishes the verified model catalog on the channel so CPA can route it.
//
// One row belongs to one credential. Several accounts of a kind share the same
// gateway base URL, so the target row is selected by base URL AND credential:
// selecting by base URL alone collapsed every account into one row, which renamed
// every sibling on a rename and let the last bind replace the other accounts'
// keys. The label is written when this call creates a row; a plain re-bind of an
// existing row keeps the operator's name.
func (a *App) bindOpenAICompatibleChannel(ctx context.Context, managementKey, channelBaseURL, apiKey, label, defaultLabel string, models []string, headers map[string]string, aliases map[string][]string) (ProviderChannelBindingResult, error) {
	return a.bindOpenAICompatibleChannelForAccount(ctx, managementKey, "", channelBaseURL, apiKey, label, defaultLabel, models, headers, aliases)
}

// bindOpenAICompatibleChannelForAccount is bindOpenAICompatibleChannel for a bind
// whose row belongs to a stored account (Cline Pass). The live channel list is
// re-read after the write anyway, and the CPA auth indexes that row carries are
// recorded for that account there, so a usage callback that only names the index
// CPA assigned is still attributed to the account whose figures the operator
// reads. The OpenCode path passes no account id and records nothing.
func (a *App) bindOpenAICompatibleChannelForAccount(ctx context.Context, managementKey, accountID, channelBaseURL, apiKey, label, defaultLabel string, models []string, headers map[string]string, aliases map[string][]string) (ProviderChannelBindingResult, error) {
	result := ProviderChannelBindingResult{Kind: "openai-compatibility", BaseURL: channelBaseURL, Index: -1}
	if a == nil {
		return result, fmt.Errorf("AI provider channel service is unavailable")
	}
	if strings.TrimSpace(managementKey) == "" {
		return result, fmt.Errorf("management key is unavailable")
	}
	if strings.TrimSpace(result.BaseURL) == "" || strings.TrimSpace(apiKey) == "" {
		return result, fmt.Errorf("an upstream base URL and API key are both required")
	}
	listKind := "openai-compatibility"
	entries, errRead := a.aiProviderChannelEntries(ctx, managementKey, listKind)
	if errRead != nil {
		return result, fmt.Errorf("CPA channels could not be read")
	}
	items := make([]map[string]any, 0, len(entries)+1)
	target := -1
	// An account keeps ONE row across credential rotations. A rotated token must not append a
	// second row for the same account, so the row is matched by its credential first and by its
	// label second: the label is per account (for example "Cline Pass work laptop"), which is the
	// only stable identity a channel row carries, since CPA drops fields it does not know when the
	// list is written back.
	byLabel := -1
	// unclaimed is a row for the same gateway that carries no credential at all. It is what an
	// older release left behind before every account got its own row, so this account adopts it
	// instead of adding a duplicate. A row that already holds another account's credential is
	// never adopted.
	unclaimed := -1
	channelBase := canonicalProviderBaseURL(result.BaseURL)
	wantedLabel := strings.TrimSpace(defaultLabel)
	if trimmed := strings.TrimSpace(label); trimmed != "" {
		wantedLabel = trimmed
	}
	for index, entry := range entries {
		cloned := make(map[string]any, len(entry)+1)
		for key, value := range entry {
			cloned[key] = value
		}
		if canonicalProviderBaseURL(aiProviderChannelBaseURL(cloned)) == channelBase {
			switch {
			case providerChannelHoldsCredential(cloned, apiKey):
				if target < 0 {
					target = index
				}
			case strings.EqualFold(strings.TrimSpace(aiProviderChannelName(cloned)), wantedLabel) && byLabel < 0:
				byLabel = index
			case !providerChannelHoldsAnyCredential(cloned) && unclaimed < 0:
				unclaimed = index
			}
		}
		items = append(items, cloned)
	}
	// An adopted row belonged to this account but holds a superseded credential, so the credential
	// is replaced rather than added next to the stale one.
	adopted := false
	if target < 0 {
		target = byLabel
		adopted = target >= 0
	}
	if target < 0 {
		target = unclaimed
		adopted = target >= 0
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = defaultLabel
	}
	if target < 0 {
		// No row carries this credential and no row is free to adopt, so this account gets its
		// own row even though other accounts already publish the same base URL.
		items = append(items, map[string]any{})
		target = len(items) - 1
		result.Created = true
	}
	entry := items[target]
	entry["base-url"] = result.BaseURL
	// Only a row this bind created (or a row that carries no label at all yet) is
	// named: the operator's name has to survive a plain re-bind, and a bind must
	// never rename a sibling account's row.
	if result.Created || strings.TrimSpace(aiProviderChannelName(entry)) == "" {
		entry["name"] = label
	}
	// The credential lives in the weighted key list; the legacy top-level field is
	// accepted by CPA's JSON decoder but ignored for OpenAI-compatible channels.
	entry["api-key-entries"] = mergeProviderChannelKeyEntries(entry["api-key-entries"], apiKey, adopted)
	// The channel carries the operator's automatic retry budget, so a channel this
	// bind creates or reuses inherits the setting without waiting for the next
	// apply pass. CPA reads this row field as the credential's request_retry.
	entry["request-retry"] = a.autoRetry.Attempts()
	delete(entry, "api-key")
	mergedHeaders, _ := entry["headers"].(map[string]any)
	if mergedHeaders == nil {
		mergedHeaders = map[string]any{}
	}
	for name, value := range headers {
		mergedHeaders[name] = value
	}
	entry["headers"] = mergedHeaders
	// Publish the verified catalog on the channel: CPA matches routed requests
	// against this list, so a channel without it cannot serve the models the
	// operator just tested.
	channelModels := mergeProviderChannelModels(entry["models"], models, aliases)
	entry["models"] = channelModels
	// The operator-facing count is per distinct upstream model id, not per row:
	// the rows a binding does not own are operator additions and can outnumber it.
	result.Models = providerChannelPublishedModelCount(models, channelModels)
	items[target] = entry

	writer, errWriter := a.newWriteManagementClient(managementKey)
	if errWriter != nil {
		return result, errWriter
	}
	defer clearManagementWriterSecrets(writer)
	if errWrite := writer.putAIProviderChannel(ctx, listKind, items); errWrite != nil {
		return result, fmt.Errorf("CPA channel could not be saved")
	}
	// Re-read the channel list so the newly written channel's CPA auth index is
	// recorded for session attribution immediately after binding.
	if entries, errEntries := a.aiProviderChannelEntries(ctx, managementKey, listKind); errEntries == nil {
		_ = a.syncAIProviderChannelBindings(listKind, entries)
		// CPA assigns the auth index on write and can change it, so the account
		// whose row this bind owns records every index the row now carries. That is
		// what attributes a usage callback that only names the index.
		if accountID != "" && a.clinePass != nil {
			a.clinePass.SetRouteAuthIndexes(accountID, clinePassRouteAuthIndexes(entries, result.BaseURL, apiKey, label))
		}
	}
	result.Index = target
	result.ChannelKey = fmt.Sprintf("%s:%d", listKind, target)
	return result, nil
}

// mergeProviderChannelKeyEntries upserts one credential in the weighted key list
// of a channel row. The row that already carries the credential is updated in
// place, so any extra weighted rows the operator added for that credential
// survive; a credential the row does not carry yet is appended as its own
// weighted row. One row belongs to one account, so binding a second account must
// never overwrite the first account's key: that would leave the first account
// unroutable and unattributable.
func mergeProviderChannelKeyEntries(existing any, apiKey string, replaceStale bool) []map[string]any {
	wanted := strings.TrimSpace(apiKey)
	rows := make([]map[string]any, 0, 2)
	if list, ok := existing.([]any); ok {
		for _, item := range list {
			record, isRecord := item.(map[string]any)
			if !isRecord {
				continue
			}
			cloned := make(map[string]any, len(record)+1)
			for key, value := range record {
				cloned[key] = value
			}
			rows = append(rows, cloned)
		}
	}
	for _, row := range rows {
		if key, ok := row["api-key"].(string); ok && strings.TrimSpace(key) == wanted {
			row["api-key"] = wanted
			return rows
		}
	}
	if replaceStale && len(rows) > 0 {
		// One account, one row: the credential this row held was superseded by a rotation, so it is
		// replaced. Extra rows the operator added for the same row are kept as they are.
		rows[0]["api-key"] = wanted
		return rows
	}
	return append(rows, map[string]any{"api-key": wanted})
}

// providerChannelHoldsCredential reports whether one channel row carries the
// supplied credential in its weighted key list. The credential identifies the row
// of one account among the rows that share a base URL, and a credential that
// appears in a later weighted row still identifies the row.
func providerChannelHoldsCredential(entry map[string]any, apiKey string) bool {
	wanted := strings.TrimSpace(apiKey)
	if wanted == "" {
		return false
	}
	if list, ok := entry["api-key-entries"].([]any); ok {
		for _, item := range list {
			record, isRecord := item.(map[string]any)
			if !isRecord {
				continue
			}
			if key, isText := record["api-key"].(string); isText && strings.TrimSpace(key) == wanted {
				return true
			}
		}
	}
	if key, isText := entry["api-key"].(string); isText && strings.TrimSpace(key) == wanted {
		return true
	}
	return false
}

// providerChannelHoldsAnyCredential reports whether a channel row already owns a credential, so an
// adoption never steals a row that belongs to another account.
func providerChannelHoldsAnyCredential(entry map[string]any) bool {
	if key, isText := entry["api-key"].(string); isText && strings.TrimSpace(key) != "" {
		return true
	}
	list, ok := entry["api-key-entries"].([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		record, isRecord := item.(map[string]any)
		if !isRecord {
			continue
		}
		if key, isText := record["api-key"].(string); isText && strings.TrimSpace(key) != "" {
			return true
		}
	}
	return false
}

// mergeProviderChannelModels publishes a model catalog on a CPA channel.
//
// A row is the CPA pair {"name": <upstream id>, "alias": <client-facing id>}.
// The optional aliases map carries the client-facing alias the current setting
// publishes for every id, and that alias replaces the identity: a model the
// setting publishes under a stripped id is listed under that id alone. The merge
// is alias-set aware instead of alias-replacing: the rows of one published id
// converge to exactly that alias set; a row whose alias is no longer implied is
// dropped, a missing (name, alias) pair is appended, and duplicate pairs are
// collapsed, so binding twice with the same setting leaves the row set unchanged.
// Rows for ids the binding does not
// publish are operator additions and stay untouched.
//
// Without the aliases map an existing row's alias is preserved and only missing
// ids are appended, which is what the OpenCode bindings (no prefix setting)
// rely on.
func mergeProviderChannelModels(existing any, models []string, aliases ...map[string][]string) []map[string]any {
	if len(aliases) > 0 && aliases[0] != nil {
		return mergeProviderChannelAliasModels(existing, models, aliases[0])
	}
	return mergeProviderChannelIdentityModels(existing, models)
}

// mergeProviderChannelIdentityModels keeps every existing row under its own
// alias name and appends one identity row per published id that is not routable
// yet. It is the OpenCode path, which has no alias setting.
func mergeProviderChannelIdentityModels(existing any, models []string) []map[string]any {
	wanted := make(map[string]bool, len(models))
	order := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || wanted[model] {
			continue
		}
		wanted[model] = true
		order = append(order, model)
	}
	rows := make([]map[string]any, 0, len(order)+2)
	seen := make(map[string]bool, len(order))
	appendRow := func(row map[string]any) {
		rows = append(rows, row)
		for _, field := range []string{"name", "alias"} {
			if value, ok := row[field].(string); ok && strings.TrimSpace(value) != "" {
				seen[strings.TrimSpace(value)] = true
			}
		}
	}
	switch list := existing.(type) {
	case []any:
		for _, item := range list {
			if record, isRecord := item.(map[string]any); isRecord {
				cloned := make(map[string]any, len(record)+1)
				for key, value := range record {
					cloned[key] = value
				}
				appendRow(cloned)
				continue
			}
			// A legacy or hand-written string row still names a routable model,
			// so it is preserved instead of being silently erased.
			if text, isText := item.(string); isText && strings.TrimSpace(text) != "" {
				appendRow(map[string]any{"name": strings.TrimSpace(text), "alias": strings.TrimSpace(text)})
			}
		}
	case []map[string]any:
		for _, item := range list {
			cloned := make(map[string]any, len(item)+1)
			for key, value := range item {
				cloned[key] = value
			}
			appendRow(cloned)
		}
	}
	for _, model := range order {
		if seen[model] {
			continue
		}
		seen[model] = true
		rows = append(rows, map[string]any{"name": model, "alias": model})
	}
	return rows
}

// mergeProviderChannelAliasModels converges the rows of every published id to
// the alias set the caller asked for, and that set is authoritative: CPA
// advertises each row's alias in /v1/models, so a caller that publishes a model
// under a different id (the stripped Cline Pass id) must replace the identity
// alias rather than add to it. A model the caller did not describe keeps its
// identity. An existing row for a published id is kept when its alias is part
// of that set (an empty alias reads as the identity) and dropped otherwise;
// rows for other ids are operator additions and are preserved. The returned set
// is deduplicated by (name, alias) pair, so running the merge on its own output
// is a no-op.
func mergeProviderChannelAliasModels(existing any, models []string, aliases map[string][]string) []map[string]any {
	wanted := make(map[string][]string, len(models))
	order := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := wanted[model]; exists {
			continue
		}
		desired := make([]string, 0, len(aliases[model]))
		for _, alias := range aliases[model] {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			duplicate := false
			for _, kept := range desired {
				if kept == alias {
					duplicate = true
					break
				}
			}
			if !duplicate {
				desired = append(desired, alias)
			}
		}
		if len(desired) == 0 {
			// A model the caller did not describe still publishes its identity.
			desired = []string{model}
		}
		wanted[model] = desired
		order = append(order, model)
	}
	rows := make([]map[string]any, 0, len(order)+2)
	pairs := make(map[string]bool, len(order)+2)
	record := func(name, alias string) bool {
		key := name + "\x00" + alias
		if pairs[key] {
			return false
		}
		pairs[key] = true
		return true
	}
	appendRow := func(row map[string]any) {
		if rawName, ok := row["name"].(string); ok {
			if name := strings.TrimSpace(rawName); name != "" {
				if desired, publish := wanted[name]; publish {
					rawAlias, _ := row["alias"].(string)
					alias := strings.TrimSpace(rawAlias)
					if alias == "" {
						// A row without an alias still routes the identity.
						alias = name
					}
					implied := false
					for _, candidate := range desired {
						if candidate == alias {
							implied = true
							break
						}
					}
					if !implied {
						// A stale alias the current setting no longer implies.
						return
					}
					row["name"] = name
					row["alias"] = alias
					if !record(name, alias) {
						return
					}
					rows = append(rows, row)
					return
				}
			}
		}
		// An operator-added row for an id this binding does not publish.
		name, _ := row["name"].(string)
		alias, _ := row["alias"].(string)
		name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
		if name != "" || alias != "" {
			if !record(name, alias) {
				return
			}
		}
		rows = append(rows, row)
	}
	switch list := existing.(type) {
	case []any:
		for _, item := range list {
			if record, isRecord := item.(map[string]any); isRecord {
				cloned := make(map[string]any, len(record)+1)
				for key, value := range record {
					cloned[key] = value
				}
				appendRow(cloned)
				continue
			}
			if text, isText := item.(string); isText && strings.TrimSpace(text) != "" {
				appendRow(map[string]any{"name": strings.TrimSpace(text), "alias": strings.TrimSpace(text)})
			}
		}
	case []map[string]any:
		for _, item := range list {
			cloned := make(map[string]any, len(item)+1)
			for key, value := range item {
				cloned[key] = value
			}
			appendRow(cloned)
		}
	}
	for _, model := range order {
		for _, alias := range wanted[model] {
			if record(model, alias) {
				rows = append(rows, map[string]any{"name": model, "alias": alias})
			}
		}
	}
	return rows
}

// providerChannelPublishedModelCount reports the number of distinct upstream model ids
// one bind covers, so the operator-facing count follows the catalog rather than
// the raw row count (an operator may add rows of their own). An empty requested
// catalog falls back to the row count so re-binding an existing channel keeps
// reporting what it serves.
func providerChannelPublishedModelCount(models []string, rows []map[string]any) int {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if trimmed := strings.TrimSpace(model); trimmed != "" {
			seen[trimmed] = struct{}{}
		}
	}
	if len(seen) > 0 {
		return len(seen)
	}
	return len(rows)
}

// newWriteManagementClient builds the management client used for channel writes.
func (a *App) newWriteManagementClient(managementKey string) (*managementClient, error) {
	if a == nil {
		return nil, fmt.Errorf("management client is unavailable")
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return nil, fmt.Errorf("management client is unavailable")
	}
	return client, nil
}

// putAIProviderChannel rewrites one CPA channel list.
func (c *managementClient) putAIProviderChannel(ctx context.Context, kind string, items []map[string]any) error {
	if !supportedAIProviderProxyKind(kind) {
		return fmt.Errorf("AI provider channel %s is not supported", kind)
	}
	encoded, errEncode := json.Marshal(items)
	if errEncode != nil {
		return fmt.Errorf("AI provider channel payload could not be encoded")
	}
	return c.requestJSON(ctx, http.MethodPut, "/v0/management/"+kind, strings.NewReader(string(encoded)), "application/json", nil)
}
