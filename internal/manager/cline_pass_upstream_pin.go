package manager

import (
	"bytes"
	"encoding/json"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// Cline Pass serves one subscription model from several upstreams, so a
// conversation can land on a different upstream between two steps. Prompt caches
// belong to one upstream, so the step that switches re-reads the whole context
// and can cool what the previous upstream had cached: a long step with a large
// context then spends far more of the subscription than the same conversation
// pinned to one upstream would.
//
// The gateway pins the upstream from two request-body fields, and CPA's own
// `payload.override-raw` rules write the same two fields. This transformer writes
// them in the request path instead, because the plugin already owns the Cline
// Pass channel: no host-config edit, no restart, and the switch is per
// installation. It only fills the fields in when they are absent, so a caller
// that pinned the upstream itself keeps its own choice.
const (
	// clinePassUpstreamProviderOptionsField carries the gateway routing rule.
	clinePassUpstreamProviderOptionsField = "providerOptions"
	// clinePassUpstreamProviderField carries the legacy routing rule, which older
	// gateways read.
	clinePassUpstreamProviderField = "provider"
	// clinePassUpstreamProviderName is the upstream the switch pins requests to.
	clinePassUpstreamProviderName = "deepseek"
)

// clinePassUpstreamInjection is spliced into a body that carries neither field.
// It holds the exact raw shape the gateway documents, so the plugin cannot
// invent a nesting the gateway would ignore.
var clinePassUpstreamInjection = []byte(`"providerOptions":{"gateway":{"only":["deepseek"]}},"provider":{"only":["deepseek"]}`)

// clinePassOpenAIChatMarker is the cheap preflight before any decoding: only an
// OpenAI-style chat body carries the fields the gateway reads.
var clinePassOpenAIChatMarker = []byte(`"messages"`)

// ClinePassUpstreamPinner pins Cline Pass DeepSeek requests to DeepSeek's own
// upstream while the stored switch is on.
type ClinePassUpstreamPinner struct {
	service *ClinePassService
}

func NewClinePassUpstreamPinner(service *ClinePassService) *ClinePassUpstreamPinner {
	return &ClinePassUpstreamPinner{service: service}
}

// RequestInterceptionActive keeps an installation that never turned the switch
// on, or that has no stored account, on the untouched request path.
func (p *ClinePassUpstreamPinner) RequestInterceptionActive() bool {
	return p != nil && p.service != nil && p.service.PinsRequestsToDeepseekUpstream()
}

// RequestInterceptionAcceptsFormat accepts every format: whether a request is a
// Cline Pass DeepSeek request is decided per request from its model id.
func (p *ClinePassUpstreamPinner) RequestInterceptionAcceptsFormat(string) bool { return p != nil }

// RequestInterceptionBeforeActive implements the before-path marker: this
// transformer only ever rewrites the outgoing request.
func (p *ClinePassUpstreamPinner) RequestInterceptionBeforeActive() bool {
	return p.RequestInterceptionActive()
}

// InterceptRequest adds the two upstream-pinning fields to a Cline Pass DeepSeek
// chat request that carries neither of them.
func (p *ClinePassUpstreamPinner) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if !p.RequestInterceptionActive() || len(request.Body) == 0 {
		return cpaapi.RequestInterceptResponse{}, false
	}
	trimmed := bytes.TrimSpace(request.Body)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if !bytes.Contains(trimmed, clinePassOpenAIChatMarker) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// Only the top-level keys matter, so the values stay raw and a large body is
	// never deep-decoded on the request path.
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(trimmed, &fields); errDecode != nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if _, exists := fields[clinePassUpstreamProviderOptionsField]; exists {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if _, exists := fields[clinePassUpstreamProviderField]; exists {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// CPA reports the resolved model and the model the client asked for, and they
	// can differ once the published-alias mapping is applied, so both are checked.
	model := ""
	for _, candidate := range []string{request.Model, request.RequestedModel} {
		if clinePassPinsDeepseekUpstream(candidate) {
			model = candidate
			break
		}
	}
	if model == "" {
		var bodyModel string
		if raw, exists := fields["model"]; exists && json.Unmarshal(raw, &bodyModel) == nil && clinePassPinsDeepseekUpstream(bodyModel) {
			model = bodyModel
		}
	}
	if model == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// Splice the two fields in behind the opening brace: every other byte of the
	// caller's payload, including its formatting, is preserved.
	inner := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	updated := make([]byte, 0, len(trimmed)+len(clinePassUpstreamInjection)+2)
	updated = append(updated, '{')
	updated = append(updated, clinePassUpstreamInjection...)
	if len(inner) > 0 {
		updated = append(updated, ',')
		updated = append(updated, inner...)
	}
	updated = append(updated, '}')
	return cpaapi.RequestInterceptResponse{Body: updated}, true
}

// clinePassPinsDeepseekUpstream reports whether a model id names a DeepSeek model
// this plugin publishes for Cline Pass. The published rows may drop the literal
// cline-pass/ prefix, so both spellings are accepted, and a model outside the
// Cline Pass catalog is never pinned even when its id mentions deepseek: that
// keeps the switch inside the channel the plugin owns.
func clinePassPinsDeepseekUpstream(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || !strings.Contains(strings.ToLower(trimmed), clinePassUpstreamProviderName) {
		return false
	}
	for _, catalogModel := range clinePassCatalog {
		if catalogModel.ID == trimmed || strings.TrimPrefix(catalogModel.ID, clinePassModelPrefix) == trimmed {
			return true
		}
	}
	return false
}
