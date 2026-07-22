package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestInspectionRemediationSummarySeparatesCurrentStateAndRecommendations(t *testing.T) {
	results := []InspectionResult{
		{ID: "delete", Health: InspectionHealthDeactivated, ReasonCode: "workspace_deactivated", Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDelete, Editable: true},
		{ID: "disable", Recommendation: InspectionRecommendationDisable, Editable: true},
		{ID: "already-disabled", Recommendation: InspectionRecommendationDisable, Editable: true, Disabled: true},
		{ID: "enable", Recommendation: InspectionRecommendationEnable, Editable: true, Disabled: true, OwnedDisable: true},
		{ID: "reauth", Recommendation: InspectionRecommendationReauth, Editable: true},
		{ID: "review", Recommendation: InspectionRecommendationReview},
		{ID: "keep", Recommendation: InspectionRecommendationKeep},
	}
	summary := summarizeInspectionRemediation(results)
	if summary.Actionable != 4 || summary.SuggestedDelete != 1 || summary.SuggestedDisable != 1 ||
		summary.SuggestedEnable != 1 || summary.Reauth != 1 || summary.Review != 1 || summary.Keep != 2 || summary.Handled != 0 ||
		summary.EditableEnabled != 3 || summary.EditableDisabled != 2 {
		t.Fatalf("remediation summary = %#v", summary)
	}
}

func TestAutomaticInspectionUsesCPAStatusAPIWhenManagementCredentialIsArmed(t *testing.T) {
	host := inspectionEditableHost(false)
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		patchCalls++
		if request.Method != http.MethodPatch || request.URL.Path != "/v0/management/auth-files/status" {
			t.Errorf("status request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"disabled":true,"name":"inspection.json"}` && string(body) != `{"name":"inspection.json","disabled":true}` {
			t.Errorf("status payload = %s", body)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	records := map[string]inspectionRecord{
		"inspection-account": {Result: InspectionResult{
			ID: "inspection-account", Name: "inspection.json", Provider: "codex",
			Health: InspectionHealthQuotaLimited, ReasonCode: "quota_exhausted",
			Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationDisable,
			Editable: true, AutoDisableEligible: true, SignalSource: InspectionSignalNative,
		}},
	}
	accounts := map[string]Account{
		"inspection-account": {ID: "inspection-account", Name: "inspection.json", Provider: "codex", Editable: true, path: "/auths/inspection.json"},
	}
	policy := defaultInspectionPolicy()
	policy.AutoDisable = true
	summary, actions := engine.applyAutomaticActions(context.Background(), policy, accounts, records, time.Now().UTC(), server.URL, "management-secret")
	if patchCalls != 1 || summary.AutoDisabled != 1 || summary.Failed != 0 || len(actions) != 1 || actions[0].Status != InspectionActionSucceeded {
		t.Fatalf("automatic action summary=%#v actions=%#v calls=%d", summary, actions, patchCalls)
	}
	if !records["inspection-account"].Result.Disabled || !records["inspection-account"].Result.OwnedDisable {
		t.Fatalf("automatic action did not update inspection state: %#v", records["inspection-account"])
	}
	if len(host.saves) != 0 {
		t.Fatalf("automatic action bypassed CPA status API with %d host saves", len(host.saves))
	}
}

func TestAutomaticInspectionRepairsStaleCPARuntimeDisabledState(t *testing.T) {
	host := inspectionEditableHost(false)
	host.details["inspection-account"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "inspection-account",
		Name:      "inspection.json",
		Path:      "/auths/inspection.json",
		JSON:      json.RawMessage(`{"type":"codex","email":"inspection@example.com","access_token":"account-secret","disabled":true}`),
	}
	accounts := NewAccountService(host)
	listed, errList := accounts.baseAccounts(context.Background())
	if errList != nil || len(listed) != 1 || listed[0].Disabled {
		t.Fatalf("stale CPA account state = %#v error=%v", listed, errList)
	}
	engine := NewInspectionEngine(accounts, host, NewMutationCoordinator())
	outcome, errDisable := engine.setInspectionDisabled(context.Background(), listed[0], inspectionRecord{}, true, nil)
	if errDisable != nil {
		t.Fatalf("repair stale CPA state: %v", errDisable)
	}
	if !outcome.Changed || host.saveCalls["inspection.json"] != 1 || len(host.saves) != 1 {
		t.Fatalf("stale CPA state was not rewritten: outcome=%#v calls=%d saves=%d", outcome, host.saveCalls["inspection.json"], len(host.saves))
	}
}

func TestManualInspectionDeleteRequiresConfirmationAndHighConfidenceRecommendation(t *testing.T) {
	host := editableAccountDeleteHost()
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		deleteCalls++
		if request.Method != http.MethodDelete || request.URL.Path != "/v0/management/auth-files" || request.URL.Query().Get("name") != "operator.json" {
			t.Errorf("delete request = %s %s", request.Method, request.URL.String())
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mutations := NewMutationCoordinator()
	accounts := NewAccountService(host)
	deletions := NewAccountDeleteService(accounts, mutations)
	deletions.doer = server.Client()
	engine := NewInspectionEngine(accounts, host, mutations)
	engine.records["auth-1"] = inspectionRecord{Result: InspectionResult{
		ID: "auth-1", Name: "operator.json", Provider: "codex", Editable: true,
		Health: InspectionHealthInvalidCredentials, ReasonCode: "invalid_credentials",
		Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
		SignalSource: InspectionSignalNative,
	}}

	_, errUnconfirmed := engine.ExecuteManualDeletes(context.Background(), deletions, server.URL, "management-secret", InspectionManualDeleteRequest{AccountIDs: []string{"auth-1"}})
	if !errors.Is(errUnconfirmed, ErrInspectionDeleteConfirmation) || deleteCalls != 0 {
		t.Fatalf("unconfirmed delete error=%v calls=%d", errUnconfirmed, deleteCalls)
	}
	run, errDelete := engine.ExecuteManualDeletes(context.Background(), deletions, server.URL, "management-secret", InspectionManualDeleteRequest{AccountIDs: []string{"auth-1"}, Confirm: true})
	if errDelete != nil || run.Succeeded != 1 || run.Failed != 0 || run.Skipped != 0 || deleteCalls != 1 {
		t.Fatalf("manual delete run=%#v error=%v calls=%d", run, errDelete, deleteCalls)
	}
	actions := engine.Actions(10)
	if len(actions) != 1 || actions[0].Action != InspectionActionDelete || actions[0].Source != OperationSourceManual {
		t.Fatalf("manual delete actions = %#v", actions)
	}
	if _, ok := operationFromInspectionAction(actions[0]); ok {
		t.Fatalf("manual per-account delete action was reconciled into a duplicate operation: %#v", actions[0])
	}
	if len(engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50}).Results) != 0 {
		t.Fatal("deleted inspection result remained in the active result set")
	}

	activeProbe := InspectionResult{
		Editable: true, Health: InspectionHealthInvalidCredentials, ReasonCode: "authentication_failed",
		Confidence: InspectionConfidenceHigh, Recommendation: InspectionRecommendationReauth,
		SignalSource: InspectionSignalActiveProbe,
	}
	if inspectionManualDeleteAllowed(activeProbe) {
		t.Fatal("an active model probe became manual bulk-deletion evidence")
	}
}
