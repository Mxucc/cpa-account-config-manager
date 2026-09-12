package releasepack

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	versionPattern  = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
	goarchPattern   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

type Options struct {
	PluginID string
	Version  string
	GOOS     string
	GOARCH   string
	Library  string
	// UI is the built single-file interface that ships as ui/index.html in the archive.
	UI        string
	OutputDir string
}

type Result struct {
	ArchivePath  string
	ChecksumPath string
	SHA256       string
}

func Pack(options Options) (Result, error) {
	options.PluginID = strings.TrimSpace(options.PluginID)
	options.Version = strings.TrimSpace(options.Version)
	options.GOOS = strings.TrimSpace(options.GOOS)
	options.GOARCH = strings.TrimSpace(options.GOARCH)
	options.Library = strings.TrimSpace(options.Library)
	options.OutputDir = strings.TrimSpace(options.OutputDir)

	if !pluginIDPattern.MatchString(options.PluginID) {
		return Result{}, fmt.Errorf("invalid plugin id %q", options.PluginID)
	}
	if !versionPattern.MatchString(options.Version) || strings.HasPrefix(strings.ToLower(options.Version), "v") {
		return Result{}, fmt.Errorf("invalid plugin version %q", options.Version)
	}
	if !goarchPattern.MatchString(options.GOARCH) {
		return Result{}, fmt.Errorf("invalid GOARCH %q", options.GOARCH)
	}
	extension, errExtension := pluginExtension(options.GOOS)
	if errExtension != nil {
		return Result{}, errExtension
	}
	inputLibrary := options.PluginID + extension
	if filepath.Base(options.Library) != inputLibrary {
		return Result{}, fmt.Errorf("library filename must be %s", inputLibrary)
	}
	if options.OutputDir == "" {
		options.OutputDir = "dist/release"
	}

	libraryData, errRead := os.ReadFile(options.Library)
	if errRead != nil {
		return Result{}, fmt.Errorf("read library: %w", errRead)
	}
	if errMkdir := os.MkdirAll(options.OutputDir, 0o755); errMkdir != nil {
		return Result{}, fmt.Errorf("create output directory: %w", errMkdir)
	}

	archiveName := fmt.Sprintf("%s_%s_%s_%s.zip", options.PluginID, options.Version, options.GOOS, options.GOARCH)
	archiveLibrary := fmt.Sprintf("%s-v%s%s", options.PluginID, options.Version, extension)
	archivePath := filepath.Join(options.OutputDir, archiveName)
	archive, errCreate := os.Create(archivePath)
	if errCreate != nil {
		return Result{}, fmt.Errorf("create archive: %w", errCreate)
	}
	zipWriter := zip.NewWriter(archive)
	header := &zip.FileHeader{Name: archiveLibrary, Method: zip.Deflate}
	header.SetMode(0o755)
	entry, errEntry := zipWriter.CreateHeader(header)
	if errEntry == nil {
		_, errEntry = entry.Write(libraryData)
	}
	// The built interface travels with the release so the plugin can stage it and serve it
	// straight from disk: interface-only updates then apply on a page refresh instead of
	// waiting for a CPA restart. Older plugins ignore the extra member.
	if errEntry == nil && options.UI != "" {
		uiData, errReadUI := os.ReadFile(options.UI)
		if errReadUI != nil {
			errEntry = fmt.Errorf("read interface: %w", errReadUI)
		} else if len(bytes.TrimSpace(uiData)) == 0 {
			errEntry = fmt.Errorf("interface file %s is empty", options.UI)
		} else {
			uiHeader := &zip.FileHeader{Name: "ui/index.html", Method: zip.Deflate}
			uiHeader.SetMode(0o644)
			uiEntry, errCreateUI := zipWriter.CreateHeader(uiHeader)
			if errCreateUI != nil {
				errEntry = errCreateUI
			} else {
				_, errEntry = uiEntry.Write(uiData)
			}
		}
	}
	if errCloseZip := zipWriter.Close(); errEntry == nil {
		errEntry = errCloseZip
	}
	if errCloseArchive := archive.Close(); errEntry == nil {
		errEntry = errCloseArchive
	}
	if errEntry != nil {
		_ = os.Remove(archivePath)
		return Result{}, fmt.Errorf("write archive: %w", errEntry)
	}

	archiveData, errReadArchive := os.ReadFile(archivePath)
	if errReadArchive != nil {
		return Result{}, fmt.Errorf("read archive for checksum: %w", errReadArchive)
	}
	digest := sha256.Sum256(archiveData)
	checksum := hex.EncodeToString(digest[:])
	checksumPath := archivePath + ".sha256"
	checksumLine := checksum + "  " + archiveName + "\n"
	if errWrite := os.WriteFile(checksumPath, []byte(checksumLine), 0o644); errWrite != nil {
		return Result{}, fmt.Errorf("write checksum: %w", errWrite)
	}
	return Result{ArchivePath: archivePath, ChecksumPath: checksumPath, SHA256: checksum}, nil
}

func pluginExtension(goos string) (string, error) {
	switch goos {
	case "linux":
		return ".so", nil
	case "darwin":
		return ".dylib", nil
	case "windows":
		return ".dll", nil
	default:
		return "", fmt.Errorf("unsupported GOOS %q", goos)
	}
}
