package manager

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// The auth directory normally resolves from CPA's configuration before any host auth-list read.
// CPA_AUTH_DIR is the convention shared by sibling CPA plugins; the config file is located the
// same way CPA itself is started, so an installation that only has config.yaml still resolves.
const (
	cpaAuthDirEnvVar    = "CPA_AUTH_DIR"
	cpaConfigPathEnvVar = "CPA_CONFIG_PATH"
	cpaConfigFileEnvVar = "CPA_CONFIG_FILE"
	cpaConfigFileName   = "config.yaml"
)

// cpaConfigFile is the subset of CPA's configuration this resolver reads. CPA accepts both the
// dashed and the underscored spelling, so both are accepted here.
type cpaConfigFile struct {
	AuthDir  string `yaml:"auth-dir"`
	AuthDir2 string `yaml:"auth_dir"`
}

// authDirectoryMemo caches the last successful resolution. The config file does not change during
// a process lifetime in practice, but tests and the host reconfigure path may change the process
// inputs, so the memo records a signature of those inputs and recomputes when it changes. A miss
// is never memoized: CPA may still be writing its config file when the first configure runs.
var authDirectoryMemo struct {
	mu        sync.Mutex
	signature string
	value     string
	valid     bool
}

// resolveAuthDirectory reports CPA's auth directory as a resolved, existing directory, or "" when
// it cannot be determined safely. It never creates anything and never fails a caller.
func resolveAuthDirectory() string {
	signature := authDirectorySignature()
	authDirectoryMemo.mu.Lock()
	defer authDirectoryMemo.mu.Unlock()
	if authDirectoryMemo.valid && authDirectoryMemo.signature == signature {
		return authDirectoryMemo.value
	}
	value := resolveAuthDirectoryUncached()
	if value == "" {
		return ""
	}
	authDirectoryMemo.value = value
	authDirectoryMemo.signature = signature
	authDirectoryMemo.valid = true
	return value
}

// authDirectorySignature names every process input the resolver reads, so a cached value is only
// reused when the inputs that produced it are unchanged.
func authDirectorySignature() string {
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	return strings.Join([]string{
		os.Getenv(cpaAuthDirEnvVar),
		os.Getenv(cpaConfigPathEnvVar),
		os.Getenv(cpaConfigFileEnvVar),
		cwd,
		executable,
		strings.Join(os.Args, "\x00"),
	}, "\x1f")
}

// resolveAuthDirectoryUncached resolves CPA's auth directory in a fixed precedence order:
//
//  1. the CPA_AUTH_DIR environment variable;
//  2. CPA's config file, located by CPA_CONFIG_PATH, CPA_CONFIG_FILE, a --config argument, the
//     working directory, the CPA executable's directory, or the user home directory;
//  3. the `auth-dir`/`auth_dir` key of that file.
//
// A source that is present but unusable yields "": an explicitly configured directory that does
// not exist yet must never silently fall through to a different one.
func resolveAuthDirectoryUncached() string {
	if envDir := strings.TrimSpace(os.Getenv(cpaAuthDirEnvVar)); envDir != "" {
		return normalizeAuthDirectory(envDir)
	}
	configPath := locateCPAConfigFile()
	if configPath == "" {
		return ""
	}
	raw, errRead := os.ReadFile(configPath)
	if errRead != nil {
		// Never log or return the file content: it may carry credentials.
		return ""
	}
	var parsed cpaConfigFile
	if errUnmarshal := yaml.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return ""
	}
	authDir := strings.TrimSpace(parsed.AuthDir)
	if authDir == "" {
		authDir = strings.TrimSpace(parsed.AuthDir2)
	}
	return normalizeAuthDirectory(authDir)
}

// locateCPAConfigFile returns the first existing regular config.yaml candidate.
func locateCPAConfigFile() string {
	candidates := make([]string, 0, 8)
	candidates = append(candidates,
		os.Getenv(cpaConfigPathEnvVar),
		os.Getenv(cpaConfigFileEnvVar))
	candidates = append(candidates, configFileArguments()...)
	if cwd, errCwd := os.Getwd(); errCwd == nil {
		candidates = append(candidates, filepath.Join(cwd, cpaConfigFileName))
	}
	if executable, errExecutable := os.Executable(); errExecutable == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), cpaConfigFileName))
	}
	if home := userHomeDir(); home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".cli-proxy-api", cpaConfigFileName),
			filepath.Join(home, cpaConfigFileName))
	}
	for _, candidate := range candidates {
		path := strings.TrimSpace(candidate)
		if path == "" {
			continue
		}
		if info, errStat := os.Stat(path); errStat == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// configFileArguments extracts --config <path> and --config=<path> from the process arguments.
func configFileArguments() []string {
	args := os.Args
	if len(args) <= 1 {
		return nil
	}
	paths := make([]string, 0, 2)
	for index := 1; index < len(args); index++ {
		switch arg := args[index]; {
		case arg == "--config":
			if index+1 < len(args) {
				paths = append(paths, args[index+1])
				index++
			}
		case strings.HasPrefix(arg, "--config="):
			paths = append(paths, strings.TrimPrefix(arg, "--config="))
		}
	}
	return paths
}

// normalizeAuthDirectory expands a leading ~, resolves symlinks, and requires an existing
// directory. Anything else resolves to "" so a caller never receives a non-directory.
func normalizeAuthDirectory(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		home := userHomeDir()
		if home == "" {
			return ""
		}
		trimmed = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(trimmed, "~"), `/\`))
	}
	resolved, errResolve := filepath.EvalSymlinks(trimmed)
	if errResolve != nil {
		return ""
	}
	info, errStat := os.Stat(resolved)
	if errStat != nil || !info.IsDir() {
		return ""
	}
	return filepath.Clean(resolved)
}

// userHomeDir reports the user home directory, or "" when it is unknown.
func userHomeDir() string {
	home, errHome := os.UserHomeDir()
	if errHome != nil {
		return ""
	}
	return strings.TrimSpace(home)
}
