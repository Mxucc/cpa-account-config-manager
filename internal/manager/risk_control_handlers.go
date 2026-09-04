package manager

import (
	"net/http"

	"cpa-account-config-manager/internal/cpaapi"
)

func (a *App) handleRiskControlUpdate(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.riskControl == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "risk-control service is unavailable"})
	}
	var config RiskControlConfig
	if err := decodeJSONRequest(req.Body, &config); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	snapshot, err := a.riskControl.UpdateConfig(config)
	if err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) handleRiskControlClear(hashes bool) cpaapi.ManagementResponse {
	if a == nil || a.riskControl == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "risk-control service is unavailable"})
	}
	var (
		snapshot RiskControlSnapshot
		err      error
	)
	if hashes {
		snapshot, err = a.riskControl.ClearHashes()
	} else {
		snapshot, err = a.riskControl.ClearEvents()
	}
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, snapshot)
}
