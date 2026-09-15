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

const (
	testUIPage         = "<!doctype html><html><body>new interface</body></html>"
	testEmbeddedUIPage = "<!doctype html><html><body>embedded</body></html>"
)

// resetUIOverrideCache clears the module-level cache and diagnostic note so one test cannot
// hide a staged file from the next.
func resetUIOverrideCache(t *testing.T) {
	t.Helper()
	uiOverrideCache.mu.Lock()
	uiOverrideCache.path = ""
	uiOverrideCache.loadedAt = time.Time{}
	uiOverrideCache.found = false
	uiOverrideCache.fileExists = false
	uiOverrideCache.fileSize = 0
	uiOverrideCache.fileModTime = time.Time{}
	uiOverrideCache.versionExists = false
	uiOverrideCache.versionSize = 0
	uiOverrideCache.versionModTime = time.Time{}
	uiOverrideCache.mu.Unlock()
	setUIOverrideNote("")
}

// withRunningVersion pins the version the running library reports for one test.
func withRunningVersion(t *testing.T, version string) {
	t.Helper()
	original := PluginVersion
	PluginVersion = version
	t.Cleanup(func() { PluginVersion = original })
}

// uiOverrideTestApp builds an app whose interface is the embedded copy from the test.
func uiOverrideTestApp(t *testing.T, dataDir string) *App {
	t.Helper()
	app := NewApp(&fakeAuthHost{}, []byte(testEmbeddedUIPage))
	app.Configure([]byte("data_dir: " + dataDir))
	return app
}

// readUIOverridePage fetches the served index.html over the public management path.
func readUIOverridePage(t *testing.T, app *App) string {
	t.Helper()
	response := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: resourceRoutePrefix + "/index.html",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	return string(response.Body)
}

// writeUIOverrideFiles writes the staged interface and its version sidecar directly, the way
// an older or damaged release would leave them behind. A nil sidecar means "no sidecar".
func writeUIOverrideFiles(t *testing.T, dataDir string, page, version []byte) {
	t.Helper()
	pagePath := uiOverridePath(dataDir)
	if errMkdir := os.MkdirAll(filepath.Dir(pagePath), 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	if errWrite := os.WriteFile(pagePath, page, 0o600); errWrite != nil {
		t.Fatalf("write page: %v", errWrite)
	}
	if version == nil {
		if errRemove := os.Remove(uiOverrideVersionPath(dataDir)); errRemove != nil && !os.IsNotExist(errRemove) {
			t.Fatalf("remove sidecar: %v", errRemove)
		}
		return
	}
	if errWrite := os.WriteFile(uiOverrideVersionPath(dataDir), version, 0o600); errWrite != nil {
		t.Fatalf("write sidecar: %v", errWrite)
	}
}

// The interface is compiled into the library, so a UI-only release could not take effect
// without a CPA restart. A staged copy from a newer release is served instead, which makes
// those updates apply on a page refresh.
func TestStagedInterfaceIsServedInsteadOfTheEmbeddedCopy(t *testing.T) {
	withRunningVersion(t, "1.0.0")
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	app := uiOverrideTestApp(t, dataDir)

	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("initial body = %q", body)
	}

	path, errStage := stageUIOverride(dataDir, "1.2.0", []byte(testUIPage))
	if errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if path != uiOverridePath(dataDir) {
		t.Fatalf("staged path = %q", path)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("the staged interface was not served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != "" {
		t.Fatalf("served interface diagnostic = %q", note)
	}

	// A failed staging attempt must keep the embedded copy working.
	if _, errBad := stageUIOverride(dataDir, "1.3.0", []byte("not html")); errBad == nil {
		t.Fatalf("a non-HTML payload must be rejected")
	}
	if _, errEmpty := stageUIOverride(dataDir, "1.3.0", nil); errEmpty == nil {
		t.Fatalf("an empty payload must be rejected")
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("the rejected payload changed the served page: %q", body)
	}

	// The staged interface and its version sidecar are private state, like every other file
	// in the data directory.
	for _, staged := range []string{path, uiOverrideVersionPath(dataDir)} {
		info, errStat := os.Stat(staged)
		if errStat != nil {
			t.Fatalf("stat %s: %v", staged, errStat)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("staged file %s permissions = %v", staged, info.Mode().Perm())
		}
	}
	raw, errVersion := os.ReadFile(uiOverrideVersionPath(dataDir))
	if errVersion != nil || string(raw) != "1.2.0" {
		t.Fatalf("staged version = %q err=%v", raw, errVersion)
	}
}

// Regression for the live deployment: a host plugin-store install replaces only the library,
// so an override staged by an older release (which wrote index.html without the version
// sidecar) survived and kept masking the newer interface inside the running library. It must
// be ignored, and the decision must be visible in the self-update snapshot.
func TestStagedInterfaceWithoutVersionSidecarIsIgnored(t *testing.T) {
	withRunningVersion(t, "0.3.1432")
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	app := uiOverrideTestApp(t, dataDir)

	// The old release's footprint: the page, and no sidecar at all.
	writeUIOverrideFiles(t, dataDir, []byte(testUIPage), nil)
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("a stale interface without a version was served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != uiOverrideNoteNoVersion {
		t.Fatalf("diagnostic = %q, want %q", note, uiOverrideNoteNoVersion)
	}

	// A newer interface staged now is served, and removing its sidecar stops it immediately:
	// the stat cache must notice the sidecar change instead of serving a versionless file.
	if _, errStage := stageUIOverride(dataDir, "0.3.1433", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("a newer interface was not served: %q", body)
	}
	if errRemove := os.Remove(uiOverrideVersionPath(dataDir)); errRemove != nil {
		t.Fatalf("remove sidecar: %v", errRemove)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("the override was still served after its sidecar vanished: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != uiOverrideNoteNoVersion {
		t.Fatalf("diagnostic after sidecar removal = %q, want %q", note, uiOverrideNoteNoVersion)
	}

	// The operator sees the ignored override through the existing self-update snapshot.
	service := selfUpdateTestService(t, "0.3.1432", nil)
	service.Configure(Config{DataDir: dataDir})
	if got := service.Snapshot().UIOverrideNote; got != uiOverrideNoteNoVersion {
		t.Fatalf("snapshot ui_override_note = %q, want %q", got, uiOverrideNoteNoVersion)
	}
}

// Only a strictly newer release may override the embedded interface: an equal or older
// version would mask whatever the running library already carries.
func TestStagedInterfaceThatIsNotNewerThanTheRunningLibraryIsIgnored(t *testing.T) {
	withRunningVersion(t, "1.0.0")
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	app := uiOverrideTestApp(t, dataDir)

	// Equal version.
	if _, errStage := stageUIOverride(dataDir, "1.0.0", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("an equal-version interface was served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != uiOverrideNoteStale {
		t.Fatalf("diagnostic = %q, want %q", note, uiOverrideNoteStale)
	}

	// Older version, staged right after the equal one, is picked up without waiting.
	if _, errStage := stageUIOverride(dataDir, "0.9.9", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("an older interface was served: %q", body)
	}

	// Strictly newer is the one case that is served.
	if _, errStage := stageUIOverride(dataDir, "1.0.1", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("a newer interface was not served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != "" {
		t.Fatalf("diagnostic after serving = %q", note)
	}
}

// An interface-only self-update stages the sidecar and must be visible on the very next read,
// without a restart and without waiting out the stat interval, in both directions.
func TestStagingRewritesSidecarAndInvalidatesTheCache(t *testing.T) {
	withRunningVersion(t, "1.0.0")
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	app := uiOverrideTestApp(t, dataDir)

	// The first staging is ignored (equal version), which fills the negative cache.
	if _, errStage := stageUIOverride(dataDir, "1.0.0", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("an equal-version interface was served: %q", body)
	}

	// The same payload with a newer version must be served on the next read.
	if _, errStage := stageUIOverride(dataDir, "2.0.0", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	raw, errVersion := os.ReadFile(uiOverrideVersionPath(dataDir))
	if errVersion != nil || string(raw) != "2.0.0" {
		t.Fatalf("staged version = %q err=%v", raw, errVersion)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("the newly staged interface was not served: %q", body)
	}

	// Downgrading the staged copy (an older release staging again) takes effect just as fast.
	if _, errStage := stageUIOverride(dataDir, "0.5.0", []byte(testUIPage)); errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("the downgraded override was still served: %q", body)
	}
}

// A corrupt sidecar or a non-HTML payload degrades to the embedded interface instead of an
// error, and a fixed sidecar makes the same payload servable again.
func TestUnusableStagedInterfaceDegradesToTheEmbeddedCopy(t *testing.T) {
	withRunningVersion(t, "1.0.0")
	dataDir := t.TempDir()
	resetUIOverrideCache(t)
	app := uiOverrideTestApp(t, dataDir)

	// A non-HTML payload behind a valid newer version.
	writeUIOverrideFiles(t, dataDir, []byte("not html"), []byte("2.0.0"))
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("a non-HTML override was served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != uiOverrideNoteUnusable {
		t.Fatalf("diagnostic = %q, want %q", note, uiOverrideNoteUnusable)
	}

	// A valid page with an unparsable sidecar reads the same as an old release's file.
	writeUIOverrideFiles(t, dataDir, []byte(testUIPage), []byte("not-a-version"))
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("an unparsable version was served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != uiOverrideNoteNoVersion {
		t.Fatalf("diagnostic = %q, want %q", note, uiOverrideNoteNoVersion)
	}

	// An oversized sidecar is corrupt as well.
	writeUIOverrideFiles(t, dataDir, []byte(testUIPage), bytes.Repeat([]byte("1"), uiOverrideMaxVersionBytes+1))
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("embedded")) {
		t.Fatalf("an oversized version was served: %q", body)
	}
	if note := uiOverrideDiagnostic(); note != uiOverrideNoteNoVersion {
		t.Fatalf("diagnostic = %q, want %q", note, uiOverrideNoteNoVersion)
	}

	// Repairing the sidecar serves the same payload again.
	writeUIOverrideFiles(t, dataDir, []byte(testUIPage), []byte("2.0.0"))
	if body := readUIOverridePage(t, app); !bytes.Contains([]byte(body), []byte("new interface")) {
		t.Fatalf("a repaired override was not served: %q", body)
	}
}

// Staging needs a comparable release version, because an override without one is ignored by
// readers and must not be reported as a live interface update.
func TestStageUIOverrideRequiresAReleaseVersion(t *testing.T) {
	dataDir := t.TempDir()
	for _, version := range []string{"", "   ", "dev", "not-a-version"} {
		if _, errStage := stageUIOverride(dataDir, version, []byte(testUIPage)); errStage == nil {
			t.Fatalf("stageUIOverride() accepted version %q", version)
		}
	}
}

// A release carries the built interface next to the library; the updater stages it with the
// release version and reports that the interface (but not the library) is already live.
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

	staged, errStage := stageUIOverride(dataDir, "9.9.9", payload)
	if errStage != nil {
		t.Fatalf("stageUIOverride() error = %v", errStage)
	}
	served, errRead := os.ReadFile(staged)
	if errRead != nil || string(served) != testUIPage {
		t.Fatalf("served interface = %q err=%v", served, errRead)
	}
	rawVersion, errVersion := os.ReadFile(uiOverrideVersionPath(dataDir))
	if errVersion != nil || string(rawVersion) != "9.9.9" {
		t.Fatalf("staged version = %q err=%v", rawVersion, errVersion)
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
