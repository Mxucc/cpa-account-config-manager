package manager

import "testing"

// The plugin's own label store must not rename siblings: the URL record is shared by every channel
// with that base URL, so it may only carry a label while a single channel owns it.
func TestAIProviderChannelBaseURLOwnersCountsSiblings(t *testing.T) {
	first := map[string]any{"base-url": "https://api.cline.bot/api/v1"}
	entries := []map[string]any{
		first,
		{"base-url": "https://api.cline.bot/api/v1/"},
		{"base-url": "https://opencode.ai/zen/v1"},
	}
	if got := aiProviderChannelBaseURLOwners(entries, first); got != 2 {
		t.Fatalf("owners = %d, want 2 for the shared gateway", got)
	}
	zen := entries[2]
	if got := aiProviderChannelBaseURLOwners(entries, zen); got != 1 {
		t.Fatalf("owners = %d, want 1 for the unshared gateway", got)
	}
	if got := aiProviderChannelBaseURLOwners(entries, map[string]any{}); got != 0 {
		t.Fatalf("owners = %d, want 0 for an entry without a base URL", got)
	}
}
