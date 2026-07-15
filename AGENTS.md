# AGENTS.md

Standalone CLIProxyAPI native plugin for safe account configuration listing and batch edits.

## Commands

```bash
gofmt -w .
go test ./...
make build
make verify
```

## Conventions

- Keep the CGO ABI bridge thin; business logic belongs under `internal/` and must be testable without loading CLIProxyAPI.
- Treat Management Keys, Auth JSON, tokens, cookies, API keys, proxy credentials, and header values as secrets.
- Never persist or log secrets. Public API models must be explicitly allow-listed and redacted.
- Plugin Management routes are exact paths. Do not use dynamic path parameters.
- Resource routes serve static UI only. Privileged data and writes belong behind authenticated Management routes.
- Comments and new Markdown documentation are English unless a language-specific file is explicitly created.
- Use contextual errors and bounded concurrency. Do not panic in request or job paths.
