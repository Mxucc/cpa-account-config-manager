package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// autoRetryApplyTimeout bounds one apply pass. The pass is best-effort, so a
	// host that answers slowly must never hold a caller or a goroutine open.
	autoRetryApplyTimeout = 10 * time.Second
	// autoRetryBootstrapDelay is how long after a configuration the automatic
	// pass runs. It is long enough that the pass never competes with the host's
	// own configure sequence, and it is skipped entirely on a retired instance.
	autoRetryBootstrapDelay = 15 * time.Second
)

// autoRetryApplyResult is one pass outcome. Counts describe credentials the
// pass actually wrote: a row that already carries the configured budget is not
// counted, because nothing was applied to it.
type autoRetryApplyResult struct {
	CodexAccounts          int
	OpenCodeChannels       int
	ClinePassChannels      int
	Skipped                int
	HostRequestRetryRaised bool
	HostIntervalRaised     bool
	Host                   AutoRetryHostState
}

func (result autoRetryApplyResult) appliedState() AutoRetryAppliedState {
	return AutoRetryAppliedState{
		CodexAccounts:          result.CodexAccounts,
		OpenCodeChannels:       result.OpenCodeChannels,
		ClinePassChannels:      result.ClinePassChannels,
		Skipped:                result.Skipped,
		HostRequestRetryRaised: result.HostRequestRetryRaised,
		HostIntervalRaised:     result.HostIntervalRaised,
	}
}

// runAutoRetryApply publishes the operator's retry budget to every credential
// the plugin manages and raises the host retry knobs a disabled value would
// otherwise defeat. The pass is best-effort and bounded: every step is
// independent, a failure skips the rest of that step, and nothing here is ever
// surfaced to a client request.
//
// The Codex half works through the host auth API alone, so it also runs before
// any Management credential is known; the channel and host halves need one.
func (a *App) runAutoRetryApply(ctx context.Context, managementKey string) autoRetryApplyResult {
	result := autoRetryApplyResult{}
	if a == nil || a.autoRetry == nil {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := a.autoRetry.Attempts()
	applyCtx, cancel := context.WithTimeout(ctx, autoRetryApplyTimeout)
	defer cancel()

	result.CodexAccounts = a.applyAutoRetryToCodexAuthFiles(applyCtx, attempts)

	client, errClient := a.newWriteManagementClient(strings.TrimSpace(managementKey))
	if errClient != nil {
		a.autoRetry.recordApply(nil, result.appliedState())
		return result
	}
	defer clearManagementWriterSecrets(client)

	result.OpenCodeChannels, result.ClinePassChannels, result.Skipped = a.applyAutoRetryToChannels(applyCtx, client, managementKey, attempts)
	result.Host, result.HostRequestRetryRaised, result.HostIntervalRaised = a.applyAutoRetryToHostKnobs(applyCtx, client, attempts)

	a.autoRetry.recordApply(&result.Host, result.appliedState())
	return result
}

// applyAutoRetryToCodexAuthFiles writes the budget into every editable Codex
// auth file. The raw JSON is decoded, one top-level key is set, and the document
// is written straight back: it is never logged or kept anywhere else.
func (a *App) applyAutoRetryToCodexAuthFiles(ctx context.Context, attempts int) int {
	if a.accounts == nil {
		return 0
	}
	// One bounded page of the largest size the account service serves: it covers
	// a realistic installation without an unbounded walk that could outlive the
	// pass deadline.
	response, errList := a.accounts.List(ctx, ListQuery{Page: 1, PageSize: maxPageSize})
	if errList != nil {
		return 0
	}
	updated := 0
	for _, account := range response.Accounts {
		if ctx.Err() != nil {
			break
		}
		if !strings.EqualFold(strings.TrimSpace(account.Provider), "codex") {
			continue
		}
		if !account.Editable {
			continue
		}
		name := strings.TrimSpace(account.Name)
		if name == "" || !safeAuthJSONName(name) {
			continue
		}
		if a.applyAutoRetryToCodexAuthFile(ctx, account.ID, name, attempts) {
			updated++
		}
	}
	return updated
}

// applyAutoRetryToCodexAuthFile returns whether the file had to be rewritten.
// CPA unmarshals the whole file into the scheduler metadata, so a top-level
// request_retry key is the per-credential override the request loop reads.
func (a *App) applyAutoRetryToCodexAuthFile(ctx context.Context, authIndex, name string, attempts int) bool {
	detail, errGet := a.accounts.GetAuth(ctx, authIndex)
	if errGet != nil {
		return false
	}
	raw := bytes.TrimSpace(detail.JSON)
	if len(raw) == 0 {
		return false
	}
	// UseNumber keeps every other value byte-faithful when the document is
	// encoded again: a large integer or a decimal must survive the round trip.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	document := make(map[string]any)
	if errDecode := decoder.Decode(&document); errDecode != nil || document == nil {
		return false
	}
	if current, known := autoRetryValue(document["request_retry"]); known && current == attempts {
		return false
	}
	document["request_retry"] = attempts
	encoded, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return false
	}
	if _, errSave := a.accounts.SaveAuth(ctx, name, json.RawMessage(encoded)); errSave != nil {
		return false
	}
	return true
}

// applyAutoRetryToChannels classifies every OpenAI-compatible row by its base
// URL and patches only the rows of the two managed products that carry a
// different budget. A row that belongs to neither product is counted as skipped
// and never written.
func (a *App) applyAutoRetryToChannels(ctx context.Context, client *managementClient, managementKey string, attempts int) (openCode, clinePass, skipped int) {
	entries, errList := a.aiProviderChannelEntries(ctx, managementKey, "openai-compatibility")
	if errList != nil {
		return 0, 0, 0
	}
	for index, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		kind := ""
		switch {
		case isOpenCodeGatewayBaseURL(aiProviderChannelBaseURL(entry)):
			kind = "opencode"
		case isClinePassGatewayBaseURL(aiProviderChannelBaseURL(entry)):
			kind = "cline-pass"
		default:
			skipped++
			continue
		}
		if current, known := autoRetryValue(entry["request-retry"]); known && current == attempts {
			// Already correct: nothing is applied to this row.
			continue
		}
		if errPatch := client.patch(ctx, "/v0/management/openai-compatibility", map[string]any{
			"index": index,
			"value": map[string]any{"request-retry": attempts},
		}); errPatch != nil {
			continue
		}
		if kind == "opencode" {
			openCode++
		} else {
			clinePass++
		}
	}
	return openCode, clinePass, skipped
}

// isClinePassGatewayBaseURL reports whether a channel base URL points at a Cline
// Pass gateway. Stored Cline Pass base URLs are normalized by
// normalizeClinePassBaseURL, so the classification runs on that normalized form
// and only the documented cline.bot family qualifies.
func isClinePassGatewayBaseURL(baseURL string) bool {
	normalized := normalizeClinePassBaseURL(baseURL)
	if !validClinePassBaseURL(normalized) {
		return false
	}
	parsed, errParse := url.Parse(normalized)
	if errParse != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "cline.bot" || strings.HasSuffix(host, ".cline.bot")
}

// applyAutoRetryToHostKnobs reads the host retry configuration and raises the
// two knobs a disabled value would defeat, but only while attempts is positive:
// 0 means "no retry" for the credentials the plugin manages, not a change to
// host policy. An existing non-zero value is never lowered.
func (a *App) applyAutoRetryToHostKnobs(ctx context.Context, client *managementClient, attempts int) (AutoRetryHostState, bool, bool) {
	host := a.autoRetry.hostState()
	intervalKnown := false
	if value, errRead := client.getIntValue(ctx, "/v0/management/max-retry-interval", "max-retry-interval"); errRead == nil {
		host.MaxRetryInterval = value
		intervalKnown = true
	}
	requestKnown := false
	if value, errRead := client.getIntValue(ctx, "/v0/management/request-retry", "request-retry"); errRead == nil {
		host.RequestRetry = value
		requestKnown = true
	}
	// The host configuration document also carries the credential cap and the
	// streaming bootstrap value, which have no dedicated route. It is optional:
	// a host that does not report them simply omits the value.
	if document, errRead := client.getConfigDocument(ctx); errRead == nil {
		if value, known := autoRetryValue(document["max-retry-credentials"]); known {
			host.MaxRetryCredentials = value
		}
		if value, known := autoRetryValue(document["transient-error-cooldown-seconds"]); known {
			cooldown := value
			host.TransientCooldownSeconds = &cooldown
		}
		if streaming, ok := document["streaming"].(map[string]any); ok {
			if value, known := autoRetryValue(streaming["bootstrap-retries"]); known {
				bootstrap := value
				host.BootstrapRetries = &bootstrap
			}
		}
	}
	host.Configured = intervalKnown && requestKnown

	raisedRequestRetry := false
	raisedInterval := false
	if attempts > 0 {
		// A transient failure freezes its credential for the host's transient-error
		// cooldown, and CPA refuses a wait longer than max-retry-interval, so an
		// interval below that cooldown would silently disable the retry the operator
		// asked for. The interval is only ever raised.
		requiredInterval := autoRetryRequiredRetryInterval(autoRetryTransientCooldown(host))
		if intervalKnown && host.MaxRetryInterval < requiredInterval {
			if errPut := client.putIntValue(ctx, "/v0/management/max-retry-interval", requiredInterval); errPut == nil {
				host.MaxRetryInterval = requiredInterval
				raisedInterval = true
			}
		}
		if requestKnown && host.RequestRetry == 0 {
			if errPut := client.putIntValue(ctx, "/v0/management/request-retry", attempts); errPut == nil {
				host.RequestRetry = attempts
				raisedRequestRetry = true
			}
		}
	}
	return host, raisedRequestRetry, raisedInterval
}

// autoRetryValue reads an integer JSON value in whichever shape the decoder
// produced it (float64 for a plain map, json.Number for a UseNumber decoder).
func autoRetryValue(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case json.Number:
		if parsed, errInt := typed.Int64(); errInt == nil {
			return int(parsed), true
		}
		if parsed, errFloat := typed.Float64(); errFloat == nil && parsed == math.Trunc(parsed) {
			return int(parsed), true
		}
	case float64:
		if typed == math.Trunc(typed) {
			return int(typed), true
		}
	case int:
		return typed, true
	case int64:
		return int(typed), true
	}
	return 0, false
}

// getIntValue reads one integer field of a Management API response.
func (c *managementClient) getIntValue(ctx context.Context, path, field string) (int, error) {
	var response map[string]json.RawMessage
	if errRequest := c.requestJSON(ctx, http.MethodGet, path, nil, "", &response); errRequest != nil {
		return 0, errRequest
	}
	raw, exists := response[field]
	if !exists {
		return 0, fmt.Errorf("management API response is missing %s", field)
	}
	var value int
	if errDecode := json.Unmarshal(raw, &value); errDecode != nil {
		return 0, fmt.Errorf("management API response has an invalid %s", field)
	}
	return value, nil
}

// getConfigDocument reads the host configuration document. It is best-effort
// and only ever inspected for non-sensitive retry settings.
func (c *managementClient) getConfigDocument(ctx context.Context) (map[string]any, error) {
	var document map[string]any
	if errRequest := c.requestJSON(ctx, http.MethodGet, "/v0/management/config", nil, "", &document); errRequest != nil {
		return nil, errRequest
	}
	return document, nil
}

// putIntValue sends one integer setting to the host Management API. CPA's
// integer update handlers read the value under the "value" key.
func (c *managementClient) putIntValue(ctx context.Context, path string, value int) error {
	payload, errMarshal := json.Marshal(map[string]any{"value": value})
	if errMarshal != nil {
		return fmt.Errorf("management request could not be encoded")
	}
	return c.request(ctx, http.MethodPut, path, bytes.NewReader(payload), "application/json")
}
