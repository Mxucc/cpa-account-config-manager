package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarballFixture builds an npm-shaped tarball (everything under package/).
func tarballFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	for name, body := range files {
		header := &tar.Header{Name: "package/" + name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if errWrite := writer.WriteHeader(header); errWrite != nil {
			t.Fatalf("write header: %v", errWrite)
		}
		if _, errWrite := writer.Write([]byte(body)); errWrite != nil {
			t.Fatalf("write body: %v", errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close tar: %v", errClose)
	}
	if errClose := gzipWriter.Close(); errClose != nil {
		t.Fatalf("close gzip: %v", errClose)
	}
	return buffer.Bytes()
}

type installDoer struct {
	packument string
	tarball   []byte
	seen      []string
}

func (d *installDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.String())
	// The packument is a bare package URL; the tarball carries a path.
	if strings.HasSuffix(req.URL.Path, "%2Fopencode-zcode-auth") || strings.HasSuffix(req.URL.Path, "/opencode-zcode-auth") {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(d.packument)), Header: http.Header{}, Request: req}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(d.tarball)), Header: http.Header{}, Request: req}, nil
}

// A market import must put the package's code on disk: the sign-in and the model
// hooks live there, so a manifest-only import could never offer a sign-in.
func TestPackageInstallerUnpacksThePublishedPackage(t *testing.T) {
	root := t.TempDir()
	doer := &installDoer{
		packument: `{"dist-tags":{"latest":"0.1.57"},"versions":{"0.1.57":{"dist":{"tarball":"https://registry.npmjs.org/x/-/x-0.1.57.tgz"}}}}`,
		tarball: tarballFixture(t, map[string]string{
			"package.json": `{"name":"@magpie-community/opencode-zcode-auth","main":"./opencode.mjs"}`,
			"opencode.mjs": "export default {}",
		}),
	}
	installer := &PackageInstaller{Doer: doer, Root: root}
	directory, errInstall := installer.Install(context.Background(), "@magpie-community/opencode-zcode-auth")
	if errInstall != nil {
		t.Fatalf("Install: %v", errInstall)
	}
	manifest, errRead := os.ReadFile(filepath.Join(directory, "package.json"))
	if errRead != nil {
		t.Fatalf("read installed manifest: %v", errRead)
	}
	var document map[string]any
	if errUnmarshal := json.Unmarshal(manifest, &document); errUnmarshal != nil {
		t.Fatalf("decode installed manifest: %v", errUnmarshal)
	}
	if document["main"] != "./opencode.mjs" {
		t.Fatalf("manifest = %#v", document)
	}
	if _, errStat := os.Stat(filepath.Join(directory, "opencode.mjs")); errStat != nil {
		t.Fatalf("the module was not unpacked: %v", errStat)
	}
	if _, found := installer.Installed("@magpie-community/opencode-zcode-auth"); !found {
		t.Fatal("the installer does not report the package it installed")
	}
	if len(doer.seen) != 2 {
		t.Fatalf("requests = %v, want the packument then the tarball", doer.seen)
	}
}

// An archive that tries to escape the package directory is refused.
func TestPackageInstallerRefusesTraversal(t *testing.T) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	body := "x"
	_ = writer.WriteHeader(&tar.Header{Name: "package/../../escape.txt", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
	_ = gzipWriter.Close()

	target := filepath.Join(t.TempDir(), "pkg")
	if errUnpack := unpackNpmTarball(buffer.Bytes(), target); errUnpack == nil {
		t.Fatal("a traversal entry was accepted")
	}
}

func TestPackagePathSegmentKeepsAScopedNameFlat(t *testing.T) {
	segment := packagePathSegment("@magpie-community/opencode-zcode-auth")
	if strings.Contains(segment, "/") || strings.Contains(segment, "..") {
		t.Fatalf("segment = %q", segment)
	}
	if packagePathSegment("") != "" {
		t.Fatal("an empty name produced a directory")
	}
}
