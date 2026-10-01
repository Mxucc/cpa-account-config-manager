package manager

import (
	"fmt"
	"net/url"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// The status route used to read only the first journal page and slice it, so a
// route window that starts past that page came back empty although the journal
// held the entries.
func TestAutoModelWhitelistRecentEntriesWalkJournalPages(t *testing.T) {
	app := &App{operations: NewOperationJournal()}
	// Extended history keeps more than one journal page, which is the only case
	// where a route window can start past the first page.
	app.operations.Configure(normalizeConfig(Config{
		DataDir:           t.TempDir(),
		OperationSettings: &OperationSettingsConfig{ExtendedHistory: true},
	}))
	total := operationPageSize + 20
	for index := 0; index < total; index++ {
		app.recordAutoModelWhitelist(fmt.Sprintf("auth-%04d", index), &ModelTestPolicyAdjustment{
			Status: "applied", ReasonCode: "applied",
		}, OperationSourceBackground)
	}

	first := app.autoModelWhitelistRecentEntries(cpaapi.ManagementRequest{}, map[string]string{})
	if len(first) != autoModelWhitelistRecentDefaultPageSize {
		t.Fatalf("first page returned %d entries, want %d", len(first), autoModelWhitelistRecentDefaultPageSize)
	}

	// A window that starts after the first journal page must still be served.
	window := url.Values{"page": {"6"}, "page_size": {"100"}}
	second := app.autoModelWhitelistRecentEntries(cpaapi.ManagementRequest{Query: window}, map[string]string{})
	if want := total - operationPageSize; len(second) != want {
		t.Fatalf("the window past the first journal page returned %d entries, want %d", len(second), want)
	}
}
