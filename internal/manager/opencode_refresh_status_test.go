package manager

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A refresh that could not read the upstream must answer 200 with the reason.
// A 5xx here was replaced by the edge proxy with an opaque gateway error, so the
// page showed "error code: 502" instead of the upstream reason.
func TestOpenCodeRefreshReportsAFailedReadAsData(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	response := app.HandleManagement(t.Context(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/opencode/refresh-account",
		"", map[string][]string{"account_id": {"missing-account"}},
	))
	// 200 with the reason: the caller can show it instead of an opaque 502.
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, response.Body)
	}
	var decoded struct {
		Result *OpenCodeQuotaResult `json:"result"`
	}
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if decoded.Result == nil || decoded.Result.Success || decoded.Result.Error == "" {
		t.Fatalf("result = %+v", decoded.Result)
	}
}

// A probe the upstream rejected is a payload, never a gateway failure.
func TestOpenCodeProbeReportsAFailedProbeAsData(t *testing.T) {
	app := newAccountLoginTestApp(t, &stubLoginDoer{})
	response := app.HandleManagement(t.Context(), accountLoginRequest(
		http.MethodPost, "/v0/management"+managementRoutePrefix+"/opencode/probe",
		`{"workspace_id":"wrk-1","auth_cookie":"cookie"}`, nil,
	))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("probe status = %d body = %s", response.StatusCode, response.Body)
	}
	var decoded struct {
		Result *OpenCodeQuotaResult `json:"result"`
	}
	if errDecode := json.Unmarshal(response.Body, &decoded); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if decoded.Result == nil || decoded.Result.Success {
		t.Fatalf("probe result = %+v", decoded.Result)
	}
	if decoded.Result.Error == "" {
		t.Fatal("a failed probe carries no reason")
	}
}
