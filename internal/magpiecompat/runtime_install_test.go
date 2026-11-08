package magpiecompat

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type runtimeDoer struct {
	body   []byte
	status int
	seen   []string
}

func (d *runtimeDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.String())
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(bytes.NewReader(d.body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// zipFixture builds the runtime archive shape: <dir>/bun plus noise.
func zipFixture(t *testing.T, name string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct{ name, body string }{
		{"bun-linux-x64/README.md", "docs"},
		{"bun-linux-x64/" + name, "#!/bin/sh\n"},
	} {
		handle, errCreate := writer.Create(entry.name)
		if errCreate != nil {
			t.Fatalf("create entry: %v", errCreate)
		}
		if _, errWrite := handle.Write([]byte(entry.body)); errWrite != nil {
			t.Fatalf("write entry: %v", errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close zip: %v", errClose)
	}
	return buffer.Bytes()
}

// A host without a runtime must be able to install one through the plugin, which
// is what makes a community provider's sign-in possible at all.
func TestInstallRuntimeWritesTheExecutable(t *testing.T) {
	name := "bun"
	if runtime.GOOS == "windows" {
		name = "bun.exe"
	}
	doer := &runtimeDoer{body: zipFixture(t, name), status: http.StatusOK}
	dataDir := t.TempDir()
	result, errInstall := InstallRuntime(context.Background(), doer, dataDir)
	if errInstall != nil {
		t.Fatalf("InstallRuntime: %v", errInstall)
	}
	if !result.Installed {
		t.Fatalf("result = %+v", result)
	}
	target := RuntimePath(dataDir)
	if result.Path != target {
		t.Fatalf("path = %q, want %q", result.Path, target)
	}
	info, errStat := os.Stat(target)
	if errStat != nil {
		t.Fatalf("stat runtime: %v", errStat)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the runtime is not executable: %v", info.Mode())
	}
	if len(doer.seen) != 1 || !strings.Contains(doer.seen[0], "bun-") {
		t.Fatalf("requests = %v", doer.seen)
	}
}

func TestInstallRuntimeReportsADownloadFailure(t *testing.T) {
	doer := &runtimeDoer{body: []byte("nope"), status: http.StatusNotFound}
	if _, errInstall := InstallRuntime(context.Background(), doer, t.TempDir()); errInstall == nil {
		t.Fatal("a 404 download was accepted")
	}
}

func TestInstallRuntimeNeedsADataDirectory(t *testing.T) {
	if _, errInstall := InstallRuntime(context.Background(), &runtimeDoer{}, ""); errInstall == nil {
		t.Fatal("an empty data directory was accepted")
	}
}

// The plugin-managed copy is found when PATH has no runtime.
func TestManagedRuntimeIsAFallback(t *testing.T) {
	dataDir := t.TempDir()
	target := RuntimePath(dataDir)
	if errMkdir := os.MkdirAll(filepath.Dir(target), 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	if errWrite := os.WriteFile(target, []byte("x"), 0o755); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	t.Setenv("CPA_ACCOUNT_MANAGER_DATA_DIR", dataDir)
	if resolved := resolveBun(); resolved == "" {
		t.Fatal("the managed runtime was not found")
	}
}
