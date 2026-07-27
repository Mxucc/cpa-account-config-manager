package manager

import (
	"context"
	"fmt"
)

func (a *App) applyConditionalModelPolicy(ctx context.Context, account Account, patch ModelPolicyPatch, managementKey string) (bool, error) {
	if a == nil || a.accounts == nil {
		return false, fmt.Errorf("account service is unavailable")
	}
	validated, errValidate := patch.Validate()
	if errValidate != nil {
		return false, errValidate
	}
	resolved, errResolve := a.accounts.ResolveTargets(ctx, TargetScope{Mode: "selected", IDs: []string{account.ID}})
	if errResolve != nil || len(resolved.Accounts) != 1 || !resolved.Accounts[0].Editable {
		return false, fmt.Errorf("account is unavailable for model policy")
	}
	account = resolved.Accounts[0]
	document, errDocument := a.accounts.CurrentAuthDocument(ctx, account)
	if errDocument != nil {
		return false, fmt.Errorf("account changed before model policy")
	}
	if modelPolicyMatchesPatch(modelPolicySummary(document.Metadata), validated) {
		return false, nil
	}
	client, errClient := newManagementClient(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
	if errClient != nil {
		return false, fmt.Errorf("management client is unavailable")
	}
	defer client.clearSecrets()
	var catalog []AccountModelOption
	if validated.Mode != ModelPolicyModeAll {
		models, errModels := client.GetAuthFileModels(ctx, account.Name)
		if errModels != nil {
			return false, fmt.Errorf("account model catalog could not be loaded")
		}
		catalog = mergeAccountModelCatalog(models, document.Metadata)
	}
	fields, errFields := resolveModelPolicyFields(document.Metadata, validated, catalog)
	if errFields != nil {
		return false, errFields
	}
	resolvedPatch := BatchPatch{ModelPolicy: &validated, resolvedModelFields: fields}
	if errPatch := client.PatchFields(ctx, account.Name, resolvedPatch); errPatch != nil {
		return false, fmt.Errorf("account model policy update failed")
	}
	return true, nil
}

func modelPolicyMatchesPatch(current *AccountModelPolicySummary, patch ModelPolicyPatch) bool {
	if current == nil || current.Mode != patch.Mode || len(current.Models) != len(patch.Models) {
		return false
	}
	for index := range patch.Models {
		if current.Models[index] != patch.Models[index] {
			return false
		}
	}
	return true
}
