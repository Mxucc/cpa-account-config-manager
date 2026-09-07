package manager

import (
	"net/url"
	"path/filepath"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

const pluginOwnedStateMarker = ".cpa-account-config-manager"

var pluginOwnedStateFileNames = []string{
	"ai-provider-runtime.json",
	"usage-snapshots.state",
}

// filterPluginOwnedAuthEntries removes durable plugin state that a recursive
// CPA auth-file scan may expose as if it were an account. The dedicated state
// directory is stored beside auth files only for restart persistence; none of
// its current or future contents may enter account projection, policy
// reconciliation, import matching, or usage bindings.
func filterPluginOwnedAuthEntries(entries []cpaapi.HostAuthFileEntry) []cpaapi.HostAuthFileEntry {
	filtered := make([]cpaapi.HostAuthFileEntry, 0, len(entries))
	for _, entry := range entries {
		if isPluginOwnedAuthEntry(entry) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func isPluginOwnedAuthEntry(entry cpaapi.HostAuthFileEntry) bool {
	for _, raw := range []string{entry.Path, entry.Name, entry.ID, entry.AuthIndex, entry.Account} {
		if containsPluginOwnedStateMarker(raw) {
			return true
		}
	}
	return false
}

// isPluginOwnedAccountProjection is a second boundary check after CPA data has
// been projected into the public Account model. It is intentionally separate
// from isPluginOwnedAuthEntry: a newer CPA host may populate a new identity
// field or derive an account name from the path before this plugin sees it.
// Such a row must still never reach the management response.
func isPluginOwnedAccountProjection(account Account) bool {
	for _, raw := range []string{account.ID, account.AuthID, account.Name, account.Label, account.Email, account.Source, account.path} {
		if containsPluginOwnedStateMarker(raw) {
			return true
		}
	}
	return false
}

func containsPluginOwnedStateMarker(raw string) bool {
	value := normalizeAuthEntryPath(raw)
	if value == "" {
		return false
	}
	// Check the marker as a path component first so a normal account whose
	// name merely contains a similar string is not accidentally removed.
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if strings.EqualFold(part, pluginOwnedStateMarker) {
			return true
		}
		for _, fileName := range pluginOwnedStateFileNames {
			if strings.EqualFold(part, fileName) {
				return true
			}
		}
	}
	// CPA versions have returned a relative path, an absolute path, and an
	// URL-escaped auth index for the same file over time. Once separators,
	// escaping, and case are normalized, the dedicated marker must never be
	// allowed through even when it is embedded in a composite identifier.
	return strings.Contains(strings.ToLower(value), pluginOwnedStateMarker)
}

func normalizeAuthEntryPath(raw string) string {
	value := strings.Trim(strings.TrimSpace(raw), "\"")
	if value == "" {
		return ""
	}
	// Some host versions return an auth index that has been URL-encoded more
	// than once. Decode boundedly so the plugin-owned marker remains hidden
	// without risking an unbounded transformation of user-controlled values.
	for range 4 {
		decoded, err := url.PathUnescape(value)
		if err != nil || decoded == value {
			break
		}
		value = decoded
	}
	value = strings.Trim(strings.TrimSpace(value), "\"")
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.ReplaceAll(value, "//", "/")
	return strings.Trim(strings.TrimSpace(filepath.ToSlash(value)), "/")
}
