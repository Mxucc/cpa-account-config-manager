package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

// newStringReadCloser wraps a literal body for a stubbed response.
func newStringReadCloser(value string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(value))
}

// newBytesReadCloser wraps a binary body for a stubbed response.
func newBytesReadCloser(value []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(value))
}

// parseURLForTest mirrors the URL parsing the host policy relies on.
func parseURLForTest(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

// selfUpdateRoundTripper answers the self-update endpoints from a table.
type selfUpdateRoundTripper func(*http.Request) (*http.Response, error)

func (f selfUpdateRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// selfUpdateTestArchive builds a release archive holding one plugin library.
func selfUpdateTestArchive(t *testing.T, version string, payload []byte) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	header := &zip.FileHeader{Name: selfUpdateTestLibraryName(version), Method: zip.Deflate}
	entry, errCreate := writer.CreateHeader(header)
	if errCreate != nil {
		t.Fatalf("create archive entry: %v", errCreate)
	}
	if _, errWrite := entry.Write(payload); errWrite != nil {
		t.Fatalf("write archive entry: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close archive: %v", errClose)
	}
	return buffer.Bytes()
}

func selfUpdateTestLibraryName(version string) string {
	extension := map[string]string{"darwin": ".dylib", "windows": ".dll"}[runtime.GOOS]
	if extension == "" {
		extension = ".so"
	}
	return PluginID + "-v" + version + extension
}

// selfUpdateTestService builds a service whose HTTP client is stubbed.
func selfUpdateTestService(t *testing.T, current string, handler func(*http.Request) (*http.Response, error)) *SelfUpdateService {
	t.Helper()
	service := NewSelfUpdateService(current)
	service.client = &http.Client{
		Transport: selfUpdateRoundTripper(handler),
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if !selfUpdateHostAllowed(request.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	t.Cleanup(service.Close)
	service.Configure(Config{DataDir: t.TempDir()})
	return service
}

func selfUpdateJSON(t *testing.T, status int, body any) *http.Response {
	t.Helper()
	encoded, errEncode := json.Marshal(body)
	if errEncode != nil {
		t.Fatalf("encode response: %v", errEncode)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       newStringReadCloser(string(encoded)),
	}
}

func TestSelfUpdateResolvesLatestReleaseThroughFallbacks(t *testing.T) {
	// The API answers first and wins.
	apiService := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(request.URL.String(), "https://api.github.com/repos/") {
			t.Fatalf("unexpected request %s", request.URL)
		}
		return selfUpdateJSON(t, http.StatusOK, map[string]any{"tag_name": "v1.2.3"}), nil
	})
	snapshot, errCheck := apiService.Check(context.Background())
	if errCheck != nil {
		t.Fatalf("check: %v", errCheck)
	}
	if snapshot.LatestVersion != "1.2.3" || snapshot.Source != selfUpdateSourceAPI || !snapshot.UpdateAvailable {
		t.Fatalf("api snapshot = %#v", snapshot)
	}
	if snapshot.AssetName != selfUpdateAssetName("1.2.3") || !strings.Contains(snapshot.AssetURL, "/releases/download/v1.2.3/") {
		t.Fatalf("api asset = %q / %q", snapshot.AssetName, snapshot.AssetURL)
	}

	// When the API fails (rate limit), the release redirect is used.
	redirectService := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		if strings.HasPrefix(request.URL.String(), "https://api.github.com/") {
			return selfUpdateJSON(t, http.StatusForbidden, map[string]any{"message": "rate limited"}), nil
		}
		if strings.Contains(request.URL.Path, "/releases/latest") {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://github.com/Mxucc/cpa-account-config-manager/releases/tag/v1.4.0"}},
				Body:       newStringReadCloser(""),
			}, nil
		}
		t.Fatalf("unexpected request %s", request.URL)
		return nil, nil
	})
	snapshot, errCheck = redirectService.Check(context.Background())
	if errCheck != nil || snapshot.LatestVersion != "1.4.0" || snapshot.Source != selfUpdateSourceRedirect {
		t.Fatalf("redirect snapshot = %#v err=%v", snapshot, errCheck)
	}

	// When both fail, the Atom feed still resolves the version.
	atomService := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(request.URL.String(), "https://api.github.com/"):
			return selfUpdateJSON(t, http.StatusForbidden, map[string]any{"message": "rate limited"}), nil
		case strings.Contains(request.URL.Path, "/releases/latest"):
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: newStringReadCloser("")}, nil
		case strings.HasSuffix(request.URL.Path, "/releases.atom"):
			// GitHub fills <title> with the release name, so the tag has to come from the
			// entry link or id; the stub mirrors that shape on purpose.
			feed := `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><id>tag:github.com,2008:Repository/1/v1.5.2</id><link rel="alternate" type="text/html" href="https://github.com/Mxucc/cpa-account-config-manager/releases/tag/v1.5.2"/><title>CPA Account Config Manager v1.5.2</title></entry></feed>`
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newStringReadCloser(feed)}, nil
		}
		t.Fatalf("unexpected request %s", request.URL)
		return nil, nil
	})
	snapshot, errCheck = atomService.Check(context.Background())
	if errCheck != nil || snapshot.LatestVersion != "1.5.2" || snapshot.Source != selfUpdateSourceAtom {
		t.Fatalf("atom snapshot = %#v err=%v", snapshot, errCheck)
	}

	// Nothing resolvable is reported as an error, never as "up to date".
	failingService := selfUpdateTestService(t, "1.0.0", func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}, Body: newStringReadCloser("")}, nil
	})
	if _, errFail := failingService.Check(context.Background()); errFail == nil {
		t.Fatalf("an unresolvable check must report an error")
	}
	if failingService.Snapshot().UpdateAvailable {
		t.Fatalf("an unresolvable check must not report an available update")
	}
}

func TestSelfUpdateInstallVerifiesChecksumAndAppliesAtomically(t *testing.T) {
	version := "9.9.9"
	archive := selfUpdateTestArchive(t, version, []byte("new-plugin-library"))
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + selfUpdateAssetName(version) + "\n"

	pluginDir := t.TempDir()
	pluginFile := filepath.Join(pluginDir, PluginID+".so")
	if errWrite := os.WriteFile(pluginFile, []byte("old-plugin-library"), 0o755); errWrite != nil {
		t.Fatalf("seed plugin file: %v", errWrite)
	}

	service := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		url := request.URL.String()
		switch {
		case strings.HasPrefix(url, "https://api.github.com/"):
			return selfUpdateJSON(t, http.StatusOK, map[string]any{"tag_name": "v" + version}), nil
		case strings.HasSuffix(url, "/checksums.txt"):
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newStringReadCloser(checksums)}, nil
		case strings.HasSuffix(url, ".zip"):
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newBytesReadCloser(archive)}, nil
		}
		t.Fatalf("unexpected request %s", url)
		return nil, nil
	})
	if _, errFile := service.SetPluginFile(pluginFile); errFile != nil {
		t.Fatalf("set plugin file: %v", errFile)
	}

	snapshot, errInstall := service.Install(context.Background())
	if errInstall != nil {
		t.Fatalf("install: %v", errInstall)
	}
	if !snapshot.ChecksumOK || snapshot.AppliedVersion != version || !snapshot.RestartRequired {
		t.Fatalf("install snapshot = %#v", snapshot)
	}
	installed, errRead := os.ReadFile(pluginFile)
	if errRead != nil || string(installed) != "new-plugin-library" {
		t.Fatalf("installed library = %q err=%v", installed, errRead)
	}
	// The previous library is kept for a rollback.
	backup, errBackup := os.ReadFile(pluginFile + ".previous")
	if errBackup != nil || string(backup) != "old-plugin-library" {
		t.Fatalf("backup library = %q err=%v", backup, errBackup)
	}
	// The staged copy is removed by the rename, so no half-written file remains.
	if _, errStat := os.Stat(pluginFile + "." + selfUpdateStagedFileName); errStat == nil {
		t.Fatalf("the staged file was left behind")
	}

	// A tampered archive is rejected and never applied.
	tampered := selfUpdateTestArchive(t, version, []byte("tampered-library"))
	tamperedSum := sha256.Sum256(tampered)
	_ = tamperedSum
	tamperedService := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		url := request.URL.String()
		switch {
		case strings.HasPrefix(url, "https://api.github.com/"):
			return selfUpdateJSON(t, http.StatusOK, map[string]any{"tag_name": "v" + version}), nil
		case strings.HasSuffix(url, "/checksums.txt"):
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newStringReadCloser(checksums)}, nil
		case strings.HasSuffix(url, ".zip"):
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newBytesReadCloser(tampered)}, nil
		}
		t.Fatalf("unexpected request %s", url)
		return nil, nil
	})
	if _, errFile := tamperedService.SetPluginFile(pluginFile); errFile != nil {
		t.Fatalf("set plugin file: %v", errFile)
	}
	if _, errTampered := tamperedService.Install(context.Background()); errTampered == nil ||
		!strings.Contains(errTampered.Error(), "checksum") {
		t.Fatalf("tampered install error = %v", errTampered)
	}
	afterTamper, _ := os.ReadFile(pluginFile)
	if string(afterTamper) != "new-plugin-library" {
		t.Fatalf("a rejected update modified the installed library: %q", afterTamper)
	}
}

func TestSelfUpdateHelpers(t *testing.T) {
	// Repository parsing accepts the configured shapes and rejects anything else.
	for repository, want := range map[string]string{
		"https://github.com/Mxucc/cpa-account-config-manager":     "Mxucc/cpa-account-config-manager",
		"https://github.com/Mxucc/cpa-account-config-manager.git": "Mxucc/cpa-account-config-manager",
		"https://github.com/Mxucc/cpa-account-config-manager/":    "Mxucc/cpa-account-config-manager",
		"https://gitlab.com/Mxucc/other":                          "",
		"not-a-url":                                               "",
	} {
		if got := selfUpdateRepoSlug(repository); got != want {
			t.Fatalf("selfUpdateRepoSlug(%q) = %q, want %q", repository, got, want)
		}
	}
	// Downloads are restricted to GitHub hosts over HTTPS.
	for target, want := range map[string]bool{
		"https://github.com/a/b/releases/download/v1/x.zip": true,
		"https://objects.githubusercontent.com/x":           true,
		"https://api.github.com/repos/a/b/releases/latest":  true,
		"http://github.com/a/b":                             false,
		"https://evil.example.com/x.zip":                    false,
		"https://github.com.evil.example/x":                 false,
	} {
		parsed, errParse := parseURLForTest(target)
		if errParse != nil {
			t.Fatalf("parse %q: %v", target, errParse)
		}
		if got := selfUpdateHostAllowed(parsed); got != want {
			t.Fatalf("selfUpdateHostAllowed(%q) = %v, want %v", target, got, want)
		}
	}
	// Checksum lookup tolerates the checksum-tool prefix and any column width.
	checksums := "aaa  other.zip\n" + strings.Repeat("b", 64) + " *" + "target.zip\n"
	if got := selfUpdateExpectedChecksum(checksums, "target.zip"); got != strings.Repeat("b", 64) {
		t.Fatalf("checksum lookup = %q", got)
	}
	if got := selfUpdateExpectedChecksum(checksums, "missing.zip"); got != "" {
		t.Fatalf("missing checksum = %q", got)
	}
	// The asset name always targets the running platform.
	name := selfUpdateAssetName("1.2.3")
	if !strings.Contains(name, runtime.GOOS) || !strings.Contains(name, runtime.GOARCH) || !strings.HasSuffix(name, ".zip") {
		t.Fatalf("asset name = %q", name)
	}
}

func TestSelfUpdateRoutesRequireKeyAndReportState(t *testing.T) {
	app := NewApp(&fakeAuthHost{}, nil)
	app.Configure([]byte("data_dir: " + t.TempDir()))

	// A 401 without the management key.
	unauthorized := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/self-update",
	})
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without key = %d", unauthorized.StatusCode)
	}

	headers := http.Header{"Authorization": []string{"Bearer management-secret"}}
	read := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: "/v0/management" + managementRoutePrefix + "/self-update", Headers: headers,
	})
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d body=%s", read.StatusCode, read.Body)
	}
	var payload struct {
		SelfUpdate SelfUpdateSnapshot `json:"self_update"`
	}
	if errDecode := json.Unmarshal(read.Body, &payload); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if payload.SelfUpdate.CurrentVersion != PluginVersion {
		t.Fatalf("current version = %q, want %q", payload.SelfUpdate.CurrentVersion, PluginVersion)
	}
	// The snapshot never carries credentials, only paths and versions.
	encoded := string(read.Body)
	for _, secret := range []string{"management-secret", "Bearer"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("the snapshot leaked %q: %s", secret, encoded)
		}
	}

	// The settings route records an explicit plugin file.
	settings := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPut, Path: "/v0/management" + managementRoutePrefix + "/self-update/settings", Headers: headers,
		Body: []byte(`{"plugin_file":"/opt/cpa/plugins/cpa-account-config-manager.so"}`),
	})
	if settings.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d body=%s", settings.StatusCode, settings.Body)
	}
	if got := app.selfUpdate.Snapshot().PluginFile; got != "/opt/cpa/plugins/cpa-account-config-manager.so" {
		t.Fatalf("recorded plugin file = %q", got)
	}
}

// A restarted plugin must not report "up to date" just because no check has run yet: the
// resolved release is restored from disk with its derived asset and availability.
func TestSelfUpdateRestoresTheResolvedReleaseFromDisk(t *testing.T) {
	dataDir := t.TempDir()
	store := selfUpdateStorePath(dataDir)
	writeStore := func(persisted persistedSelfUpdate) {
		t.Helper()
		encoded, errEncode := json.Marshal(persisted)
		if errEncode != nil {
			t.Fatalf("encode store: %v", errEncode)
		}
		if errWrite := os.WriteFile(store, encoded, 0o600); errWrite != nil {
			t.Fatalf("write store: %v", errWrite)
		}
	}
	writeStore(persistedSelfUpdate{
		Version:       selfUpdateVersion,
		LatestVersion: "0.3.1416",
		Source:        selfUpdateSourceAPI,
		CheckedAt:     time.Now().UTC(),
	})

	service := NewSelfUpdateService("0.3.1415")
	t.Cleanup(service.Close)
	service.Configure(Config{DataDir: dataDir})
	snapshot := service.Snapshot()
	if !snapshot.UpdateAvailable {
		t.Fatalf("a restored release must stay installable: %#v", snapshot)
	}
	if snapshot.AssetName != selfUpdateAssetName("0.3.1416") || snapshot.AssetURL == "" {
		t.Fatalf("derived asset = %q / %q", snapshot.AssetName, snapshot.AssetURL)
	}

	// Once the library on disk was replaced, the same release must not be offered again
	// before the restart: the operator only has to restart CPA.
	writeStore(persistedSelfUpdate{
		Version:        selfUpdateVersion,
		LatestVersion:  "0.3.1416",
		AppliedVersion: "0.3.1416",
		Source:         selfUpdateSourceAPI,
	})
	restarted := NewSelfUpdateService("0.3.1415")
	t.Cleanup(restarted.Close)
	restarted.Configure(Config{DataDir: dataDir})
	applied := restarted.Snapshot()
	if applied.UpdateAvailable {
		t.Fatalf("an already applied release must not be offered again: %#v", applied)
	}
	if applied.AppliedVersion != "0.3.1416" {
		t.Fatalf("applied version = %q", applied.AppliedVersion)
	}
}

// Two installations must never interleave: the loser gets a clear message instead of
// fighting over the same staged file.
func TestSelfUpdateSerializesConcurrentInstalls(t *testing.T) {
	version := "9.9.9"
	archive := selfUpdateTestArchive(t, version, []byte("new-plugin-library"))
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + selfUpdateAssetName(version) + "\n"

	pluginDir := t.TempDir()
	pluginFile := filepath.Join(pluginDir, PluginID+".so")
	if errWrite := os.WriteFile(pluginFile, []byte("old-plugin-library"), 0o755); errWrite != nil {
		t.Fatalf("seed plugin file: %v", errWrite)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	service := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		url := request.URL.String()
		switch {
		case strings.HasPrefix(url, "https://api.github.com/"):
			return selfUpdateJSON(t, http.StatusOK, map[string]any{"tag_name": "v" + version}), nil
		case strings.HasSuffix(url, "/checksums.txt"):
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newStringReadCloser(checksums)}, nil
		case strings.HasSuffix(url, ".zip"):
			close(entered)
			<-release
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newBytesReadCloser(archive)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: newStringReadCloser("")}, nil
	})
	if _, errFile := service.SetPluginFile(pluginFile); errFile != nil {
		t.Fatalf("set plugin file: %v", errFile)
	}

	finished := make(chan error, 1)
	go func() {
		_, errInstall := service.Install(context.Background())
		finished <- errInstall
	}()
	<-entered
	if _, errSecond := service.Install(context.Background()); errSecond == nil || !strings.Contains(errSecond.Error(), "already running") {
		t.Fatalf("concurrent install error = %v", errSecond)
	}
	close(release)
	if errFirst := <-finished; errFirst != nil {
		t.Fatalf("first install: %v", errFirst)
	}
	installed, errRead := os.ReadFile(pluginFile)
	if errRead != nil || string(installed) != "new-plugin-library" {
		t.Fatalf("installed library = %q err=%v", installed, errRead)
	}
}

// A release *name* or a non-version tag must never become a download URL, and only a
// plugin library may be recorded as the replacement target.
func TestSelfUpdateRejectsUnusableReferencesAndTargets(t *testing.T) {
	namedFeed := `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>CPA Account Config Manager v1.5.2</title></entry></feed>`
	service := selfUpdateTestService(t, "1.0.0", func(request *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(request.URL.String(), "https://api.github.com/"):
			return selfUpdateJSON(t, http.StatusForbidden, map[string]any{"message": "rate limited"}), nil
		case strings.Contains(request.URL.Path, "/releases/latest"):
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: newStringReadCloser("")}, nil
		case strings.HasSuffix(request.URL.Path, "/releases.atom"):
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: newStringReadCloser(namedFeed)}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: newStringReadCloser("")}, nil
	})
	if _, errCheck := service.Check(context.Background()); errCheck == nil {
		t.Fatalf("a release name must not be accepted as a version")
	}

	for value, want := range map[string]bool{
		"v1.2.3": true, "1.2.3": true, "v0.3.1416": true,
		"release-2024": false, "v1.2": false, "": false, "v1.2.3.4": false, "v1.2.3-rc1": false,
	} {
		if got := selfUpdateVersionValid(value); got != want {
			t.Fatalf("selfUpdateVersionValid(%q) = %v, want %v", value, got, want)
		}
	}

	for path, want := range map[string]bool{
		"/opt/cpa/plugins/cpa-account-config-manager.so":    true,
		"/opt/cpa/plugins/cpa-account-config-manager.dylib": true,
		"C:\\cpa\\plugins\\cpa-account-config-manager.dll":  true,
		"/etc/cpa/config.yaml":                              false,
		"/opt/cpa/plugins/cpa-account-config-manager.yaml":  false,
		"/opt/cpa/plugins/another-plugin.so":                false,
	} {
		if got := selfUpdateLibraryTarget(path); got != want {
			t.Fatalf("selfUpdateLibraryTarget(%q) = %v, want %v", path, got, want)
		}
	}

	target := selfUpdateTestService(t, "1.0.0", func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no request is expected")
	})
	if _, errSet := target.SetPluginFile("/etc/cpa/config.yaml"); errSet == nil {
		t.Fatalf("an unrelated file must not become the update target")
	}
	if got := target.Snapshot().PluginFile; got == "/etc/cpa/config.yaml" {
		t.Fatalf("a rejected path must not be recorded: %q", got)
	}
}

// The archive must be a zip that really contains this plugin's library.
func TestSelfUpdateArchiveMustCarryThePluginLibrary(t *testing.T) {
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	entry, errCreate := writer.Create("docs/readme.txt")
	if errCreate != nil {
		t.Fatalf("create entry: %v", errCreate)
	}
	if _, errWrite := entry.Write([]byte("no library here")); errWrite != nil {
		t.Fatalf("write entry: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close archive: %v", errClose)
	}
	if _, errExtract := selfUpdateExtractLibrary(buffer.Bytes()); errExtract == nil {
		t.Fatalf("an archive without the plugin library must be rejected")
	}
	if _, errBundle := selfUpdateExtractLibrary([]byte("not a zip at all")); errBundle == nil {
		t.Fatalf("a non-zip archive must be rejected")
	}
}
