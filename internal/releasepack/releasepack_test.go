package releasepack

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackCreatesPluginStoreCompatibleArchive(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		goarch string
		ext    string
	}{
		{name: "linux amd64", goos: "linux", goarch: "amd64", ext: ".so"},
		{name: "linux arm64", goos: "linux", goarch: "arm64", ext: ".so"},
		{name: "darwin arm64", goos: "darwin", goarch: "arm64", ext: ".dylib"},
		{name: "windows amd64", goos: "windows", goarch: "amd64", ext: ".dll"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			libraryPath := filepath.Join(tempDir, "cpa-account-config-manager"+test.ext)
			libraryData := []byte("native-plugin-" + test.goos + "-" + test.goarch)
			if errWrite := os.WriteFile(libraryPath, libraryData, 0o755); errWrite != nil {
				t.Fatal(errWrite)
			}

			result, errPack := Pack(Options{
				PluginID:  "cpa-account-config-manager",
				Version:   "0.1.3",
				GOOS:      test.goos,
				GOARCH:    test.goarch,
				Library:   libraryPath,
				OutputDir: filepath.Join(tempDir, "release"),
			})
			if errPack != nil {
				t.Fatalf("Pack() error = %v", errPack)
			}
			archiveName := fmt.Sprintf("cpa-account-config-manager_0.1.3_%s_%s.zip", test.goos, test.goarch)
			if filepath.Base(result.ArchivePath) != archiveName {
				t.Fatalf("archive = %s", result.ArchivePath)
			}

			archive, errOpen := zip.OpenReader(result.ArchivePath)
			if errOpen != nil {
				t.Fatalf("open archive: %v", errOpen)
			}
			defer func() { _ = archive.Close() }()
			archiveLibrary := "cpa-account-config-manager-v0.1.3" + test.ext
			if len(archive.File) != 1 || archive.File[0].Name != archiveLibrary {
				t.Fatalf("archive entries = %#v, want %s", archive.File, archiveLibrary)
			}
			if archive.File[0].Mode().Perm() != 0o755 {
				t.Fatalf("library mode = %o", archive.File[0].Mode().Perm())
			}
			handle, errEntry := archive.File[0].Open()
			if errEntry != nil {
				t.Fatalf("open library entry: %v", errEntry)
			}
			extracted, errExtract := io.ReadAll(handle)
			if errExtract != nil {
				_ = handle.Close()
				t.Fatalf("read library entry: %v", errExtract)
			}
			_ = handle.Close()
			if string(extracted) != string(libraryData) {
				t.Fatalf("library data = %q", extracted)
			}

			archiveData, errRead := os.ReadFile(result.ArchivePath)
			if errRead != nil {
				t.Fatal(errRead)
			}
			digest := sha256.Sum256(archiveData)
			wantChecksum := hex.EncodeToString(digest[:])
			checksumData, errChecksum := os.ReadFile(result.ChecksumPath)
			if errChecksum != nil {
				t.Fatal(errChecksum)
			}
			wantChecksumLine := wantChecksum + "  " + archiveName + "\n"
			if result.SHA256 != wantChecksum || string(checksumData) != wantChecksumLine {
				t.Fatalf("checksum result = %#v file=%q", result, checksumData)
			}
		})
	}
}

func TestPackRejectsUnexpectedLibraryName(t *testing.T) {
	libraryPath := filepath.Join(t.TempDir(), "renamed.so")
	if errWrite := os.WriteFile(libraryPath, []byte("plugin"), 0o755); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, errPack := Pack(Options{
		PluginID: "cpa-account-config-manager",
		Version:  "0.1.0",
		GOOS:     "linux",
		GOARCH:   "amd64",
		Library:  libraryPath,
	})
	if errPack == nil || !strings.Contains(errPack.Error(), "library filename") {
		t.Fatalf("Pack() error = %v", errPack)
	}
}

func TestPluginExtensionMatchesCLIProxyAPIPlatforms(t *testing.T) {
	tests := map[string]string{
		"linux":   ".so",
		"darwin":  ".dylib",
		"windows": ".dll",
	}
	for goos, expected := range tests {
		extension, errExtension := pluginExtension(goos)
		if errExtension != nil || extension != expected {
			t.Fatalf("pluginExtension(%q) = %q, %v", goos, extension, errExtension)
		}
	}
	if _, errExtension := pluginExtension("plan9"); errExtension == nil {
		t.Fatal("pluginExtension(plan9) succeeded")
	}
}
