package manager

import (
	"context"
	"strings"
)

// OpenCode gateways are OpenAI-compatible, so CPA can route their models
// natively. Binding writes (or updates) one CPA OpenAI-compatible channel that
// points at the OpenCode base URL with the account credential and the OpenCode
// client headers, which is what makes the models reachable through CPA without a
// separate proxy process.

const (
	openCodeBoundChannelName = "OpenCode"
)

// openCodeChannelHeaders are the headers the upstream expects from a client. CPA
// forwards configured channel headers, so the same identity used by the model
// probe is applied to routed traffic.
//
// The session header is included as a static baseline: OpenCode Go rejects a
// request that carries no x-opencode-session at all, and the plugin's request
// interceptor (which replaces this value with a per-conversation id whenever it
// runs) is only attached on hosts that support request interception. The baseline
// therefore keeps an older host routable instead of failing every request.
func openCodeChannelHeaders() map[string]string {
	return map[string]string{
		"x-opencode-client":  "cli",
		"x-opencode-session": openCodeChannelSessionBaseline,
		"User-Agent":         openCodeClientUserAgent(),
	}
}

// openCodeChannelSessionBaseline is the channel-level session id used until the
// request interceptor substitutes a per-conversation one.
const openCodeChannelSessionBaseline = "oc-cli-baseline"

// bindOpenCodeChannel writes one OpenAI-compatible CPA channel for an OpenCode
// credential. The row that already carries this credential is updated in place so
// repeated binds are idempotent and keep unrelated fields untouched; another
// account of the same kind (same gateway base URL, other credential) gets its own
// row instead of sharing this one.
func (a *App) bindOpenCodeChannel(ctx context.Context, managementKey, baseURL, apiKey, label string, models []string) (ProviderChannelBindingResult, error) {
	// OpenCode has no alias setting, so its rows keep any operator alias that is
	// already configured and only missing ids are appended.
	return a.bindOpenAICompatibleChannel(ctx, managementKey, openCodeAPIBase(baseURL)+"/v1", apiKey, label, openCodeBoundChannelName, models, openCodeChannelHeaders(), nil)
}

// openCodeZenChannelLabel names the CPA channel of one OpenCode Zen account. Every
// Zen account may share the same gateway base URL, so the label carries the
// account identity (its operator name, else its id) to keep the bound rows
// distinguishable; the bare constant remains the fallback when neither is known.
func openCodeZenChannelLabel(service *OpenCodeZenService, accountID string) string {
	label := openCodeBoundChannelName + " Zen"
	if service == nil {
		return label
	}
	id := strings.TrimSpace(accountID)
	if id == "" {
		return label
	}
	for _, account := range service.ListAccounts() {
		if account.ID != id {
			continue
		}
		if name := strings.TrimSpace(account.Name); name != "" {
			return label + " " + name
		}
		break
	}
	return label + " " + id
}
