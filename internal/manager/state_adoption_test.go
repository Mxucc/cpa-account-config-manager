package manager

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStateFile creates one file with its parent directories, mirroring how a store writes state.
func writeStateFile(t *testing.T, path, content string) {
	t.Helper()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), errMkdir)
	}
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write %s: %v", path, errWrite)
	}
}

func readStateFile(t *testing.T, path string) string {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}
	return string(raw)
}

func TestAdoptStateDirectoryCopiesOnlyMissingFiles(t *testing.T) {
	previous := t.TempDir()
	target := t.TempDir()
	writeStateFile(t, filepath.Join(previous, "cline-pass.json"), `{"previous":true}`)
	writeStateFile(t, filepath.Join(previous, "opencode-quota.json"), `{"quota":true}`)
	// The target already holds a newer copy of one store: adoption must not touch it.
	writeStateFile(t, filepath.Join(target, "cline-pass.json"), `{"target":true}`)

	copied, errAdopt := adoptStateDirectory(previous, target)
	if errAdopt != nil {
		t.Fatalf("adoptStateDirectory() error = %v", errAdopt)
	}
	if copied != 1 {
		t.Fatalf("adopted %d files, want 1", copied)
	}
	if got := readStateFile(t, filepath.Join(target, "cline-pass.json")); got != `{"target":true}` {
		t.Fatalf("an existing target file was overwritten: %s", got)
	}
	if got := readStateFile(t, filepath.Join(target, "opencode-quota.json")); got != `{"quota":true}` {
		t.Fatalf("missing target file content = %s", got)
	}
}

func TestAdoptStateDirectoryCopiesNestedStoreDirectories(t *testing.T) {
	previous := t.TempDir()
	target := t.TempDir()
	writeStateFile(t, filepath.Join(previous, "operation-log", "manifest.json"), `{"entries":3}`)

	copied, errAdopt := adoptStateDirectory(previous, target)
	if errAdopt != nil {
		t.Fatalf("adoptStateDirectory() error = %v", errAdopt)
	}
	if copied != 1 {
		t.Fatalf("adopted %d files, want 1", copied)
	}
	if got := readStateFile(t, filepath.Join(target, "operation-log", "manifest.json")); got != `{"entries":3}` {
		t.Fatalf("nested store content = %s", got)
	}
}

// Locks and live runtime markers describe a running process, and staged plugin libraries carry
// absolute paths that only a reinstall can fix, so none of them may be adopted.
func TestAdoptStateDirectorySkipsLocksRuntimeMarkersAndStagedLibraries(t *testing.T) {
	previous := t.TempDir()
	target := t.TempDir()
	writeStateFile(t, filepath.Join(previous, "usage-snapshots.json.lock"), `lock`)
	writeStateFile(t, filepath.Join(previous, "runtime-instances", "scope", "instance.json"), `{"claim":true}`)
	writeStateFile(t, filepath.Join(previous, "plugins", "cpa-account-config-manager.so"), `library`)
	writeStateFile(t, filepath.Join(previous, "cline-pass.json"), `{"accounts":[]}`)

	copied, errAdopt := adoptStateDirectory(previous, target)
	if errAdopt != nil {
		t.Fatalf("adoptStateDirectory() error = %v", errAdopt)
	}
	if copied != 1 {
		t.Fatalf("adopted %d files, want only the state file", copied)
	}
	for _, unexpected := range []string{
		filepath.Join(target, "usage-snapshots.json.lock"),
		filepath.Join(target, "runtime-instances", "scope", "instance.json"),
		filepath.Join(target, "plugins", "cpa-account-config-manager.so"),
	} {
		if _, errStat := os.Stat(unexpected); errStat == nil {
			t.Fatalf("%s was adopted but must be skipped", filepath.Base(unexpected))
		}
	}
}

func TestAdoptStateDirectoryIsANoOpWithoutARealSource(t *testing.T) {
	target := t.TempDir()
	for name, previous := range map[string]string{
		"empty":     "",
		"same":      target,
		"not there": filepath.Join(t.TempDir(), "missing"),
	} {
		copied, errAdopt := adoptStateDirectory(previous, target)
		if errAdopt != nil || copied != 0 {
			t.Fatalf("%s: adoptStateDirectory() = (%d, %v), want (0, nil)", name, copied, errAdopt)
		}
	}
}

func TestAdoptStateDirectoryStopsAtItsFileBound(t *testing.T) {
	previous := t.TempDir()
	target := t.TempDir()
	for index := 0; index < 3; index++ {
		writeStateFile(t, filepath.Join(previous, "store-"+string(rune('a'+index))+".json"), `{}`)
	}
	originalMax := stateAdoptionMaxFiles
	stateAdoptionMaxFiles = 2
	t.Cleanup(func() { stateAdoptionMaxFiles = originalMax })

	copied, errAdopt := adoptStateDirectory(previous, target)
	if errAdopt != nil {
		t.Fatalf("adoptStateDirectory() error = %v", errAdopt)
	}
	if copied != 2 {
		t.Fatalf("adopted %d files, want the bound of 2", copied)
	}
}

func TestAdoptStateDirectorySkipsOversizedFiles(t *testing.T) {
	previous := t.TempDir()
	target := t.TempDir()
	writeStateFile(t, filepath.Join(previous, "big.json"), "0123456789")
	originalMaxSize := stateAdoptionMaxFileSize
	stateAdoptionMaxFileSize = 4
	t.Cleanup(func() { stateAdoptionMaxFileSize = originalMaxSize })

	copied, errAdopt := adoptStateDirectory(previous, target)
	if errAdopt != nil {
		t.Fatalf("adoptStateDirectory() error = %v", errAdopt)
	}
	if copied != 0 {
		t.Fatalf("adopted %d files, want the oversized file skipped", copied)
	}
}

// This is the regression the maintainer reported: the plugin state lived in a working-directory
// relative path, so a restart from another directory (or a container with a different working
// directory) showed no accounts at all. With the auth directory resolvable, state must be pinned
// beside it and must survive a restart from a different working directory.
func TestPluginStateStaysBesideTheAuthDirectoryAcrossRestarts(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	authDir := t.TempDir()
	t.Setenv(cpaAuthDirEnvVar, authDir)
	stateDir := filepath.Join(resolvedPath(t, authDir), usageDurableDirName)

	firstWorkingDir := t.TempDir()
	t.Chdir(firstWorkingDir)
	first := NewApp(&fakeAuthHost{}, []byte("index"))
	first.Configure(nil)
	if _, errSet := first.experiments.Set(ExperimentalSettings{AgentIdentityEnabled: true}); errSet != nil {
		t.Fatalf("persist state through the first instance: %v", errSet)
	}
	if errSet := first.clinePass.SetStripModelPrefix(false); errSet != nil {
		t.Fatalf("persist Cline Pass state through the first instance: %v", errSet)
	}
	first.Close()

	if _, errStat := os.Stat(filepath.Join(stateDir, clinePassStoreFileName)); errStat != nil {
		t.Fatalf("Cline Pass state was not written beside the auth directory: %v", errStat)
	}
	// Nothing may be written under the working directory any more: that path is what made the
	// accounts disappear when CPA restarted from somewhere else.
	if _, errStat := os.Stat(filepath.Join(firstWorkingDir, implicitDataDirName)); errStat == nil {
		t.Fatalf("state was written under the working directory (%s)", filepath.Join(firstWorkingDir, implicitDataDirName))
	}

	secondWorkingDir := t.TempDir()
	t.Chdir(secondWorkingDir)
	second := NewApp(&fakeAuthHost{}, []byte("index"))
	second.Configure(nil)
	defer second.Close()

	if second.clinePass.StripModelPrefix() {
		t.Fatal("the Cline Pass setting persisted by the first run is missing after a restart")
	}
	if !second.experiments.AgentIdentityEnabled() {
		t.Fatal("the experimental setting persisted by the first run is missing after a restart")
	}
}

// An operator who pinned a data directory and later lets the plugin resolve the auth directory must
// keep the accounts written under the pinned directory: the change adopts them.
func TestStateDirectoryChangeAdoptsThePreviousDirectory(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	authDir := t.TempDir()
	t.Setenv(cpaAuthDirEnvVar, authDir)
	t.Chdir(t.TempDir())

	pinnedDir := t.TempDir()
	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.Configure([]byte("data_dir: " + pinnedDir + "\n"))
	if errSet := app.clinePass.SetStripModelPrefix(false); errSet != nil {
		t.Fatalf("persist into the pinned directory: %v", errSet)
	}
	if _, errStat := os.Stat(filepath.Join(pinnedDir, clinePassStoreFileName)); errStat != nil {
		t.Fatalf("state was not pinned as asked: %v", errStat)
	}

	// The next configure drops the pin, so the plugin resolves the auth-adjacent directory.
	app.Configure(nil)
	app.WaitForReconfigure(5 * 1_000_000_000)

	adoptedPath := filepath.Join(pinnedDir, clinePassStoreFileName)
	if _, errStat := os.Stat(adoptedPath); errStat != nil {
		t.Fatalf("the pinned state file disappeared: %v", errStat)
	}
	if app.clinePass.StripModelPrefix() {
		t.Fatal("the setting written under the pinned directory was lost by the directory change")
	}
}

func TestStateAdoptionReadsTheFallbackDirectories(t *testing.T) {
	fallback := t.TempDir()
	target := t.TempDir()
	writeStateFile(t, filepath.Join(fallback, "opencode-quota.json"), `{"go":true}`)
	writeStateFile(t, filepath.Join(fallback, "cline-pass.json"), `{"version":1,"strip_model_prefix":false}`)
	// A store the target already owns must win over the fallback copy.
	writeStateFile(t, filepath.Join(target, "cline-pass.json"), `{"version":1,"strip_model_prefix":true}`)

	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	result := app.adoptStateDirectoryChange("", Config{DataDir: target, DataDirAlternates: []string{fallback}})

	if result.copied != 1 {
		t.Fatalf("adopted %d files, want only the missing one", result.copied)
	}
	if got := readStateFile(t, filepath.Join(target, "opencode-quota.json")); got != `{"go":true}` {
		t.Fatalf("fallback store content = %s", got)
	}
	if got := readStateFile(t, filepath.Join(target, "cline-pass.json")); got != `{"version":1,"strip_model_prefix":true}` {
		t.Fatalf("the target's own store was overwritten: %s", got)
	}
}

// This mirrors the live deployment: an older release kept its state under the process working
// directory, the plugin now pins state beside the CPA auth files, and the fallback directory is the
// only place the previous accounts still exist. They must be adopted instead of looking deleted.
func TestStateInAFallbackDirectoryIsAdoptedWhenStateIsPinnedBesideTheAuthDirectory(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	authDir := t.TempDir()
	t.Setenv(cpaAuthDirEnvVar, authDir)
	workingDir := t.TempDir()
	t.Chdir(workingDir)

	// What an earlier release left behind, under the implicit working-directory path.
	legacyDir := filepath.Join(workingDir, implicitDataDirName)
	writeStateFile(t, filepath.Join(legacyDir, clinePassStoreFileName), `{"version":1,"strip_model_prefix":false}`)

	app := NewApp(&fakeAuthHost{}, []byte("index"))
	defer app.Close()
	app.Configure(nil)

	if app.clinePass.StripModelPrefix() {
		t.Fatal("state written under the fallback directory was not adopted")
	}
	adopted := filepath.Join(resolvedPath(t, authDir), usageDurableDirName, clinePassStoreFileName)
	if _, errStat := os.Stat(adopted); errStat != nil {
		t.Fatalf("the adopted store is not beside the auth directory: %v", errStat)
	}
}
