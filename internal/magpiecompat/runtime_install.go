package magpiecompat

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Installing the JavaScript runtime.
//
// A community plugin's sign-in and model hooks are JavaScript, so they need a
// runtime. Asking an operator to install Bun on the gateway host by hand is not a
// feature: the plugin downloads the official release into its own data directory
// and uses that copy. An operator-installed runtime on PATH still wins, so a host
// that already has one is untouched.

const (
	// runtimeDownloadMaxBytes bounds one runtime archive (~100 MB unpacked).
	runtimeDownloadMaxBytes = 160 << 20
	// runtimeDownloadTimeout bounds the download.
	runtimeDownloadTimeout = 5 * time.Minute
	// runtimeReleaseBase is the official release host for the runtime.
	runtimeReleaseBase = "https://github.com/oven-sh/bun/releases/latest/download"
)

// RuntimeDoer performs the runtime download. It is injectable so a test never
// reaches the network.
type RuntimeDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RuntimeInstallResult reports what an install did.
type RuntimeInstallResult struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// runtimeArchiveName is the published asset for this platform.
func runtimeArchiveName() (string, bool) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "bun-linux-x64.zip", true
	case "linux/arm64":
		return "bun-linux-aarch64.zip", true
	case "darwin/amd64":
		return "bun-darwin-x64.zip", true
	case "darwin/arm64":
		return "bun-darwin-aarch64.zip", true
	case "windows/amd64":
		return "bun-windows-x64.zip", true
	default:
		return "", false
	}
}

// RuntimePath returns the executable a plugin-managed runtime would use.
func RuntimePath(dataDir string) string {
	name := "bun"
	if runtime.GOOS == "windows" {
		name = "bun.exe"
	}
	return filepath.Join(strings.TrimSpace(dataDir), "runtime", name)
}

// InstallRuntime downloads the runtime for this platform into dataDir and returns
// the executable path. It refuses to overwrite a runtime that already answers.
func InstallRuntime(ctx context.Context, doer RuntimeDoer, dataDir string) (RuntimeInstallResult, error) {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		return RuntimeInstallResult{}, fmt.Errorf("the plugin data directory is unavailable")
	}
	target := RuntimePath(dir)
	if info, errStat := os.Stat(target); errStat == nil && !info.IsDir() {
		return RuntimeInstallResult{Installed: false, Path: target, Reason: "a managed runtime is already installed"}, nil
	}
	asset, supported := runtimeArchiveName()
	if !supported {
		return RuntimeInstallResult{}, fmt.Errorf("no runtime release is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if doer == nil {
		doer = &http.Client{Timeout: runtimeDownloadTimeout}
	}
	downloadContext, cancel := context.WithTimeout(ctx, runtimeDownloadTimeout)
	defer cancel()
	request, errRequest := http.NewRequestWithContext(downloadContext, http.MethodGet, runtimeReleaseBase+"/"+asset, nil)
	if errRequest != nil {
		return RuntimeInstallResult{}, fmt.Errorf("build the runtime download: %w", errRequest)
	}
	response, errDo := doer.Do(request)
	if errDo != nil {
		return RuntimeInstallResult{}, fmt.Errorf("download the runtime: %w", errDo)
	}
	if response == nil || response.Body == nil {
		return RuntimeInstallResult{}, fmt.Errorf("the runtime download was empty")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RuntimeInstallResult{}, fmt.Errorf("the runtime download answered HTTP %d", response.StatusCode)
	}
	archive, errRead := io.ReadAll(io.LimitReader(response.Body, runtimeDownloadMaxBytes+1))
	if errRead != nil {
		return RuntimeInstallResult{}, fmt.Errorf("read the runtime download: %w", errRead)
	}
	if int64(len(archive)) > runtimeDownloadMaxBytes {
		return RuntimeInstallResult{}, fmt.Errorf("the runtime archive exceeds the size limit")
	}
	reader, errZip := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if errZip != nil {
		return RuntimeInstallResult{}, fmt.Errorf("the runtime archive is not a zip: %w", errZip)
	}
	// The archive nests the executable under a directory named after the asset;
	// take the first entry whose base name is the runtime.
	wanted := "bun"
	if runtime.GOOS == "windows" {
		wanted = "bun.exe"
	}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() || filepath.Base(entry.Name) != wanted {
			continue
		}
		if errMkdir := os.MkdirAll(filepath.Dir(target), 0o700); errMkdir != nil {
			return RuntimeInstallResult{}, fmt.Errorf("create the runtime directory: %w", errMkdir)
		}
		handle, errOpen := entry.Open()
		if errOpen != nil {
			return RuntimeInstallResult{}, fmt.Errorf("open the runtime in the archive: %w", errOpen)
		}
		file, errCreate := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if errCreate != nil {
			_ = handle.Close()
			return RuntimeInstallResult{}, fmt.Errorf("create the runtime file: %w", errCreate)
		}
		if _, errCopy := io.Copy(file, io.LimitReader(handle, runtimeDownloadMaxBytes)); errCopy != nil {
			_ = handle.Close()
			_ = file.Close()
			_ = os.Remove(target)
			return RuntimeInstallResult{}, fmt.Errorf("write the runtime: %w", errCopy)
		}
		_ = handle.Close()
		if errClose := file.Close(); errClose != nil {
			return RuntimeInstallResult{}, fmt.Errorf("close the runtime: %w", errClose)
		}
		if errChmod := os.Chmod(target, 0o755); errChmod != nil && runtime.GOOS != "windows" {
			return RuntimeInstallResult{}, fmt.Errorf("make the runtime executable: %w", errChmod)
		}
		return RuntimeInstallResult{Installed: true, Path: target}, nil
	}
	return RuntimeInstallResult{}, fmt.Errorf("the runtime archive does not contain %s", wanted)
}
