package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxModelTestPreviewBytes       = 8 << 10
	maxModelTestPreviewStringBytes = 1024
	maxModelTestPreviewDepth       = 6
	maxModelTestPreviewItems       = 50
	maxModelTestPreviewHeaders     = 16
	maxModelTestHeaderValueBytes   = 256
)

var (
	modelResponseBearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	modelResponseJWTPattern    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	modelResponseAPIKeyPattern = regexp.MustCompile(`\b(?:sk|rk|pk)-[A-Za-z0-9_-]{8,}\b`)
	modelResponseEmailPattern  = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	modelResponseSecretPattern = regexp.MustCompile(`(?i)((?:access|refresh|id)[_-]?token|api[_-]?key|client[_-]?secret|password|authorization|cookie)\s*[:=]\s*["']?[^\s,"'}]+`)
	modelResponseAllowedKeys   = map[string]struct{}{
		"error": {}, "errors": {}, "message": {}, "detail": {}, "reason": {}, "type": {}, "code": {},
		"error_code": {}, "status": {}, "status_code": {}, "param": {}, "model": {}, "object": {},
		"request_id": {}, "trace_id": {}, "retry_after": {}, "retry_after_seconds": {},
		"rate_limit": {}, "ratelimit": {}, "allowed": {}, "limit_reached": {},
		"primary_window": {}, "secondary_window": {}, "used_percent": {}, "limit_window_seconds": {},
		"reset_after_seconds": {}, "reset_at": {}, "remaining": {}, "limit": {},
		"response": {}, "output": {}, "content": {}, "text": {}, "event": {}, "data": {},
		"candidates": {}, "finish_reason": {}, "finishreason": {}, "stop_reason": {}, "usage": {},
		"input_tokens": {}, "output_tokens": {}, "total_tokens": {},
	}
)

func sanitizeModelTestResponsePreview(response modelProbeHTTPResponse) *ModelTestResponsePreview {
	preview := &ModelTestResponsePreview{
		Format:  "empty",
		Body:    "",
		Headers: safeModelTestResponseHeaders(response.Header),
	}
	trimmed := bytes.TrimSpace(response.Body)
	if len(trimmed) == 0 {
		return preview
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&value); errDecode == nil && decoderOnlyWhitespace(decoder) {
		sanitized, truncated := sanitizeModelResponseJSON(value, 0)
		encoded, errMarshal := json.MarshalIndent(sanitized, "", "  ")
		if errMarshal == nil {
			preview.Format = "json"
			preview.Body, preview.Truncated = boundedModelResponseText(string(encoded), maxModelTestPreviewBytes)
			preview.Truncated = preview.Truncated || truncated
			return preview
		}
	}

	preview.Format = "text"
	preview.Body, preview.Truncated = boundedModelResponseText(redactModelResponseText(string(trimmed)), maxModelTestPreviewBytes)
	return preview
}

func decoderOnlyWhitespace(decoder *json.Decoder) bool {
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func sanitizeModelResponseJSON(value any, depth int) (any, bool) {
	if depth >= maxModelTestPreviewDepth {
		return "[truncated]", true
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		truncated := len(keys) > maxModelTestPreviewItems
		if truncated {
			keys = keys[:maxModelTestPreviewItems]
		}
		out := make(map[string]any, len(keys)+1)
		omitted := 0
		for _, key := range keys {
			safeKey, keyTruncated := boundedModelResponseText(strings.TrimSpace(key), 128)
			truncated = truncated || keyTruncated
			if safeKey == "" {
				safeKey = "field"
			}
			if modelResponseSensitiveKey(key) {
				out[safeKey] = "[redacted]"
				continue
			}
			if !modelResponseAllowedKey(key) {
				omitted++
				continue
			}
			child, childTruncated := sanitizeModelResponseJSON(typed[key], depth+1)
			out[safeKey] = child
			truncated = truncated || childTruncated
		}
		if len(typed) > len(keys) {
			out["_truncated_fields"] = len(typed) - len(keys)
		}
		if omitted > 0 {
			out["_omitted_fields"] = omitted
		}
		return out, truncated
	case []any:
		limit := len(typed)
		truncated := false
		if limit > maxModelTestPreviewItems {
			limit = maxModelTestPreviewItems
			truncated = true
		}
		out := make([]any, 0, limit+1)
		for index := 0; index < limit; index++ {
			child, childTruncated := sanitizeModelResponseJSON(typed[index], depth+1)
			out = append(out, child)
			truncated = truncated || childTruncated
		}
		if len(typed) > limit {
			out = append(out, "[truncated]")
		}
		return out, truncated
	case string:
		redacted := redactModelResponseText(typed)
		bounded, truncated := boundedModelResponseText(redacted, maxModelTestPreviewStringBytes)
		return bounded, truncated
	case json.Number, float64, bool, nil:
		return typed, false
	default:
		return "[unsupported value]", true
	}
}

func modelResponseAllowedKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(normalized)
	_, allowed := modelResponseAllowedKeys[normalized]
	return allowed
}

func modelResponseSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(normalized)
	for _, fragment := range []string{
		"token", "secret", "password", "passwd", "authorization", "cookie", "api_key", "apikey",
		"access_key", "credential", "session_id", "proxy_url", "proxy_password",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	switch normalized {
	case "email", "user_id", "account_id", "organization_id", "workspace_id", "project_id":
		return true
	default:
		return false
	}
}

func redactModelResponseText(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' || character >= 0x20 {
			return character
		}
		return -1
	}, value)
	value = modelResponseBearerPattern.ReplaceAllString(value, `${1}[redacted]`)
	value = modelResponseJWTPattern.ReplaceAllString(value, "[redacted-jwt]")
	value = modelResponseAPIKeyPattern.ReplaceAllString(value, "[redacted-key]")
	value = modelResponseEmailPattern.ReplaceAllString(value, "[redacted-email]")
	value = modelResponseSecretPattern.ReplaceAllString(value, `${1}=[redacted]`)
	if parsed, errParse := url.Parse(value); errParse == nil && parsed.IsAbs() {
		parsed.User = nil
		query := parsed.Query()
		for key := range query {
			if modelResponseSensitiveKey(key) {
				query.Set(key, "[redacted]")
			}
		}
		parsed.RawQuery = query.Encode()
		value = parsed.String()
	}
	return value
}

func safeModelTestResponseHeaders(headers map[string][]string) []ModelTestResponseHeader {
	if len(headers) == 0 {
		return []ModelTestResponseHeader{}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		if safeModelTestHeaderName(name) != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(left, right int) bool {
		return strings.ToLower(names[left]) < strings.ToLower(names[right])
	})
	if len(names) > maxModelTestPreviewHeaders {
		names = names[:maxModelTestPreviewHeaders]
	}
	out := make([]ModelTestResponseHeader, 0, len(names))
	for _, name := range names {
		safeName := safeModelTestHeaderName(name)
		values := headers[name]
		if safeName == "" || len(values) == 0 {
			continue
		}
		value, _ := boundedModelResponseText(redactModelResponseText(strings.Join(values, ", ")), maxModelTestHeaderValueBytes)
		if value == "" {
			continue
		}
		out = append(out, ModelTestResponseHeader{Name: safeName, Value: value})
	}
	return out
}

func safeModelTestHeaderName(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case normalized == "content-type", normalized == "retry-after", normalized == "request-id",
		normalized == "x-request-id", normalized == "x-correlation-id", normalized == "traceparent",
		normalized == "cf-ray", strings.HasPrefix(normalized, "x-ratelimit-"),
		strings.HasPrefix(normalized, "ratelimit-"), strings.HasPrefix(normalized, "anthropic-ratelimit-"):
		return normalized
	default:
		return ""
	}
}

func boundedModelResponseText(value string, maximum int) (string, bool) {
	value = strings.TrimSpace(value)
	if maximum <= 0 || len(value) <= maximum {
		return value, false
	}
	cut := maximum
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut]) + "\n... [truncated]", true
}
