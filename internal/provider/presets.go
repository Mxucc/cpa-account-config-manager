package provider

// Presets are the provider templates the provider page offers, modelled on the
// way Magpie lists vendors: one entry per upstream with the protocol it speaks,
// the base URL it lives at, and how a user signs in. They are templates, not
// accounts: choosing one pre-fills an import or a custom provider, and an
// imported Magpie plugin becomes an entry beside them.
type Preset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Protocol    string   `json:"protocol"`
	BaseURL     string   `json:"base_url,omitempty"`
	AuthMode    string   `json:"auth_mode"`
	DocsURL     string   `json:"docs_url,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Presets covers the vendors Magpie's provider catalog and community plugins
// most often serve. A preset only pre-fills a form; it never creates an account
// on its own and never claims a capability the plugin has not implemented.
var Presets = []Preset{
	{ID: "codex", Name: "Codex", Kind: "oauth", Protocol: "responses", BaseURL: "https://chatgpt.com/backend-api/codex", AuthMode: "oauth", DocsURL: "https://github.com/router-for-me/CLIProxyAPI", Description: "OpenAI Codex subscription, managed through CPA credentials.", Tags: []string{"builtin", "subscription"}},
	{ID: "opencode-go", Name: "OpenCode Go", Kind: "subscription", Protocol: "openai", BaseURL: "https://opencode.ai/zen/go/v1", AuthMode: "cookie", DocsURL: "https://opencode.ai/docs/go/", Description: "OpenCode Go workspace with a $10/month allowance split across three windows.", Tags: []string{"builtin", "subscription"}},
	{ID: "opencode-zen", Name: "OpenCode Zen", Kind: "wallet", Protocol: "openai", BaseURL: "https://opencode.ai/zen/v1", AuthMode: "api_key", DocsURL: "https://opencode.ai/docs/zen/", Description: "OpenCode Zen metered wallet with a per-account model catalog.", Tags: []string{"builtin", "metered"}},
	{ID: "cline-pass", Name: "Cline Pass", Kind: "subscription", Protocol: "openai", BaseURL: "https://api.cline.bot/api/v1", AuthMode: "device_code", DocsURL: "https://app.cline.bot/", Description: "Cline account with ClinePass limits, wallet balance and automatic credential repair.", Tags: []string{"builtin", "subscription"}},
	{ID: "openrouter", Name: "OpenRouter", Kind: "channel", Protocol: "openai", BaseURL: "https://openrouter.ai/api/v1", AuthMode: "api_key", DocsURL: "https://openrouter.ai/docs", Description: "OpenAI-compatible aggregator; add it as a CPA channel to route its models.", Tags: []string{"aggregator", "openai-compatible"}},
	{ID: "deepseek", Name: "DeepSeek", Kind: "channel", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1", AuthMode: "api_key", DocsURL: "https://platform.deepseek.com/api-docs", Tags: []string{"openai-compatible"}},
	{ID: "moonshot", Name: "Moonshot (Kimi)", Kind: "channel", Protocol: "openai", BaseURL: "https://api.moonshot.cn/v1", AuthMode: "api_key", DocsURL: "https://platform.moonshot.cn/docs", Tags: []string{"openai-compatible"}},
	{ID: "zhipu", Name: "Zhipu GLM", Kind: "channel", Protocol: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", AuthMode: "api_key", DocsURL: "https://open.bigmodel.cn/dev/api", Tags: []string{"openai-compatible"}},
	{ID: "dashscope", Name: "Alibaba DashScope", Kind: "channel", Protocol: "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", AuthMode: "api_key", DocsURL: "https://help.aliyun.com/zh/dashscope/", Tags: []string{"openai-compatible"}},
	{ID: "groq", Name: "Groq", Kind: "channel", Protocol: "openai", BaseURL: "https://api.groq.com/openai/v1", AuthMode: "api_key", DocsURL: "https://console.groq.com/docs", Tags: []string{"openai-compatible"}},
	{ID: "mistral", Name: "Mistral", Kind: "channel", Protocol: "openai", BaseURL: "https://api.mistral.ai/v1", AuthMode: "api_key", DocsURL: "https://docs.mistral.ai/", Tags: []string{"openai-compatible"}},
	{ID: "xai", Name: "xAI Grok", Kind: "channel", Protocol: "openai", BaseURL: "https://api.x.ai/v1", AuthMode: "api_key", DocsURL: "https://docs.x.ai/", Tags: []string{"openai-compatible"}},
	{ID: "ollama", Name: "Ollama", Kind: "channel", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1", AuthMode: "none", DocsURL: "https://github.com/ollama/ollama/blob/main/docs/openai.md", Tags: []string{"local", "openai-compatible"}},
	{ID: "lmstudio", Name: "LM Studio", Kind: "channel", Protocol: "openai", BaseURL: "http://127.0.0.1:1234/v1", AuthMode: "none", DocsURL: "https://lmstudio.ai/docs/local-server", Tags: []string{"local", "openai-compatible"}},
	{ID: "vllm", Name: "vLLM", Kind: "channel", Protocol: "openai", BaseURL: "http://127.0.0.1:8000/v1", AuthMode: "none", DocsURL: "https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html", Tags: []string{"local", "openai-compatible"}},
	{ID: "custom", Name: "Custom OpenAI-compatible", Kind: "channel", Protocol: "openai", AuthMode: "api_key", Description: "Any endpoint that speaks OpenAI's Chat Completions API.", Tags: []string{"openai-compatible"}},
}
