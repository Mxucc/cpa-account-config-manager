package manager

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestCredentialExportCPAUsesSingleJSONOrSafeMultiAccountZIP(t *testing.T) {
	now := time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC)
	collection, errSources := NewAccountService(credentialExportHost()).ExportCredentialSources(t.Context(), AccountFilters{Provider: "codex"})
	if errSources != nil {
		t.Fatalf("ExportCredentialSources() error = %v", errSources)
	}
	defer clearCredentialExportCollection(&collection)
	if len(collection.Sources) != 2 || collection.Skipped != 1 {
		t.Fatalf("collection = %d sources, %d skipped", len(collection.Sources), collection.Skipped)
	}

	single, errSingle := renderCredentialExport(CredentialExportFormatCPA, credentialExportCollection{Sources: collection.Sources[:1]}, now)
	if errSingle != nil {
		t.Fatalf("single CPA export error = %v", errSingle)
	}
	if single.Filename != "alice@example.com.json" || single.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("single CPA download = %#v", single)
	}
	if !bytes.Contains(single.Body, []byte("access-secret-a")) || !bytes.Contains(single.Body, []byte("proxy-secret-a")) {
		t.Fatalf("single CPA export did not preserve Auth credentials: %s", single.Body)
	}

	multi, errMulti := renderCredentialExport(CredentialExportFormatCPA, collection, now)
	if errMulti != nil {
		t.Fatalf("multi CPA export error = %v", errMulti)
	}
	if multi.Filename != "cpa-accounts.zip" || multi.ContentType != "application/zip" || multi.Exported != 2 || multi.Skipped != 1 {
		t.Fatalf("multi CPA download = %#v", multi)
	}
	reader, errZIP := zip.NewReader(bytes.NewReader(multi.Body), int64(len(multi.Body)))
	if errZIP != nil {
		t.Fatalf("open CPA ZIP: %v", errZIP)
	}
	if len(reader.File) != 2 {
		t.Fatalf("CPA ZIP entries = %d, want 2", len(reader.File))
	}
	wantNames := []string{"alice@example.com.json", "alice@example.com-2.json"}
	for index, entry := range reader.File {
		if entry.Name != wantNames[index] {
			t.Fatalf("CPA ZIP entry %d = %q, want %q", index, entry.Name, wantNames[index])
		}
		if path.Base(entry.Name) != entry.Name || strings.ContainsAny(entry.Name, "/\\") {
			t.Fatalf("CPA ZIP entry has unsafe path %q", entry.Name)
		}
		if entry.Mode().Perm() != 0o600 {
			t.Fatalf("CPA ZIP entry mode = %o, want 600", entry.Mode().Perm())
		}
		handle, errOpen := entry.Open()
		if errOpen != nil {
			t.Fatalf("open CPA ZIP entry: %v", errOpen)
		}
		var document map[string]any
		errDecode := json.NewDecoder(handle).Decode(&document)
		_ = handle.Close()
		if errDecode != nil || document["access_token"] == "" || document["custom_provider_value"] == nil {
			t.Fatalf("CPA ZIP entry document = %#v error=%v", document, errDecode)
		}
	}

	response := exportDownloadResponse(multi)
	if response.Headers.Get("Cache-Control") != "no-store, private, max-age=0" || response.Headers.Get("X-Exported-Accounts") != "2" || response.Headers.Get("X-Skipped-Accounts") != "1" || !strings.Contains(response.Headers.Get("Access-Control-Expose-Headers"), "Content-Disposition") {
		t.Fatalf("credential response headers = %#v", response.Headers)
	}
}

func TestCredentialExportTargetMappingsMatchReferenceShapes(t *testing.T) {
	now := time.Date(2026, time.July, 15, 10, 30, 0, 0, time.UTC)
	collection, errSources := NewAccountService(credentialExportHost()).ExportCredentialSources(t.Context(), AccountFilters{Search: "alice-a.json"})
	if errSources != nil {
		t.Fatalf("ExportCredentialSources() error = %v", errSources)
	}
	defer clearCredentialExportCollection(&collection)
	if len(collection.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(collection.Sources))
	}

	formats := []string{
		CredentialExportFormatSub2API,
		CredentialExportFormatCockpit,
		CredentialExportFormat9Router,
		CredentialExportFormatCodex,
		CredentialExportFormatAxonHub,
		CredentialExportFormatCodexManager,
	}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			download, errRender := renderCredentialExport(format, collection, now)
			if errRender != nil {
				t.Fatalf("renderCredentialExport(%q) error = %v", format, errRender)
			}
			if !download.Credential || download.Exported != 1 || download.Skipped != 0 || !bytes.Contains(download.Body, []byte("access-secret-a")) {
				t.Fatalf("%s download = %#v body=%s", format, download, download.Body)
			}
			var document any
			if errDecode := json.Unmarshal(download.Body, &document); errDecode != nil {
				t.Fatalf("decode %s export: %v", format, errDecode)
			}
			assertCredentialTargetShape(t, format, document)
		})
	}
}

func TestSelectedCredentialExportUsesOnlyFixedIDsAndCountsMissingTargets(t *testing.T) {
	service := NewAccountService(credentialExportHost())
	collection, errSources := service.ExportSelectedCredentialSources(t.Context(), TargetScope{
		Mode: "selected",
		IDs:  []string{"b", "missing", "b"},
	})
	if errSources != nil {
		t.Fatalf("ExportSelectedCredentialSources() error = %v", errSources)
	}
	defer clearCredentialExportCollection(&collection)
	if len(collection.Sources) != 1 || collection.Sources[0].Account.ID != "b" || collection.Skipped != 1 {
		t.Fatalf("selected collection = %#v", collection)
	}
	download, errRender := renderCredentialExport(CredentialExportFormatCPA, collection, time.Now())
	if errRender != nil {
		t.Fatalf("render selected export: %v", errRender)
	}
	if !bytes.Contains(download.Body, []byte("access-secret-b")) || bytes.Contains(download.Body, []byte("access-secret-a")) {
		t.Fatalf("selected export contained the wrong account: %s", download.Body)
	}
}

func TestCredentialExportTargetFormatsSkipNonCodexAccessTokens(t *testing.T) {
	host := credentialExportHost()
	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
		AuthIndex: "gemini", Name: "gemini.json", Provider: "gemini", Type: "gemini", Email: "gemini@example.com", Source: "file", Path: "/auths/gemini.json",
	})
	host.details["gemini"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "gemini", Name: "gemini.json", Path: "/auths/gemini.json",
		JSON: mustCredentialJSON(map[string]any{"type": "gemini", "email": "gemini@example.com", "access_token": "gemini-access-secret"}),
	}
	service := NewAccountService(host)
	geminiCollection, errGeminiSources := service.ExportCredentialSources(t.Context(), AccountFilters{Provider: "gemini"})
	if errGeminiSources != nil {
		t.Fatalf("Gemini ExportCredentialSources() error = %v", errGeminiSources)
	}
	defer clearCredentialExportCollection(&geminiCollection)
	cpa, errCPA := renderCredentialExport(CredentialExportFormatCPA, geminiCollection, time.Now())
	if errCPA != nil || !bytes.Contains(cpa.Body, []byte("gemini-access-secret")) {
		t.Fatalf("CPA export did not retain Gemini Auth data: error=%v body=%s", errCPA, cpa.Body)
	}

	collection, errSources := service.ExportCredentialSources(t.Context(), AccountFilters{})
	if errSources != nil {
		t.Fatalf("ExportCredentialSources() error = %v", errSources)
	}
	defer clearCredentialExportCollection(&collection)
	target, errTarget := renderCredentialExport(CredentialExportFormatSub2API, collection, time.Now())
	if errTarget != nil {
		t.Fatalf("sub2api export error = %v", errTarget)
	}
	if target.Exported != 2 || target.Skipped != 2 || bytes.Contains(target.Body, []byte("gemini-access-secret")) {
		t.Fatalf("sub2api export included incompatible provider: %#v body=%s", target, target.Body)
	}
}

func TestCredentialExportRejectsOversizedAuthEntry(t *testing.T) {
	host := credentialExportHost()
	host.entries = host.entries[:1]
	host.details["a"] = cpaapi.HostAuthGetResponse{
		AuthIndex: "a", Name: "alice-a.json", Path: "/auths/alice-a.json",
		JSON: json.RawMessage(`{"type":"codex","padding":"` + strings.Repeat("x", int(maxCredentialExportEntry)) + `"}`),
	}
	_, errExport := NewAccountService(host).ExportCredentialSources(t.Context(), AccountFilters{})
	if !errors.Is(errExport, ErrCredentialExportTooLarge) {
		t.Fatalf("oversized export error = %v, want %v", errExport, ErrCredentialExportTooLarge)
	}
}

func TestCredentialExportAccountLimitRemainsFiveHundred(t *testing.T) {
	if maxCredentialExportAccounts != 500 {
		t.Fatalf("credential export account limit = %d, want 500", maxCredentialExportAccounts)
	}
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}
	for index := 0; index < maxCredentialExportAccounts+1; index++ {
		name := fmt.Sprintf("account-%03d.json", index)
		host.entries = append(host.entries, cpaapi.HostAuthFileEntry{
			AuthIndex: name, Name: name, Provider: "codex", Type: "codex", Source: "file", Path: "/auths/" + name,
		})
	}
	_, errExport := NewAccountService(host).ExportCredentialSources(t.Context(), AccountFilters{})
	if !errors.Is(errExport, ErrCredentialExportTooLarge) {
		t.Fatalf("501-account credential export error = %v, want %v", errExport, ErrCredentialExportTooLarge)
	}
}

func TestCredentialExportRoutesRequireExplicitFormatAndDoNotLeakErrors(t *testing.T) {
	app := NewApp(credentialExportHost(), []byte("index"))
	defer app.Close()
	path := "/v0/management/plugins/cpa-account-config-manager/export/accounts"

	missing := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{Method: http.MethodGet, Path: path})
	if missing.StatusCode != http.StatusBadRequest || !strings.Contains(string(missing.Body), "format is required") {
		t.Fatalf("missing format response = %d %s", missing.StatusCode, missing.Body)
	}
	invalid := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: path, Query: map[string][]string{"format": {"csv"}},
	})
	if invalid.StatusCode != http.StatusBadRequest || strings.Contains(string(invalid.Body), "access-secret") {
		t.Fatalf("invalid format response = %d %s", invalid.StatusCode, invalid.Body)
	}
	valid := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: path, Query: map[string][]string{"format": {"sub2api"}, "search": {"alice-a.json"}},
	})
	if valid.StatusCode != http.StatusOK || valid.Headers.Get("Cache-Control") == "" || valid.Headers.Get("X-Exported-Accounts") != "1" {
		t.Fatalf("valid credential response = %d %#v %s", valid.StatusCode, valid.Headers, valid.Body)
	}
	selectedBody, errSelectedBody := json.Marshal(CredentialExportRequest{Scope: TargetScope{Mode: "selected", IDs: []string{"b"}}})
	if errSelectedBody != nil {
		t.Fatalf("marshal selected export request: %v", errSelectedBody)
	}
	selected := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   path,
		Query:  map[string][]string{"format": {"cpa"}},
		Body:   selectedBody,
	})
	if selected.StatusCode != http.StatusOK || selected.Headers.Get("X-Exported-Accounts") != "1" ||
		!bytes.Contains(selected.Body, []byte("access-secret-b")) || bytes.Contains(selected.Body, []byte("access-secret-a")) {
		t.Fatalf("selected credential response = %d %#v %s", selected.StatusCode, selected.Headers, selected.Body)
	}
	filteredPost := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   path,
		Query:  map[string][]string{"format": {"cpa"}},
		Body:   []byte(`{"scope":{"mode":"filtered"}}`),
	})
	if filteredPost.StatusCode != http.StatusBadRequest {
		t.Fatalf("filtered POST response = %d %s", filteredPost.StatusCode, filteredPost.Body)
	}

	failingHost := credentialExportHost()
	failingHost.errors = map[string]error{"a": errors.New("upstream access-secret-a")}
	failingHost.entries = failingHost.entries[:1]
	failingApp := NewApp(failingHost, []byte("index"))
	defer failingApp.Close()
	failed := failingApp.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodGet, Path: path, Query: map[string][]string{"format": {"cpa"}},
	})
	if failed.StatusCode != http.StatusUnprocessableEntity || strings.Contains(string(failed.Body), "access-secret-a") {
		t.Fatalf("failed credential response = %d %s", failed.StatusCode, failed.Body)
	}
}

func TestCredentialExportFilenamesRemainUniqueAcrossNumberedStems(t *testing.T) {
	used := make(map[string]int)
	got := []string{
		uniqueCredentialFilename("alice", used),
		uniqueCredentialFilename("alice-2", used),
		uniqueCredentialFilename("alice", used),
	}
	want := []string{"alice.json", "alice-2.json", "alice-3.json"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("filename %d = %q, want %q (all=%#v)", index, got[index], want[index], got)
		}
	}
}

func TestCredentialExportFilenameRemovesPathComponents(t *testing.T) {
	source := credentialExportSource{
		Account: Account{Name: "safe.json"},
		Object:  map[string]any{"email": "../../private\\secret"},
	}
	stem := credentialFileStem(source, 0)
	filename := uniqueCredentialFilename(stem, make(map[string]int))
	if filename != "private-secret.json" || path.Base(filename) != filename || strings.ContainsAny(filename, "/\\") {
		t.Fatalf("sanitized credential filename = %q", filename)
	}
}

func TestResultExportFormatsRemainSanitizedAndFormulaSafe(t *testing.T) {
	snapshot := JobSnapshot{
		ID: "job-1", State: JobStatePartial,
		Results: []JobResult{{
			ID: "auth-1", Name: "operator.json", Provider: "codex", Label: "=HYPERLINK(\"https://invalid\")",
			Status: ResultFailed, Error: "+generic failure", AppliedFields: []string{"note"}, Retryable: true,
		}},
	}
	for _, format := range []string{ResultExportFormatJSON, ResultExportFormatCSV, ResultExportFormatJSONL} {
		download, errRender := renderResultExport(format, snapshot)
		if errRender != nil {
			t.Fatalf("renderResultExport(%q) error = %v", format, errRender)
		}
		if strings.Contains(string(download.Body), "management-secret") || download.Credential {
			t.Fatalf("result %s export = %#v", format, download)
		}
	}
	csvDownload, errCSV := renderResultExport(ResultExportFormatCSV, snapshot)
	if errCSV != nil {
		t.Fatalf("render result CSV: %v", errCSV)
	}
	records, errRead := csv.NewReader(strings.NewReader(string(csvDownload.Body))).ReadAll()
	if errRead != nil || len(records) != 2 {
		t.Fatalf("read result CSV = %#v error=%v", records, errRead)
	}
	header := make(map[string]int, len(records[0]))
	for index, name := range records[0] {
		header[name] = index
	}
	if !strings.HasPrefix(records[1][header["label"]], "'") || !strings.HasPrefix(records[1][header["error"]], "'") {
		t.Fatalf("result CSV formulas were not neutralized: %#v", records[1])
	}
}

func credentialExportHost() *fakeAuthHost {
	expiresAt := time.Now().Add(24 * time.Hour).Unix()
	accessA := credentialTestJWT(map[string]any{
		"exp": expiresAt,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-a", "chatgpt_user_id": "user-a", "chatgpt_plan_type": "plus",
		},
	})
	accessB := credentialTestJWT(map[string]any{
		"exp":                         expiresAt,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-b"},
	})
	return &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "alice-a.json", Provider: "codex", Type: "codex", Label: "alice@example.com", Email: "alice@example.com", Source: "file", Path: "/auths/alice-a.json"},
			{AuthIndex: "b", Name: "alice-b.json", Provider: "codex", Type: "codex", Label: "alice@example.com", Email: "alice@example.com", Source: "file", Path: "/auths/alice-b.json"},
			{AuthIndex: "runtime", Name: "runtime.json", Provider: "codex", Type: "codex", Label: "runtime@example.com", Source: "runtime", RuntimeOnly: true},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a": {AuthIndex: "a", Name: "alice-a.json", Path: "/auths/alice-a.json", JSON: mustCredentialJSON(map[string]any{
				"type": "codex", "access_token": accessA + ".access-secret-a", "refresh_token": "", "id_token": "header.payload.id-a",
				"session_token": "session-secret-a", "email": "alice@example.com", "account_id": "account-a", "plan_type": "plus",
				"proxy_url": "http://user:proxy-secret-a@127.0.0.1:7890", "custom_provider_value": "preserved-a",
			})},
			"b": {AuthIndex: "b", Name: "alice-b.json", Path: "/auths/alice-b.json", JSON: mustCredentialJSON(map[string]any{
				"type": "codex", "access_token": accessB + ".access-secret-b", "refresh_token": "refresh-secret-b",
				"id_token": "header.payload.id-b", "email": "alice@example.com", "account_id": "account-b", "custom_provider_value": "preserved-b",
			})},
		},
	}
}

func credentialTestJWT(payload map[string]any) string {
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	body, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body) + ".signature"
}

func mustCredentialJSON(value any) json.RawMessage {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		panic(errMarshal)
	}
	return body
}

func assertCredentialTargetShape(t *testing.T, format string, document any) {
	t.Helper()
	object, ok := document.(map[string]any)
	if !ok {
		t.Fatalf("%s document type = %T", format, document)
	}
	switch format {
	case CredentialExportFormatSub2API:
		accounts := object["accounts"].([]any)
		account := accounts[0].(map[string]any)
		if account["platform"] != "openai" || account["type"] != "oauth" || account["auto_pause_on_expired"] != true {
			t.Fatalf("sub2api account = %#v", account)
		}
	case CredentialExportFormatCockpit:
		if object["type"] != "codex" || object["account_id"] != "account-a" {
			t.Fatalf("cockpit account = %#v", object)
		}
	case CredentialExportFormat9Router:
		if object["provider"] != "codex" || object["authType"] != "oauth" {
			t.Fatalf("9router account = %#v", object)
		}
	case CredentialExportFormatCodex:
		if object["auth_mode"] != "chatgpt" || object["OPENAI_API_KEY"] != nil {
			t.Fatalf("codex account = %#v", object)
		}
	case CredentialExportFormatAxonHub:
		if object["auth_mode"] != "chatgpt" || object["axonhub_refresh_token_placeholder"] != true {
			t.Fatalf("AxonHub account = %#v", object)
		}
	case CredentialExportFormatCodexManager:
		if object["tokens"] == nil || object["meta"] == nil {
			t.Fatalf("Codex-Manager account = %#v", object)
		}
	}
}
