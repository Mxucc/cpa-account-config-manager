package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Installing a community plugin's code.
//
// The market import used to record a manifest only, which left the plugin with no
// code to run: its sign-in could never be offered, so the page told the operator to
// go sign in somewhere else. The community plugin is a normal npm package whose
// sign-in lives in its own module, so the plugin downloads that package into its
// private data directory and runs the sign-in itself.
//
// The download is bounded and the archive is unpacked defensively: only regular
// files, no absolute paths, no traversal, a per-entry and a total cap.

const (
	// pluginPackageMaxBytes bounds one downloaded tarball.
	pluginPackageMaxBytes = 24 << 20
	// pluginPackageMaxExpanded bounds everything one package unpacks to.
	pluginPackageMaxExpanded = 96 << 20
	// pluginPackageMaxEntries bounds the archive entry count.
	pluginPackageMaxEntries = 4096
)

// PackageInstaller downloads and unpacks a community plugin package.
type PackageInstaller struct {
	Doer MarketDoer
	Root string
}

// PackageDirectory returns where one package lives, keyed by its npm name.
func (i *PackageInstaller) PackageDirectory(name string) (string, error) {
	root := strings.TrimSpace(i.Root)
	if root == "" {
		return "", fmt.Errorf("the plugin data directory is unavailable")
	}
	safe := packagePathSegment(name)
	if safe == "" {
		return "", fmt.Errorf("the package name is not usable as a directory")
	}
	return filepath.Join(root, safe), nil
}

// Installed reports whether the package has already been unpacked.
func (i *PackageInstaller) Installed(name string) (string, bool) {
	directory, errDirectory := i.PackageDirectory(name)
	if errDirectory != nil {
		return "", false
	}
	if info, errStat := os.Stat(filepath.Join(directory, "package.json")); errStat == nil && !info.IsDir() {
		return directory, true
	}
	return "", false
}

// Install downloads one published package and unpacks it. It returns the directory
// the plugin's module now lives in. Reinstalling replaces the previous copy so a
// new version is never mixed with an old one.
func (i *PackageInstaller) Install(ctx context.Context, name string) (string, error) {
	validated, errName := marketPackageID(name)
	if errName != nil {
		return "", errName
	}
	directory, errDirectory := i.PackageDirectory(validated)
	if errDirectory != nil {
		return "", errDirectory
	}
	doer := i.Doer
	if doer == nil {
		doer = &http.Client{Timeout: marketRequestTime}
	}

	tarball, errTarball := i.tarballURL(ctx, doer, validated)
	if errTarball != nil {
		return "", errTarball
	}
	archive, errFetch := i.fetch(ctx, doer, tarball)
	if errFetch != nil {
		return "", errFetch
	}
	// Unpack into a sibling first, then swap, so a failed download never leaves a
	// half-written package that the sidecar would try to import.
	staging := directory + ".staging"
	_ = os.RemoveAll(staging)
	if errUnpack := unpackNpmTarball(archive, staging); errUnpack != nil {
		_ = os.RemoveAll(staging)
		return "", errUnpack
	}
	if _, errStat := os.Stat(filepath.Join(staging, "package.json")); errStat != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("the package archive has no package.json")
	}
	_ = os.RemoveAll(directory)
	if errRename := os.Rename(staging, directory); errRename != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("install the package: %w", errRename)
	}
	return directory, nil
}

// tarballURL resolves the published tarball for the package's latest version.
func (i *PackageInstaller) tarballURL(ctx context.Context, doer MarketDoer, name string) (string, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, marketNpmPackageURL+escapePackagePath(name), nil)
	if errRequest != nil {
		return "", fmt.Errorf("build the package request: %w", errRequest)
	}
	request.Header.Set("Accept", "application/json")
	response, errDo := doer.Do(request)
	if errDo != nil {
		return "", fmt.Errorf("read the package document: %w", errDo)
	}
	if response == nil || response.Body == nil {
		return "", fmt.Errorf("npm returned an empty package document")
	}
	defer func() { _ = response.Body.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, marketMaxPackumentBytes))
	if errRead != nil {
		return "", fmt.Errorf("read the package document: %w", errRead)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("npm answered HTTP %d for %s", response.StatusCode, name)
	}
	var document struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if errUnmarshal := json.Unmarshal(raw, &document); errUnmarshal != nil {
		return "", fmt.Errorf("npm did not answer with a package document")
	}
	latest := strings.TrimSpace(document.DistTags["latest"])
	if latest == "" {
		return "", fmt.Errorf("the package has no published version")
	}
	version, found := document.Versions[latest]
	if !found || strings.TrimSpace(version.Dist.Tarball) == "" {
		return "", fmt.Errorf("the package does not carry version %s", latest)
	}
	return strings.TrimSpace(version.Dist.Tarball), nil
}

func (i *PackageInstaller) fetch(ctx context.Context, doer MarketDoer, target string) ([]byte, error) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("build the download request: %w", errRequest)
	}
	response, errDo := doer.Do(request)
	if errDo != nil {
		return nil, fmt.Errorf("download the package: %w", errDo)
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("the package download was empty")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("the package download answered HTTP %d", response.StatusCode)
	}
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, pluginPackageMaxBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("read the package download: %w", errRead)
	}
	if int64(len(raw)) > pluginPackageMaxBytes {
		return nil, fmt.Errorf("the package exceeds the size limit")
	}
	return raw, nil
}

// unpackNpmTarball expands an npm tarball, which wraps everything in "package/".
func unpackNpmTarball(archive []byte, target string) error {
	gzipReader, errGzip := gzip.NewReader(bytes.NewReader(archive))
	if errGzip != nil {
		return fmt.Errorf("the package archive is not gzip: %w", errGzip)
	}
	defer func() { _ = gzipReader.Close() }()
	if errMkdir := os.MkdirAll(target, 0o700); errMkdir != nil {
		return fmt.Errorf("create the package directory: %w", errMkdir)
	}
	reader := tar.NewReader(gzipReader)
	var expanded int64
	entries := 0
	for {
		header, errNext := reader.Next()
		if errNext == io.EOF {
			break
		}
		if errNext != nil {
			return fmt.Errorf("read the package archive: %w", errNext)
		}
		entries++
		if entries > pluginPackageMaxEntries {
			return fmt.Errorf("the package archive has too many entries")
		}
		name := strings.TrimPrefix(filepath.ToSlash(header.Name), "./")
		relative := strings.TrimPrefix(name, "package/")
		if relative == "" || relative == name && !strings.HasPrefix(name, "package/") {
			// Everything in an npm tarball sits under package/; anything else is
			// not part of the package.
			continue
		}
		cleaned := filepath.Clean(relative)
		if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) || strings.Contains(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the package archive contains an unsafe path")
		}
		destination := filepath.Join(target, cleaned)
		switch header.Typeflag {
		case tar.TypeDir:
			if errMkdir := os.MkdirAll(destination, 0o700); errMkdir != nil {
				return fmt.Errorf("create a package directory: %w", errMkdir)
			}
		case tar.TypeReg:
			expanded += header.Size
			if expanded > pluginPackageMaxExpanded {
				return fmt.Errorf("the package expands beyond the size limit")
			}
			if errMkdir := os.MkdirAll(filepath.Dir(destination), 0o700); errMkdir != nil {
				return fmt.Errorf("create a package directory: %w", errMkdir)
			}
			file, errCreate := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if errCreate != nil {
				return fmt.Errorf("create a package file: %w", errCreate)
			}
			if _, errCopy := io.Copy(file, io.LimitReader(reader, pluginPackageMaxExpanded)); errCopy != nil {
				_ = file.Close()
				return fmt.Errorf("write a package file: %w", errCopy)
			}
			if errClose := file.Close(); errClose != nil {
				return fmt.Errorf("close a package file: %w", errClose)
			}
		}
	}
	return nil
}

// packagePathSegment turns a package name into one safe directory segment.
func packagePathSegment(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	replaced := strings.NewReplacer("/", "__", "@", "", "\\", "_", "..", "_").Replace(trimmed)
	cleaned := strings.Trim(replaced, "._")
	return cleaned
}

// escapePackagePath escapes a scoped npm name for a URL path.
func escapePackagePath(name string) string {
	return strings.ReplaceAll(name, "/", "%2F")
}
