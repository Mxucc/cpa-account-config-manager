package manager

import (
	"context"
	"strings"
	"time"
)

const autoModelWhitelistMutationPrefix = "auto-model-whitelist:"

func (a *App) applyDetectedModelWhitelist(ctx context.Context, accountID string, models []string, config Config, managementKey string) *ModelTestPolicyAdjustment {
	adjustment := &ModelTestPolicyAdjustment{
		Mode: ModelPolicyModeAllowOnly, Models: append([]string(nil), models...), Status: "failed", ReasonCode: "operation_failed",
	}
	validated, errValidate := (ModelPolicyPatch{Mode: ModelPolicyModeAllowOnly, Models: models}).Validate()
	if errValidate != nil || a == nil || a.accounts == nil || a.jobs == nil || a.jobs.mutations == nil {
		return adjustment
	}
	adjustment.Models = append([]string(nil), validated.Models...)
	resolved, errResolve := a.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{accountID}})
	if errResolve != nil || len(resolved.Accounts) != 1 || !resolved.Accounts[0].Editable {
		adjustment.Status = "skipped"
		adjustment.ReasonCode = "account_read_only"
		a.recordAutoModelWhitelist(accountID, adjustment)
		return adjustment
	}
	account := resolved.Accounts[0]
	ownerID, errID := randomIdentifier()
	if errID != nil || !a.jobs.mutations.TryAcquire(autoModelWhitelistMutationPrefix+ownerID) {
		adjustment.Status = "skipped"
		adjustment.ReasonCode = "mutation_busy"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	defer a.jobs.mutations.Release(autoModelWhitelistMutationPrefix + ownerID)

	document, errDocument := a.accounts.CurrentAuthDocument(ctx, account)
	if errDocument != nil {
		adjustment.ReasonCode = "account_changed"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	if existing := modelPolicySummary(document.Metadata); existing != nil && existing.Mode != ModelPolicyModeAll ||
		len(stringListMetadata(document.Metadata, "excluded_models")) > 0 {
		adjustment.Status = "skipped"
		adjustment.ReasonCode = "existing_model_policy"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(config.ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		adjustment.ReasonCode = "management_unavailable"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	defer client.clearSecrets()
	catalog, errCatalog := client.GetAuthFileModels(ctx, account.Name)
	if errCatalog != nil || len(catalog) == 0 {
		adjustment.ReasonCode = "model_catalog_unavailable"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	for _, model := range validated.Models {
		catalog = append(catalog, AccountModelOption{ID: model})
	}
	catalog = mergeAccountModelCatalog(catalog, document.Metadata)
	fields, errFields := resolveModelPolicyFields(document.Metadata, validated, catalog)
	if errFields != nil {
		adjustment.ReasonCode = "model_catalog_unavailable"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	patch := BatchPatch{ModelPolicy: &validated, resolvedModelFields: fields}
	if errPatch := client.PatchFields(ctx, account.Name, patch); errPatch != nil {
		adjustment.ReasonCode = "management_unavailable"
		a.recordAutoModelWhitelist(account.ID, adjustment)
		return adjustment
	}
	adjustment.Status = "applied"
	adjustment.ReasonCode = "model_compatibility_detected"
	a.recordAutoModelWhitelist(account.ID, adjustment)
	return adjustment
}

func (a *App) recordAutoModelWhitelist(accountID string, adjustment *ModelTestPolicyAdjustment) {
	if a == nil || a.operations == nil || adjustment == nil {
		return
	}
	now := time.Now().UTC()
	status := OperationStatusFailed
	succeeded, failed, skipped := 0, 1, 0
	switch strings.ToLower(strings.TrimSpace(adjustment.Status)) {
	case "applied":
		status, succeeded, failed = OperationStatusSucceeded, 1, 0
	case "skipped":
		status, failed, skipped = OperationStatusSkipped, 0, 1
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: OperationActionAutoModelWhitelist, Status: status,
		Source: OperationSourceManual, Scope: OperationScopeSingle, TargetID: accountID, TargetCount: 1,
		Succeeded: succeeded, Failed: failed, Skipped: skipped, StartedAt: now, FinishedAt: now,
		ReasonCode: adjustment.ReasonCode,
	})
}
