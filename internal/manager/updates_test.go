package manager

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestReleaseVersionComparisonIgnoresDevelopmentSuffix(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
		ok      bool
	}{
		{current: "0.2.0-dev", latest: "v0.3.0", want: true, ok: true},
		{current: "0.2.0-dev", latest: "v0.2.0", want: false, ok: true},
		{current: "1.2.3", latest: "v1.2.2", want: false, ok: true},
		{current: "development", latest: "v1.0.0", want: false, ok: false},
	}
	for _, test := range tests {
		got, _, ok := releaseVersionNewer(test.current, test.latest)
		if got != test.want || ok != test.ok {
			t.Fatalf("releaseVersionNewer(%q, %q) = %v, _, %v", test.current, test.latest, got, ok)
		}
	}
}

func TestUpdateCheckerRecordsPluginStoreCheckWithoutReleaseMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	checker := NewUpdateChecker("0.2.0-dev")
	checker.now = func() time.Time { return now }
	checker.store = filepath.Join(t.TempDir(), "update-state.json")
	snapshot := checker.RequestCheck()
	if snapshot.LatestVersion != "" || snapshot.UpdateAvailable || snapshot.ReleaseURL != "" || snapshot.Error != "" || !snapshot.CheckedAt.Equal(now) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	stored, errRead := os.ReadFile(checker.store)
	if errRead != nil {
		t.Fatalf("read update state: %v", errRead)
	}
	if bytes.Contains(stored, []byte("latest_version")) || bytes.Contains(stored, []byte("release metadata")) || bytes.Contains(stored, []byte("github")) {
		t.Fatalf("update state retained direct release metadata: %s", stored)
	}
}

func TestUpdateCheckerClearsLegacyGitHubFailureOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	store := filepath.Join(dataDir, "update-state.json")
	legacy := []byte(`{"version":1,"policy":{"check_enabled":true,"check_interval_hours":24,"auto_update":false},"latest_version":"0.2.9","checked_at":"2026-07-20T14:00:00Z","error":"release metadata request failed"}`)
	if errWrite := os.WriteFile(store, legacy, 0o600); errWrite != nil {
		t.Fatalf("write legacy update state: %v", errWrite)
	}
	checker := NewUpdateChecker("0.2.9")
	checker.Configure(Config{DataDir: dataDir})
	snapshot := checker.Snapshot()
	if snapshot.Error != "" || snapshot.LatestVersion != "" || snapshot.UpdateAvailable {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	checker.RequestCheck()
	raw, errRead := os.ReadFile(store)
	if errRead != nil {
		t.Fatalf("read migrated update state: %v", errRead)
	}
	if bytes.Contains(raw, []byte("latest_version")) || bytes.Contains(raw, []byte("release metadata")) {
		t.Fatalf("legacy GitHub state was not cleared: %s", raw)
	}
}

func TestUpdateCheckerLoadsPolicyOnFirstConfigureWhenStorePathAlreadyMatches(t *testing.T) {
	dataDir := t.TempDir()
	store := filepath.Join(dataDir, "update-state.json")
	stored := []byte(`{"version":1,"policy":{"check_enabled":true,"check_interval_hours":72,"auto_update":false},"checked_at":"2026-07-20T14:00:00Z"}`)
	if errWrite := os.WriteFile(store, stored, 0o600); errWrite != nil {
		t.Fatalf("write update state: %v", errWrite)
	}
	checker := NewUpdateChecker("0.2.9")
	checker.store = store
	checker.Configure(Config{DataDir: dataDir})
	if checker.Snapshot().Policy.CheckIntervalHours != 72 {
		t.Fatalf("snapshot = %#v", checker.Snapshot())
	}
}

func TestUpdatePolicyRouteRequiresExplicitAutoUpdateConfirmation(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	app.Configure([]byte("data_dir: " + t.TempDir()))
	defer app.Close()
	body := []byte(`{"policy":{"check_enabled":true,"check_interval_hours":24,"auto_update":true}}`)
	withoutConfirmation := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/updates",
		Body:   body,
	})
	if withoutConfirmation.StatusCode != http.StatusBadRequest || !bytes.Contains(withoutConfirmation.Body, []byte("explicit confirmation")) {
		t.Fatalf("without confirmation = %d %s", withoutConfirmation.StatusCode, withoutConfirmation.Body)
	}
	confirmed := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut,
		Path:   "/v0/management/plugins/cpa-account-config-manager/updates",
		Body:   []byte(`{"policy":{"check_enabled":true,"check_interval_hours":24,"auto_update":true},"confirm_auto_update":true}`),
	})
	if confirmed.StatusCode != http.StatusOK {
		t.Fatalf("confirmed = %d %s", confirmed.StatusCode, confirmed.Body)
	}
}
