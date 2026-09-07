package manager

import (
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestIsPluginOwnedAuthEntryMatchesDedicatedStateDirectory(t *testing.T) {
	tests := []struct {
		name  string
		entry cpaapi.HostAuthFileEntry
		want  bool
	}{
		{name: "provider runtime path", entry: cpaapi.HostAuthFileEntry{Path: "/auths/.cpa-account-config-manager/ai-provider-runtime.json"}, want: true},
		{name: "provider runtime auth index", entry: cpaapi.HostAuthFileEntry{AuthIndex: ".cpa-account-config-manager/ai-provider-runtime.json"}, want: true},
		{name: "provider runtime id", entry: cpaapi.HostAuthFileEntry{ID: ".cpa-account-config-manager/ai-provider-runtime.json"}, want: true},
		{name: "usage state path", entry: cpaapi.HostAuthFileEntry{Name: ".cpa-account-config-manager/usage-snapshots.state"}, want: true},
		{name: "future state file", entry: cpaapi.HostAuthFileEntry{Path: "/auths/.CPA-ACCOUNT-CONFIG-MANAGER/future-state.json"}, want: true},
		{name: "windows state file", entry: cpaapi.HostAuthFileEntry{Path: `C:\auths\.cpa-account-config-manager\future-state.json`}, want: true},
		{name: "state directory itself", entry: cpaapi.HostAuthFileEntry{Path: "/auths/.cpa-account-config-manager"}, want: true},
		{name: "url escaped state path", entry: cpaapi.HostAuthFileEntry{AuthIndex: "%2Ecpa-account-config-manager%2Fai-provider-runtime.json"}, want: true},
		{name: "composite state identifier", entry: cpaapi.HostAuthFileEntry{ID: "host:.cpa-account-config-manager/ai-provider-runtime.json"}, want: true},
		{name: "account alias state path", entry: cpaapi.HostAuthFileEntry{Account: `.cpa-account-config-manager/ai-provider-runtime.json`}, want: true},
		{name: "double escaped state path", entry: cpaapi.HostAuthFileEntry{AuthIndex: "%252Ecpa-account-config-manager%252Fai-provider-runtime.json"}, want: true},
		{name: "normal auth file", entry: cpaapi.HostAuthFileEntry{Path: "/auths/account.json", Name: "account.json"}, want: false},
		{name: "same filename outside state directory", entry: cpaapi.HostAuthFileEntry{Path: "/auths/ai-provider-runtime.json", Name: "ai-provider-runtime.json"}, want: true},
		{name: "usage state filename", entry: cpaapi.HostAuthFileEntry{Name: "usage-snapshots.state"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPluginOwnedAuthEntry(tt.entry); got != tt.want {
				t.Fatalf("isPluginOwnedAuthEntry() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPluginOwnedAccountProjectionIsHiddenEvenWhenCPAIdentityFieldsChange(t *testing.T) {
	account := Account{
		ID:    "ai-provider-runtime.json",
		Name:  "ai-provider-runtime.json",
		Label: ".cpa-account-config-manager/ai-provider-runtime.json",
		path:  `/auths/.cpa-account-config-manager/ai-provider-runtime.json`,
	}
	if !isPluginOwnedAccountProjection(account) {
		t.Fatal("plugin-owned account projection was not detected")
	}
}
