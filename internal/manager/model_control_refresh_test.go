package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// keepCodexChannelModelsSnapshot restores the package-level channel scan after the test, so
// the shared cache cannot leak into other tests.
func keepCodexChannelModelsSnapshot(t *testing.T) {
	t.Helper()
	codexChannelModelsMu.Lock()
	previous := codexChannelModelsState
	codexChannelModelsMu.Unlock()
	t.Cleanup(func() {
		codexChannelModelsMu.Lock()
		codexChannelModelsState = previous
		codexChannelModelsMu.Unlock()
	})
}

// shortenModelControlChannelRefreshTimeout makes the refresh bound testable.
func shortenModelControlChannelRefreshTimeout(t *testing.T, bound time.Duration) {
	t.Helper()
	previous := modelControlChannelRefreshTimeout
	modelControlChannelRefreshTimeout = bound
	t.Cleanup(func() {
		modelControlChannelRefreshTimeout = previous
		waitForModelControlRefresh(t)
	})
}

// waitForModelControlRefresh waits until no background scan holds the single-flight slot.
func waitForModelControlRefresh(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if modelControlRefreshMu.TryLock() {
			modelControlRefreshMu.Unlock()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("a background channel scan was still running")
}

// A model-control write must answer without waiting for the CPA management API. Waiting is
// what made a click look like it did nothing: the change was applied, but the response only
// came back after the outbound scan (client timeout 15s) finished.
func TestModelControlWriteDoesNotWaitForTheChannelScan(t *testing.T) {
	keepCodexChannelModelsSnapshot(t)

	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonHTTPResponse(http.StatusOK, `{"codex-api-key":[]}`), nil
	})
	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	route := "/v0/management" + managementRoutePrefix + "/codex/models"

	// Warm the channel scan once, exactly like opening the tab does.
	if read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: route, Headers: headers}); read.StatusCode != http.StatusOK {
		t.Fatalf("warm read status = %d body=%s", read.StatusCode, read.Body)
	}

	// The management API now stops answering. The write must still return promptly.
	blocked := make(chan struct{})
	var closeOnce sync.Once
	releaseScan := func() { closeOnce.Do(func() { close(blocked) }) }
	t.Cleanup(releaseScan)
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		<-blocked
		return jsonHTTPResponse(http.StatusOK, `{"codex-api-key":[]}`), nil
	})

	done := make(chan cpaapi.ManagementResponse, 1)
	go func() {
		done <- app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodPut, Path: route, Headers: headers,
			Body: []byte(`{"disabled":["gpt-5.4-codex"]}`),
		})
	}()

	select {
	case response := <-done:
		if response.StatusCode != http.StatusOK {
			t.Fatalf("write status = %d body=%s", response.StatusCode, response.Body)
		}
		var payload struct {
			Disabled []string               `json:"disabled"`
			Models   []CodexModelControlRow `json:"models"`
		}
		if errDecode := json.Unmarshal(response.Body, &payload); errDecode != nil {
			t.Fatalf("decode: %v", errDecode)
		}
		// The write reports the applied set, and the rows survive the cache-only response.
		if len(payload.Disabled) != 1 || payload.Disabled[0] != "gpt-5.4-codex" {
			t.Fatalf("disabled = %#v", payload.Disabled)
		}
		if !app.codexModelControl.Disabled("gpt-5.4-codex") {
			t.Fatalf("the change was not applied")
		}
		found := false
		for _, row := range payload.Models {
			if row.ID == "gpt-5.4-codex" && row.Disabled {
				found = true
			}
		}
		if !found {
			t.Fatalf("the disabled row is missing from the write response: %#v", payload.Models)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("the write waited for the blocked channel scan")
	}

	releaseScan()
	waitForModelControlRefresh(t)
}

// A read still waits for the scan, but only up to the refresh bound.
func TestModelControlReadIsBoundedByTheRefreshTimeout(t *testing.T) {
	keepCodexChannelModelsSnapshot(t)
	shortenModelControlChannelRefreshTimeout(t, 150*time.Millisecond)

	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))
	blocked := make(chan struct{})
	var closeOnce sync.Once
	releaseScan := func() { closeOnce.Do(func() { close(blocked) }) }
	t.Cleanup(releaseScan)
	// A never-answering management API must not hold the tab open.
	app.managementDoer = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		select {
		case <-blocked:
		case <-time.After(50 * time.Millisecond):
		}
		return jsonHTTPResponse(http.StatusOK, `{"codex-api-key":[]}`), nil
	})

	started := time.Now()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/codex/models",
		Headers: http.Header{"Authorization": []string{"Bearer management-secret"}},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body=%s", response.StatusCode, response.Body)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the read waited %v instead of failing fast", elapsed)
	}
	releaseScan()
}
