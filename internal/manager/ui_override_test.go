package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const testUIPage = "<!doctype html><html><body>new interface</body></html>"

// resetUIOverrideCache clears the module-level cache so one test cannot hide a staged file
// from the next.
func resetUIOverrideCache(t *testing.T) {
	t.Helper()
	uiOverrideCache.mu.Lock()
	uiOverrideCache.path = ""
	uiOverrideCache.loadedAt = time.Time{}
	uiOverrideCache.found = false
	uiOverrideCache.mu.Unlock()
}

// The interface is compiled into the library, so a UI-only release could not take effect
// without a CPA restart. A staged copy is now served instead, which makes those updates
// apply on a page refresh.
func TestStagedInterfaceIsServedInsteadOfTheEmbeddedCopy(t *testing.T) {
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	app := NewApp(&fakeAuthHost{}, []byte("<!doctype html><html><body>embedded</body></html>"))
	app.Configure([]byte("data_dir: " + dataDir))

	read := func() string {
		response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
			Method: http.MethodGet, Path: resourceRoutePrefix + "/index.html",
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", response.StatusCode)
		}
		return string(response.Body)
	}
	if body := read(); body == "" || !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("initial body = %q", body)
	}

	path, errStage := stageUIOverride(dataDir, []byte(testUIPage))
	if errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if path != uiOverridePath(dataDir) {
		t.Fatalf("staged path = %q", path)
	}
	if body := read(); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("the staged interface was not served: %q", body)
	}

	// A failed staging attempt must keep the embedded copy working.
	if _, errBad := stageUIOverride(dataDir, []byte("not html")); errBad == nil {
		t.Fatalf("a non-HTML payload must be rejected")
	}
	if _, errEmpty := stageUIOverride(dataDir, nil); errEmpty == nil {
		t.Fatalf("an empty payload must be rejected")
	}
	if body := read(); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("the rejected payload changed the served page: %q", body)
	}
	// The staged file is private state, like every other file in the data directory.
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat: %v", errStat)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("staged interface permissions = %v", info.Mode().Perm())
	}
}

// A release carries the built interface next to the library; the updater stages it and
// reports that the interface (but not the library) is already live.
func TestSelfUpdateStagesTheInterfaceFromTheRelease(t *testing.T) {
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	service := selfUpdateTestService(t, "1.0.0", nil)
	service.Configure(Config{DataDir: dataDir})

	archive := selfUpdateTestArchiveWithUI(t, "9.9.9", []byte("new-plugin-library"), []byte(testUIPage))
	payload, ok := selfUpdateExtractUI(archive)
	if !ok || string(payload) != testUIPage {
		t.Fatalf("extract UI = %q ok=%v", payload, ok)
	}
	// A release without the interface member (older releases) is not an error.
	if _, okOld := selfUpdateExtractUI(selfUpdateTestArchive(t, "9.9.9", []byte("library"))); okOld {
		t.Fatalf("a library-only archive must not report an interface")
	}

	staged, errStage := stageUIOverride(dataDir, payload)
	if errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	served, errRead := os.ReadFile(staged)
	if errRead != nil || string(served) != testUIPage {
		t.Fatalf("served interface = %q err=%v", served, errRead)
	}
	// The file is not inside a directory the host would ever execute or import.
	if filepath.Dir(staged) != filepath.Join(dataDir, "ui") {
		t.Fatalf("staged interface was written to %q", staged)
	}
}

// selfUpdateTestArchiveWithUI builds a release archive holding the library and the interface.
func selfUpdateTestArchiveWithUI(t *testing.T, version string, library, ui []byte) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	header := &zip.FileHeader{Name: selfUpdateTestLibraryName(version), Method: zip.Deflate}
	entry, errCreate := writer.CreateHeader(header)
	if errCreate != nil {
		t.Fatalf("create library entry: %v", errCreate)
	}
	if _, errWrite := entry.Write(library); errWrite != nil {
		t.Fatalf("write library entry: %v", errWrite)
	}
	if ui != nil {
		uiHeader := &zip.FileHeader{Name: uiIndexArchiveMember, Method: zip.Deflate}
		uiEntry, errCreateUI := writer.CreateHeader(uiHeader)
		if errCreateUI != nil {
			t.Fatalf("create ui entry: %v", errCreateUI)
		}
		if _, errWrite := uiEntry.Write(ui); errWrite != nil {
			t.Fatalf("write ui entry: %v", errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close archive: %v", errClose)
	}
	return buffer.Bytes()
}
