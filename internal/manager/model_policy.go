package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	ModelPolicyModeAll       = "all"
	ModelPolicyModeAllowOnly = "allow_only"
	ModelPolicyModeDenyOnly  = "deny_only"

	modelPolicySchema      = 1
	maxModelPolicyModels   = 1000
	maxModelIdentifierLen  = 256
	maxModelCatalogTargets = 10000
)

type ModelPolicyPatch struct {
	Mode   string   `json:"mode"`
	Models []string `json:"models,omitempty"`
}

type AccountModelPolicySummary struct {
	Mode          string   `json:"mode"`
	Models        []string `json:"models,omitempty"`
	ExcludedCount int      `json:"excluded_count"`
}

type storedModelPolicy struct {
	Schema                int      `json:"schema"`
	Mode                  string   `json:"mode"`
	Models                []string `json:"models,omitempty"`
	ManagedExcludedModels []string `json:"managed_excluded_models,omitempty"`
	BaseExcludedModels    []string `json:"base_excluded_models,omitempty"`
}

type AccountModelOption struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Type        string `json:"type,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
}

type AccountModelCatalogRequest struct {
	Scope TargetScope `json:"scope"`
}

type AccountModelCatalogResponse struct {
	Models        []AccountModelOption       `json:"models"`
	CurrentPolicy *AccountModelPolicySummary `json:"current_policy,omitempty"`
	Total         int                        `json:"total"`
	Eligible      int                        `json:"eligible"`
	Loaded        int                        `json:"loaded"`
	Failed        int                        `json:"failed"`
	ReadOnly      int                        `json:"read_only"`
	Missing       int                        `json:"missing"`
	Warnings      []string                   `json:"warnings,omitempty"`
}

type currentAuthDocument struct {
	Revision string
	Metadata map[string]any
}

type ManagementModelCatalog interface {
	GetAuthFileModels(context.Context, string) ([]AccountModelOption, error)
}

func (patch ModelPolicyPatch) Validate() (ModelPolicyPatch, error) {
	patch.Mode = strings.ToLower(strings.TrimSpace(patch.Mode))
	switch patch.Mode {
	case ModelPolicyModeAll:
		if len(patch.Models) > 0 {
			return ModelPolicyPatch{}, fmt.Errorf("all-model policy cannot include selected models")
		}
		patch.Models = nil
	case ModelPolicyModeAllowOnly, ModelPolicyModeDenyOnly:
		models, errModels := normalizeModelIdentifiers(patch.Models)
		if errModels != nil {
			return ModelPolicyPatch{}, errModels
		}
		if len(models) == 0 {
			return ModelPolicyPatch{}, fmt.Errorf("model policy requires at least one selected model")
		}
		patch.Models = models
	default:
		return ModelPolicyPatch{}, fmt.Errorf("model policy mode must be all, allow_only, or deny_only")
	}
	return patch, nil
}

func normalizeModelIdentifiers(input []string) ([]string, error) {
	if len(input) > maxModelPolicyModels {
		return nil, fmt.Errorf("model policy exceeds %d models", maxModelPolicyModels)
	}
	byKey := make(map[string]string, len(input))
	for _, raw := range input {
		model := strings.TrimSpace(raw)
		if !validModelIdentifier(model) {
			return nil, fmt.Errorf("model identifier is invalid")
		}
		key := strings.ToLower(model)
		if _, exists := byKey[key]; !exists {
			byKey[key] = model
		}
	}
	models := make([]string, 0, len(byKey))
	for _, model := range byKey {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i]) < strings.ToLower(models[j]) })
	return models, nil
}

func validModelIdentifier(value string) bool {
	if value == "" || len(value) > maxModelIdentifierLen {
		return false
	}
	for index, char := range value {
		if char > 127 {
			return false
		}
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && strings.ContainsRune("-._:/+@", char) {
			continue
		}
		return false
	}
	return true
}

func modelPolicySummary(metadata map[string]any) *AccountModelPolicySummary {
	policy, ok := readStoredModelPolicy(metadata)
	if !ok {
		return nil
	}
	return &AccountModelPolicySummary{
		Mode:          policy.Mode,
		Models:        append([]string(nil), policy.Models...),
		ExcludedCount: len(policy.ManagedExcludedModels),
	}
}

func readStoredModelPolicy(metadata map[string]any) (storedModelPolicy, bool) {
	root, ok := metadata["cpa_account_config_manager"].(map[string]any)
	if !ok {
		return storedModelPolicy{}, false
	}
	raw, ok := root["model_policy"]
	if !ok {
		return storedModelPolicy{}, false
	}
	encoded, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return storedModelPolicy{}, false
	}
	var policy storedModelPolicy
	if errUnmarshal := json.Unmarshal(encoded, &policy); errUnmarshal != nil || policy.Schema != modelPolicySchema {
		return storedModelPolicy{}, false
	}
	normalized, errPatch := (ModelPolicyPatch{Mode: policy.Mode, Models: policy.Models}).Validate()
	if errPatch != nil {
		return storedModelPolicy{}, false
	}
	policy.Mode = normalized.Mode
	policy.Models = normalized.Models
	policy.ManagedExcludedModels = safeStoredModelIdentifiers(policy.ManagedExcludedModels)
	policy.BaseExcludedModels = safeStoredModelIdentifiers(policy.BaseExcludedModels)
	return policy, true
}

func safeStoredModelIdentifiers(input []string) []string {
	models, errModels := normalizeModelIdentifiers(input)
	if errModels != nil {
		return nil
	}
	return models
}

func stringListMetadata(metadata map[string]any, key string) []string {
	values, ok := metadata[key].([]any)
	if !ok {
		if strings, okStrings := metadata[key].([]string); okStrings {
			return safeStoredModelIdentifiers(strings)
		}
		return nil
	}
	models := make([]string, 0, len(values))
	for _, value := range values {
		if model, okModel := value.(string); okModel {
			models = append(models, model)
		}
	}
	return safeStoredModelIdentifiers(models)
}

func resolveModelPolicyFields(metadata map[string]any, patch ModelPolicyPatch, catalog []AccountModelOption) (map[string]any, error) {
	validated, errPatch := patch.Validate()
	if errPatch != nil {
		return nil, errPatch
	}
	currentExcluded := stringListMetadata(metadata, "excluded_models")
	previous, hasPrevious := readStoredModelPolicy(metadata)
	base := append([]string(nil), currentExcluded...)
	if hasPrevious {
		base = unionModelIdentifiers(previous.BaseExcludedModels, subtractModelIdentifiers(currentExcluded, previous.ManagedExcludedModels))
	}

	known := make([]string, 0, len(catalog)+len(previous.Models)+len(previous.ManagedExcludedModels))
	for _, option := range catalog {
		known = append(known, option.ID)
	}
	known = unionModelIdentifiers(known, previous.Models, previous.ManagedExcludedModels)
	selected := validated.Models
	if validated.Mode != ModelPolicyModeAll {
		if len(known) == 0 {
			return nil, fmt.Errorf("account model catalog is empty")
		}
		knownSet := modelIdentifierSet(known)
		for _, model := range selected {
			if _, exists := knownSet[strings.ToLower(model)]; !exists {
				return nil, fmt.Errorf("selected model is unavailable for this account")
			}
		}
	}

	managed := []string(nil)
	switch validated.Mode {
	case ModelPolicyModeAllowOnly:
		managed = subtractModelIdentifiers(known, selected)
	case ModelPolicyModeDenyOnly:
		managed = append([]string(nil), selected...)
	}
	persisted := storedModelPolicy{
		Schema:                modelPolicySchema,
		Mode:                  validated.Mode,
		Models:                append([]string(nil), selected...),
		ManagedExcludedModels: managed,
		BaseExcludedModels:    base,
	}
	return map[string]any{
		"excluded_models":                         unionModelIdentifiers(base, managed),
		"cpa_account_config_manager.model_policy": persisted,
	}, nil
}

func modelIdentifierSet(models []string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, model := range models {
		set[strings.ToLower(model)] = struct{}{}
	}
	return set
}

func unionModelIdentifiers(groups ...[]string) []string {
	values := make([]string, 0)
	for _, group := range groups {
		values = append(values, group...)
	}
	return safeStoredModelIdentifiers(values)
}

func subtractModelIdentifiers(models, remove []string) []string {
	removed := modelIdentifierSet(remove)
	result := make([]string, 0, len(models))
	for _, model := range models {
		if _, exists := removed[strings.ToLower(model)]; !exists {
			result = append(result, model)
		}
	}
	return safeStoredModelIdentifiers(result)
}

func mergeAccountModelCatalog(catalog []AccountModelOption, metadata map[string]any) []AccountModelOption {
	byID := make(map[string]AccountModelOption, len(catalog))
	for _, option := range catalog {
		if sanitized, ok := sanitizeAccountModelOption(option); ok {
			byID[strings.ToLower(sanitized.ID)] = sanitized
		}
	}
	if policy, ok := readStoredModelPolicy(metadata); ok {
		for _, model := range unionModelIdentifiers(policy.Models, policy.ManagedExcludedModels) {
			key := strings.ToLower(model)
			if _, exists := byID[key]; !exists {
				byID[key] = AccountModelOption{ID: model}
			}
		}
	}
	models := make([]AccountModelOption, 0, len(byID))
	for _, option := range byID {
		models = append(models, option)
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID) })
	return models
}

func sanitizeAccountModelOption(option AccountModelOption) (AccountModelOption, bool) {
	option.ID = strings.TrimSpace(option.ID)
	if !validModelIdentifier(option.ID) {
		return AccountModelOption{}, false
	}
	option.DisplayName = safeModelLabel(option.DisplayName)
	option.Type = safeModelLabel(option.Type)
	option.OwnedBy = safeModelLabel(option.OwnedBy)
	return option, true
}

func safeModelLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 || hasUnsafeControl(value, true) {
		return ""
	}
	return value
}

func commonAccountModels(catalogs [][]AccountModelOption) []AccountModelOption {
	if len(catalogs) == 0 {
		return []AccountModelOption{}
	}
	common := make(map[string]AccountModelOption, len(catalogs[0]))
	for _, option := range catalogs[0] {
		common[strings.ToLower(option.ID)] = option
	}
	for _, catalog := range catalogs[1:] {
		present := make(map[string]struct{}, len(catalog))
		for _, option := range catalog {
			present[strings.ToLower(option.ID)] = struct{}{}
		}
		for key := range common {
			if _, exists := present[key]; !exists {
				delete(common, key)
			}
		}
	}
	models := make([]AccountModelOption, 0, len(common))
	for _, option := range common {
		models = append(models, option)
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID) })
	return models
}

func loadCommonAccountModels(ctx context.Context, accounts *AccountService, targets []Account, client ManagementModelCatalog, workers int) ([][]AccountModelOption, int) {
	if workers < 1 {
		workers = 1
	}
	if workers > len(targets) {
		workers = len(targets)
	}
	if workers == 0 {
		return nil, 0
	}
	type result struct {
		models []AccountModelOption
		err    error
	}
	jobs := make(chan Account)
	results := make(chan result, len(targets))
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for account := range jobs {
				document, errDocument := accounts.CurrentAuthDocument(ctx, account)
				if errDocument != nil {
					results <- result{err: errDocument}
					continue
				}
				models, errModels := client.GetAuthFileModels(ctx, account.Name)
				if errModels != nil {
					results <- result{err: errModels}
					continue
				}
				models = mergeAccountModelCatalog(models, document.Metadata)
				if len(models) == 0 {
					results <- result{err: fmt.Errorf("account model catalog is empty")}
					continue
				}
				results <- result{models: models}
			}
		}()
	}
	go func() {
		for _, account := range targets {
			jobs <- account
		}
		close(jobs)
		group.Wait()
		close(results)
	}()
	catalogs := make([][]AccountModelOption, 0, len(targets))
	failed := 0
	for item := range results {
		if item.err != nil {
			failed++
			continue
		}
		catalogs = append(catalogs, item.models)
	}
	return catalogs, failed
}
