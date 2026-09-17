package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// The exhausted-retry interceptor turns the attempt CPA would make after the
// operator's retry budget into a 503 for the client, so a request that consumed
// every configured retry no longer ends with the raw upstream error. CPA invokes
// HandleRequestAfter once per attempt immediately before the upstream call, so a
// bounded per-request counter can recognize exactly that attempt. The budget
// published to CPA is one higher than the operator's setting (see
// autoRetryPublishedAttempts): that extra attempt is the one terminated here,
// which keeps the number of real upstream retries at the configured value.
const (
	// modelRetryExhaustionMaxEntries caps the in-memory request map, so a busy
	// host with many distinct request ids cannot grow it without bound.
	modelRetryExhaustionMaxEntries = 4096
	// modelRetryExhaustionTTL is how long one request stays tracked. A request
	// whose completion never arrives is forgotten after this window.
	modelRetryExhaustionTTL = 15 * time.Minute
)

type modelRetryExhaustionEntry struct {
	attempts   int
	terminated bool
	updatedAt  time.Time
}

// modelRetryExhaustionTracker counts the interception attempts of one client
// request. It is keyed by the host request id and bounded in size and lifetime.
type modelRetryExhaustionTracker struct {
	mu      sync.Mutex
	entries map[string]modelRetryExhaustionEntry
	now     func() time.Time
}

func newModelRetryExhaustionTracker() *modelRetryExhaustionTracker {
	return &modelRetryExhaustionTracker{entries: make(map[string]modelRetryExhaustionEntry), now: time.Now}
}

func (t *modelRetryExhaustionTracker) currentTime() time.Time {
	if t.now != nil {
		return t.now().UTC()
	}
	return time.Now().UTC()
}

// observe counts one interception attempt of requestID and reports whether the
// attempt is past the operator's budget. A missing request id or a non-positive
// budget disables the tracker for that request: nothing is counted and nothing is
// ever terminated.
func (t *modelRetryExhaustionTracker) observe(requestID string, budget int) (attempt int, terminate bool) {
	if t == nil || budget <= 0 {
		return 0, false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, false
	}
	now := t.currentTime()
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, exists := t.entries[requestID]
	if exists && now.Sub(entry.updatedAt) > modelRetryExhaustionTTL {
		delete(t.entries, requestID)
		entry = modelRetryExhaustionEntry{}
		exists = false
	}
	if !exists && len(t.entries) >= modelRetryExhaustionMaxEntries {
		t.pruneExpiredLocked(now)
		if len(t.entries) >= modelRetryExhaustionMaxEntries {
			t.evictOldestLocked()
		}
	}
	entry.attempts++
	entry.updatedAt = now
	if entry.attempts > budget+1 {
		entry.terminated = true
	}
	t.entries[requestID] = entry
	return entry.attempts, entry.terminated
}

// snapshot reports what the tracker knows about one request. The attempts value
// is only meaningful while tracked is true.
func (t *modelRetryExhaustionTracker) snapshot(requestID string) (attempts int, terminated bool, tracked bool) {
	if t == nil {
		return 0, false, false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, false, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, exists := t.entries[requestID]
	if !exists {
		return 0, false, false
	}
	return entry.attempts, entry.terminated, true
}

// forget drops one finished request. The completion callback does this so a
// completed request never lingers until the TTL.
func (t *modelRetryExhaustionTracker) forget(requestID string) {
	if t == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	t.mu.Lock()
	delete(t.entries, requestID)
	t.mu.Unlock()
}

// pruneExpiredLocked removes every entry older than the TTL. The caller must hold
// t.mu; it only runs while the map is at its size cap.
func (t *modelRetryExhaustionTracker) pruneExpiredLocked(now time.Time) {
	for requestID, entry := range t.entries {
		if now.Sub(entry.updatedAt) > modelRetryExhaustionTTL {
			delete(t.entries, requestID)
		}
	}
}

// evictOldestLocked removes the least recently touched entry so one insert can
// always stay within the size cap. The caller must hold t.mu.
func (t *modelRetryExhaustionTracker) evictOldestLocked() {
	oldestID := ""
	var oldest time.Time
	for requestID, entry := range t.entries {
		if oldestID == "" || entry.updatedAt.Before(oldest) {
			oldestID = requestID
			oldest = entry.updatedAt
		}
	}
	if oldestID != "" {
		delete(t.entries, oldestID)
	}
}

// modelRetryExhaustedResponse counts one request-after interception and, when the
// attempt is past the operator's retry budget, answers it with a 503 instead of
// letting CPA call upstream. The JSON body matches the plugin's other terminated
// responses and carries the request id for correlation with the model-error log.
func (a *App) modelRetryExhaustedResponse(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if a == nil || a.modelRetry == nil {
		return cpaapi.RequestInterceptResponse{}, false
	}
	budget := a.autoRetry.Attempts()
	if _, terminate := a.modelRetry.observe(request.RequestID, budget); !terminate {
		return cpaapi.RequestInterceptResponse{}, false
	}
	body, errMarshal := json.Marshal(map[string]any{"error": map[string]any{
		"type":        "server_error",
		"message":     fmt.Sprintf("the configured retry budget of %d retries is exhausted", budget),
		"code":        "retry_exhausted",
		"retry_limit": budget,
		"request_id":  strings.TrimSpace(request.RequestID),
	}})
	if errMarshal != nil {
		// Fail open: a response that cannot be encoded must not end the request.
		return cpaapi.RequestInterceptResponse{}, false
	}
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusServiceUnavailable,
		ResponseHeaders: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		ResponseBody:    body,
	}, true
}
