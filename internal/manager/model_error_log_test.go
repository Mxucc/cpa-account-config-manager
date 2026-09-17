package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// newModelErrorTestApp wires the smallest App the model-error log and the
// exhausted-retry interceptor need: a configured operation journal, the
// automatic retry service and a fresh request tracker.
func newModelErrorTestApp(t *testing.T, attempts int) *App {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, nil)
	t.Cleanup(app.Close)
	app.operations.Configure(Config{DataDir: t.TempDir()})
	app.autoRetry.Configure(Config{DataDir: t.TempDir()})
	if errSet := app.autoRetry.SetAttempts(attempts); errSet != nil {
		t.Fatalf("SetAttempts(%d) error = %v", attempts, errSet)
	}
	return app
}

func TestModelErrorLogRecordsSanitizedFailedCompletion(t *testing.T) {
	app := newModelErrorTestApp(t, 5)
	app.HandleRequestComplete(cpaapi.RequestCompletion{
		RequestID:  "req-failed-1",
		Model:      "gpt-5.6-sol",
		Outcome:    "failed",
		StatusCode: http.StatusBadGateway,
		Error:      "upstream said: model overloaded; Authorization: Bearer abc123; key sk-abc1234567890; pat at-abc1234567890",
		Metadata:   map[string]any{"selected_auth_id": "auth-7"},
	})

	response := app.operations.List(OperationQuery{Page: 1, PageSize: 20})
	if response.Total != 1 || len(response.Operations) != 1 {
		t.Fatalf("journal = %#v", response)
	}
	entry := response.Operations[0]
	if entry.Category != OperationCategoryModelError || entry.Action != OperationActionModelFailure {
		t.Fatalf("entry identity = %#v", entry)
	}
	if entry.Status != OperationStatusFailed || entry.Source != OperationSourceBackground {
		t.Fatalf("entry status/source = %#v", entry)
	}
	if entry.HTTPStatus != http.StatusBadGateway || entry.Attempts != 5 {
		t.Fatalf("entry status/budget = %#v", entry)
	}
	if entry.TargetID != "req-failed-1" || entry.Model != "gpt-5.6-sol" || entry.Scope != "auth-7" {
		t.Fatalf("entry correlation fields = %#v", entry)
	}
	if entry.ReasonCode != OperationFailureModelUpstream {
		t.Fatalf("entry reason = %#v", entry)
	}
	// The upstream text survives sanitization, because it is the point of the
	// field, but no embedded credential may.
	if !strings.Contains(entry.Message, "upstream said") || !strings.Contains(entry.Message, "model overloaded") {
		t.Fatalf("upstream message was dropped: %q", entry.Message)
	}
	for _, secret := range []string{"abc123", "sk-abc1234567890", "at-abc1234567890"} {
		if strings.Contains(entry.Message, secret) {
			t.Fatalf("secret %q survived in %q", secret, entry.Message)
		}
	}
}

func TestModelErrorLogSkipsSuccessfulAndEmptyCompletions(t *testing.T) {
	app := newModelErrorTestApp(t, 5)
	app.HandleRequestComplete(cpaapi.RequestCompletion{RequestID: "req-ok", Outcome: "succeeded", StatusCode: http.StatusOK})
	app.HandleRequestComplete(cpaapi.RequestCompletion{RequestID: "req-empty"})
	if response := app.operations.List(OperationQuery{Page: 1, PageSize: 20}); response.Total != 0 {
		t.Fatalf("journal recorded a non-failure: %#v", response)
	}
}

func TestModelErrorLogRecordsClientCancel(t *testing.T) {
	app := newModelErrorTestApp(t, 5)
	app.HandleRequestComplete(cpaapi.RequestCompletion{
		RequestID: "req-cancel",
		Outcome:   "canceled",
		Error:     "client closed the connection",
	})
	response := app.operations.List(OperationQuery{Page: 1, PageSize: 20})
	if response.Total != 1 {
		t.Fatalf("journal = %#v", response)
	}
	entry := response.Operations[0]
	if entry.Status != OperationStatusInterrupted || entry.ReasonCode != OperationFailureModelCanceled {
		t.Fatalf("cancel entry = %#v", entry)
	}
	if entry.HTTPStatus != 0 {
		t.Fatalf("cancel entry status code = %#v", entry)
	}
}

func TestModelErrorRetryExhaustionTerminatesAttemptPastBudget(t *testing.T) {
	app := newModelErrorTestApp(t, 5)
	request := cpaapi.RequestInterceptRequest{RequestID: "req-budget", ToFormat: "openai", Model: "gpt-5"}
	for attempt := 1; attempt <= 6; attempt++ {
		if response := app.HandleRequestAfter(request); response.Terminate {
			t.Fatalf("attempt %d was terminated: %#v", attempt, response)
		}
	}
	response := app.HandleRequestAfter(request)
	if !response.Terminate {
		t.Fatal("the attempt after the retry budget was not terminated")
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("terminated status = %d", response.StatusCode)
	}
	if response.ResponseHeaders.Get("Content-Type") == "" {
		t.Fatalf("terminated headers = %#v", response.ResponseHeaders)
	}
	var payload struct {
		Error struct {
			Message    string `json:"message"`
			Type       string `json:"type"`
			Code       string `json:"code"`
			RetryLimit int    `json:"retry_limit"`
			RequestID  string `json:"request_id"`
		} `json:"error"`
	}
	if errDecode := json.Unmarshal(response.ResponseBody, &payload); errDecode != nil {
		t.Fatalf("decode terminated body %q: %v", response.ResponseBody, errDecode)
	}
	if payload.Error.Type != "server_error" || payload.Error.Code != "retry_exhausted" ||
		payload.Error.RetryLimit != 5 || payload.Error.RequestID != "req-budget" {
		t.Fatalf("terminated body = %#v", payload)
	}
	// The attempt is terminated from now on until the request completes.
	if again := app.HandleRequestAfter(request); !again.Terminate {
		t.Fatalf("a later attempt was not terminated: %#v", again)
	}

	// The host reports the plugin's 503 back through request.complete, so the
	// operator sees the 503 and the original upstream error in one entry.
	app.HandleRequestComplete(cpaapi.RequestCompletion{
		RequestID:  "req-budget",
		Model:      "gpt-5",
		Outcome:    "failed",
		StatusCode: http.StatusServiceUnavailable,
		Error:      "upstream: 502 bad gateway",
	})
	list := app.operations.List(OperationQuery{Page: 1, PageSize: 20})
	if list.Total != 1 {
		t.Fatalf("journal = %#v", list)
	}
	entry := list.Operations[0]
	if entry.HTTPStatus != http.StatusServiceUnavailable || entry.ReasonCode != OperationFailureModelRetryExhausted {
		t.Fatalf("exhausted entry = %#v", entry)
	}
	if entry.Attempts != 5 || !strings.Contains(entry.Message, "502 bad gateway") {
		t.Fatalf("exhausted entry lost the upstream error: %#v", entry)
	}
	// The completion releases the counter, so a new request with the same id
	// starts from the first attempt again.
	if leftover := app.HandleRequestAfter(request); leftover.Terminate {
		t.Fatalf("the counter survived the completion: %#v", leftover)
	}
}

func TestModelErrorRetryExhaustionStaysOffForZeroBudget(t *testing.T) {
	app := newModelErrorTestApp(t, 0)
	request := cpaapi.RequestInterceptRequest{RequestID: "req-off", ToFormat: "openai", Model: "gpt-5"}
	for attempt := 0; attempt < 50; attempt++ {
		if response := app.HandleRequestAfter(request); response.Terminate {
			t.Fatalf("attempt %d was terminated with the feature off: %#v", attempt+1, response)
		}
	}
	app.modelRetry.mu.Lock()
	size := len(app.modelRetry.entries)
	app.modelRetry.mu.Unlock()
	if size != 0 {
		t.Fatalf("a disabled tracker stored %d entries", size)
	}
}

func TestModelErrorRetryExhaustionRequiresRequestID(t *testing.T) {
	app := newModelErrorTestApp(t, 5)
	request := cpaapi.RequestInterceptRequest{ToFormat: "openai", Model: "gpt-5"}
	for attempt := 0; attempt < 50; attempt++ {
		if response := app.HandleRequestAfter(request); response.Terminate {
			t.Fatalf("attempt %d without a request id was terminated: %#v", attempt+1, response)
		}
	}
	app.modelRetry.mu.Lock()
	size := len(app.modelRetry.entries)
	app.modelRetry.mu.Unlock()
	if size != 0 {
		t.Fatalf("a request without an id stored %d entries", size)
	}
}

func TestModelErrorRetryExhaustionCounterStaysBounded(t *testing.T) {
	tracker := newModelRetryExhaustionTracker()
	for index := 0; index < modelRetryExhaustionMaxEntries+256; index++ {
		tracker.observe(fmt.Sprintf("req-%d", index), 5)
	}
	tracker.mu.Lock()
	size := len(tracker.entries)
	tracker.mu.Unlock()
	if size > modelRetryExhaustionMaxEntries {
		t.Fatalf("tracker size = %d, want at most %d", size, modelRetryExhaustionMaxEntries)
	}
	if size == 0 {
		t.Fatal("tracker stored nothing")
	}
}

func TestModelErrorRetryExhaustionEntriesExpire(t *testing.T) {
	tracker := newModelRetryExhaustionTracker()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	if _, terminate := tracker.observe("req-expire", 1); terminate {
		t.Fatal("the first attempt was terminated")
	}
	if _, terminate := tracker.observe("req-expire", 1); terminate {
		t.Fatal("the second attempt was terminated")
	}
	if _, terminate := tracker.observe("req-expire", 1); !terminate {
		t.Fatal("the attempt past the budget was not terminated")
	}
	now = now.Add(modelRetryExhaustionTTL + time.Minute)
	attempt, terminate := tracker.observe("req-expire", 1)
	if terminate || attempt != 1 {
		t.Fatalf("an expired request was not reset: attempt=%d terminate=%v", attempt, terminate)
	}
}

// The dedicated category rides the existing operation-log route, so the operator
// can filter the journal down to model errors without a new endpoint.
func TestModelErrorEntriesAreExposedThroughTheOperationsRoute(t *testing.T) {
	app := newModelErrorTestApp(t, 5)
	app.HandleRequestComplete(cpaapi.RequestCompletion{
		RequestID:  "req-route",
		Outcome:    "failed",
		StatusCode: http.StatusBadGateway,
		Error:      "upstream unavailable",
	})
	listPath := "/v0/management/plugins/cpa-account-config-manager/operations"
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: listPath, Query: url.Values{"category": {"model_error"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list response = %d %s", response.StatusCode, response.Body)
	}
	var listed OperationListResponse
	if errDecode := json.Unmarshal(response.Body, &listed); errDecode != nil {
		t.Fatalf("decode list response: %v", errDecode)
	}
	if listed.Total != 1 || len(listed.Operations) != 1 || listed.Operations[0].Category != OperationCategoryModelError {
		t.Fatalf("listed = %#v", listed)
	}
	if !strings.Contains(listed.Operations[0].Message, "upstream unavailable") {
		t.Fatalf("listed entry lost the message: %#v", listed.Operations[0])
	}
	// The category filter separates model errors from the rest of the journal.
	other := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: listPath, Query: url.Values{"category": {"account"}},
	})
	var filtered OperationListResponse
	if errDecode := json.Unmarshal(other.Body, &filtered); errDecode != nil {
		t.Fatalf("decode filtered response: %v", errDecode)
	}
	if filtered.Total != 0 {
		t.Fatalf("an unrelated category returned %d entries", filtered.Total)
	}
}
