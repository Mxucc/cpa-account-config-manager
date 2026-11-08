package manager

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

// providerPackageInstallTimeout bounds one package download: a package is a few
// megabytes, and a stalled mirror must not hold the request open.
const providerPackageInstallTimeout = 90 * time.Second

// providerPackageRoot is where installed community packages live.
func (a *App) providerPackageRoot() string {
	if a == nil {
		return ""
	}
	dataDir := strings.TrimSpace(a.configSnapshot().DataDir)
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "provider-packages")
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
		return directory, entry.Name, nil
	}
	return "", "", fmt.Errorf("provider %q is not imported", id)
}
