package manager

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
)

// openCodeStatusCSS is the standalone status page styling ported from the
// community opencode-go-quota-cpa-plugin.
const openCodeStatusCSS = `:root{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;--bg:#f7f8fa;--text:#1f2937;--card-bg:#ffffff;--card-border:#eef0f4;--card-shadow:0 2px 8px rgba(0,0,0,.06);--sub-text:#6b7280;--label:#374151;--error-bg:#fef2f2;--error-border:#fecaca;--error-text:#b91c1c;--hint-bg:#eff6ff;--hint-border:#bfdbfe;--hint-text:#1d4ed8;--foot-text:#9ca3af;--bar-bg:#e5e7eb;--bar-fill:#ef4444;--input-bg:#f9fafb;--input-border:#d1d5db;--input-text:#1f2937;--btn-bg:#ffffff;--btn-text:#374151;--btn-border:#d1d5db;--btn-hover:#f3f4f6;--btn-outline-text:#374151;--btn-outline-border:#d1d5db;--btn-outline-bg:#ffffff;--btn-ghost-text:#6b7280;--btn-ghost-hover:#f3f4f6;--status-bg:#f0fdf4;--status-border:#86efac;--status-text:#166534;--status-empty-bg:#fef2f2;--status-empty-border:#fecaca;--status-empty-text:#991b1b;color-scheme:light dark}@media(prefers-color-scheme:dark){:root{--bg:#0f1115;--text:#f3f4f6;--card-bg:#181b21;--card-border:#2a2f36;--card-shadow:0 2px 8px rgba(0,0,0,.3);--sub-text:#9ca3af;--label:#d1d5db;--error-bg:#2a1717;--error-border:#6b2d2d;--error-text:#fecaca;--hint-bg:#15202b;--hint-border:#2a3a4a;--hint-text:#9fc7e0;--foot-text:#6b7280;--bar-bg:#374151;--bar-fill:#f87171;--input-bg:#111827;--input-border:#374151;--input-text:#f3f4f6;--btn-bg:#1f2937;--btn-text:#f3f4f6;--btn-border:#4b5563;--btn-hover:#374151;--btn-outline-text:#d1d5db;--btn-outline-border:#4b5563;--btn-outline-bg:#1f2937;--btn-ghost-text:#9ca3af;--btn-ghost-hover:#374151;--status-bg:#052e16;--status-border:#166534;--status-text:#86efac;--status-empty-bg:#2a1717;--status-empty-border:#6b2d2d;--status-empty-text:#fecaca}}body{margin:0;padding:24px;background:var(--bg);color:var(--text)}.wrap{max-width:1200px;margin:auto}.head{margin-bottom:20px}.head h1{font-size:22px;font-weight:700;margin:0 0 4px}.sub{color:var(--sub-text);font-size:13px}.status-bar{background:var(--status-bg);border:1px solid var(--status-border);border-radius:10px;padding:10px 14px;color:var(--status-text);font-size:13px;margin-bottom:16px}.status-empty{background:var(--status-empty-bg);border-color:var(--status-empty-border);color:var(--status-empty-text)}.form{background:var(--card-bg);border:1px solid var(--card-border);border-radius:16px;padding:18px;box-shadow:var(--card-shadow);margin-bottom:20px}.row{display:grid;gap:6px}.label{font-size:13px;color:var(--label);font-weight:500}.input{background:var(--input-bg);border:1px solid var(--input-border);border-radius:10px;padding:10px 12px;color:var(--input-text);font-size:14px;outline:none}.input:focus{border-color:#9ca3af}.inline{display:flex;gap:12px;align-items:flex-end}.inline .row{flex:1;min-width:200px}.btn-row{display:flex;gap:10px;flex-wrap:wrap;margin-top:12px}.btn{background:var(--btn-bg);color:var(--btn-text);border:1px solid var(--btn-border);border-radius:10px;padding:9px 16px;font-size:13px;font-weight:500;cursor:pointer;transition:background .15s}.btn:hover{background:var(--btn-hover)}.btn-sm{font-size:12px;padding:6px 12px}.btn-outline{background:var(--btn-outline-bg);color:var(--btn-outline-text);border:1px solid var(--btn-outline-border)}.btn-outline:hover{background:var(--btn-ghost-hover)}.btn-ghost{background:transparent;color:var(--btn-ghost-text);border:1px solid transparent}.btn-ghost:hover{background:var(--btn-ghost-hover);border-color:var(--btn-outline-border)}.accounts-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:16px}.account-card{background:var(--card-bg);border:1px solid var(--card-border);border-radius:16px;padding:16px;box-shadow:var(--card-shadow);display:flex;flex-direction:column}.account-card-head{margin-bottom:12px}.account-title{font-size:15px;font-weight:700;color:var(--text);word-break:break-all}.account-sub{font-size:12px;color:var(--sub-text);margin-top:2px}.window-row{margin:10px 0}.window-top{display:flex;justify-content:space-between;align-items:center;margin-bottom:6px}.window-label{font-size:14px;font-weight:700;color:var(--text)}.window-meta{font-size:12px;color:var(--sub-text);white-space:nowrap}.window-meta span{font-weight:700;color:var(--text)}.bar{height:8px;background:var(--bar-bg);border-radius:99px;overflow:hidden}.fill{height:100%;background:var(--bar-fill);border-radius:99px;transition:width .3s ease}.account-actions{display:flex;justify-content:center;gap:10px;margin-top:auto;padding-top:14px;border-top:1px solid var(--card-border)}.error{background:var(--error-bg);border:1px solid var(--error-border);padding:14px 16px;border-radius:12px;color:var(--error-text);font-size:13px}.hint{background:var(--hint-bg);border:1px solid var(--hint-border);padding:14px 16px;border-radius:12px;color:var(--hint-text);font-size:13px}.foot{margin-top:18px;color:var(--foot-text);font-size:12px}`

// renderOpenCodeStatusPage renders the standalone OpenCode Go status page.
func renderOpenCodeStatusPage(snapshot OpenCodeQuotaSnapshot, message string) string {
	var statusHTML string
	if len(snapshot.Accounts) == 0 {
		statusHTML = `<div class="status-bar status-empty">No accounts bound yet. Enter workspace_id and auth_cookie below and save.</div>`
	} else {
		statusHTML = `<div class="status-bar">Accounts bound: ` + strconv.Itoa(len(snapshot.Accounts)) + ` <form method="get" action="" style="display:inline;margin:0"><input type="hidden" name="action" value="refresh"><button class="btn btn-sm btn-outline" type="submit">Refresh all</button></form></div>`
	}

	var content string
	if len(snapshot.Accounts) == 0 {
		content = `<div class="hint">No accounts bound yet.</div>`
	} else {
		var builder strings.Builder
		builder.WriteString(`<div class="accounts-grid">`)
		for _, account := range snapshot.Accounts {
			result := snapshot.Results[account.ID]
			builder.WriteString(`<div class="account-card">`)
			builder.WriteString(`<div class="account-card-head">`)
			builder.WriteString(`<div><div class="account-title">` + html.EscapeString(account.WorkspaceID) + `</div><div class="account-sub">Workspace</div></div>`)
			builder.WriteString(`</div>`)
			if result != nil && result.Success {
				builder.WriteString(renderOpenCodeWindowRow("5 hours", result.Rolling))
				builder.WriteString(renderOpenCodeWindowRow("7 days", result.Weekly))
				builder.WriteString(renderOpenCodeWindowRow("30 days", result.Monthly))
				builder.WriteString(`<div class="account-actions"><form method="get" action="" style="margin:0"><input type="hidden" name="action" value="refresh"><button class="btn btn-outline" type="submit">Refresh quota</button></form><form method="get" action="" style="margin:0"><input type="hidden" name="action" value="remove"><input type="hidden" name="account_id" value="` + html.EscapeString(account.ID) + `"><button class="btn btn-ghost" type="submit">Remove</button></form></div>`)
			} else if result != nil {
				builder.WriteString(`<div class="error" style="margin-top:10px"><strong>Query failed</strong><div>` + html.EscapeString(result.Error) + `</div></div>`)
			}
			builder.WriteString(`</div>`)
		}
		builder.WriteString(`</div>`)
		content = builder.String()
	}

	fetched := "-"
	if !snapshot.FetchedAt.IsZero() {
		fetched = snapshot.FetchedAt.Local().Format("2006-01-02 15:04:05")
	}

	formHTML := buildOpenCodeCredentialForm()
	if message != "" {
		formHTML = `<div class="hint" style="margin-bottom:14px">` + html.EscapeString(message) + `</div>` + formHTML
	}

	return `<!doctype html><!-- opencode-go-quota --><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>OpenCode Go Quota</title><style>` + openCodeStatusCSS + `</style></head><body><div class="wrap"><div class="head"><h1>OpenCode Go</h1><div class="sub">Multi-account quota monitor · Last refresh ` + html.EscapeString(fetched) + `</div></div>` +
		statusHTML +
		formHTML +
		content + `<div class="foot">Credentials are stored only in the plugin private data directory; they are never returned to the browser or written to logs.</div></div></body></html>`
}

// buildOpenCodeCredentialForm renders the inline credential configuration form.
func buildOpenCodeCredentialForm() string {
	return `<form class="form" id="status-form" method="get" action="" onsubmit="return onOpenCodeFormSubmit(this)">
  <div class="inline">
    <div class="row"><div class="label">Workspace ID</div><input class="input" name="workspace_id" placeholder="wrk_xxxxxxxx" autocomplete="off"></div>
    <div class="row"><div class="label">Auth Cookie (value of auth)</div><input class="input" type="password" name="auth_cookie" placeholder="Fe26.2*..." autocomplete="off"></div>
  </div>
  <input type="hidden" name="action" id="form-action" value="query">
  <div class="btn-row">
    <button class="btn" type="submit" onclick="setOpenCodeAction('query')">Query (no save)</button>
    <button class="btn btn-outline" type="submit" onclick="setOpenCodeAction('save')">Save & query</button>
  </div>
</form>
<script>
function setOpenCodeAction(a){ document.getElementById('form-action').value=a; }
function onOpenCodeFormSubmit(f){
  var btn=f.querySelector('button[type=submit]:focus')||f.querySelector('button[type=submit]');
  if(btn){btn.disabled=true;btn.textContent='Working…';}
  return true;
}
</script>`
}

// renderOpenCodeWindowRow renders one quota window as a compact card row.
func renderOpenCodeWindowRow(name string, window *OpenCodeWindowUsage) string {
	if window == nil {
		return `<div class="window-row"><div class="window-top"><span class="window-label">` + html.EscapeString(name) + `</span><span class="window-meta">No data</span></div></div>`
	}
	used := math.Max(0, math.Min(100, window.UsagePercent))
	resetAt := window.ResetAt.Local().Format("01/02 15:04")
	relative := formatOpenCodeRelativeTime(window.ResetInSec)
	return `<div class="window-row">
  <div class="window-top">
    <span class="window-label">` + html.EscapeString(name) + `</span>
    <span class="window-meta"><span>` + fmt.Sprintf("%.0f%%", used) + `</span> ` + html.EscapeString(resetAt) + ` · ` + html.EscapeString(relative) + `</span>
  </div>
  <div class="bar"><div class="fill" style="width:` + fmt.Sprintf("%.2f", used) + `%"></div></div>
</div>`
}

// formatOpenCodeRelativeTime converts seconds until reset into a Chinese
// relative expression such as "2h" or "-29d".
func formatOpenCodeRelativeTime(seconds int64) string {
	if seconds == 0 {
		return "now"
	}
	if seconds < 0 {
		value := -seconds
		days := value / 86400
		value %= 86400
		hours := value / 3600
		value %= 3600
		minutes := value / 60
		switch {
		case days > 0:
			return fmt.Sprintf("-%dd", days)
		case hours > 0:
			return fmt.Sprintf("-%dh", hours)
		case minutes > 0:
			return fmt.Sprintf("-%dm", minutes)
		default:
			return "just now"
		}
	}
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return "now"
	}
}

func formatOpenCodeDuration(seconds int64) string {
	if seconds <= 0 {
		return "now"
	}
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}
