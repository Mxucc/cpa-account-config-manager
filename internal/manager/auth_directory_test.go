package manager

import (
	"os"
	"path/filepath"
	"testing"
)

// resetAuthDirectoryMemo clears the process-wide resolution memo so one test cannot observe a
// directory another test resolved.
func resetAuthDirectoryMemo(t *testing.T) {
	t.Helper()
	authDirectoryMemo.mu.Lock()
	authDirectoryMemo.signature = ""
	authDirectoryMemo.value = ""
	authDirectoryMemo.valid = false
	authDirectoryMemo.mu.Unlock()
	t.Cleanup(func() {
		authDirectoryMemo.mu.Lock()
		authDirectoryMemo.signature = ""
		authDirectoryMemo.value = ""
		authDirectoryMemo.valid = false
		authDirectoryMemo.mu.Unlock()
	})
}

// resolvedPath mirrors what the resolver reports: it resolves symlinks, so on platforms where the
// temporary directory lives behind a link (macOS /var -> /private/var) the expectation has to be
// resolved the same way.
func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, errResolve := filepath.EvalSymlinks(path)
	if errResolve != nil {
		t.Fatalf("resolve %q: %v", path, errResolve)
	}
	return filepath.Clean(resolved)
}
func clearAuthDirectoryEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv(cpaAuthDirEnvVar, "")
	t.Setenv(cpaConfigPathEnvVar, "")
	t.Setenv(cpaConfigFileEnvVar, "")
}

func TestResolveAuthDirectoryPrefersItsEnvironmentVariable(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	authDir := t.TempDir()
	t.Setenv(cpaAuthDirEnvVar, authDir)

	if got := resolveAuthDirectory(); got != resolvedPath(t, authDir) {
		t.Fatalf("resolveAuthDirectory() = %q, want %q", got, resolvedPath(t, authDir))
	}
}

func TestResolveAuthDirectoryReadsBothCPAConfigSpellings(t *testing.T) {
	for _, key := range []string{"auth-dir", "auth_dir"} {
		t.Run(key, func(t *testing.T) {
			resetAuthDirectoryMemo(t)
			clearAuthDirectoryEnvironment(t)
			authDir := t.TempDir()
			configPath := filepath.Join(t.TempDir(), cpaConfigFileName)
			if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"+key+": "+authDir+"\n"), 0o600); errWrite != nil {
				t.Fatalf("write CPA config: %v", errWrite)
			}
			t.Setenv(cpaConfigPathEnvVar, configPath)

			if got := resolveAuthDirectory(); got != resolvedPath(t, authDir) {
				t.Fatalf("resolveAuthDirectory() = %q, want %q from %s", got, resolvedPath(t, authDir), key)
			}
		})
	}
}

func TestResolveAuthDirectoryHonoursTheLegacyConfigFileVariable(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	authDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), cpaConfigFileName)
	if errWrite := os.WriteFile(configPath, []byte("auth-dir: "+authDir+"\n"), 0o600); errWrite != nil {
		t.Fatalf("write CPA config: %v", errWrite)
	}
	t.Setenv(cpaConfigFileEnvVar, configPath)

	if got := resolveAuthDirectory(); got != resolvedPath(t, authDir) {
		t.Fatalf("resolveAuthDirectory() = %q, want %q", got, resolvedPath(t, authDir))
	}
}

func TestResolveAuthDirectoryExpandsTheHomePrefix(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if errMkdir := os.MkdirAll(filepath.Join(home, "auths"), 0o700); errMkdir != nil {
		t.Fatalf("mkdir auths: %v", errMkdir)
	}
	configPath := filepath.Join(t.TempDir(), cpaConfigFileName)
	if errWrite := os.WriteFile(configPath, []byte("auth-dir: ~/auths\n"), 0o600); errWrite != nil {
		t.Fatalf("write CPA config: %v", errWrite)
	}
	t.Setenv(cpaConfigPathEnvVar, configPath)

	want := resolvedPath(t, filepath.Join(home, "auths"))
	if got := resolveAuthDirectory(); got != want {
		t.Fatalf("resolveAuthDirectory() = %q, want %q", got, want)
	}
}

// A source that is present but unusable must not silently fall through to a different directory:
// the operator asked for a specific one, and guessing another would split the plugin's state.
func TestResolveAuthDirectoryRejectsUnusableValues(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	configPath := filepath.Join(t.TempDir(), cpaConfigFileName)
	if errWrite := os.WriteFile(configPath, []byte("auth-dir: \n"), 0o600); errWrite != nil {
		t.Fatalf("write CPA config: %v", errWrite)
	}
	t.Setenv(cpaConfigPathEnvVar, configPath)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(cpaAuthDirEnvVar, missing)
	if got := resolveAuthDirectory(); got != "" {
		t.Fatalf("a missing auth directory resolved to %q, want an empty result", got)
	}

	regularFile := filepath.Join(t.TempDir(), "auth-file.json")
	if errWrite := os.WriteFile(regularFile, []byte("{}"), 0o600); errWrite != nil {
		t.Fatalf("write regular file: %v", errWrite)
	}
	t.Setenv(cpaAuthDirEnvVar, regularFile)
	if got := resolveAuthDirectory(); got != "" {
		t.Fatalf("a regular file resolved to %q, want an empty result", got)
	}
}

func TestResolveAuthDirectoryWithoutAnySource(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	// The resolver falls back to config.yaml candidates derived from the process working
	// directory and executable, so a temporary working directory keeps this case hermetic.
	t.Chdir(t.TempDir())

	if got := resolveAuthDirectory(); got != "" {
		t.Fatalf("resolveAuthDirectory() = %q, want an empty result", got)
	}
}

// The memo only exists to avoid re-reading a file on every configure, so it must still notice a
// changed process input instead of pinning the first answer forever.
func TestResolveAuthDirectoryMemoFollowsItsInputs(t *testing.T) {
	resetAuthDirectoryMemo(t)
	clearAuthDirectoryEnvironment(t)
	first := t.TempDir()
	second := t.TempDir()

	t.Setenv(cpaAuthDirEnvVar, first)
	if got := resolveAuthDirectory(); got != resolvedPath(t, first) {
		t.Fatalf("first resolution = %q, want %q", got, resolvedPath(t, first))
	}
	t.Setenv(cpaAuthDirEnvVar, second)
	if got := resolveAuthDirectory(); got != resolvedPath(t, second) {
		t.Fatalf("second resolution = %q, want %q", got, resolvedPath(t, second))
	}
}
