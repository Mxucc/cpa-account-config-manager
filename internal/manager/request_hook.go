package manager

import (
	"net/http"
	"strings"
	"sync"

	"cpa-account-config-manager/internal/cpaapi"
)

// RequestTransformer is the removable feature boundary behind the permanent
// CPA request-interceptor capability.
type RequestTransformer interface {
	InterceptRequest(cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool)
}

type requestTransformerGate interface {
	RequestInterceptionActive() bool
	RequestInterceptionAcceptsFormat(string) bool
}

type RequestHook struct {
	mu           sync.RWMutex
	transformers []RequestTransformer
}

func NewRequestHook(transformers ...RequestTransformer) *RequestHook {
	hook := &RequestHook{}
	for _, transformer := range transformers {
		hook.Register(transformer)
	}
	return hook
}

func (h *RequestHook) Register(transformer RequestTransformer) {
	if h == nil || transformer == nil {
		return
	}
	h.mu.Lock()
	h.transformers = append(h.transformers, transformer)
	h.mu.Unlock()
}

func (h *RequestHook) InterceptBefore(cpaapi.RequestInterceptRequest) cpaapi.RequestInterceptResponse {
	return cpaapi.RequestInterceptResponse{}
}

func (h *RequestHook) Active() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, transformer := range h.transformers {
		gate, gated := transformer.(requestTransformerGate)
		if !gated || gate.RequestInterceptionActive() {
			return true
		}
	}
	return false
}

func (h *RequestHook) AcceptsFormat(format string) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, transformer := range h.transformers {
		gate, gated := transformer.(requestTransformerGate)
		if !gated || gate.RequestInterceptionActive() && gate.RequestInterceptionAcceptsFormat(format) {
			return true
		}
	}
	return false
}

func (h *RequestHook) InterceptAfter(request cpaapi.RequestInterceptRequest) cpaapi.RequestInterceptResponse {
	if h == nil {
		return cpaapi.RequestInterceptResponse{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	response := cpaapi.RequestInterceptResponse{}
	current := request
	for _, transformer := range h.transformers {
		modification, changed := transformer.InterceptRequest(current)
		if !changed {
			continue
		}
		if len(modification.Body) > 0 {
			// A transformer owns a changed body until the hook returns it. Keep one
			// reference for the next transformer instead of copying a large body twice.
			response.Body = modification.Body
			current.Body = modification.Body
		}
		if len(modification.ClearHeaders) > 0 {
			response.ClearHeaders = appendUniqueHeaderNames(response.ClearHeaders, modification.ClearHeaders...)
		}
		if len(modification.Headers) > 0 {
			if response.Headers == nil {
				response.Headers = make(http.Header)
			}
			for name, values := range modification.Headers {
				response.Headers[name] = append([]string(nil), values...)
			}
		}
		if modification.Terminate {
			response.Terminate = true
			response.StatusCode = modification.StatusCode
			response.ResponseHeaders = modification.ResponseHeaders.Clone()
			response.ResponseBody = append([]byte(nil), modification.ResponseBody...)
			break
		}
	}
	return response
}

func appendUniqueHeaderNames(current []string, values ...string) []string {
	seen := make(map[string]struct{}, len(current)+len(values))
	for _, value := range current {
		seen[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		current = append(current, value)
	}
	return current
}
