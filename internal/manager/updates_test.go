package manager

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type fakeUpdateHTTPHost struct {
	request  cpaapi.HTTPRequest
	response cpaapi.HTTPResponse
	err      error
}

func (h *fakeUpdateHTTPHost) DoHTTP(_ context.Context, request cpaapi.HTTPRequest) (cpaapi.HTTPResponse, error) {
	h.request = request
	return h.response, h.err
}

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

func TestUpdateCheckerUsesFixedPublicGitHubEndpointAndPersistsSanitizedState(t *testing.T) {
	now := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	host := &fakeUpdateHTTPHost{response: cpaapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"tag_name":"v0.3.0","draft":false,"prerelease":false,"token":"release-secret"}`),
	}}
	checker := NewUpdateChecker(host, "0.2.0-dev", DefaultPluginRepository)
	checker.now = func() time.Time { return now }
	checker.store = filepath.Join(t.TempDir(), "update-state.json")
	checker.check(context.Background())

	if host.request.Method != http.MethodGet || host.request.URL != "https://api.github.com/repos/Mxucc/cpa-account-config-manager/releases/latest" {
		t.Fatalf("request = %#v", host.request)
	}
	if host.request.Headers.Get("Authorization") != "" || len(host.request.Body) != 0 {
		t.Fatalf("update request carried credentials: %#v", host.request)
	}
	snapshot := checker.Snapshot()
	if snapshot.LatestVersion != "0.3.0" || !snapshot.UpdateAvailable || snapshot.ReleaseURL != "https://github.com/Mxucc/cpa-account-config-manager/releases/tag/v0.3.0" || !snapshot.CheckedAt.Equal(now) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	stored, errRead := os.ReadFile(checker.store)
	if errRead != nil {
		t.Fatalf("read update state: %v", errRead)
	}
	if bytes.Contains(stored, []byte("release-secret")) || bytes.Contains(stored, []byte("Authorization")) {
		t.Fatalf("update state leaked response data: %s", stored)
	}
}

func TestUpdateCheckerReturnsFixedErrorWithoutLeakingHostFailure(t *testing.T) {
	host := &fakeUpdateHTTPHost{err: errors.New("Bearer network-secret")}
	checker := NewUpdateChecker(host, "0.2.0", DefaultPluginRepository)
	checker.store = filepath.Join(t.TempDir(), "update-state.json")
	checker.check(context.Background())
	snapshot := checker.Snapshot()
	if snapshot.Error != "release metadata request failed" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	raw, _ := os.ReadFile(checker.store)
	if bytes.Contains(raw, []byte("network-secret")) || bytes.Contains(raw, []byte("Bearer")) {
		t.Fatalf("stored update error leaked host failure: %s", raw)
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

func TestGitHubRepositoryParserRejectsNonCanonicalURLs(t *testing.T) {
	for _, value := range []string{
		"http://github.com/Mxucc/cpa-account-config-manager",
		"https://evil.example/Mxucc/cpa-account-config-manager",
		"https://github.com/Mxucc/cpa-account-config-manager/extra",
		"https://user@github.com/Mxucc/cpa-account-config-manager",
	} {
		if _, _, ok := parseGitHubRepository(value); ok {
			t.Fatalf("repository %q was accepted", value)
		}
	}
}
