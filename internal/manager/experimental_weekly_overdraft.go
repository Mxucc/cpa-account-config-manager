package manager

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	defaultExperimentalRequestBodyLimit = 32 * 1024 * 1024
	experimentalExecInput               = `const r = await tools.exec_command({"cmd":"true","yield_time_ms":1000,"max_output_tokens":1000}); text(r.output);`
	weeklyOverdraftProbeAttempts        = 5
	weeklyOverdraftInputMarker          = `"input"`
)

var weeklyOverdraftInputMarkerBytes = []byte(weeklyOverdraftInputMarker)

type WeeklyOverdraftExperiment struct {
	enabled      func() bool
	gate         overdraftGate
	maxBodyBytes int
	newCallID    func() (string, bool)
}

// overdraftGate lets the interceptor read the current usage percentages and
// overdraft cycle states and record successful injections without depending on
// the usage tracker directly.
type overdraftGate interface {
	OverdraftGateState(string) overdraftGateState
	NoteOverdraftInjection(string)
}

func NewWeeklyOverdraftExperiment(enabled func() bool) *WeeklyOverdraftExperiment {
	if enabled == nil {
		enabled = func() bool { return false }
	}
	return &WeeklyOverdraftExperiment{
		enabled:      enabled,
		maxBodyBytes: defaultExperimentalRequestBodyLimit,
		newCallID:    newExperimentalCallID,
	}
}

// WithOverdraftGate attaches the usage-backed gate used for the 95% pre-arm
// and the auto-disable veto. Without a gate the experiment keeps the legacy
// always-inject behavior so callers that only exercise the transformer can
// stay independent of the usage tracker.
func (e *WeeklyOverdraftExperiment) WithOverdraftGate(gate overdraftGate) *WeeklyOverdraftExperiment {
	if e == nil {
		return nil
	}
	e.gate = gate
	return e
}

// overdraftInjectionEligible mirrors the sub2api-overdraft pre-arm decision:
// inject while an open overdraft cycle's recovery deadline is still in the
// future (pending/passed/inconclusive), never inject for a cycle confirmed
// failed or recovered, and otherwise inject once a window crosses 95% while
// its reset deadline is still in the future. An expired cycle or window falls
// back to the plain 95% pre-arm rule exactly like the reference fork.
func (e *WeeklyOverdraftExperiment) overdraftInjectionEligible(authIndex string) bool {
	if e == nil || e.gate == nil {
		return true
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return false
	}
	state := e.gate.OverdraftGateState(authIndex)
	if !state.Has {
		return false
	}
	now := time.Now().UTC()
	windowEligible := func(percent float64, status string, resetAt, recoverAt time.Time) bool {
		switch status {
		case overdraftStatusPassed, overdraftStatusPending, overdraftStatusInconclusive:
			if !recoverAt.IsZero() && recoverAt.After(now) {
				return true
			}
			// Recovery deadline already passed: fall through to the plain 95%
			// pre-arm rule exactly like the reference fork.
		case overdraftStatusFailed, overdraftStatusRecovered:
			return false
		}
		if percent < overdraftPrearmPercent {
			return false
		}
		return resetAt.IsZero() || resetAt.After(now)
	}
	return windowEligible(state.FiveHourUsedPercent, state.FiveHourCycleStatus, state.FiveHourResetAt, state.FiveHourRecoverAt) ||
		windowEligible(state.SevenDayUsedPercent, state.SevenDayCycleStatus, state.SevenDayResetAt, state.SevenDayRecoverAt)
}

// overdraftAuthIndexFromMetadata resolves the selected account key from the
// CPA request metadata. runtimeIdentityFromMetadata treats "selected_auth_id"
// as an opaque credential id and returns an empty auth index, but the CPA
// request lifecycle populates that key with the selected account (see
// AccountConcurrencyService), so the overdraft gate must use the raw value
// from either the index or the id key family.
func overdraftAuthIndexFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"selected_auth_index", "selected_auth_id", "auth_index", "auth_id"} {
		if value, ok := metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func (e *WeeklyOverdraftExperiment) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if !e.RequestInterceptionActive() || !e.RequestInterceptionAcceptsFormat(request.ToFormat) ||
		len(request.Body) == 0 || len(request.Body) > e.bodyLimit() {
		return cpaapi.RequestInterceptResponse{}, false
	}
	authIndex := overdraftAuthIndexFromMetadata(request.Metadata)
	if !e.overdraftInjectionEligible(authIndex) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// Match the field marker before invoking the JSON decoder. This is the same
	// cheap preflight used by ai-bridge for request-body transforms; normal
	// Codex requests without an input array stay on the no-allocation path.
	if !bytes.Contains(request.Body, weeklyOverdraftInputMarkerBytes) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	var document struct {
		Input json.RawMessage `json:"input"`
	}
	if errDecode := json.Unmarshal(request.Body, &document); errDecode != nil || len(document.Input) == 0 {
		return cpaapi.RequestInterceptResponse{}, false
	}
	var input []json.RawMessage
	if errInput := json.Unmarshal(document.Input, &input); errInput != nil || len(input) == 0 {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// Never double-inject: a replayed or forwarded request that already
	// carries the no-op exec pair (this plugin or the sub2api-overdraft fork)
	// stays unchanged, mirroring codexQuotaOverdraftInputHasInjection.
	if inputHasWeeklyOverdraftInjection(input) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	var last struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if errLast := json.Unmarshal(input[len(input)-1], &last); errLast != nil || last.Type != "message" || last.Role != "user" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	callID, ok := e.newID()
	if !ok {
		return cpaapi.RequestInterceptResponse{}, false
	}
	call, errCall := json.Marshal(map[string]any{
		"type": "custom_tool_call", "name": "exec", "call_id": callID, "input": experimentalExecInput,
	})
	output, errOutput := json.Marshal(map[string]any{
		"type": "custom_tool_call_output", "call_id": callID,
		"output": []map[string]string{{"type": "input_text", "text": "Script completed\nWall time 0.0 seconds\nOutput:\n"}},
	})
	if errCall != nil || errOutput != nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	updatedInput := make([]byte, 0, len(document.Input)+len(call)+len(output)+2)
	trimmedInput := bytes.TrimSpace(document.Input)
	if len(trimmedInput) < 2 || trimmedInput[0] != '[' || trimmedInput[len(trimmedInput)-1] != ']' {
		return cpaapi.RequestInterceptResponse{}, false
	}
	updatedInput = append(updatedInput, trimmedInput[:len(trimmedInput)-1]...)
	if len(input) > 0 {
		updatedInput = append(updatedInput, ',')
	}
	updatedInput = append(updatedInput, call...)
	updatedInput = append(updatedInput, ',')
	updatedInput = append(updatedInput, output...)
	updatedInput = append(updatedInput, ']')
	updated, replaced := replaceTopLevelJSONFieldValue(request.Body, "input", document.Input, updatedInput)
	if !replaced || len(updated) > e.bodyLimit() {
		return cpaapi.RequestInterceptResponse{}, false
	}
	// Record the injection as passed-evidence for the account so a later
	// successful usage record can confirm the overdraft cycle (the usage ABI
	// does not expose the originating RequestID).
	if e.gate != nil {
		e.gate.NoteOverdraftInjection(authIndex)
	}
	return cpaapi.RequestInterceptResponse{Body: updated}, true
}

// replaceTopLevelJSONFieldValue patches one object field without re-encoding
// the complete request. Re-encoding large Codex prompts is a measurable part
// of TTFT, and preserving the surrounding bytes also keeps unknown fields
// untouched. The scanner only locates a value; json.Unmarshal remains the
// correctness gate for the request and input array.
func replaceTopLevelJSONFieldValue(document []byte, field string, oldValue, newValue []byte) ([]byte, bool) {
	fieldBytes := []byte(field)
	index := skipJSONWhitespace(document, 0)
	if index >= len(document) || document[index] != '{' {
		return nil, false
	}
	index++
	for {
		index = skipJSONWhitespace(document, index)
		if index >= len(document) {
			return nil, false
		}
		if document[index] == '}' {
			return nil, false
		}
		keyStart := index
		keyEnd, ok := scanJSONString(document, keyStart)
		if !ok {
			return nil, false
		}
		index = skipJSONWhitespace(document, keyEnd)
		if index >= len(document) || document[index] != ':' {
			return nil, false
		}
		valueStart := skipJSONWhitespace(document, index+1)
		valueEnd, ok := scanJSONValue(document, valueStart)
		if !ok {
			return nil, false
		}
		if bytes.Equal(document[keyStart+1:keyEnd-1], fieldBytes) &&
			bytes.Equal(document[valueStart:valueEnd], bytes.TrimSpace(oldValue)) {
			updated := make([]byte, 0, len(document)-valueEnd+valueStart+len(newValue))
			updated = append(updated, document[:valueStart]...)
			updated = append(updated, newValue...)
			updated = append(updated, document[valueEnd:]...)
			return updated, true
		}
		index = skipJSONWhitespace(document, valueEnd)
		if index >= len(document) || document[index] == '}' {
			return nil, false
		}
		if document[index] != ',' {
			return nil, false
		}
		index++
	}
}

func skipJSONWhitespace(document []byte, index int) int {
	for index < len(document) {
		switch document[index] {
		case ' ', '\t', '\r', '\n':
			index++
		default:
			return index
		}
	}
	return index
}

func scanJSONString(document []byte, start int) (int, bool) {
	if start >= len(document) || document[start] != '"' {
		return 0, false
	}
	for index := start + 1; index < len(document); index++ {
		switch document[index] {
		case '\\':
			index++
		case '"':
			return index + 1, true
		}
	}
	return 0, false
}

func scanJSONValue(document []byte, start int) (int, bool) {
	if start >= len(document) {
		return 0, false
	}
	switch document[start] {
	case '"':
		return scanJSONString(document, start)
	case '{', '[':
		opening := document[start]
		closing := byte('}')
		if opening == '[' {
			closing = ']'
		}
		depth := 0
		for index := start; index < len(document); index++ {
			switch document[index] {
			case '"':
				var ok bool
				index, ok = scanJSONString(document, index)
				if !ok {
					return 0, false
				}
				index--
			case opening:
				depth++
			case closing:
				depth--
				if depth == 0 {
					return index + 1, true
				}
			}
		}
		return 0, false
	default:
		index := start
		for index < len(document) && !isJSONValueDelimiter(document[index]) {
			index++
		}
		return index, index > start
	}
}

func isJSONValueDelimiter(value byte) bool {
	switch value {
	case ' ', '\t', '\r', '\n', ',', '}', ']':
		return true
	default:
		return false
	}
}

func (e *WeeklyOverdraftExperiment) RequestInterceptionActive() bool {
	return e != nil && e.enabled != nil && e.enabled()
}

func (e *WeeklyOverdraftExperiment) RequestInterceptionAcceptsFormat(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "codex")
}

func (e *WeeklyOverdraftExperiment) AllowUsageAutoDisable(record cpaapi.UsageRecord, _ time.Time) bool {
	if e == nil || e.enabled == nil || !e.enabled() || e.gate == nil {
		return true
	}
	// UsageRecord.AuthIndex is the primary binding key, but fall back to the
	// credential ID when the host only reports AuthID so the veto still reaches
	// the same usage state (OverdraftGateState reverse-resolves credential IDs).
	state := e.gate.OverdraftGateState(strings.TrimSpace(firstNonEmpty(record.AuthIndex, record.AuthID)))
	if !state.Has {
		return true
	}
	// A cycle that is pending, passed, or inconclusive is still overdrafting:
	// veto the automatic disable so business traffic can keep collecting
	// evidence. Only a confirmed-failed (or recovered) cycle lets the account
	// be disabled.
	if overdraftCycleStatusActive(state.FiveHourCycleStatus) || overdraftCycleStatusActive(state.SevenDayCycleStatus) {
		return false
	}
	return true
}

func overdraftCycleStatusActive(status string) bool {
	switch status {
	case overdraftStatusPending, overdraftStatusPassed, overdraftStatusInconclusive:
		return true
	default:
		return false
	}
}

func (e *WeeklyOverdraftExperiment) AllowInspectionAutoDisable(result InspectionResult) bool {
	if e == nil || e.enabled == nil || !e.enabled() || result.ReasonCode != "quota_exhausted" && result.ReasonCode != "quota_limited" {
		return true
	}
	if !codexQuotaWindowSupportsOverdraftProbe(result.QuotaWindow) {
		return true
	}
	return result.AutoDisableProbeStatus == InspectionAutoDisableProbeFailed &&
		result.AutoDisableProbeAttempts >= weeklyOverdraftProbeAttempts
}

func (e *WeeklyOverdraftExperiment) AutomaticDisableProbePlan(account Account, result InspectionResult, preferredModel string) (AutomaticDisableProbePlan, bool) {
	if e == nil || e.enabled == nil || !e.enabled() ||
		(result.ReasonCode != "quota_exhausted" && result.ReasonCode != "quota_limited") ||
		!codexQuotaWindowSupportsOverdraftProbe(result.QuotaWindow) ||
		!strings.EqualFold(strings.TrimSpace(firstNonEmpty(account.Provider, account.Type)), "codex") &&
			!isAgentIdentityProvider(firstNonEmpty(account.Provider, account.Type)) {
		return AutomaticDisableProbePlan{}, false
	}
	return AutomaticDisableProbePlan{
		Name:         "weekly_overdraft",
		AttemptLimit: weeklyOverdraftProbeAttempts,
		Models:       []string{preferredModel, defaultCodexFallbackModel, codexCompatibilityMiniModel},
		Request: ModelTestRequest{
			ExperimentalWeeklyOverdraft: true,
			Inspection:                  true,
			SelectPolicyFallback:        true,
		},
	}, true
}

func codexQuotaWindowSupportsOverdraftProbe(quotaWindow string) bool {
	switch normalizeInspectionQuotaWindow(quotaWindow) {
	case InspectionQuotaWindowFiveHour, InspectionQuotaWindowFiveHourFallback,
		InspectionQuotaWindowSevenDay, InspectionQuotaWindowMultiple:
		return true
	default:
		return false
	}
}

func (e *WeeklyOverdraftExperiment) bodyLimit() int {
	if e.maxBodyBytes > 0 {
		return e.maxBodyBytes
	}
	return defaultExperimentalRequestBodyLimit
}

func (e *WeeklyOverdraftExperiment) newID() (string, bool) {
	if e.newCallID == nil {
		return newExperimentalCallID()
	}
	return e.newCallID()
}

// weeklyOverdraftCallIDPrefixes covers the call-id families this plugin and
// the sub2api-overdraft fork use for the no-op exec pair, so forwarded or
// replayed requests are recognized as already injected.
var weeklyOverdraftCallIDPrefixes = []string{"call_cpa_overdraft_", "call_sub2api_overdraft_"}

func inputHasWeeklyOverdraftInjection(input []json.RawMessage) bool {
	for _, raw := range input {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil || item.Type != "custom_tool_call" {
			continue
		}
		for _, prefix := range weeklyOverdraftCallIDPrefixes {
			if strings.HasPrefix(item.CallID, prefix) {
				return true
			}
		}
	}
	return false
}

func newExperimentalCallID() (string, bool) {
	var random [12]byte
	if _, errRead := rand.Read(random[:]); errRead != nil {
		return "", false
	}
	return "call_cpa_overdraft_" + hex.EncodeToString(random[:]), true
}
