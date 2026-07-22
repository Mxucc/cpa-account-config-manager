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

func (h *RequestHook) InterceptAfter(request cpaapi.RequestInterceptRequest) cpaapi.RequestInterceptResponse {
	if h == nil {
		return cpaapi.RequestInterceptResponse{}
	}
	h.mu.RLock()
	transformers := append([]RequestTransformer(nil), h.transformers...)
	h.mu.RUnlock()
	response := cpaapi.RequestInterceptResponse{}
	current := request
	for _, transformer := range transformers {
		modification, changed := transformer.InterceptRequest(current)
		if !changed {
			continue
		}
		if len(modification.Body) > 0 {
			current.Body = append([]byte(nil), modification.Body...)
			response.Body = append([]byte(nil), modification.Body...)
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
