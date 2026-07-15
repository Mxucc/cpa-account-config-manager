package manager

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

const (
	maxNoteLength        = 2000
	maxPrefixLength      = 256
	maxProxyURLLength    = 4096
	maxHeaderNameLength  = 128
	maxHeaderValueLength = 8192
	maxHeaderOperations  = 100
)

type TargetScope struct {
	Mode    string         `json:"mode"`
	IDs     []string       `json:"ids,omitempty"`
	Filters AccountFilters `json:"filters,omitempty"`
}

type HeaderPatch struct {
	Set    map[string]string `json:"set,omitempty"`
	Remove []string          `json:"remove,omitempty"`
}

type BatchPatch struct {
	Disabled   *bool        `json:"disabled,omitempty"`
	Priority   *int         `json:"priority,omitempty"`
	Note       *string      `json:"note,omitempty"`
	Prefix     *string      `json:"prefix,omitempty"`
	ProxyURL   *string      `json:"proxy_url,omitempty"`
	Websockets *bool        `json:"websockets,omitempty"`
	Headers    *HeaderPatch `json:"headers,omitempty"`
}

type PatchSummary struct {
	Fields        []string `json:"fields"`
	HeaderSet     []string `json:"header_set,omitempty"`
	HeaderRemove  []string `json:"header_remove,omitempty"`
	ProxyMutation bool     `json:"proxy_mutation"`
}

func (scope TargetScope) Validate() (TargetScope, error) {
	scope.Mode = strings.ToLower(strings.TrimSpace(scope.Mode))
	switch scope.Mode {
	case "selected":
		seen := make(map[string]struct{}, len(scope.IDs))
		ids := make([]string, 0, len(scope.IDs))
		for _, rawID := range scope.IDs {
			id := strings.TrimSpace(rawID)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return TargetScope{}, fmt.Errorf("selected scope requires at least one account id")
		}
		scope.IDs = ids
		scope.Filters = AccountFilters{}
	case "filtered":
		scope.IDs = nil
	default:
		return TargetScope{}, fmt.Errorf("scope mode must be selected or filtered")
	}
	return scope, nil
}

func (patch BatchPatch) Validate() (BatchPatch, error) {
	if patch.Note != nil {
		value := strings.TrimSpace(*patch.Note)
		if len(value) > maxNoteLength {
			return BatchPatch{}, fmt.Errorf("note exceeds %d bytes", maxNoteLength)
		}
		if hasUnsafeControl(value, true) {
			return BatchPatch{}, fmt.Errorf("note contains unsupported control characters")
		}
		patch.Note = stringPointer(value)
	}
	if patch.Prefix != nil {
		value := strings.TrimSpace(*patch.Prefix)
		if len(value) > maxPrefixLength {
			return BatchPatch{}, fmt.Errorf("prefix exceeds %d bytes", maxPrefixLength)
		}
		if hasUnsafeControl(value, false) {
			return BatchPatch{}, fmt.Errorf("prefix contains unsupported control characters")
		}
		patch.Prefix = stringPointer(value)
	}
	if patch.ProxyURL != nil {
		value := strings.TrimSpace(*patch.ProxyURL)
		if errValidate := validateProxyURL(value); errValidate != nil {
			return BatchPatch{}, errValidate
		}
		patch.ProxyURL = stringPointer(value)
	}
	if patch.Headers != nil {
		headers, errHeaders := normalizeHeaderPatch(*patch.Headers)
		if errHeaders != nil {
			return BatchPatch{}, errHeaders
		}
		if len(headers.Set) == 0 && len(headers.Remove) == 0 {
			patch.Headers = nil
		} else {
			patch.Headers = &headers
		}
	}
	if patch.Empty() {
		return BatchPatch{}, fmt.Errorf("at least one patch field is required")
	}
	return patch, nil
}

func (patch BatchPatch) Empty() bool {
	return patch.Disabled == nil && patch.Priority == nil && patch.Note == nil &&
		patch.Prefix == nil && patch.ProxyURL == nil && patch.Websockets == nil && patch.Headers == nil
}

func (patch BatchPatch) Summary() PatchSummary {
	fields := make([]string, 0, 7)
	if patch.Disabled != nil {
		fields = append(fields, "disabled")
	}
	if patch.Priority != nil {
		fields = append(fields, "priority")
	}
	if patch.Note != nil {
		fields = append(fields, "note")
	}
	if patch.Prefix != nil {
		fields = append(fields, "prefix")
	}
	if patch.ProxyURL != nil {
		fields = append(fields, "proxy_url")
	}
	if patch.Websockets != nil {
		fields = append(fields, "websockets")
	}
	summary := PatchSummary{Fields: fields, ProxyMutation: patch.ProxyURL != nil}
	if patch.Headers != nil {
		summary.Fields = append(summary.Fields, "headers")
		for name := range patch.Headers.Set {
			summary.HeaderSet = append(summary.HeaderSet, name)
		}
		summary.HeaderRemove = append(summary.HeaderRemove, patch.Headers.Remove...)
		sort.Slice(summary.HeaderSet, func(i, j int) bool {
			return strings.ToLower(summary.HeaderSet[i]) < strings.ToLower(summary.HeaderSet[j])
		})
		sort.Slice(summary.HeaderRemove, func(i, j int) bool {
			return strings.ToLower(summary.HeaderRemove[i]) < strings.ToLower(summary.HeaderRemove[j])
		})
	}
	return summary
}

func (patch BatchPatch) FieldPayload(name string) map[string]any {
	payload := map[string]any{"name": name}
	if patch.Priority != nil {
		payload["priority"] = *patch.Priority
	}
	if patch.Note != nil {
		payload["note"] = *patch.Note
	}
	if patch.Prefix != nil {
		payload["prefix"] = *patch.Prefix
	}
	if patch.ProxyURL != nil {
		payload["proxy_url"] = *patch.ProxyURL
	}
	if patch.Websockets != nil {
		payload["websockets"] = *patch.Websockets
	}
	if patch.Headers != nil {
		headers := make(map[string]string, len(patch.Headers.Set)+len(patch.Headers.Remove))
		for headerName, value := range patch.Headers.Set {
			headers[headerName] = value
		}
		for _, headerName := range patch.Headers.Remove {
			headers[headerName] = ""
		}
		payload["headers"] = headers
	}
	return payload
}

func (patch BatchPatch) HasFieldUpdates() bool {
	return patch.Priority != nil || patch.Note != nil || patch.Prefix != nil ||
		patch.ProxyURL != nil || patch.Websockets != nil || patch.Headers != nil
}

func cloneBatchPatch(patch BatchPatch) BatchPatch {
	clone := patch
	if patch.Disabled != nil {
		value := *patch.Disabled
		clone.Disabled = &value
	}
	if patch.Priority != nil {
		value := *patch.Priority
		clone.Priority = &value
	}
	if patch.Note != nil {
		clone.Note = stringPointer(*patch.Note)
	}
	if patch.Prefix != nil {
		clone.Prefix = stringPointer(*patch.Prefix)
	}
	if patch.ProxyURL != nil {
		clone.ProxyURL = stringPointer(*patch.ProxyURL)
	}
	if patch.Websockets != nil {
		value := *patch.Websockets
		clone.Websockets = &value
	}
	if patch.Headers != nil {
		headers := &HeaderPatch{
			Set:    make(map[string]string, len(patch.Headers.Set)),
			Remove: append([]string(nil), patch.Headers.Remove...),
		}
		for name, value := range patch.Headers.Set {
			headers.Set[name] = value
		}
		clone.Headers = headers
	}
	return clone
}

func normalizeHeaderPatch(input HeaderPatch) (HeaderPatch, error) {
	if len(input.Set)+len(input.Remove) > maxHeaderOperations {
		return HeaderPatch{}, fmt.Errorf("headers patch exceeds %d operations", maxHeaderOperations)
	}
	normalized := HeaderPatch{Set: make(map[string]string)}
	seen := make(map[string]string, len(input.Set)+len(input.Remove))
	for rawName, rawValue := range input.Set {
		name, key, errName := normalizeHeaderName(rawName)
		if errName != nil {
			return HeaderPatch{}, errName
		}
		value := strings.TrimSpace(rawValue)
		if len(value) > maxHeaderValueLength {
			return HeaderPatch{}, fmt.Errorf("header %s exceeds %d bytes", name, maxHeaderValueLength)
		}
		if strings.ContainsAny(value, "\r\n") || hasUnsafeControl(value, false) {
			return HeaderPatch{}, fmt.Errorf("header %s contains unsupported control characters", name)
		}
		if _, exists := seen[key]; exists {
			return HeaderPatch{}, fmt.Errorf("header %s is duplicated", name)
		}
		seen[key] = name
		if value == "" {
			normalized.Remove = append(normalized.Remove, name)
			continue
		}
		normalized.Set[name] = value
	}
	for _, rawName := range input.Remove {
		name, key, errName := normalizeHeaderName(rawName)
		if errName != nil {
			return HeaderPatch{}, errName
		}
		if _, exists := seen[key]; exists {
			return HeaderPatch{}, fmt.Errorf("header %s cannot be set and removed in the same patch", name)
		}
		seen[key] = name
		normalized.Remove = append(normalized.Remove, name)
	}
	if len(normalized.Set) == 0 {
		normalized.Set = nil
	}
	sort.Slice(normalized.Remove, func(i, j int) bool {
		return strings.ToLower(normalized.Remove[i]) < strings.ToLower(normalized.Remove[j])
	})
	return normalized, nil
}

func normalizeHeaderName(raw string) (string, string, error) {
	name := strings.TrimSpace(raw)
	if len(name) > maxHeaderNameLength {
		return "", "", fmt.Errorf("header name exceeds %d bytes", maxHeaderNameLength)
	}
	if !validHeaderName(name) {
		return "", "", fmt.Errorf("invalid header name")
	}
	key := strings.ToLower(name)
	switch key {
	case "connection", "content-length", "host", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return "", "", fmt.Errorf("header %s cannot be managed as a custom header", name)
	}
	return name, key, nil
}

func validateProxyURL(value string) error {
	if value == "" || strings.EqualFold(value, "direct") || strings.EqualFold(value, "none") {
		return nil
	}
	if len(value) > maxProxyURLLength {
		return fmt.Errorf("proxy_url exceeds %d bytes", maxProxyURLLength)
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil || parsed.Host == "" {
		return fmt.Errorf("proxy_url must be empty, direct, none, or a valid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxy_url scheme must be http, https, socks5, or socks5h")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("proxy_url must not contain a fragment")
	}
	return nil
}

func hasUnsafeControl(value string, allowNewline bool) bool {
	for _, char := range value {
		if allowNewline && (char == '\n' || char == '\t') {
			continue
		}
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string {
	return &value
}
