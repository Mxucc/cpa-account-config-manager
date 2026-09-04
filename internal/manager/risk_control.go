package manager

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	riskControlStoreVersion    = 1
	riskControlStoreName       = "risk-control.json"
	riskControlMaxBodyBytes    = 1 << 20
	riskControlMaxTextBytes    = 64 << 10
	riskControlMaxKeywords     = 256
	riskControlMaxKeywordRunes = 128
	riskControlMaxModels       = 128
	riskControlMaxEventsLimit  = 2000
	riskControlMaxHashes       = 4096
)

type RiskControlMode string

const (
	RiskControlModeOff      RiskControlMode = "off"
	RiskControlModeObserve  RiskControlMode = "observe"
	RiskControlModePreBlock RiskControlMode = "pre_block"
)

type RiskControlModelFilterMode string

const (
	RiskControlModelFilterAll     RiskControlModelFilterMode = "all"
	RiskControlModelFilterInclude RiskControlModelFilterMode = "include"
	RiskControlModelFilterExclude RiskControlModelFilterMode = "exclude"
)

type RiskControlModelFilter struct {
	Mode   RiskControlModelFilterMode `json:"mode"`
	Models []string                   `json:"models,omitempty"`
}

type RiskControlConfig struct {
	Enabled             bool                    `json:"enabled"`
	Mode                RiskControlMode         `json:"mode"`
	BlockedKeywords     []string                `json:"blocked_keywords"`
	ModelFilter         RiskControlModelFilter  `json:"model_filter"`
	PreHashCheckEnabled bool                    `json:"pre_hash_check_enabled"`
	BlockStatus         int                     `json:"block_status"`
	BlockMessage        string                  `json:"block_message"`
	EventRetentionDays  int                     `json:"event_retention_days"`
	MaxEvents           int                     `json:"max_events"`
	PromptAudit         RiskExternalAuditConfig `json:"prompt_audit"`
	CustomAudit         RiskCustomAuditConfig   `json:"custom_audit"`
}

type RiskControlEvent struct {
	ID           string    `json:"id"`
	Time         time.Time `json:"time"`
	Action       string    `json:"action"`
	AccountRef   string    `json:"account_ref,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Model        string    `json:"model,omitempty"`
	Format       string    `json:"format,omitempty"`
	MatchedRules []string  `json:"matched_rules,omitempty"`
	InputHash    string    `json:"input_hash"`
	LatencyMS    int64     `json:"latency_ms"`
	Module       string    `json:"module,omitempty"`
	Decision     string    `json:"decision,omitempty"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	RiskLevel    string    `json:"risk_level,omitempty"`
	Confidence   float64   `json:"confidence,omitempty"`
}

type RiskControlStatus struct {
	Active           bool                  `json:"active"`
	Mode             RiskControlMode       `json:"mode"`
	TotalEvents      int                   `json:"total_events"`
	Observed         int                   `json:"observed"`
	Blocked          int                   `json:"blocked"`
	KeywordHits      int                   `json:"keyword_hits"`
	HashHits         int                   `json:"hash_hits"`
	RememberedHashes int                   `json:"remembered_hashes"`
	LastEventAt      *time.Time            `json:"last_event_at,omitempty"`
	PromptAudit      RiskAuditModuleStatus `json:"prompt_audit"`
	CustomAudit      RiskAuditModuleStatus `json:"custom_audit"`
}

type RiskControlSnapshot struct {
	Config       RiskControlConfig  `json:"config"`
	Status       RiskControlStatus  `json:"status"`
	Events       []RiskControlEvent `json:"events"`
	StorageError string             `json:"storage_error,omitempty"`
}

type persistedRiskControl struct {
	Version int                `json:"version"`
	Config  RiskControlConfig  `json:"config"`
	Events  []RiskControlEvent `json:"events,omitempty"`
	Hashes  []string           `json:"hashes,omitempty"`
}

type RiskControlService struct {
	mu           sync.RWMutex
	dataDir      string
	store        string
	config       RiskControlConfig
	events       []RiskControlEvent
	hashes       map[string]struct{}
	storageError string
	now          func() time.Time
	audit        *riskAuditRuntime
}

func NewRiskControlService() *RiskControlService {
	return &RiskControlService{
		config: defaultRiskControlConfig(),
		hashes: make(map[string]struct{}),
		now:    time.Now,
		audit:  newRiskAuditRuntime(nil),
	}
}

func defaultRiskControlConfig() RiskControlConfig {
	return RiskControlConfig{
		Mode:                RiskControlModeOff,
		ModelFilter:         RiskControlModelFilter{Mode: RiskControlModelFilterAll},
		PreHashCheckEnabled: true,
		BlockStatus:         http.StatusForbidden,
		BlockMessage:        "request blocked by the configured risk-control policy",
		EventRetentionDays:  30,
		MaxEvents:           500,
		PromptAudit:         defaultPromptAuditConfig(),
		CustomAudit:         defaultCustomAuditConfig(),
	}
}

func (s *RiskControlService) Configure(config Config) {
	if s == nil {
		return
	}
	dataDir := normalizeConfig(config).DataDir
	store := filepath.Join(dataDir, riskControlStoreName)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == store {
		return
	}
	s.dataDir = dataDir
	s.store = store
	s.config = defaultRiskControlConfig()
	s.events = nil
	s.hashes = make(map[string]struct{})
	s.storageError = ""
	raw, err := os.ReadFile(store)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.storageError = "risk-control state is unavailable"
		return
	}
	var persisted persistedRiskControl
	if err := json.Unmarshal(raw, &persisted); err != nil || persisted.Version != riskControlStoreVersion {
		s.storageError = "risk-control state is invalid"
		return
	}
	normalized, err := normalizeRiskControlConfig(persisted.Config)
	if err != nil {
		s.storageError = "risk-control configuration is invalid"
		return
	}
	s.config = normalized
	s.events = append([]RiskControlEvent(nil), persisted.Events...)
	for _, hash := range persisted.Hashes {
		if len(s.hashes) >= riskControlMaxHashes {
			break
		}
		if validRiskHash(hash) {
			s.hashes[hash] = struct{}{}
		}
	}
	s.pruneLocked(s.now().UTC())
}

func (s *RiskControlService) RequestInterceptionActive() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Enabled && s.config.Mode != RiskControlModeOff ||
		s.config.PromptAudit.Enabled && s.config.PromptAudit.Mode != RiskControlModeOff ||
		s.config.CustomAudit.Enabled && s.config.CustomAudit.Mode != RiskControlModeOff
}

func (s *RiskControlService) RequestInterceptionAcceptsFormat(string) bool {
	return s.RequestInterceptionActive()
}

func (s *RiskControlService) InterceptRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	if s == nil || len(request.Body) == 0 || len(request.Body) > riskControlMaxBodyBytes {
		return cpaapi.RequestInterceptResponse{}, false
	}
	if response, changed := s.interceptContentRequest(request); changed || response.Terminate {
		return response, changed
	}
	text, bodyModel, ok := extractRiskControlText(request.Body)
	if !ok || strings.TrimSpace(text) == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	model := riskFirstNonEmpty(request.Model, request.RequestedModel, bodyModel)
	format := safeRiskControlLabel(riskFirstNonEmpty(request.ToFormat, request.SourceFormat), 32)
	account := riskControlAccountRef(request.Metadata)
	digest := sha256.Sum256([]byte(normalizeRiskText(text)))
	inputHash := hex.EncodeToString(digest[:])

	s.mu.RLock()
	promptConfig := s.config.PromptAudit
	customConfig := s.config.CustomAudit
	s.mu.RUnlock()
	modules := []riskAuditTask{}
	if promptConfig.Enabled && promptConfig.Mode != RiskControlModeOff {
		promptText := text
		if promptConfig.LatestTurnOnly {
			if latest, found := latestRiskControlTurn(request.Body); found {
				promptText = latest
			}
		}
		modules = append(modules, riskAuditTask{module: "prompt_audit", config: promptConfig, text: promptText, model: model, format: format, account: account, hash: inputHash})
	}
	if customConfig.Enabled && customConfig.Mode != RiskControlModeOff {
		customCopy := customConfig
		modules = append(modules, riskAuditTask{module: "custom_audit", config: customCopy.RiskExternalAuditConfig, custom: &customCopy, text: text, model: model, format: format, account: account, hash: inputHash})
	}
	for _, task := range modules {
		if task.config.Mode == RiskControlModeObserve {
			s.enqueueAudit(task)
			continue
		}
		if response, changed := s.processAudit(task); changed {
			return response, true
		}
	}
	return cpaapi.RequestInterceptResponse{}, false
}

func (s *RiskControlService) interceptContentRequest(request cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	started := time.Now()
	if s == nil || len(request.Body) == 0 || len(request.Body) > riskControlMaxBodyBytes {
		return cpaapi.RequestInterceptResponse{}, false
	}
	text, bodyModel, ok := extractRiskControlText(request.Body)
	if !ok {
		return cpaapi.RequestInterceptResponse{}, false
	}
	normalizedText := normalizeRiskText(text)
	if normalizedText == "" {
		return cpaapi.RequestInterceptResponse{}, false
	}
	model := riskFirstNonEmpty(request.Model, request.RequestedModel, bodyModel)
	inputDigest := sha256.Sum256([]byte(normalizedText))
	inputHash := hex.EncodeToString(inputDigest[:])
	now := s.now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.Enabled || s.config.Mode == RiskControlModeOff || !riskControlModelSelected(s.config.ModelFilter, model) {
		return cpaapi.RequestInterceptResponse{}, false
	}
	s.pruneLocked(now)
	action := ""
	matchedRules := []string(nil)
	if s.config.PreHashCheckEnabled {
		if _, exists := s.hashes[inputHash]; exists {
			if s.config.Mode == RiskControlModePreBlock {
				action = "hash_block"
			} else {
				action = "hash_observe"
			}
		}
	}
	if action == "" {
		folded := strings.ToLower(normalizedText)
		for _, keyword := range s.config.BlockedKeywords {
			if strings.Contains(folded, strings.ToLower(keyword)) {
				matchedRules = append(matchedRules, riskRuleReference(keyword))
			}
		}
		if len(matchedRules) == 0 {
			return cpaapi.RequestInterceptResponse{}, false
		}
		if s.config.Mode == RiskControlModePreBlock {
			action = "keyword_block"
		} else {
			action = "keyword_observe"
		}
		if s.config.PreHashCheckEnabled && len(s.hashes) < riskControlMaxHashes {
			s.hashes[inputHash] = struct{}{}
		}
	}
	event := RiskControlEvent{
		ID:           newRiskControlEventID(now),
		Time:         now,
		Action:       action,
		AccountRef:   riskControlAccountRef(request.Metadata),
		Provider:     riskControlMetadataLabel(request.Metadata, "provider", "type", "account_type"),
		Model:        safeRiskControlLabel(model, 96),
		Format:       safeRiskControlLabel(riskFirstNonEmpty(request.ToFormat, request.SourceFormat), 32),
		MatchedRules: matchedRules,
		InputHash:    inputHash,
		LatencyMS:    time.Since(started).Milliseconds(),
	}
	s.events = append([]RiskControlEvent{event}, s.events...)
	s.pruneLocked(now)
	if err := s.persistLocked(); err != nil {
		s.storageError = "risk-control state could not be saved"
	} else {
		s.storageError = ""
	}
	if s.config.Mode != RiskControlModePreBlock {
		return cpaapi.RequestInterceptResponse{}, false
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":    "risk_control_blocked",
		"message": s.config.BlockMessage,
		"action":  action,
	}})
	return cpaapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      s.config.BlockStatus,
		ResponseHeaders: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		ResponseBody:    body,
	}, true
}

func (s *RiskControlService) Snapshot() RiskControlSnapshot {
	if s == nil {
		return RiskControlSnapshot{Config: defaultRiskControlConfig()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now().UTC())
	events := append([]RiskControlEvent(nil), s.events...)
	config := cloneRiskControlConfig(s.config)
	status := RiskControlStatus{Active: config.Enabled && config.Mode != RiskControlModeOff || config.PromptAudit.Enabled && config.PromptAudit.Mode != RiskControlModeOff || config.CustomAudit.Enabled && config.CustomAudit.Mode != RiskControlModeOff, Mode: config.Mode, TotalEvents: len(events), RememberedHashes: len(s.hashes)}
	status.PromptAudit = s.auditStatus(config.PromptAudit, "prompt_audit")
	status.CustomAudit = s.auditStatus(config.CustomAudit.RiskExternalAuditConfig, "custom_audit")
	for _, event := range events {
		if strings.HasSuffix(event.Action, "_block") || event.Action == "error_block" {
			status.Blocked++
		} else {
			status.Observed++
		}
		if strings.HasPrefix(event.Action, "keyword_") {
			status.KeywordHits++
		}
		if strings.HasPrefix(event.Action, "hash_") {
			status.HashHits++
		}
	}
	if len(events) > 0 {
		value := events[0].Time
		status.LastEventAt = &value
	}
	return RiskControlSnapshot{Config: config, Status: status, Events: events, StorageError: s.storageError}
}

func (s *RiskControlService) UpdateConfig(config RiskControlConfig) (RiskControlSnapshot, error) {
	if s == nil {
		return RiskControlSnapshot{}, fmt.Errorf("risk-control service is unavailable")
	}
	normalized, err := normalizeRiskControlConfig(config)
	if err != nil {
		return RiskControlSnapshot{}, err
	}
	s.mu.Lock()
	previous := s.config
	previousEvents := append([]RiskControlEvent(nil), s.events...)
	s.config = normalized
	s.pruneLocked(s.now().UTC())
	if err := s.persistLocked(); err != nil {
		s.config = previous
		s.events = previousEvents
		s.storageError = "risk-control configuration could not be saved"
		s.mu.Unlock()
		return RiskControlSnapshot{}, fmt.Errorf("save risk-control configuration: %w", err)
	}
	s.storageError = ""
	s.mu.Unlock()
	return s.Snapshot(), nil
}

func (s *RiskControlService) ClearEvents() (RiskControlSnapshot, error) {
	return s.clear(false)
}

func (s *RiskControlService) ClearHashes() (RiskControlSnapshot, error) {
	return s.clear(true)
}

func (s *RiskControlService) clear(hashes bool) (RiskControlSnapshot, error) {
	if s == nil {
		return RiskControlSnapshot{}, fmt.Errorf("risk-control service is unavailable")
	}
	s.mu.Lock()
	oldEvents := s.events
	oldHashes := s.hashes
	if hashes {
		s.hashes = make(map[string]struct{})
	} else {
		s.events = nil
	}
	if err := s.persistLocked(); err != nil {
		s.events = oldEvents
		s.hashes = oldHashes
		s.storageError = "risk-control state could not be saved"
		s.mu.Unlock()
		return RiskControlSnapshot{}, fmt.Errorf("save risk-control state: %w", err)
	}
	s.storageError = ""
	s.mu.Unlock()
	return s.Snapshot(), nil
}

func normalizeRiskControlConfig(config RiskControlConfig) (RiskControlConfig, error) {
	defaults := defaultRiskControlConfig()
	switch config.Mode {
	case "":
		config.Mode = defaults.Mode
	case RiskControlModeOff, RiskControlModeObserve, RiskControlModePreBlock:
	default:
		return RiskControlConfig{}, fmt.Errorf("mode must be off, observe, or pre_block")
	}
	switch config.ModelFilter.Mode {
	case "":
		config.ModelFilter.Mode = RiskControlModelFilterAll
	case RiskControlModelFilterAll, RiskControlModelFilterInclude, RiskControlModelFilterExclude:
	default:
		return RiskControlConfig{}, fmt.Errorf("model_filter.mode must be all, include, or exclude")
	}
	keywords, err := normalizeRiskStringList(config.BlockedKeywords, riskControlMaxKeywords, riskControlMaxKeywordRunes, "blocked_keywords")
	if err != nil {
		return RiskControlConfig{}, err
	}
	models, err := normalizeRiskStringList(config.ModelFilter.Models, riskControlMaxModels, 96, "model_filter.models")
	if err != nil {
		return RiskControlConfig{}, err
	}
	config.BlockedKeywords = keywords
	config.ModelFilter.Models = models
	if config.BlockStatus == 0 {
		config.BlockStatus = defaults.BlockStatus
	}
	if config.BlockStatus < 400 || config.BlockStatus > 499 {
		return RiskControlConfig{}, fmt.Errorf("block_status must be between 400 and 499")
	}
	config.BlockMessage = strings.TrimSpace(config.BlockMessage)
	if config.BlockMessage == "" {
		config.BlockMessage = defaults.BlockMessage
	}
	if utf8.RuneCountInString(config.BlockMessage) > 256 {
		return RiskControlConfig{}, fmt.Errorf("block_message must be 256 characters or fewer")
	}
	if config.EventRetentionDays == 0 {
		config.EventRetentionDays = defaults.EventRetentionDays
	}
	if config.EventRetentionDays < 1 || config.EventRetentionDays > 365 {
		return RiskControlConfig{}, fmt.Errorf("event_retention_days must be between 1 and 365")
	}
	if config.MaxEvents == 0 {
		config.MaxEvents = defaults.MaxEvents
	}
	if config.MaxEvents < 1 || config.MaxEvents > riskControlMaxEventsLimit {
		return RiskControlConfig{}, fmt.Errorf("max_events must be between 1 and 2000")
	}
	promptAudit, err := normalizeExternalAuditConfig(config.PromptAudit, defaults.PromptAudit, "prompt_audit")
	if err != nil {
		return RiskControlConfig{}, err
	}
	customAudit, err := normalizeCustomAuditConfig(config.CustomAudit)
	if err != nil {
		return RiskControlConfig{}, err
	}
	config.PromptAudit = promptAudit
	config.CustomAudit = customAudit
	return config, nil
}

func normalizeRiskStringList(values []string, maxItems, maxRunes int, field string) ([]string, error) {
	if len(values) > maxItems {
		return nil, fmt.Errorf("%s supports at most %d entries", field, maxItems)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if value == "" {
			continue
		}
		if utf8.RuneCountInString(value) > maxRunes {
			return nil, fmt.Errorf("%s entries must be %d characters or fewer", field, maxRunes)
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func cloneRiskControlConfig(config RiskControlConfig) RiskControlConfig {
	config.BlockedKeywords = append([]string(nil), config.BlockedKeywords...)
	config.ModelFilter.Models = append([]string(nil), config.ModelFilter.Models...)
	config.PromptAudit.Scanners = append([]string(nil), config.PromptAudit.Scanners...)
	config.CustomAudit.Scanners = append([]string(nil), config.CustomAudit.Scanners...)
	return config
}

func (s *RiskControlService) pruneLocked(now time.Time) {
	cutoff := now.Add(-time.Duration(s.config.EventRetentionDays) * 24 * time.Hour)
	filtered := s.events[:0]
	for _, event := range s.events {
		if !event.Time.Before(cutoff) && validRiskHash(event.InputHash) {
			filtered = append(filtered, event)
		}
	}
	s.events = filtered
	if len(s.events) > s.config.MaxEvents {
		s.events = s.events[:s.config.MaxEvents]
	}
}

func (s *RiskControlService) persistLocked() error {
	if strings.TrimSpace(s.store) == "" {
		return fmt.Errorf("risk-control store is not configured")
	}
	hashes := make([]string, 0, len(s.hashes))
	for hash := range s.hashes {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	return savePrivateJSON(s.store, persistedRiskControl{Version: riskControlStoreVersion, Config: cloneRiskControlConfig(s.config), Events: append([]RiskControlEvent(nil), s.events...), Hashes: hashes})
}

func riskControlModelSelected(filter RiskControlModelFilter, model string) bool {
	if filter.Mode == RiskControlModelFilterAll {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	matched := false
	for _, candidate := range filter.Models {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			matched = true
			break
		}
	}
	if filter.Mode == RiskControlModelFilterInclude {
		return matched
	}
	return !matched
}

func extractRiskControlText(raw []byte) (string, string, bool) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", "", false
	}
	collector := &riskTextCollector{remaining: riskControlMaxTextBytes}
	collector.addValue(root["instructions"])
	collector.addValue(root["prompt"])
	collector.addInput(root["input"])
	collector.addMessages(root["messages"])
	collector.addValue(root["system"])
	collector.addGeminiContents(root["contents"])
	model, _ := root["model"].(string)
	text := strings.Join(collector.parts, "\n")
	return text, model, strings.TrimSpace(text) != ""
}

type riskTextCollector struct {
	parts     []string
	remaining int
}

func (c *riskTextCollector) addText(value string) {
	if c.remaining <= 0 {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if len(value) > c.remaining {
		value = string([]byte(value)[:c.remaining])
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	if value != "" {
		c.parts = append(c.parts, value)
		c.remaining -= len(value)
	}
}

func (c *riskTextCollector) addValue(value any) {
	switch typed := value.(type) {
	case string:
		c.addText(typed)
	case []any:
		for _, item := range typed {
			c.addValue(item)
		}
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			c.addText(text)
		}
		if text, ok := typed["input_text"].(string); ok {
			c.addText(text)
		}
		if content, exists := typed["content"]; exists {
			c.addValue(content)
		}
	}
}

func (c *riskTextCollector) addInput(value any) {
	c.addValue(value)
}

func (c *riskTextCollector) addMessages(value any) {
	messages, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c.addValue(message["content"])
	}
}

func (c *riskTextCollector) addGeminiContents(value any) {
	contents, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parts, _ := content["parts"].([]any)
		for _, partValue := range parts {
			part, ok := partValue.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				c.addText(text)
			}
		}
	}
}

func normalizeRiskText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func riskRuleReference(keyword string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(keyword))))
	return "kw:" + hex.EncodeToString(digest[:6])
}

func riskControlAccountRef(metadata map[string]any) string {
	identifier := requestAuthIdentifier(metadata)
	if identifier == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(identifier))
	return "acct:" + hex.EncodeToString(digest[:12])
}

func riskControlMetadataLabel(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok {
			if safe := safeRiskControlLabel(value, 64); safe != "" {
				return safe
			}
		}
	}
	return ""
}

func safeRiskControlLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_./:+", r) {
			continue
		}
		return ""
	}
	return value
}

func validRiskHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func newRiskControlEventID(now time.Time) string {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err == nil {
		return fmt.Sprintf("risk-%d-%s", now.UnixMilli(), hex.EncodeToString(random))
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d", now.UnixNano())))
	return "risk-" + hex.EncodeToString(digest[:10])
}

func riskFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
