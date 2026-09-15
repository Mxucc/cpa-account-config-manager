package manager

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// blockingServiceConfigurer is a deliberately blocking service configuration applier. It lets a
// test prove that ConfigureHostBounded returns the registration without waiting for the services.
type blockingServiceConfigurer struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingServiceConfigurer) applyServiceConfig(Config, uint32) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
}

// Regression test for GitHub issue #7: App.DiscoverAuthStorage runs on AccountService.baseAccounts,
// which InspectionEngine.scanWithMode calls while it already holds InspectionEngine.scanMu. The old
// implementation re-entered InspectionEngine.Configure inline, which self-deadlocks on the
// non-reentrant mutex. This test drives that exact stack and fails (rather than hanging CI) if
// DiscoverAuthStorage ever blocks again.
func TestDiscoverAuthStorageDoesNotReenterInspectionConfigure(t *testing.T) {
	authDir := t.TempDir()
	authPath := writeAuthEntryWithDir(t, authDir, "codex-one.json")
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		Name: "codex-one.json", Path: authPath, Source: "file",
	}}}
	app := NewApp(host, nil)
	app.ConfigureHost([]byte(""), cpaapi.SchemaVersion)
	defer app.Close()
	returned := make(chan struct{})
	go func() {
		// The inspection scan stack holds scanMu while it reads accounts through baseAccounts.
		app.inspection.scanMu.Lock()
		defer app.inspection.scanMu.Unlock()
		app.DiscoverAuthStorage(host.entries)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("DiscoverAuthStorage blocked while the inspection scan lock was held; it must not re-enter a service lock")
	}
	if !app.WaitForReconfigure(5 * time.Second) {
		t.Fatal("the coalesced state-directory reconfigure did not settle")
	}
	if app.ReconfigurePending() {
		t.Fatal("ReconfigurePending() must be false once the reconfigure settled")
	}
	// The resolved directories must have become effective: OpenCode state now lives beside the
	// auth files, exactly as the synchronous implementation produced.
	if got := app.opencode.Storage().DataDir; resolvePathForTest(t, got) != resolvePathForTest(t, stateDirUnderAuthDir(authDir)) {
		t.Fatalf("state dir = %q, want %q", got, stateDirUnderAuthDir(authDir))
	}
}

// A bounded configure must return the registration within the bound even when the service side
// blocks forever, and it must record an operator-visible diagnostic plus a journal entry.
func TestConfigureHostBoundedReturnsRegistrationWhenServiceBlocks(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	defer app.Close()
	dataDir := t.TempDir()
	if !app.ConfigureHostBounded([]byte("data_dir: "+dataDir+"\n"), cpaapi.SchemaVersion, 5*time.Second) {
		t.Fatal("the healthy setup configure did not complete within its bound")
	}

	blocker := &blockingServiceConfigurer{started: make(chan struct{}, 1), release: make(chan struct{})}
	app.testServiceConfigurer = blocker
	t.Cleanup(func() { close(blocker.release) })

	startedAt := time.Now()
	completed := app.ConfigureHostBounded([]byte("data_dir: "+dataDir+"\n"), cpaapi.SchemaVersion, 200*time.Millisecond)
	if completed {
		t.Fatal("a deliberately blocking configure must not report completion within the bound")
	}
	if elapsed := time.Since(startedAt); elapsed > 3*time.Second {
		t.Fatalf("ConfigureHostBounded returned after %v, want about the bound", elapsed)
	}
	select {
	case <-blocker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the service configuration never started on the background goroutine")
	}

	registration := app.Registration()
	if registration.SchemaVersion != cpaapi.SchemaVersion ||
		!registration.Capabilities.ManagementAPI || !registration.Capabilities.UsagePlugin || !registration.Capabilities.RequestLifecyclePlugin {
		t.Fatalf("registration = %#v, want the full host-schema capabilities", registration)
	}
	if got := app.configError(); got != configureTimeoutMessage {
		t.Fatalf("configError() = %q, want %q", got, configureTimeoutMessage)
	}
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/opencode/storage",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(response.Body), configureTimeoutMessage) {
		t.Fatalf("status response = %d %s, want the timeout diagnostic", response.StatusCode, response.Body)
	}
	found := false
	for _, operation := range app.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize}).Operations {
		if operation.ReasonCode == "plugin_configure_timeout" {
			found = true
		}
	}
	if !found {
		t.Fatal("the timeout was not journalled with reason plugin_configure_timeout")
	}
}

// The healthy path applies the configuration normally and never journals a timeout.
func TestConfigureHostBoundedAppliesConfigurationWhenHealthy(t *testing.T) {
	bounded := NewApp(&fakeAuthHost{}, nil)
	defer bounded.Close()
	dataDir := t.TempDir()
	if !bounded.ConfigureHostBounded([]byte("workers: 3\ndata_dir: "+dataDir+"\n"), cpaapi.SchemaVersion, 10*time.Second) {
		t.Fatal("a healthy ConfigureHostBounded must complete within the bound")
	}

	synchronous := NewApp(&fakeAuthHost{}, nil)
	defer synchronous.Close()
	synchronous.ConfigureHost([]byte("workers: 3\ndata_dir: "+t.TempDir()+"\n"), cpaapi.SchemaVersion)

	got := bounded.configSnapshot()
	if got.Workers != 3 || got.DataDir != dataDir {
		t.Fatalf("bounded config = %#v, want workers=3 data_dir=%q", got, dataDir)
	}
	if got.Workers != synchronous.configSnapshot().Workers {
		t.Fatalf("bounded workers = %d, synchronous workers = %d", got.Workers, synchronous.configSnapshot().Workers)
	}
	if !reflect.DeepEqual(bounded.Registration(), synchronous.Registration()) {
		t.Fatalf("bounded registration = %#v, synchronous registration = %#v", bounded.Registration(), synchronous.Registration())
	}
	if bounded.configError() != "" {
		t.Fatalf("configError() = %q, want empty", bounded.configError())
	}
	for _, operation := range bounded.operations.List(OperationQuery{Page: 1, PageSize: operationPageSize}).Operations {
		if operation.ReasonCode == "plugin_configure_timeout" {
			t.Fatal("a healthy configure must not journal a configure timeout")
		}
	}
}
