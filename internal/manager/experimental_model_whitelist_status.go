package manager

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	autoModelWhitelistRecentDefaultPageSize = 20
	autoModelWhitelistRecentMaxPageSize     = 100
)

// autoModelWhitelistStatusResponse is the stable, credential-free payload for the
// authenticated automatic allow-list status route. It exposes the experiment
// switch, aggregate Codex-account counts, and the newest detections.
type autoModelWhitelistStatusResponse struct {
	AutoModelWhitelist autoModelWhitelistStatus `json:"auto_model_whitelist"`
}

type autoModelWhitelistStatus struct {
	Enabled        bool                            `json:"enabled"`
	Accounts       int                             `json:"accounts"`
	Limited        int                             `json:"limited"`
	LastDetectedAt string                          `json:"last_detected_at,omitempty"`
	Recent         []autoModelWhitelistRecentEntry `json:"recent"`
}

type autoModelWhitelistRecentEntry struct {
	AccountID  string `json:"account_id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
	At         string `json:"at"`
}

// handleAutoModelWhitelistStatus reports what the automatic allow-list experiment
// has detected. It requires the Management key and never fails on a degraded
// account read: unavailable counts are reported as zero instead.
func (a *App) handleAutoModelWhitelistStatus(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	if a == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "plugin runtime is unavailable"})
	}
	response := autoModelWhitelistStatusResponse{AutoModelWhitelist: autoModelWhitelistStatus{
		Enabled: a.experiments.AutoModelWhitelistEnabled(),
		Recent:  []autoModelWhitelistRecentEntry{},
	}}
	accounts := a.codexInventoryAccounts(ctx)
	response.AutoModelWhitelist.Accounts = len(accounts)
	labels := make(map[string]string, len(accounts))
	var newestDetected time.Time
	for _, account := range accounts {
		if identity := strings.TrimSpace(account.ID); identity != "" {
			labels[identity] = accountDisplayLabel(account)
		}
		if a.accounts == nil {
			continue
		}
		document, errDocument := a.accounts.CurrentAuthDocument(ctx, account)
		if errDocument != nil {
			// A failed read must not fail the response; the account is simply not
			// counted as auto-detected.
			continue
		}
		policy, ok := readStoredModelPolicy(document.Metadata)
		if !ok || !policy.AutoDetected {
			continue
		}
		response.AutoModelWhitelist.Limited++
		if policy.DetectedAt != nil && policy.DetectedAt.After(newestDetected) {
			newestDetected = *policy.DetectedAt
		}
	}
	if !newestDetected.IsZero() {
		response.AutoModelWhitelist.LastDetectedAt = newestDetected.UTC().Format(time.RFC3339)
	}
	response.AutoModelWhitelist.Recent = a.autoModelWhitelistRecentEntries(req, labels)
	return jsonResponse(http.StatusOK, response)
}

// autoModelWhitelistRecentEntries returns the newest auto-model-whitelist journal
// entries for the requested page. The page and page_size inputs are clamped, so
// unknown values degrade to the first bounded page instead of erroring.
func (a *App) autoModelWhitelistRecentEntries(req cpaapi.ManagementRequest, labels map[string]string) []autoModelWhitelistRecentEntry {
	recent := []autoModelWhitelistRecentEntry{}
	if a.operations == nil {
		return recent
	}
	page := intQuery(req.Query, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := intQuery(req.Query, "page_size", autoModelWhitelistRecentDefaultPageSize)
	if pageSize < 1 {
		pageSize = autoModelWhitelistRecentDefaultPageSize
	}
	if pageSize > autoModelWhitelistRecentMaxPageSize {
		pageSize = autoModelWhitelistRecentMaxPageSize
	}
	// The journal list is already ordered newest first. It pages in units of its
	// own retention page, so the route slices the newest matching entries itself.
	listed := a.operations.List(OperationQuery{Page: 1, Search: OperationActionAutoModelWhitelist}).Operations
	matching := make([]OperationEntry, 0, len(listed))
	for _, entry := range listed {
		if entry.Action == OperationActionAutoModelWhitelist {
			matching = append(matching, entry)
		}
	}
	start := (page - 1) * pageSize
	if start > len(matching) {
		start = len(matching)
	}
	end := start + pageSize
	if end > len(matching) {
		end = len(matching)
	}
	for _, entry := range matching[start:end] {
		recent = append(recent, autoModelWhitelistRecentEntry{
			AccountID:  entry.TargetID,
			Label:      labels[entry.TargetID],
			Status:     autoModelWhitelistAdjustmentStatus(entry.Status),
			ReasonCode: entry.ReasonCode,
			At:         operationSortTime(entry).UTC().Format(time.RFC3339),
		})
	}
	return recent
}

// autoModelWhitelistAdjustmentStatus maps the internal operation status onto the
// stable applied/skipped/failed vocabulary the settings page consumes.
func autoModelWhitelistAdjustmentStatus(status string) string {
	switch status {
	case OperationStatusSucceeded:
		return "applied"
	case OperationStatusSkipped:
		return "skipped"
	default:
		return "failed"
	}
}

func accountDisplayLabel(account Account) string {
	return firstNonEmpty(strings.TrimSpace(account.Label), strings.TrimSpace(account.Email), strings.TrimSpace(account.Name))
}
