package manager

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type OperationJournal struct {
	mu         sync.RWMutex
	storeMu    sync.Mutex
	store      string
	operations []OperationEntry
	storageErr string
	configured bool
	now        func() time.Time
}

func NewOperationJournal() *OperationJournal {
	return &OperationJournal{now: time.Now}
}

func (j *OperationJournal) Configure(config Config) {
	if j == nil {
		return
	}
	path := operationStorePath(normalizeConfig(config).DataDir)
	j.mu.RLock()
	sameStore := j.configured && j.store == path
	j.mu.RUnlock()
	if sameStore {
		return
	}
	operations := []OperationEntry(nil)
	storageErr := ""
	state, errLoad := loadOperationState(path)
	if errLoad == nil {
		operations = state.Operations
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		storageErr = "operation journal could not be loaded"
	}
	j.mu.Lock()
	j.store = path
	j.operations = operations
	j.storageErr = storageErr
	j.configured = true
	j.mu.Unlock()
}

func (j *OperationJournal) Record(entry OperationEntry) OperationEntry {
	if j == nil {
		return OperationEntry{}
	}
	normalized, ok := normalizeOperationEntry(entry, j.currentTime())
	if !ok {
		return OperationEntry{}
	}
	j.mu.Lock()
	j.operations = append(j.operations, normalized)
	j.trimLocked()
	j.mu.Unlock()
	j.persist()
	return cloneOperationEntry(normalized)
}

func (j *OperationJournal) Upsert(eventID string, entry OperationEntry) OperationEntry {
	if j == nil {
		return OperationEntry{}
	}
	entry.EventID = safeOperationIdentifier(eventID, 160)
	if entry.EventID == "" {
		return j.Record(entry)
	}
	normalized, ok := normalizeOperationEntry(entry, j.currentTime())
	if !ok {
		return OperationEntry{}
	}
	changed := true
	j.mu.Lock()
	for index := range j.operations {
		if j.operations[index].EventID != normalized.EventID {
			continue
		}
		if normalized.Scope == "" {
			normalized.Scope = j.operations[index].Scope
		}
		if normalized.TargetID == "" {
			normalized.TargetID = j.operations[index].TargetID
		}
		if normalized.Format == "" {
			normalized.Format = j.operations[index].Format
		}
		if normalized.Version == "" {
			normalized.Version = j.operations[index].Version
		}
		normalized.ID = j.operations[index].ID
		changed = !operationEntryEqual(j.operations[index], normalized)
		j.operations[index] = normalized
		j.mu.Unlock()
		if changed {
			j.persist()
		}
		return cloneOperationEntry(normalized)
	}
	j.operations = append(j.operations, normalized)
	j.trimLocked()
	j.mu.Unlock()
	j.persist()
	return cloneOperationEntry(normalized)
}

func (j *OperationJournal) List(query OperationQuery) OperationListResponse {
	query = normalizeOperationQuery(query)
	if j == nil {
		return OperationListResponse{Page: query.Page, PageSize: query.PageSize}
	}
	j.mu.RLock()
	operations := cloneOperationEntries(j.operations)
	storageErr := j.storageErr
	j.mu.RUnlock()
	sort.SliceStable(operations, func(left, right int) bool {
		leftTime := operationSortTime(operations[left])
		rightTime := operationSortTime(operations[right])
		if leftTime.Equal(rightTime) {
			return operations[left].ID > operations[right].ID
		}
		return leftTime.After(rightTime)
	})
	filtered := operations[:0]
	for _, operation := range operations {
		if query.Category != "" && operation.Category != query.Category {
			continue
		}
		if query.Status != "" && operation.Status != query.Status {
			continue
		}
		if query.Source != "" && operation.Source != query.Source {
			continue
		}
		if query.Search != "" && !operationMatchesSearch(operation, query.Search) {
			continue
		}
		filtered = append(filtered, operation)
	}
	total := len(filtered)
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	pages := 0
	if total > 0 {
		pages = (total + query.PageSize - 1) / query.PageSize
	}
	return OperationListResponse{
		Operations:   cloneOperationEntries(filtered[start:end]),
		Summary:      summarizeOperations(filtered),
		Total:        total,
		Page:         query.Page,
		PageSize:     query.PageSize,
		Pages:        pages,
		StorageError: storageErr,
	}
}

func (j *OperationJournal) Clear() OperationEntry {
	if j == nil {
		return OperationEntry{}
	}
	now := j.currentTime()
	entry, _ := normalizeOperationEntry(OperationEntry{
		Category:   OperationCategoryJournal,
		Action:     OperationActionJournalClear,
		Status:     OperationStatusSucceeded,
		Source:     OperationSourceManual,
		Scope:      OperationScopeSystem,
		StartedAt:  now,
		FinishedAt: now,
	}, now)
	j.mu.Lock()
	j.operations = []OperationEntry{entry}
	j.mu.Unlock()
	j.persist()
	return cloneOperationEntry(entry)
}

func (j *OperationJournal) persist() {
	if j == nil {
		return
	}
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	j.mu.RLock()
	path := j.store
	configured := j.configured
	operations := cloneOperationEntries(j.operations)
	j.mu.RUnlock()
	if !configured || strings.TrimSpace(path) == "" {
		j.mu.Lock()
		j.storageErr = "operation journal storage is unavailable"
		j.mu.Unlock()
		return
	}
	errSave := saveOperationState(path, operations)
	j.mu.Lock()
	if errSave != nil {
		j.storageErr = "operation journal could not be persisted"
	} else {
		j.storageErr = ""
	}
	j.mu.Unlock()
}

func (j *OperationJournal) trimLocked() {
	if len(j.operations) > maxOperationEntries {
		j.operations = append([]OperationEntry(nil), j.operations[len(j.operations)-maxOperationEntries:]...)
	}
}

func (j *OperationJournal) currentTime() time.Time {
	now := time.Now
	if j != nil && j.now != nil {
		now = j.now
	}
	return now().UTC()
}

func sanitizePersistedOperations(entries []OperationEntry) []OperationEntry {
	if len(entries) > maxOperationEntries {
		entries = entries[len(entries)-maxOperationEntries:]
	}
	out := make([]OperationEntry, 0, len(entries))
	for _, entry := range entries {
		normalized, ok := normalizeOperationEntry(entry, time.Now().UTC())
		if ok {
			out = append(out, normalized)
		}
	}
	return out
}

func normalizeOperationEntry(entry OperationEntry, now time.Time) (OperationEntry, bool) {
	entry.Category = normalizeOperationCategory(entry.Category)
	entry.Action = normalizeOperationAction(entry.Action)
	entry.Status = normalizeOperationStatus(entry.Status)
	entry.Source = normalizeOperationSource(entry.Source)
	entry.Scope = normalizeOperationScope(entry.Scope)
	if entry.Category == "" || entry.Action == "" || entry.Status == "" || entry.Source == "" {
		return OperationEntry{}, false
	}
	entry.ID = safeOperationIdentifier(entry.ID, 160)
	if entry.ID == "" {
		id, errID := randomIdentifier()
		if errID == nil {
			entry.ID = id
		} else {
			entry.ID = "operation-" + now.Format("20060102T150405.000000000")
		}
	}
	entry.EventID = safeOperationIdentifier(entry.EventID, 160)
	entry.TargetID = safeOperationIdentifier(entry.TargetID, 256)
	entry.RelatedJobID = safeOperationIdentifier(entry.RelatedJobID, 160)
	entry.RelatedActionID = safeOperationIdentifier(entry.RelatedActionID, 160)
	entry.TargetCount = boundedCounter(entry.TargetCount)
	entry.Succeeded = boundedCounter(entry.Succeeded)
	entry.Failed = boundedCounter(entry.Failed)
	entry.Skipped = boundedCounter(entry.Skipped)
	entry.ReasonCode = safeOperationReason(entry.ReasonCode)
	entry.Version = safeOperationVersion(entry.Version)
	entry.Format = safeOperationFormat(entry.Format)
	entry.Model = safeModelIdentifier(entry.Model)
	if entry.StartedAt.IsZero() {
		entry.StartedAt = now
	} else {
		entry.StartedAt = entry.StartedAt.UTC()
	}
	if entry.Status == OperationStatusRunning {
		entry.FinishedAt = time.Time{}
	} else if entry.FinishedAt.IsZero() {
		entry.FinishedAt = entry.StartedAt
	} else {
		entry.FinishedAt = entry.FinishedAt.UTC()
	}
	if !entry.FinishedAt.IsZero() && entry.FinishedAt.Before(entry.StartedAt) {
		entry.FinishedAt = entry.StartedAt
	}
	return entry, true
}

func safeOperationIdentifier(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return ""
		}
	}
	return value
}

func safeOperationVersion(value string) string {
	if _, normalized, ok := parseReleaseVersion(value); ok {
		return normalized
	}
	return ""
}

func safeOperationFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cpa", "sub2api", "cockpit", "9router", "codex", "axonhub", "codexmanager", "json", "csv", "jsonl":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func safeOperationReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "completed", "partial_failure", "operation_failed", "interrupted", "conflict",
		"restart_required", "install_failed", "update_available", "up_to_date", "check_failed",
		"healthy_recent_success", "quota_exhausted", "token_revoked", "invalid_credentials",
		"account_deactivated", "workspace_deactivated", "authentication_review", "billing_review",
		"credential_permission_denied", "native_unavailable", "manual_disabled", "transient_failure",
		"no_recent_evidence", "mutation_busy", "account_changed", "account_missing", "account_read_only",
		"management_unavailable", "delete_failed":
		return value
	case "model_response_ok", "model_not_found", "account_unavailable", "authentication_failed",
		"quota_limited", "request_timeout", "upstream_unavailable", "invalid_response", "unsupported_provider":
		return value
	default:
		return "operation_failed"
	}
}

func operationSortTime(entry OperationEntry) time.Time {
	if !entry.FinishedAt.IsZero() {
		return entry.FinishedAt
	}
	return entry.StartedAt
}

func operationMatchesSearch(entry OperationEntry, search string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		entry.ID, entry.Category, entry.Action, entry.Status, entry.Source, entry.Scope,
		entry.TargetID, entry.ReasonCode, entry.RelatedJobID, entry.RelatedActionID,
		entry.Version, entry.Format, entry.Model,
	}, "\n"))
	return strings.Contains(haystack, search)
}

func summarizeOperations(entries []OperationEntry) OperationSummary {
	summary := OperationSummary{Total: len(entries)}
	for _, entry := range entries {
		switch entry.Status {
		case OperationStatusRunning:
			summary.Running++
		case OperationStatusSucceeded:
			summary.Succeeded++
		case OperationStatusFailed:
			summary.Failed++
		case OperationStatusInterrupted:
			summary.Interrupted++
		case OperationStatusPartial, OperationStatusWarning, OperationStatusSkipped:
			summary.Attention++
		}
	}
	return summary
}

func cloneOperationEntry(entry OperationEntry) OperationEntry {
	return entry
}

func cloneOperationEntries(entries []OperationEntry) []OperationEntry {
	return append([]OperationEntry(nil), entries...)
}

func operationEntryEqual(left, right OperationEntry) bool {
	return left == right
}
