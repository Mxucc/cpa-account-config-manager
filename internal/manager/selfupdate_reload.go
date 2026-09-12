package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CPA reloads native plugins after a plugin-store install, so the store route is the one way to
// make a freshly replaced library take effect without restarting the whole process. Restarting
// CPA is the only alternative, and the host offers no restart button, so the plugin can ask for
// its own reinstall and report exactly what happened.

type pluginStoreEntry struct {
	ID               string `json:"id"`
	Version          string `json:"version"`
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version"`
}

type pluginStoreSnapshot struct {
	PluginsEnabled bool               `json:"plugins_enabled"`
	Plugins        []pluginStoreEntry `json:"plugins"`
}

type pluginStoreInstallResult struct {
	Status          string `json:"status"`
	ID              string `json:"id"`
	Version         string `json:"version"`
	RestartRequired bool   `json:"restart_required"`
}

// SelfUpdateReloadResult is the redacted outcome of a reload attempt.
type SelfUpdateReloadResult struct {
	// Reloaded reports that the host accepted the reinstall without needing a full restart.
	Reloaded bool `json:"reloaded"`
	// StoreVersion is the version the store offered, and AppliedVersion the version the plugin
	// had already written to disk.
	StoreVersion   string `json:"store_version,omitempty"`
	AppliedVersion string `json:"applied_version,omitempty"`
	// RestartRequired mirrors the host answer when it still wants a restart.
	RestartRequired bool `json:"restart_required"`
	// Reason explains a refusal or a failure in operator terms.
	Reason string `json:"reason,omitempty"`
}

// ReloadThroughStore asks CPA to reinstall and reload this plugin. The store must offer a
// version that is at least the one already staged, so this can never downgrade the plugin, and
// a store that cannot answer is reported instead of silently doing nothing.
func (s *SelfUpdateService) ReloadThroughStore(ctx context.Context, managementKey string) (SelfUpdateReloadResult, error) {
	if s == nil {
		return SelfUpdateReloadResult{}, ErrSelfUpdateUnavailable
	}
	trimmedKey := strings.TrimSpace(managementKey)
	if trimmedKey == "" {
		return SelfUpdateReloadResult{}, fmt.Errorf("management key is unavailable")
	}
	snapshot := s.Snapshot()
	result := SelfUpdateReloadResult{AppliedVersion: normalizePluginVersion(snapshot.AppliedVersion)}
	if result.AppliedVersion == "" {
		result.AppliedVersion = normalizePluginVersion(snapshot.LatestVersion)
	}

	client, errClient := newManagementClient(resolveManagementBaseURL(s.managementBaseURL()), trimmedKey, s.managementDoer())
	if errClient != nil {
		result.Reason = "cpa_management_api_unavailable"
		return result, errClient
	}
	defer client.clearSecrets()

	store, errStore := client.pluginStore(ctx)
	if errStore != nil {
		result.Reason = "plugin_store_unavailable"
		return result, errStore
	}
	if !store.PluginsEnabled {
		result.Reason = "plugin_store_disabled"
		return result, fmt.Errorf("the CPA plugin store is disabled")
	}
	entry, found := pluginStoreEntryFor(store, PluginID)
	if !found {
		result.Reason = "plugin_not_in_store"
		return result, fmt.Errorf("this plugin is not listed in the CPA plugin store")
	}
	result.StoreVersion = normalizePluginVersion(entry.Version)
	if result.StoreVersion == "" {
		result.Reason = "plugin_store_version_unknown"
		return result, fmt.Errorf("the CPA plugin store did not report a version for this plugin")
	}
	// A store index that is older than the library already on disk would downgrade the plugin.
	if result.AppliedVersion != "" && compareVersions(result.StoreVersion, result.AppliedVersion) < 0 {
		result.Reason = "plugin_store_version_is_older"
		return result, fmt.Errorf("the CPA plugin store offers version %s, older than the installed %s", result.StoreVersion, result.AppliedVersion)
	}

	installed, errInstall := client.installPluginFromStore(ctx, PluginID)
	if errInstall != nil {
		result.Reason = "plugin_store_install_failed"
		return result, errInstall
	}
	if version := normalizePluginVersion(installed.Version); version != "" {
		result.StoreVersion = version
	}
	result.RestartRequired = installed.RestartRequired
	result.Reloaded = !installed.RestartRequired
	if !result.Reloaded {
		result.Reason = "host_still_requires_a_restart"
	}
	return result, nil
}

// pluginStoreEntryFor finds one plugin in the store listing.
func pluginStoreEntryFor(store pluginStoreSnapshot, pluginID string) (pluginStoreEntry, bool) {
	for _, entry := range store.Plugins {
		if strings.EqualFold(strings.TrimSpace(entry.ID), pluginID) {
			return entry, true
		}
	}
	return pluginStoreEntry{}, false
}

// pluginStore reads the CPA plugin store listing.
func (c *managementClient) pluginStore(ctx context.Context) (pluginStoreSnapshot, error) {
	var response pluginStoreSnapshot
	if errRequest := c.requestJSON(ctx, http.MethodGet, "/v0/management/plugin-store", nil, "", &response); errRequest != nil {
		return pluginStoreSnapshot{}, errRequest
	}
	return response, nil
}

// installPluginFromStore asks CPA to reinstall one plugin, which is what makes the host reload
// the native library.
func (c *managementClient) installPluginFromStore(ctx context.Context, pluginID string) (pluginStoreInstallResult, error) {
	if !safePluginStoreID(pluginID) {
		return pluginStoreInstallResult{}, fmt.Errorf("plugin id is invalid")
	}
	raw, errMarshal := json.Marshal(map[string]any{})
	if errMarshal != nil {
		return pluginStoreInstallResult{}, errMarshal
	}
	var response pluginStoreInstallResult
	path := "/v0/management/plugin-store/" + pluginID + "/install"
	if errRequest := c.requestJSON(ctx, http.MethodPost, path, strings.NewReader(string(raw)), "application/json", &response); errRequest != nil {
		return pluginStoreInstallResult{}, errRequest
	}
	return response, nil
}

// safePluginStoreID keeps a plugin id to the shape the store route uses.
func safePluginStoreID(pluginID string) bool {
	trimmed := strings.TrimSpace(pluginID)
	if trimmed == "" || len(trimmed) > 128 {
		return false
	}
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '-', char == '_', char == '.':
		default:
			return false
		}
	}
	return true
}

// SetManagementDoer lets the host inject the HTTP doer every other management call uses, so a
// test (or a host with a custom transport) does not reach the network directly.
func (s *SelfUpdateService) SetManagementDoer(doer HTTPDoer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doer = doer
}

// managementDoer returns the injected doer, if any.
func (s *SelfUpdateService) managementDoer() HTTPDoer {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doer
}

// managementBaseURL reports the configured CPA management base URL.
func (s *SelfUpdateService) managementBaseURL() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.ManagementBaseURL
}
