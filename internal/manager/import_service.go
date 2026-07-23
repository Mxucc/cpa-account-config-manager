package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	maxPublicImportSkips             = 100
	agentIdentityImportVerifyWorkers = 8
)

var (
	ErrImportPreviewNotFound = errors.New("import preview not found")
	ErrImportPreviewExpired  = errors.New("import preview expired")
	ErrImportNoAccounts      = errors.New("import contains no supported account records")
	ErrImportAuthUnavailable = errors.New("CPA Auth storage is unavailable")
)

const (
	ImportResultImported = "imported"
	ImportResultSkipped  = "skipped"
	ImportResultFailed   = "failed"
)

type ImportStartRequest struct {
	PreviewID string `json:"preview_id"`
}

type ImportPreviewItem struct {
	Index            int      `json:"index"`
	SourceName       string   `json:"source_name"`
	SourcePath       string   `json:"source_path,omitempty"`
	TargetName       string   `json:"target_name"`
	Email            string   `json:"email,omitempty"`
	AccountID        string   `json:"account_id,omitempty"`
	Label            string   `json:"label"`
	SyntheticIDToken bool     `json:"synthetic_id_token"`
	Warnings         []string `json:"warnings,omitempty"`
	CredentialType   string   `json:"credential_type,omitempty"`
}

type ImportPreview struct {
	ID           string              `json:"id"`
	CreatedAt    time.Time           `json:"created_at"`
	ExpiresAt    time.Time           `json:"expires_at"`
	InputType    string              `json:"input_type"`
	SourceFiles  int                 `json:"source_files"`
	Total        int                 `json:"total"`
	Skipped      int                 `json:"skipped"`
	Warnings     []string            `json:"warnings,omitempty"`
	Items        []ImportPreviewItem `json:"items"`
	SkippedItems []importSkipped     `json:"skipped_items,omitempty"`
}

type ImportResultItem struct {
	Index      int    `json:"index"`
	SourceName string `json:"source_name"`
	SourcePath string `json:"source_path,omitempty"`
	TargetName string `json:"target_name"`
	Email      string `json:"email,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type ImportResult struct {
	ID                     string             `json:"id"`
	State                  string             `json:"state"`
	Total                  int                `json:"total"`
	Imported               int                `json:"imported"`
	Skipped                int                `json:"skipped"`
	Failed                 int                `json:"failed"`
	StartedAt              time.Time          `json:"started_at"`
	FinishedAt             time.Time          `json:"finished_at"`
	Results                []ImportResultItem `json:"results"`
	UsageCollectionStarted bool               `json:"usage_collection_started,omitempty"`
	UsageCollectionTargets int                `json:"usage_collection_targets,omitempty"`
}

type storedImportItem struct {
	Public   ImportPreviewItem
	AuthJSON json.RawMessage
}

type storedImportPreview struct {
	Public ImportPreview
	Items  []storedImportItem
}

type importPreviewStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]storedImportPreview
	timers  map[string]*time.Timer
}

type ImportService struct {
	operationMu   sync.Mutex
	host          AuthHost
	mutations     *MutationCoordinator
	store         *importPreviewStore
	limits        importLimits
	now           func() time.Time
	agentIdentity *AgentIdentityExperiment
}

func (s *ImportService) SetAgentIdentityExperiment(experiment *AgentIdentityExperiment) {
	if s == nil {
		return
	}
	s.agentIdentity = experiment
}

func NewImportService(host AuthHost, mutations *MutationCoordinator) *ImportService {
	if mutations == nil {
		mutations = NewMutationCoordinator()
	}
	return &ImportService{
		host:      host,
		mutations: mutations,
		store: &importPreviewStore{
			ttl:     defaultPreviewTTL,
			entries: make(map[string]storedImportPreview),
			timers:  make(map[string]*time.Timer),
		},
		limits: defaultImportLimits(),
		now:    time.Now,
	}
}

func (s *ImportService) Preview(ctx context.Context, upload importUpload) (ImportPreview, error) {
	if s == nil || s.host == nil || s.store == nil {
		return ImportPreview{}, ErrImportAuthUnavailable
	}
	parsed, errParse := parseImportUpload(upload, s.limits, s.now().UTC())
	if errParse != nil {
		return ImportPreview{}, errParse
	}
	return s.previewParsed(ctx, parsed, importInputType(upload))
}

func (s *ImportService) PreviewMany(ctx context.Context, uploads []importUpload) (ImportPreview, error) {
	if s == nil || s.host == nil || s.store == nil {
		return ImportPreview{}, ErrImportAuthUnavailable
	}
	parsed, errParse := parseImportUploads(uploads, s.limits, s.now().UTC())
	if errParse != nil {
		return ImportPreview{}, errParse
	}
	return s.previewParsed(ctx, parsed, importInputTypeMany(uploads))
}

func (s *ImportService) previewParsed(ctx context.Context, parsed importParseResult, inputType string) (ImportPreview, error) {
	defer clearImportCandidates(parsed.Candidates)
	verificationErrors := verifyAgentIdentityImportCandidates(ctx, parsed.Candidates, s.agentIdentity)
	verified := parsed.Candidates[:0]
	for index, candidate := range parsed.Candidates {
		if !candidate.AgentIdentity {
			verified = append(verified, candidate)
			continue
		}
		if errVerify := verificationErrors[index]; errVerify != nil {
			parsed.Skipped = append(parsed.Skipped, importSkipped{SourceName: candidate.SourceName, SourcePath: candidate.SourcePath, Reason: errVerify.Error()})
			clear(parsed.Candidates[index].AuthJSON)
			parsed.Candidates[index].AuthJSON = nil
			continue
		}
		verified = append(verified, candidate)
	}
	parsed.Candidates = verified
	if len(parsed.Candidates) == 0 {
		return ImportPreview{}, ErrImportNoAccounts
	}
	existingEntries, errList := s.host.ListAuth(ctx)
	if errList != nil {
		return ImportPreview{}, fmt.Errorf("%w: list existing Auth files", ErrImportAuthUnavailable)
	}
	reserved := importAuthNameSet(existingEntries)

	now := s.now().UTC()
	id, errID := randomIdentifier()
	if errID != nil {
		return ImportPreview{}, fmt.Errorf("create import preview id: %w", errID)
	}
	items := make([]storedImportItem, 0, len(parsed.Candidates))
	publicItems := make([]ImportPreviewItem, 0, len(parsed.Candidates))
	for index, candidate := range parsed.Candidates {
		baseName := importTargetFilename(candidate, index)
		targetName, adjusted := reserveImportTargetName(baseName, reserved)
		warnings := append([]string(nil), candidate.Warnings...)
		if adjusted {
			warnings = append(warnings, "filename was adjusted to avoid an existing Auth file")
		}
		public := ImportPreviewItem{
			Index:            index + 1,
			SourceName:       candidate.SourceName,
			SourcePath:       candidate.SourcePath,
			TargetName:       targetName,
			Email:            candidate.Email,
			AccountID:        candidate.AccountID,
			Label:            firstNonEmpty(candidate.Email, candidate.Name, candidate.AccountID, targetName),
			SyntheticIDToken: candidate.SyntheticIDToken,
			Warnings:         warnings,
		}
		if candidate.AgentIdentity {
			public.CredentialType = "agent_identity"
		}
		publicItems = append(publicItems, public)
		items = append(items, storedImportItem{Public: public, AuthJSON: append(json.RawMessage(nil), candidate.AuthJSON...)})
	}
	warnings := []string{"existing Auth files will not be overwritten"}
	if len(parsed.Skipped) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d unsupported or duplicate record(s) were skipped", len(parsed.Skipped)))
	}
	public := ImportPreview{
		ID:           id,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.store.ttl),
		InputType:    inputType,
		SourceFiles:  parsed.SourceFiles,
		Total:        len(publicItems),
		Skipped:      len(parsed.Skipped),
		Warnings:     warnings,
		Items:        publicItems,
		SkippedItems: clonePublicImportSkips(parsed.Skipped),
	}
	s.store.put(storedImportPreview{Public: public, Items: items})
	return cloneImportPreview(public), nil
}

func verifyAgentIdentityImportCandidates(ctx context.Context, candidates []importCandidate, experiment *AgentIdentityExperiment) []error {
	results := make([]error, len(candidates))
	identityCount := 0
	for index := range candidates {
		if !candidates[index].AgentIdentity {
			continue
		}
		identityCount++
		if experiment == nil {
			results[index] = fmt.Errorf("Agent Identity import is unavailable")
		}
	}
	if identityCount == 0 || experiment == nil {
		return results
	}
	workerCount := min(identityCount, agentIdentityImportVerifyWorkers)
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = experiment.VerifyImport(ctx, candidates[index].AuthJSON)
			}
		}()
	}
	for index := range candidates {
		if candidates[index].AgentIdentity {
			jobs <- index
		}
	}
	close(jobs)
	workers.Wait()
	return results
}

func (s *ImportService) Start(ctx context.Context, previewID string) (ImportResult, error) {
	if s == nil || s.host == nil || s.store == nil {
		return ImportResult{}, ErrImportAuthUnavailable
	}
	previewID = strings.TrimSpace(previewID)
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if errValidate := s.store.validate(previewID, s.now().UTC()); errValidate != nil {
		return ImportResult{}, errValidate
	}
	owner := "import:" + previewID
	if !s.mutations.TryAcquire(owner) {
		return ImportResult{}, ErrJobBusy
	}
	defer s.mutations.Release(owner)

	entries, errList := s.host.ListAuth(ctx)
	if errList != nil {
		return ImportResult{}, fmt.Errorf("%w: verify existing Auth files", ErrImportAuthUnavailable)
	}
	preview, errTake := s.store.take(previewID, s.now().UTC())
	if errTake != nil {
		return ImportResult{}, errTake
	}
	defer clearStoredImportPreview(&preview)

	startedAt := s.now().UTC()
	result := ImportResult{
		ID:        preview.Public.ID,
		Total:     len(preview.Items),
		StartedAt: startedAt,
		Results:   make([]ImportResultItem, 0, len(preview.Items)),
	}
	knownNames := importAuthNameSet(entries)
	for index, item := range preview.Items {
		entryResult := ImportResultItem{
			Index:      item.Public.Index,
			SourceName: item.Public.SourceName,
			SourcePath: item.Public.SourcePath,
			TargetName: item.Public.TargetName,
			Email:      item.Public.Email,
			AccountID:  item.Public.AccountID,
			Label:      item.Public.Label,
		}
		if errContext := ctx.Err(); errContext != nil {
			entryResult.Status = ImportResultFailed
			entryResult.Error = "import was cancelled"
			result.Failed++
			result.Results = append(result.Results, entryResult)
			continue
		}
		if index > 0 {
			currentEntries, errRefresh := s.host.ListAuth(ctx)
			if errRefresh != nil {
				entryResult.Status = ImportResultFailed
				entryResult.Error = "could not verify the target Auth filename"
				result.Failed++
				result.Results = append(result.Results, entryResult)
				continue
			}
			for name := range importAuthNameSet(currentEntries) {
				knownNames[name] = struct{}{}
			}
		}
		nameKey := strings.ToLower(strings.TrimSpace(item.Public.TargetName))
		if _, exists := knownNames[nameKey]; exists {
			entryResult.Status = ImportResultSkipped
			entryResult.Error = "target Auth file already exists"
			result.Skipped++
			result.Results = append(result.Results, entryResult)
			continue
		}
		if _, errSave := s.host.SaveAuth(ctx, item.Public.TargetName, item.AuthJSON); errSave != nil {
			entryResult.Status = ImportResultFailed
			entryResult.Error = "CPA rejected the converted Auth file"
			result.Failed++
			result.Results = append(result.Results, entryResult)
			continue
		}
		knownNames[nameKey] = struct{}{}
		entryResult.Status = ImportResultImported
		result.Imported++
		result.Results = append(result.Results, entryResult)
	}
	result.FinishedAt = s.now().UTC()
	switch {
	case result.Imported == result.Total:
		result.State = JobStateCompleted
	case result.Imported > 0:
		result.State = JobStatePartial
	default:
		result.State = JobStateFailed
	}
	return result, nil
}

func (s *ImportService) Clear() {
	if s == nil || s.store == nil {
		return
	}
	s.store.clear()
}

func importInputType(upload importUpload) string {
	if importUploadIsZIP(upload) {
		return "zip"
	}
	extension := strings.ToLower(filepath.Ext(upload.Name))
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(upload.ContentType, ";")[0]))
	if extension == ".txt" || extension == ".jsonl" || extension == ".ndjson" || contentType == "text/plain" || contentType == "application/x-ndjson" {
		return "text"
	}
	return "json"
}

func importInputTypeMany(uploads []importUpload) string {
	inputType := ""
	for _, upload := range uploads {
		current := importInputType(upload)
		if inputType == "" {
			inputType = current
			continue
		}
		if inputType != current {
			return "mixed"
		}
	}
	if inputType == "" {
		return "json"
	}
	return inputType
}

func clearImportCandidates(candidates []importCandidate) {
	for index := range candidates {
		clear(candidates[index].AuthJSON)
		candidates[index].AuthJSON = nil
	}
}

func importAuthNameSet(entries []cpaapi.HostAuthFileEntry) map[string]struct{} {
	result := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := strings.ToLower(strings.TrimSpace(filepath.Base(entry.Name)))
		if name != "" && name != "." {
			result[name] = struct{}{}
		}
	}
	return result
}

func importTargetFilename(candidate importCandidate, index int) string {
	token := sanitizeImportFilenameToken(candidate.Email)
	if token == "" {
		token = sanitizeImportFilenameToken(candidate.AccountID)
	}
	if token == "" {
		token = sanitizeImportFilenameToken(candidate.Name)
	}
	if token == "" {
		fingerprint := candidate.fingerprint
		if len(fingerprint) > 10 {
			fingerprint = fingerprint[:10]
		}
		token = fmt.Sprintf("import-%03d-%s", index+1, strings.ToLower(fingerprint))
	}
	return "codex-" + token + ".json"
}

func sanitizeImportFilenameToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastSeparator := false
	for _, character := range value {
		isASCIIAlpha := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		if isASCIIAlpha || isDigit {
			builder.WriteRune(character)
			lastSeparator = false
			continue
		}
		if !lastSeparator && builder.Len() > 0 {
			builder.WriteByte('_')
			lastSeparator = true
		}
		if builder.Len() >= 72 {
			break
		}
	}
	return strings.Trim(builder.String(), "_")
}

func reserveImportTargetName(baseName string, reserved map[string]struct{}) (string, bool) {
	baseName = strings.TrimSpace(filepath.Base(baseName))
	if baseName == "" || baseName == "." || !strings.HasSuffix(strings.ToLower(baseName), ".json") {
		baseName = "codex-import.json"
	}
	stem := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	candidate := baseName
	for suffix := 2; ; suffix++ {
		key := strings.ToLower(candidate)
		if _, exists := reserved[key]; !exists {
			reserved[key] = struct{}{}
			return candidate, candidate != baseName
		}
		candidate = fmt.Sprintf("%s-%d.json", stem, suffix)
	}
}

func clonePublicImportSkips(items []importSkipped) []importSkipped {
	if len(items) == 0 {
		return nil
	}
	limit := len(items)
	if limit > maxPublicImportSkips {
		limit = maxPublicImportSkips
	}
	return append([]importSkipped(nil), items[:limit]...)
}

func cloneImportPreview(preview ImportPreview) ImportPreview {
	clone := preview
	clone.Warnings = append([]string(nil), preview.Warnings...)
	clone.SkippedItems = append([]importSkipped(nil), preview.SkippedItems...)
	clone.Items = make([]ImportPreviewItem, len(preview.Items))
	for index, item := range preview.Items {
		clone.Items[index] = item
		clone.Items[index].Warnings = append([]string(nil), item.Warnings...)
	}
	return clone
}

func cloneStoredImportPreview(preview storedImportPreview) storedImportPreview {
	clone := storedImportPreview{Public: cloneImportPreview(preview.Public), Items: make([]storedImportItem, len(preview.Items))}
	for index, item := range preview.Items {
		clone.Items[index] = storedImportItem{
			Public:   cloneImportPreviewItem(item.Public),
			AuthJSON: append(json.RawMessage(nil), item.AuthJSON...),
		}
	}
	return clone
}

func cloneImportPreviewItem(item ImportPreviewItem) ImportPreviewItem {
	clone := item
	clone.Warnings = append([]string(nil), item.Warnings...)
	return clone
}

func clearStoredImportPreview(preview *storedImportPreview) {
	if preview == nil {
		return
	}
	for index := range preview.Items {
		clear(preview.Items[index].AuthJSON)
		preview.Items[index].AuthJSON = nil
	}
	preview.Items = nil
}

func (s *importPreviewStore) put(preview storedImportPreview) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(preview.Public.CreatedAt)
	if len(s.entries) >= maxPreviewEntries {
		ids := make([]string, 0, len(s.entries))
		for id := range s.entries {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			return s.entries[ids[i]].Public.CreatedAt.Before(s.entries[ids[j]].Public.CreatedAt)
		})
		if len(ids) > 0 {
			s.deleteLocked(ids[0])
		}
	}
	id := preview.Public.ID
	s.deleteLocked(id)
	s.entries[id] = cloneStoredImportPreview(preview)
	delay := s.ttl
	if delay <= 0 {
		delay = defaultPreviewTTL
	}
	s.timers[id] = time.AfterFunc(delay, func() {
		s.expire(id)
	})
	clearStoredImportPreview(&preview)
}

func (s *importPreviewStore) validate(id string, now time.Time) error {
	if id == "" {
		return ErrImportPreviewNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preview, exists := s.entries[id]
	if !exists {
		return ErrImportPreviewNotFound
	}
	if !now.Before(preview.Public.ExpiresAt) {
		s.deleteLocked(id)
		return ErrImportPreviewExpired
	}
	return nil
}

func (s *importPreviewStore) take(id string, now time.Time) (storedImportPreview, error) {
	if id == "" {
		return storedImportPreview{}, ErrImportPreviewNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	preview, exists := s.entries[id]
	if !exists {
		return storedImportPreview{}, ErrImportPreviewNotFound
	}
	if !now.Before(preview.Public.ExpiresAt) {
		s.deleteLocked(id)
		return storedImportPreview{}, ErrImportPreviewExpired
	}
	if timer := s.timers[id]; timer != nil {
		timer.Stop()
		delete(s.timers, id)
	}
	delete(s.entries, id)
	return preview, nil
}

func (s *importPreviewStore) clear() {
	s.mu.Lock()
	for id := range s.entries {
		s.deleteLocked(id)
	}
	for id, timer := range s.timers {
		timer.Stop()
		delete(s.timers, id)
	}
	s.mu.Unlock()
}

func (s *importPreviewStore) purgeExpiredLocked(now time.Time) {
	for id, preview := range s.entries {
		if !now.Before(preview.Public.ExpiresAt) {
			s.deleteLocked(id)
		}
	}
}

func (s *importPreviewStore) deleteLocked(id string) {
	if timer := s.timers[id]; timer != nil {
		timer.Stop()
		delete(s.timers, id)
	}
	preview, exists := s.entries[id]
	if !exists {
		return
	}
	clearStoredImportPreview(&preview)
	delete(s.entries, id)
}

func (s *importPreviewStore) expire(id string) {
	s.mu.Lock()
	preview, exists := s.entries[id]
	if exists {
		clearStoredImportPreview(&preview)
		delete(s.entries, id)
	}
	delete(s.timers, id)
	s.mu.Unlock()
}
