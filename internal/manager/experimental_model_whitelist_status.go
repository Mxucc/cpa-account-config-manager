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

	// The route walks the journal's own pages until it can serve the requested
	// window. This bounds that walk, and the window offset it accepts, so a route
	// page past the first journal page no longer comes back empty.
	autoModelWhitelistRecentMaxJournalPages = 20
	autoModelWhitelistRecentMaxEntries      = autoModelWhitelistRecentMaxJournalPages * operationPageSize
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
	// This route counts stored auto-detected policies, so it needs each
	// credential document, but it never fans out one unbounded serial host read
	// per account: the reads run with the same bounded concurrency the account
	// list uses, and an unreadable credential is simply not counted.
	documents := a.accounts.ReadAuthDocuments(ctx, accounts)
	var newestDetected time.Time
	for _, account := range accounts {
		if identity := strings.TrimSpace(account.ID); identity != "" {
			labels[identity] = accountDisplayLabel(account)
		}
		document, read := documents[account.ID]
		if !read {
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
	// The journal list is already ordered newest first and pages in units of its
	// own retention page, so the route walks those pages and stops as soon as it
	// can serve the requested window. Reading only the first journal page returned
	// an empty list for every route page past it.
	//
	// Avoid multiplying an attacker-controlled page by pageSize before checking
	// bounds; a very large page would overflow int.
	start := autoModelWhitelistRecentMaxEntries
	if page-1 <= autoModelWhitelistRecentMaxEntries/pageSize {
		start = (page - 1) * pageSize
	}
	if start >= autoModelWhitelistRecentMaxEntries {
		// The window starts past everything this route can serve, so walking the
		// journal for it would scan every retained page to answer with nothing.
		return recent
	}
	needed := start + pageSize
	matching := make([]OperationEntry, 0, needed)
	for journalPage := 1; journalPage <= autoModelWhitelistRecentMaxJournalPages; journalPage++ {
		listed := a.operations.List(OperationQuery{
			Page: journalPage, PageSize: operationPageSize, Search: OperationActionAutoModelWhitelist,
		})
		for _, entry := range listed.Operations {
			if entry.Action == OperationActionAutoModelWhitelist {
				matching = append(matching, entry)
			}
		}
		if len(matching) >= needed || len(listed.Operations) == 0 || (listed.Pages > 0 && journalPage >= listed.Pages) {
			break
		}
	}
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
