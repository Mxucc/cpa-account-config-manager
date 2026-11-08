package provider

// Capabilities says which account actions a provider actually supports. A page
// must disable an action the provider does not implement instead of offering a
// button that fails, which is what made the three old workspaces inconsistent.
type Capabilities struct {
	Login   bool `json:"login"`
	Quota   bool `json:"quota"`
	Models  bool `json:"models"`
	Binding bool `json:"binding"`
	Billing bool `json:"billing"`
	Refresh bool `json:"refresh"`
	Test    bool `json:"test"`
	// HostAuth says the provider can complete CPA's own OAuth/login flow, so it
	// may appear as an auth entry in CPA's Management Center.
	HostAuth bool `json:"host_auth"`
}

// Definition describes one provider the plugin knows.
type Definition struct {
	ID           string       `json:"id"`
	DisplayName  string       `json:"display_name"`
	Kind         string       `json:"kind"`
	Description  string       `json:"description,omitempty"`
	DocsURL      string       `json:"docs_url,omitempty"`
	AuthModes    []string     `json:"auth_modes,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

// ProviderView is a definition plus the live counts a provider page shows.
type ProviderView struct {
	Definition
	Accounts int            `json:"accounts"`
	ByStatus map[Status]int `json:"by_status"`
}

// Definitions is the static registry. It is the single list the UI, the API and
// the tests read; adding a provider means adding one entry here plus an adapter.
var Definitions = []Definition{
	{
		ID:          "codex",
		DisplayName: "Codex",
		Kind:        "oauth",
		Description: "OpenAI Codex credentials managed through CPA, with five-hour and seven-day quota windows, plan metadata and client identity policy.",
		DocsURL:     "https://github.com/router-for-me/CLIProxyAPI",
		AuthModes:   []string{"oauth", "agent_identity"},
		Capabilities: Capabilities{
			Login: true, Quota: true, Models: true, Billing: true, Refresh: true, Test: true, HostAuth: true,
		},
	},
	{
		ID:          "opencode-go",
		DisplayName: "OpenCode Go",
		Kind:        "subscription",
		Description: "OpenCode Go workspaces: a monthly allowance split across rolling, weekly and monthly windows.",
		DocsURL:     "https://opencode.ai/docs/go/",
		AuthModes:   []string{"cookie", "api_key"},
		Capabilities: Capabilities{
			Login: false, Quota: true, Models: true, Binding: true, Billing: true, Refresh: true, Test: true,
		},
	},
	{
		ID:          "opencode-zen",
		DisplayName: "OpenCode Zen",
		Kind:        "wallet",
		Description: "OpenCode Zen credentials: metered usage against a prepaid balance, with a per-account model catalog.",
		DocsURL:     "https://opencode.ai/docs/zen/",
		AuthModes:   []string{"api_key"},
		Capabilities: Capabilities{
			Login: false, Quota: true, Models: true, Binding: true, Billing: true, Refresh: true, Test: true,
		},
	},
	{
		ID:          "cline-pass",
		DisplayName: "Cline Pass",
		Kind:        "subscription",
		Description: "Cline accounts: ClinePass's five-hour, weekly and monthly limits plus the account's wallet balance, with automatic credential repair.",
		DocsURL:     "https://app.cline.bot/",
		AuthModes:   []string{"device_code", "api_key", "cli_reuse"},
		Capabilities: Capabilities{
			Login: true, Quota: true, Models: true, Binding: true, Billing: true, Refresh: true, Test: true,
		},
	},
	{
		ID:          "ai-provider",
		DisplayName: "AI Providers",
		Kind:        "channel",
		Description: "OpenAI-compatible CPA channels that are not one of the managed products: models, runtime usage and cost only.",
		AuthModes:   []string{"api_key"},
		Capabilities: Capabilities{
			Models: true, Billing: true, Test: true,
		},
	},
}

// DefinitionFor returns the registry entry for id.
func DefinitionFor(id string) (Definition, bool) {
	for _, definition := range Definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}
