package manager

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"

	"cpa-account-config-manager/internal/magpiecompat"
	"cpa-account-config-manager/internal/provider"
)

// Community provider packages and their sign-ins.
//
// A market import records a provider definition, but the provider's own sign-in
// and model hooks live in the package's JavaScript. Without the package on disk
// the plugin can only say "sign in somewhere else", which is not an answer when
// this plugin is the one that offers the provider. So the import also installs the
// package into the plugin's private directory, and the add-account dialog runs its
// declared sign-in.

// providerRuntimeInstallTimeout bounds one runtime download.
const providerRuntimeInstallTimeout = 6 * time.Minute

// providerPackageInstallTimeout bounds one package download: a package is a few
// megabytes, and a stalled mirror must not hold the request open.
const providerPackageInstallTimeout = 90 * time.Second

// providerHomeDir is the plugin's stable home. The host's data directory can change
// between installs, so state the operator created (imports, packages, runtime) is
// kept beside the host's own plugin home instead of inside that changing path.
func providerHomeDir(dataDir string) string {
	trimmed := strings.TrimSpace(dataDir)
	if trimmed == "" {
		return ""
	}
	parent := filepath.Dir(trimmed)
	if filepath.Base(trimmed) == ".cpa-account-config-manager" {
		return trimmed
	}
	return filepath.Join(parent, ".cpa-account-config-manager")
}

// providerPackageRoot is where installed community packages live.
func (a *App) providerPackageRoot() string {
	if a == nil {
		return ""
	}
	dataDir := strings.TrimSpace(a.configSnapshot().DataDir)
	if dataDir == "" {
		return ""
	}
	return filepath.Join(providerHomeDir(dataDir), "provider-packages")
}

// providerInstaller builds the downloader for the configured data directory.
func (a *App) providerInstaller() *provider.PackageInstaller {
	return &provider.PackageInstaller{
		Doer: a.managementDoer,
		Root: a.providerPackageRoot(),
	}
}

// installProviderPackage downloads one community package. An empty name is not an
// error: a pasted manifest has no package to fetch.
func (a *App) installProviderPackage(ctx context.Context, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", nil
	}
	installer := a.providerInstaller()
	if installer.Root == "" {
		return "", fmt.Errorf("the plugin data directory is unavailable; the package was not installed")
	}
	if directory, found := installer.Installed(trimmed); found {
		return directory, nil
	}
	installContext, cancel := context.WithTimeout(ctx, providerPackageInstallTimeout)
	defer cancel()
	return installer.Install(installContext, trimmed)
}

// importedProviderDirectory resolves the on-disk package for one imported provider
// id, installing it when the record predates package installation.
func (a *App) importedProviderDirectory(ctx context.Context, id string) (string, string, error) {
	store := a.providerImportStore()
	for _, entry := range store.List() {
		if entry.ID != id {
			continue
		}
		if directory := strings.TrimSpace(entry.Directory); directory != "" {
			return directory, entry.Name, nil
		}
		packageName := strings.TrimSpace(entry.Package)
		if packageName == "" {
			return "", entry.Name, fmt.Errorf("this provider was imported without a package, so its sign-in cannot run")
		}
		directory, errInstall := a.installProviderPackage(ctx, packageName)
		if errInstall != nil {
			return "", entry.Name, errInstall
		}
		// Record the directory so the next sign-in does not download again.
		if raw, errRead := os.ReadFile(directory + "/package.json"); errRead == nil {
			if _, errReimport := store.Import(raw, entry.Source, directory); errReimport == nil {
				return directory, entry.Name, nil
			}
		}
		return directory, entry.Name, nil
	}
	return "", "", fmt.Errorf("provider %q is not imported", id)
}

// handleProviderRuntime reports and installs the JavaScript runtime the community
// plugins need. A host without it can still list and import plugins, but their
// sign-in and model hooks cannot run, so the page offers to install it here rather
// than asking the operator to provision the gateway host by hand.
func (a *App) handleProviderRuntime(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	dataDir := strings.TrimSpace(a.configSnapshot().DataDir)
	if dataDir != "" {
		// Register the managed copy (if any) before reporting, so the answer matches
		// what a sign-in would actually resolve.
		if info, errStat := os.Stat(magpiecompat.RuntimePath(dataDir)); errStat == nil && !info.IsDir() {
			magpiecompat.SetManagedRuntime(magpiecompat.RuntimePath(dataDir))
		}
	}
	// The answer is what a sign-in would actually resolve, including a runtime an
	// earlier install left behind in another data directory.
	snapshot := map[string]any{
		"available": magpiecompat.BunAvailable(),
		"managed":   false,
		"data_dir":  dataDir != "",
	}
	if path := magpiecompat.ManagedRuntime(); path != "" {
		snapshot["managed"] = true
		snapshot["managed_path"] = path
	}
	if req.Method == http.MethodGet {
		return jsonResponse(http.StatusOK, snapshot)
	}
	// A POST installs the runtime.
	if dataDir == "" {
		return jsonResponse(http.StatusOK, map[string]any{
			"installed": false,
			"reason":    "the plugin data directory is unavailable, so the runtime cannot be stored",
		})
	}
	installContext, cancel := context.WithTimeout(ctx, providerRuntimeInstallTimeout)
	defer cancel()
	result, errInstall := magpiecompat.InstallRuntime(installContext, a.managementDoer, dataDir)
	if errInstall != nil {
		return jsonResponse(http.StatusOK, map[string]any{
			"installed": false,
			"reason":    errInstall.Error(),
		})
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"installed": result.Installed,
		"path":      result.Path,
		"reason":    result.Reason,
	})
}
