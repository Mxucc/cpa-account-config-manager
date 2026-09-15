package manager

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stateAdoptionLockSuffix marks the private store lock files. They describe a live process, never
// durable state, so they must not be copied into a new directory.
const stateAdoptionLockSuffix = ".lock"

// stateAdoptionReasonCode is the allow-listed journal reason for one directory adoption.
const stateAdoptionReasonCode = "state_directory_adopted"

// stateAdoptionSkippedDirectories lists the top-level directories whose contents must never be
// adopted: staged plugin libraries keep absolute paths and have to be reinstalled, and runtime
// instance markers describe a process that is already gone after a restart.
var stateAdoptionSkippedDirectories = map[string]struct{}{
	"plugins":           {},
	"runtime-instances": {},
}

// The adoption ceilings are variables so tests can shrink them without moving hundreds of
// megabytes; production never changes them.
var (
	stateAdoptionMaxFiles     = 512
	stateAdoptionMaxTotalSize = int64(128 << 20)
	stateAdoptionMaxFileSize  = int64(64 << 20)
)

// adoptStateDirectory copies every regular state file from previousDir into targetDir, preserving
// the relative path, and returns how many files were copied. A file that already exists in the
// target is never overwritten, so a newer write always wins. Unusable inputs (empty, equal,
// missing or non-directory previous directory) are a no-op. The work is bounded; hitting a bound
// stops the walk cleanly and returns what was copied, without an error.
func adoptStateDirectory(previousDir, targetDir string) (int, error) {
	previous := strings.TrimSpace(previousDir)
	target := strings.TrimSpace(targetDir)
	if previous == "" || target == "" || filepath.Clean(previous) == filepath.Clean(target) {
		return 0, nil
	}
	previousInfo, errStat := os.Stat(previous)
	if errStat != nil || !previousInfo.IsDir() {
		return 0, nil
	}
	if errMkdir := os.MkdirAll(target, 0o700); errMkdir != nil {
		return 0, fmt.Errorf("create adopted state directory: %w", errMkdir)
	}

	copied := 0
	var totalSize int64
	var firstErr error
	recordError := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	walkErr := filepath.WalkDir(previous, func(path string, entry fs.DirEntry, errWalk error) error {
		if errWalk != nil {
			recordError(fmt.Errorf("read state directory entry: %w", errWalk))
			return nil
		}
		relative, errRelative := filepath.Rel(previous, path)
		if errRelative != nil {
			recordError(fmt.Errorf("resolve state entry path: %w", errRelative))
			return nil
		}
		if relative == "." {
			return nil
		}
		name := entry.Name()
		if shouldSkipStateAdoptionEntry(relative, name) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			// Symlinks, sockets and devices are never adopted: a state directory only holds
			// regular files, and following a link could copy something unrelated.
			return nil
		}
		if copied >= stateAdoptionMaxFiles {
			return fs.SkipAll
		}
		detail, errInfo := entry.Info()
		if errInfo != nil {
			recordError(fmt.Errorf("inspect state file %q: %w", name, errInfo))
			return nil
		}
		size := detail.Size()
		if size > stateAdoptionMaxFileSize || totalSize+size > stateAdoptionMaxTotalSize {
			return fs.SkipAll
		}
		targetPath := filepath.Join(target, relative)
		if errMkdir := os.MkdirAll(filepath.Dir(targetPath), 0o700); errMkdir != nil {
			recordError(fmt.Errorf("create adopted state subdirectory: %w", errMkdir))
			return nil
		}
		written, errCopy := copyStateFileExclusive(path, targetPath)
		if errCopy != nil {
			recordError(fmt.Errorf("adopt state file %q: %w", name, errCopy))
			return nil
		}
		if !written {
			return nil
		}
		copied++
		totalSize += size
		return nil
	})
	if walkErr != nil {
		recordError(fmt.Errorf("walk state directory: %w", walkErr))
	}
	return copied, firstErr
}

// shouldSkipStateAdoptionEntry reports whether one relative entry is out of scope for adoption.
func shouldSkipStateAdoptionEntry(relative, name string) bool {
	if strings.HasSuffix(strings.ToLower(name), stateAdoptionLockSuffix) {
		return true
	}
	segments := strings.Split(relative, string(filepath.Separator))
	if len(segments) == 0 {
		return false
	}
	_, skipped := stateAdoptionSkippedDirectories[segments[0]]
	return skipped
}

// copyStateFileExclusive copies one file only when the target does not exist yet. It reports
// whether a copy happened; an existing target is skipped, not overwritten or truncated.
func copyStateFileExclusive(sourcePath, targetPath string) (bool, error) {
	source, errOpen := os.Open(sourcePath)
	if errOpen != nil {
		return false, errOpen
	}
	defer source.Close()
	destination, errCreate := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errCreate != nil {
		if errors.Is(errCreate, fs.ErrExist) {
			return false, nil
		}
		return false, errCreate
	}
	_, errCopy := io.Copy(destination, source)
	errClose := destination.Close()
	if errCopy == nil {
		errCopy = errClose
	}
	if errCopy != nil {
		// A half-written store would look usable; remove it so a later adoption can retry.
		_ = os.Remove(targetPath)
		return false, errCopy
	}
	return true, nil
}

// stateAdoptionResult reports what one adoption attempt did. The result is recorded only after the
// operation journal has been pointed at the target directory, so the entry itself is observable in
// the new journal instead of disappearing with the old store.
type stateAdoptionResult struct {
	copied    int
	err       error
	targetDir string
	attempted bool
}

// adoptStateDirectoryChange runs the file-copy phase of adoption for one service configuration. It
// must be called with configApplyMu held, before the first service Configure for the new directory,
// so every store is re-opened against a directory that already holds the state of the previous one.
//
// It adopts from two kinds of source: the directory this instance used before (a resolved state
// directory can change while the process runs) and the fallback directories recorded in the
// configuration. The fallbacks matter most: an installation that used to keep its state under the
// process working directory, or under the plugin library directory, would otherwise read an empty
// store after the state directory was pinned beside the auth files, and every saved account would
// look deleted.
func (a *App) adoptStateDirectoryChange(previousDir string, config Config) stateAdoptionResult {
	result := stateAdoptionResult{targetDir: strings.TrimSpace(config.DataDir)}
	if a == nil {
		return result
	}
	target := result.targetDir
	if target != "" {
		a.effectiveDataDir = target
	}
	if target == "" {
		return result
	}
	sources := make([]string, 0, len(config.DataDirAlternates)+1)
	if previous := strings.TrimSpace(previousDir); previous != "" && filepath.Clean(previous) != filepath.Clean(target) {
		sources = append(sources, previous)
	}
	for _, alternate := range config.DataDirAlternates {
		trimmed := strings.TrimSpace(alternate)
		if trimmed == "" || filepath.Clean(trimmed) == filepath.Clean(target) {
			continue
		}
		sources = append(sources, trimmed)
	}
	for _, source := range sources {
		copied, errAdopt := adoptStateDirectory(source, target)
		if copied <= 0 && errAdopt == nil {
			continue
		}
		result.attempted = true
		result.copied += copied
		if errAdopt != nil && result.err == nil {
			result.err = errAdopt
		}
	}
	return result
}

// recordStateAdoptionResult journals the adoption outcome. It must run after the operation journal
// was configured for the target directory.
func (a *App) recordStateAdoptionResult(result stateAdoptionResult) {
	if a == nil || !result.attempted {
		return
	}
	if result.copied > 0 {
		a.recordStateAdoption(result.copied, result.targetDir)
	}
	if result.err != nil {
		a.recordStateAdoptionFailure()
	}
}

// recordStateAdoption journals one sanitized adoption result: counts only, plus a short note that
// names no path beyond the target directory's sanitized base name.
func (a *App) recordStateAdoption(copied int, targetDir string) {
	if a == nil {
		return
	}
	if note := buildStateAdoptionNote(copied, targetDir); note != "" {
		a.mu.Lock()
		a.stateAdoptionNote = note
		a.mu.Unlock()
	}
	if a.operations == nil {
		return
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryPlugin, Action: OperationActionPluginConfigure,
		Status: OperationStatusSucceeded, Source: OperationSourceBackground, Scope: OperationScopeSystem,
		TargetCount: copied, Succeeded: copied, ReasonCode: stateAdoptionReasonCode,
	})
}

// recordStateAdoptionFailure journals a sanitized failure when adoption could not copy state. The
// configuration itself must still proceed, so the previously resolved state stays usable.
func (a *App) recordStateAdoptionFailure() {
	if a == nil || a.operations == nil {
		return
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryPlugin, Action: OperationActionPluginConfigure,
		Status: OperationStatusFailed, Source: OperationSourceBackground, Scope: OperationScopeSystem,
		Failed: 1, ReasonCode: "operation_failed",
	})
}

// buildStateAdoptionNote returns the short operator-visible adoption note: a count plus the
// sanitized base name of the target directory, never a full path or a file name.
func buildStateAdoptionNote(copied int, targetDir string) string {
	if copied <= 0 {
		return ""
	}
	name := sanitizedStateDirectoryName(targetDir)
	if name == "" {
		return fmt.Sprintf("adopted %d state files", copied)
	}
	return fmt.Sprintf("adopted %d state files into %s", copied, name)
}

// sanitizedStateDirectoryName keeps a directory base name that is safe to surface, so a directory
// chosen by the operator can never inject control characters into a report.
func sanitizedStateDirectoryName(targetDir string) string {
	base := filepath.Base(filepath.Clean(strings.TrimSpace(targetDir)))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	cleaned := make([]rune, 0, len(base))
	for _, character := range base {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '.', character == '-', character == '_':
			cleaned = append(cleaned, character)
		default:
			cleaned = append(cleaned, '_')
		}
	}
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	return string(cleaned)
}

// stateAdoptionNoteSnapshot reports the latest sanitized adoption note, if any.
func (a *App) stateAdoptionNoteSnapshot() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.stateAdoptionNote
}
