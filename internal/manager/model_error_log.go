package manager

import (
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

// CPA request-completion outcomes. The host reports a finished client request's
// outcome as a plain string; these are the values it uses.
const (
	requestCompletionSucceeded = "succeeded"
	requestCompletionFailed    = "failed"
	requestCompletionRejected  = "rejected"
	requestCompletionCanceled  = "canceled"
)

// recordModelError journals one failed client request as a model error. CPA
// invokes request.complete exactly once per client request that ends in an error,
// after its retry loop has finished, so one entry describes the whole request:
// the status the client actually saw, the operator's retry budget, and the
// sanitized upstream error text of the last attempt. Successful requests are
// never recorded, and a completion without an outcome and without an error is
// skipped. A client cancellation is recorded as interrupted with the canceled
// reason because it is still worth seeing in the operator's log.
//
// The function is best-effort: it never returns an error and never lets a journal
// problem escape into CPA's completion path.
func (a *App) recordModelError(completion cpaapi.RequestCompletion) {
	if a == nil {
		return
	}
	defer func() {
		// A journal bug must never take down the host's completion callback.
		_ = recover()
	}()
	outcome := strings.ToLower(strings.TrimSpace(completion.Outcome))
	message := strings.TrimSpace(completion.Error)
	budget := a.autoRetry.Attempts()
	attempts, terminated, tracked := a.modelRetry.snapshot(completion.RequestID)
	a.modelRetry.forget(completion.RequestID)
	if outcome == requestCompletionSucceeded {
		return
	}
	if outcome == "" && message == "" {
		return
	}
	status := OperationStatusFailed
	reason := OperationFailureModelUpstream
	switch {
	case outcome == requestCompletionCanceled:
		status = OperationStatusInterrupted
		reason = OperationFailureModelCanceled
	case terminated || (budget > 0 && tracked && attempts >= budget+1):
		// The request consumed the configured retries completely, whether the
		// interceptor answered it with the final 503 or CPA itself failed last.
		reason = OperationFailureModelRetryExhausted
	}
	entry := OperationEntry{
		Category:   OperationCategoryModelError,
		Action:     OperationActionModelFailure,
		Status:     status,
		Source:     OperationSourceBackground,
		Scope:      modelErrorScope(completion),
		TargetID:   completion.RequestID,
		Model:      completion.Model,
		HTTPStatus: completion.StatusCode,
		Attempts:   budget,
		ReasonCode: reason,
		Message:    sanitizeOperationMessage(message),
	}
	if !completion.StartedAt.IsZero() {
		entry.StartedAt = completion.StartedAt
	}
	if !completion.CompletedAt.IsZero() {
		entry.FinishedAt = completion.CompletedAt
	}
	a.operations.Record(entry)
}

// modelErrorScope names the credential CPA selected for the request when the
// completion reports it, so an operator can correlate the entry with one account
// without persisting anything credential-like. Anything unexpected falls back to
// the system scope.
func modelErrorScope(completion cpaapi.RequestCompletion) string {
	authID, _ := completion.Metadata[selectedAuthMetadataKey].(string)
	if bounded := safeOperationScopeIdentifier(authID); bounded != "" {
		return bounded
	}
	return OperationScopeSystem
}
