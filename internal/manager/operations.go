package manager

import (
	"container/heap"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type OperationJournal struct {
	mu              sync.RWMutex
	storeMu         sync.Mutex
	store           string
	operations      []OperationEntry
	segments        []persistedOperationSegment
	nextSegmentID   uint64
	extendedHistory bool
	storageErr      string
	configured      bool
	now             func() time.Time
}

func NewOperationJournal() *OperationJournal {
	return &OperationJournal{now: time.Now}
}

func (j *OperationJournal) Configure(config Config) {
	if j == nil {
		return
	}
	dataDir := normalizeConfig(config).DataDir
	store := operationStoreDirectory(dataDir)
	j.mu.RLock()
	sameStore := j.configured && j.store == store
	j.mu.RUnlock()
	if sameStore {
		if config.OperationSettings != nil {
			if _, errUpdate := j.UpdateRetentionSettings(config.OperationSettings.ExtendedHistory); errUpdate != nil {
				j.setStorageError("operation journal settings could not be persisted")
			}
		}
		return
	}
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	manifest, errLoad := loadOperationManifest(operationManifestPath(store))
	migrated := false
	configuredSettingsChanged := false
	storageErr := ""
	if errors.Is(errLoad, os.ErrNotExist) {
		legacy, errLegacy := loadLegacyOperationState(legacyOperationStorePath(dataDir))
		switch {
		case errLegacy == nil:
			manifest = persistedOperationManifest{
				Version: operationManifestVersion, NextSegmentID: 1,
				Operations: legacy.Operations,
			}
			migrated = true
		case errors.Is(errLegacy, os.ErrNotExist):
			manifest = persistedOperationManifest{Version: operationManifestVersion, NextSegmentID: 1}
		default:
			storageErr = "operation journal could not be loaded"
		}
	} else if errLoad != nil {
		storageErr = "operation journal could not be loaded"
	}
	if config.OperationSettings != nil && manifest.ExtendedHistory != config.OperationSettings.ExtendedHistory {
		manifest.ExtendedHistory = config.OperationSettings.ExtendedHistory
		if !manifest.ExtendedHistory {
			manifest.Segments = nil
		}
		configuredSettingsChanged = true
	}
	j.mu.Lock()
	j.store = store
	j.operations = cloneOperationEntries(manifest.Operations)
	j.segments = append([]persistedOperationSegment(nil), manifest.Segments...)
	j.nextSegmentID = manifest.NextSegmentID
	if j.nextSegmentID == 0 {
		j.nextSegmentID = 1
	}
	j.extendedHistory = manifest.ExtendedHistory
	j.storageErr = storageErr
	j.configured = true
	j.mu.Unlock()
	if storageErr == "" && (migrated || configuredSettingsChanged) {
		if errPersist := j.persistLocked(); errPersist == nil {
			if migrated {
				_ = os.Remove(legacyOperationStorePath(dataDir))
			}
		}
	}
	if storageErr == "" {
		retainedSegments := manifest.Segments
		if !manifest.ExtendedHistory {
			retainedSegments = nil
		}
		if errRemove := removeUnreferencedOperationSegmentFiles(store, retainedSegments); errRemove != nil {
			j.setStorageError("operation journal could not remove archived segments")
		}
	}
}

func (j *OperationJournal) Record(entry OperationEntry) OperationEntry {
	if j == nil {
		return OperationEntry{}
	}
	normalized, ok := normalizeOperationEntry(entry, j.currentTime())
	if !ok {
		return OperationEntry{}
	}
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	j.mu.Lock()
	j.operations = append(j.operations, normalized)
	j.trimLocked()
	j.mu.Unlock()
	_ = j.persistLocked()
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
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	j.mu.Lock()
	for index := range j.operations {
		if j.operations[index].EventID != normalized.EventID {
			continue
		}
		normalized = mergeOperationEntry(j.operations[index], normalized)
		changed := !operationEntryEqual(j.operations[index], normalized)
		j.operations[index] = normalized
		j.mu.Unlock()
		if changed {
			_ = j.persistLocked()
		}
		return cloneOperationEntry(normalized)
	}
	segments := append([]persistedOperationSegment(nil), j.segments...)
	store := j.store
	extended := j.extendedHistory
	j.mu.Unlock()
	if extended {
		for index := len(segments) - 1; index >= 0; index-- {
			operations, errLoad := loadOperationSegment(store, segments[index])
			if errLoad != nil {
				j.setStorageError("operation journal could not be loaded")
				return OperationEntry{}
			}
			for entryIndex := range operations {
				if operations[entryIndex].EventID != normalized.EventID {
					continue
				}
				normalized = mergeOperationEntry(operations[entryIndex], normalized)
				if operationEntryEqual(operations[entryIndex], normalized) {
					return cloneOperationEntry(normalized)
				}
				operations[entryIndex] = normalized
				if errSave := saveOperationSegment(store, segments[index].ID, operations); errSave != nil {
					j.setStorageError("operation journal could not be persisted")
					return OperationEntry{}
				}
				j.setStorageError("")
				return cloneOperationEntry(normalized)
			}
		}
	}
	j.mu.Lock()
	j.operations = append(j.operations, normalized)
	j.trimLocked()
	j.mu.Unlock()
	_ = j.persistLocked()
	return cloneOperationEntry(normalized)
}

func (j *OperationJournal) List(query OperationQuery) OperationListResponse {
	query = normalizeOperationQuery(query)
	if j == nil {
		return OperationListResponse{Operations: []OperationEntry{}, Page: query.Page, PageSize: query.PageSize, RetentionLimit: operationPageSize}
	}
	j.storeMu.Lock()
	j.mu.RLock()
	storageErr := j.storageErr
	extended := j.extendedHistory
	archivedSegments := len(j.segments)
	retained := len(j.operations) + len(j.segments)*operationPageSize
	j.mu.RUnlock()
	start, selectionLimit := operationPageBounds(query.Page, query.PageSize, retained)
	selected, summary, total, errLoad := j.selectOperationsLocked(query, selectionLimit)
	if errLoad != nil {
		storageErr = "operation journal could not be loaded"
		j.setStorageError(storageErr)
	}
	j.storeMu.Unlock()
	sort.Slice(selected, func(left, right int) bool { return operationNewer(selected[left], selected[right]) })
	if start > len(selected) || start >= total {
		start = len(selected)
	}
	end := start + query.PageSize
	if end < start || end > len(selected) {
		end = len(selected)
	}
	pages := 0
	if total > 0 {
		pages = (total-1)/query.PageSize + 1
	}
	return OperationListResponse{
		Operations:       cloneOperationEntries(selected[start:end]),
		Summary:          summary,
		Total:            total,
		Page:             query.Page,
		PageSize:         query.PageSize,
		Pages:            pages,
		ExtendedHistory:  extended,
		ArchivedSegments: archivedSegments,
		RetentionLimit:   operationPageSize,
		Retained:         retained,
		StorageError:     storageErr,
	}
}

// ExportSnapshot reads the filtered journal once. Export rendering already
// requires an O(N) response body, so this avoids multiplying that cost by the
// number of API pages while preserving the list ordering.
func (j *OperationJournal) ExportSnapshot(query OperationQuery) ([]OperationEntry, error) {
	query = normalizeOperationQuery(query)
	if j == nil {
		return []OperationEntry{}, nil
	}
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	operations := make([]OperationEntry, 0)
	errLoad := j.scanOperationsLocked(func(operation OperationEntry) {
		if operationMatchesQuery(operation, query) {
			operations = append(operations, operation)
		}
	})
	if errLoad != nil {
		j.setStorageError("operation journal could not be loaded")
		return nil, errLoad
	}
	sort.Slice(operations, func(left, right int) bool { return operationNewer(operations[left], operations[right]) })
	return operations, nil
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
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	j.mu.Lock()
	j.operations = []OperationEntry{entry}
	j.segments = nil
	j.mu.Unlock()
	if errPersist := j.persistLocked(); errPersist == nil {
		if errRemove := removeOperationSegmentFiles(j.store); errRemove != nil {
			j.setStorageError("operation journal could not remove archived segments")
		}
	}
	return cloneOperationEntry(entry)
}

func (j *OperationJournal) RetentionSettings() OperationRetentionSettings {
	if j == nil {
		return OperationRetentionSettings{PageSize: operationPageSize}
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.retentionSettingsLocked()
}

func (j *OperationJournal) UpdateRetentionSettings(enabled bool) (OperationRetentionSettings, error) {
	if j == nil {
		return OperationRetentionSettings{PageSize: operationPageSize}, fmt.Errorf("operation journal is unavailable")
	}
	j.storeMu.Lock()
	defer j.storeMu.Unlock()
	j.mu.RLock()
	current := j.extendedHistory
	j.mu.RUnlock()
	if current == enabled {
		return j.RetentionSettings(), nil
	}
	if !enabled {
		operations, _, _, errLoad := j.selectOperationsLocked(OperationQuery{}, operationPageSize)
		if errLoad != nil {
			j.setStorageError("operation journal could not be loaded")
			return j.RetentionSettings(), fmt.Errorf("operation journal history could not be loaded")
		}
		sort.Slice(operations, func(left, right int) bool { return operationNewer(operations[right], operations[left]) })
		j.mu.Lock()
		previousOperations := cloneOperationEntries(j.operations)
		previousSegments := append([]persistedOperationSegment(nil), j.segments...)
		j.operations = cloneOperationEntries(operations)
		j.segments = nil
		j.extendedHistory = false
		j.mu.Unlock()
		if errPersist := j.persistLocked(); errPersist != nil {
			j.mu.Lock()
			j.operations = previousOperations
			j.segments = previousSegments
			j.extendedHistory = true
			j.mu.Unlock()
			return j.RetentionSettings(), errPersist
		}
		if errRemove := removeOperationSegmentFiles(j.store); errRemove != nil {
			j.setStorageError("operation journal could not remove archived segments")
			return j.RetentionSettings(), fmt.Errorf("operation journal archived segments could not be removed")
		}
	} else {
		j.mu.Lock()
		j.extendedHistory = true
		j.mu.Unlock()
		if errPersist := j.persistLocked(); errPersist != nil {
			j.mu.Lock()
			j.extendedHistory = false
			j.mu.Unlock()
			return j.RetentionSettings(), errPersist
		}
	}
	return j.RetentionSettings(), nil
}

func (j *OperationJournal) persistLocked() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.configured || strings.TrimSpace(j.store) == "" {
		j.storageErr = "operation journal storage is unavailable"
		return fmt.Errorf("operation journal storage is unavailable")
	}
	if !j.extendedHistory {
		j.trimLocked()
	}
	for j.extendedHistory && len(j.operations) > operationPageSize {
		segmentOperations := cloneOperationEntries(j.operations[:operationPageSize])
		segmentID := j.nextSegmentID
		if segmentID == 0 {
			segmentID = 1
		}
		if errSave := saveOperationSegment(j.store, segmentID, segmentOperations); errSave != nil {
			j.storageErr = "operation journal could not be persisted"
			return errSave
		}
		j.segments = append(j.segments, persistedOperationSegment{ID: segmentID, Count: operationPageSize})
		j.nextSegmentID = segmentID + 1
		j.operations = append([]OperationEntry(nil), j.operations[operationPageSize:]...)
	}
	manifest := persistedOperationManifest{
		Version:         operationManifestVersion,
		ExtendedHistory: j.extendedHistory,
		NextSegmentID:   j.nextSegmentID,
		Segments:        append([]persistedOperationSegment(nil), j.segments...),
		Operations:      cloneOperationEntries(j.operations),
	}
	if errSave := saveOperationManifest(j.store, manifest); errSave != nil {
		j.storageErr = "operation journal could not be persisted"
		return errSave
	}
	j.storageErr = ""
	return nil
}

func (j *OperationJournal) trimLocked() {
	if !j.extendedHistory && len(j.operations) > operationPageSize {
		j.operations = append([]OperationEntry(nil), j.operations[len(j.operations)-operationPageSize:]...)
	}
}

func (j *OperationJournal) currentTime() time.Time {
	now := time.Now
	if j != nil && j.now != nil {
		now = j.now
	}
	return now().UTC()
}

func sanitizePersistedOperations(entries []OperationEntry, limit int) []OperationEntry {
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
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

func (j *OperationJournal) scanOperationsLocked(visit func(OperationEntry)) error {
	j.mu.RLock()
	store := j.store
	segments := append([]persistedOperationSegment(nil), j.segments...)
	current := cloneOperationEntries(j.operations)
	extended := j.extendedHistory
	j.mu.RUnlock()
	var firstErr error
	if extended {
		for _, segment := range segments {
			page, errLoad := loadOperationSegment(store, segment)
			if errLoad != nil {
				if firstErr == nil {
					firstErr = errLoad
				}
				continue
			}
			for _, operation := range page {
				visit(operation)
			}
		}
	}
	for _, operation := range current {
		visit(operation)
	}
	return firstErr
}

func (j *OperationJournal) selectOperationsLocked(query OperationQuery, limit int) ([]OperationEntry, OperationSummary, int, error) {
	query = normalizeOperationQuery(query)
	if limit < 0 {
		limit = 0
	}
	selected := &operationOldestHeap{}
	if limit > 0 {
		*selected = make([]OperationEntry, 0, limit)
	}
	summary := OperationSummary{}
	total := 0
	errLoad := j.scanOperationsLocked(func(operation OperationEntry) {
		if !operationMatchesQuery(operation, query) {
			return
		}
		total++
		addOperationSummary(&summary, operation)
		if limit == 0 {
			return
		}
		if selected.Len() < limit {
			heap.Push(selected, operation)
			return
		}
		if operationNewer(operation, (*selected)[0]) {
			(*selected)[0] = operation
			heap.Fix(selected, 0)
		}
	})
	summary.Total = total
	return append([]OperationEntry(nil), (*selected)...), summary, total, errLoad
}

func operationPageBounds(page, pageSize, retained int) (int, int) {
	if page < 1 || pageSize < 1 || retained < 1 {
		return 0, 0
	}
	offset := page - 1
	if offset > retained/pageSize {
		return retained, 0
	}
	start := offset * pageSize
	if start >= retained {
		return retained, 0
	}
	limit := start + pageSize
	if limit < start || limit > retained {
		limit = retained
	}
	return start, limit
}

type operationOldestHeap []OperationEntry

func (h operationOldestHeap) Len() int { return len(h) }
func (h operationOldestHeap) Less(left, right int) bool {
	return operationNewer(h[right], h[left])
}
func (h operationOldestHeap) Swap(left, right int) { h[left], h[right] = h[right], h[left] }
func (h *operationOldestHeap) Push(value any) {
	*h = append(*h, value.(OperationEntry))
}
func (h *operationOldestHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func (j *OperationJournal) retentionSettingsLocked() OperationRetentionSettings {
	return OperationRetentionSettings{
		ExtendedHistory:  j.extendedHistory,
		PageSize:         operationPageSize,
		Retained:         len(j.operations) + len(j.segments)*operationPageSize,
		ArchivedSegments: len(j.segments),
	}
}

func (j *OperationJournal) setStorageError(message string) {
	j.mu.Lock()
	j.storageErr = message
	j.mu.Unlock()
}

func mergeOperationEntry(existing, replacement OperationEntry) OperationEntry {
	if replacement.Scope == "" {
		replacement.Scope = existing.Scope
	}
	if replacement.TargetID == "" {
		replacement.TargetID = existing.TargetID
	}
	if replacement.Format == "" {
		replacement.Format = existing.Format
	}
	if replacement.Version == "" {
		replacement.Version = existing.Version
	}
	replacement.ID = existing.ID
	return replacement
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
	entry.HTTPStatus = boundedHTTPStatus(entry.HTTPStatus)
	entry.Attempts = boundedCounter(entry.Attempts)
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
		"restart_required", "install_failed", "update_available", "up_to_date", "check_completed", "check_failed",
		"healthy_recent_success", "quota_exhausted", "token_revoked", "invalid_credentials",
		"account_deactivated", "workspace_deactivated", "authentication_review", "billing_review",
		"credential_permission_denied", "native_unavailable", "manual_disabled", "transient_failure",
		"no_recent_evidence", "mutation_busy", "account_changed", "account_missing", "account_read_only",
		"management_unavailable", "delete_failed", "credential_converted", "experiment_disabled",
		"login_state_not_found", "login_state_expired", "conversion_running", "session_rejected", "invalid_session":
		return value
	case "notification_delivered", "notification_failed", "notification_rejected", "notification_queue_full":
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

func operationNewer(left, right OperationEntry) bool {
	leftTime := operationSortTime(left)
	rightTime := operationSortTime(right)
	if leftTime.Equal(rightTime) {
		return left.ID > right.ID
	}
	return leftTime.After(rightTime)
}

func operationMatchesQuery(entry OperationEntry, query OperationQuery) bool {
	return (query.Category == "" || entry.Category == query.Category) &&
		(query.Status == "" || entry.Status == query.Status) &&
		(query.Source == "" || entry.Source == query.Source) &&
		(query.Search == "" || operationMatchesSearch(entry, query.Search))
}

func operationMatchesSearch(entry OperationEntry, search string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		entry.ID, entry.Category, entry.Action, entry.Status, entry.Source, entry.Scope,
		entry.TargetID, entry.ReasonCode, entry.RelatedJobID, entry.RelatedActionID,
		entry.Version, entry.Format, entry.Model, strconv.Itoa(entry.HTTPStatus), strconv.Itoa(entry.Attempts),
	}, "\n"))
	return strings.Contains(haystack, search)
}

func summarizeOperations(entries []OperationEntry) OperationSummary {
	summary := OperationSummary{Total: len(entries)}
	for _, entry := range entries {
		addOperationSummary(&summary, entry)
	}
	return summary
}

func addOperationSummary(summary *OperationSummary, entry OperationEntry) {
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

func cloneOperationEntry(entry OperationEntry) OperationEntry {
	return entry
}

func cloneOperationEntries(entries []OperationEntry) []OperationEntry {
	return append([]OperationEntry{}, entries...)
}

func operationEntryEqual(left, right OperationEntry) bool {
	return left == right
}
