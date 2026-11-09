package manager

import "testing"

// A v8 channel group carries no name, so a sign-in that matched its own earlier row
// by label would never match and would pile up one channel per sign-in. The row is
// matched by the base URL and the credential instead.
func TestCommunityChannelOwnership(t *testing.T) {
	const base = "https://opencode.ai/inference/v1"
	row := func(url, key string) map[string]any {
		return map[string]any{
			"base-url":        url,
			"api-key-entries": []map[string]any{{"api-key": key}},
		}
	}

	if !communityChannelIsOwn(row(base, "public"), base, "public", nil) {
		t.Fatal("a row publishing this credential was not recognised")
	}
	if communityChannelIsOwn(row("https://api.z.ai/api/anthropic", "public"), base, "public", nil) {
		t.Fatal("another provider's base URL was adopted")
	}
	if communityChannelIsOwn(row(base, "someone-else"), base, "public", nil) {
		t.Fatal("a row publishing another credential was adopted")
	}
	// A provider that rotated the credential keeps the same row: the digest of what
	// this label published last time is the proof.
	previous := []string{communityCredentialDigest("old-credential")}
	if !communityChannelIsOwn(row(base, "old-credential"), base, "new-credential", previous) {
		t.Fatal("a rotated row of the same provider was not recognised")
	}
	// A trailing slash difference must not hide the row.
	if !communityChannelIsOwn(row(base+"/", "public"), base, "public", nil) {
		t.Fatal("a base URL with a trailing slash was treated as another provider")
	}
	// A row without a credential is never adopted: it cannot prove ownership.
	if communityChannelIsOwn(map[string]any{"base-url": base}, base, "public", previous) {
		t.Fatal("a credential-less row was adopted")
	}
}
