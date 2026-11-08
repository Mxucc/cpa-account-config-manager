package manager

import (
	"context"
	"errors"
	"strings"

	"cpa-account-config-manager/internal/magpiecompat"
	"cpa-account-config-manager/internal/provider"
)

// Provider hooks and settings accessors.

// providerSettingsService returns the settings store for the configured data
// directory, rebuilding it when the directory changes.
func (a *App) providerSettingsService() *ProviderSettingsService {
	if a == nil {
		return NewProviderSettingsService("")
	}
	path := providerSettingsPath(strings.TrimSpace(a.configSnapshot().DataDir))
	a.providerImportsMu.Lock()
	defer a.providerImportsMu.Unlock()
	if a.providerSettings != nil && a.providerSettings.path == path {
		return a.providerSettings
	}
	a.providerSettings = NewProviderSettingsService(path)
	return a.providerSettings
}

// pluginHooks returns the runner used for imported plugin probes.
func (a *App) pluginHooks() pluginHookRunner {
	if a == nil {
		return defaultPluginHookRunner{}
	}
	a.hookRunnerMu.Lock()
	defer a.hookRunnerMu.Unlock()
	if a.hookRunner != nil {
		return a.hookRunner
	}
	return defaultPluginHookRunner{}
}

// setPluginHookRunner is the test seam: it lets a test describe a probe result
// (or a failure) without running third-party code.
func (a *App) setPluginHookRunner(runner pluginHookRunner) {
	if a == nil {
		return
	}
	a.hookRunnerMu.Lock()
	a.hookRunner = runner
	a.hookRunnerMu.Unlock()
}

// pluginRuntimeAvailable reports whether a sidecar runtime exists.
func (a *App) pluginRuntimeAvailable() bool {
	if a == nil {
		return false
	}
	a.hookRunnerMu.Lock()
	override := a.hookRuntimeAvailable
	a.hookRunnerMu.Unlock()
	if override != nil {
		return override()
	}
	return magpiecompat.BunAvailable()
}

func (a *App) recordHookError(providerID, message string) {
	if a == nil || providerID == "" {
		return
	}
	a.hookErrorsMu.Lock()
	if a.hookErrors == nil {
		a.hookErrors = map[string]string{}
	}
	if message == "" {
		delete(a.hookErrors, providerID)
	} else {
		a.hookErrors[providerID] = message
	}
	a.hookErrorsMu.Unlock()
}

// hookError returns the last probe failure for one provider, if any.
func (a *App) hookError(providerID string) string {
	if a == nil {
		return ""
	}
	a.hookErrorsMu.Lock()
	defer a.hookErrorsMu.Unlock()
	return a.hookErrors[providerID]
}

// importedPluginAccounts turns the imported plugins into normalized accounts when
// hook execution is enabled. Everything about the mapping lives in
// provider.FromMagpiePlugin; this only decides whether to run a probe and records
// why one failed.
func (a *App) importedPluginAccounts(ctx context.Context) []provider.Account {
	if a == nil || !a.providerSettingsService().MagpieHookExecution() {
		return nil
	}
	imported := a.providerImportStore().List()
	if len(imported) == 0 {
		return nil
	}
	out := make([]provider.Account, 0, len(imported))
	for _, entry := range imported {
		if strings.TrimSpace(entry.Directory) == "" {
			// Manifest-only entry: nothing to execute, and the catalog says so.
			continue
		}
		if !a.pluginRuntimeAvailable() {
			a.recordHookError(entry.ID, magpiecompat.ErrBunUnavailable.Error())
			continue
		}
		input, errProbe := a.pluginHooks().Probe(entry.Directory)
		if errProbe != nil {
			a.recordHookError(entry.ID, sanitizedHookError(errProbe))
			continue
		}
		a.recordHookError(entry.ID, "")
		input.ProviderID = entry.ID
		if strings.TrimSpace(input.DisplayName) == "" {
			input.DisplayName = entry.Name
		}
		out = append(out, provider.FromMagpiePlugin(input))
	}
	return out
}

// sanitizedHookError keeps a probe failure actionable without echoing plugin
// output, which may contain credentials.
func sanitizedHookError(err error) string {
	switch {
	case errors.Is(err, magpiecompat.ErrBunUnavailable):
		return magpiecompat.ErrBunUnavailable.Error()
	case errors.Is(err, magpiecompat.ErrTimeout):
		return magpiecompat.ErrTimeout.Error()
	case errors.Is(err, magpiecompat.ErrOutputTooLarge):
		return magpiecompat.ErrOutputTooLarge.Error()
	case errors.Is(err, magpiecompat.ErrPluginInvalid):
		return "plugin did not answer the probe"
	default:
		return "plugin probe failed"
	}
}
