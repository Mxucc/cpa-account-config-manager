package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestImportConvertReferenceFormats(t *testing.T) {
	now := time.Date(2026, time.July, 15, 7, 30, 0, 0, time.UTC)
	accessToken := importTestJWT(map[string]any{
		"exp":   1780473960,
		"email": "jwt@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-from-jwt",
			"chatgpt_plan_type":  "plus",
		},
	})
	payload := map[string]any{
		"export": map[string]any{
			"accounts": []any{
				map[string]any{
					"name":     "sub2api account",
					"platform": "openai",
					"type":     "oauth",
					"credentials": map[string]any{
						"access_token":       accessToken,
						"refresh_token":      "refresh-secret",
						"id_token":           "real.header.signature",
						"chatgpt_account_id": "sub2api-account",
						"email":              "sub2api@example.com",
					},
				},
				map[string]any{
					"user":         map[string]any{"id": "user-2", "email": "session@example.com"},
					"account":      map[string]any{"id": "session-account", "planType": "team"},
					"accessToken":  "opaque-access-token",
					"sessionToken": "session-secret",
				},
			},
		},
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}

	result, errParse := parseImportUpload(importUpload{
		Name:        "accounts.json",
		ContentType: "application/json",
		Data:        raw,
	}, defaultImportLimits(), now)
	if errParse != nil {
		t.Fatalf("parseImportUpload() error = %v", errParse)
	}
	if result.SourceFiles != 1 || len(result.Candidates) != 2 {
		t.Fatalf("result = %#v, want one source and two candidates", result)
	}

	first := decodeImportCandidate(t, result.Candidates[0])
	if first["type"] != "codex" || first["email"] != "sub2api@example.com" || first["account_id"] != "sub2api-account" {
		t.Fatalf("first candidate identity = %#v", first)
	}
	if first["access_token"] != accessToken || first["refresh_token"] != "refresh-secret" || first["id_token"] != "real.header.signature" {
		t.Fatalf("first candidate credentials were not preserved: %#v", first)
	}
	if _, exists := first["expired"]; exists {
		t.Fatalf("refreshable candidate should not carry access-token expiry: %#v", first)
	}

	second := decodeImportCandidate(t, result.Candidates[1])
	if second["email"] != "session@example.com" || second["account_id"] != "session-account" {
		t.Fatalf("second candidate identity = %#v", second)
	}
	if second["session_token"] != "session-secret" || second["refresh_token"] != "" {
		t.Fatalf("second candidate tokens = %#v", second)
	}
	idToken, _ := second["id_token"].(string)
	if len(strings.Split(idToken, ".")) != 3 || second["id_token_synthetic"] != true {
		t.Fatalf("synthetic id token = %#v", second)
	}
	if second["last_refresh"] != now.Format(time.RFC3339) {
		t.Fatalf("last_refresh = %#v, want %q", second["last_refresh"], now.Format(time.RFC3339))
	}
}

func TestImportParseArbitraryNestedJSON(t *testing.T) {
	accessToken := importTestJWT(map[string]any{
		"email":                       "nested@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "nested-account"},
	})
	raw := []byte(`{
		"unrelated":{"enabled":true},
		"payload":{"batches":[{"records":[
			{"provider":"codex","authType":"oauth","accessToken":"` + accessToken + `","providerSpecificData":{"email":"nested@example.com","chatgptAccountId":"nested-account"}},
			{"message":"not an account"}
		]}]}
	}`)

	result, errParse := parseImportUpload(importUpload{Name: "nested.json", Data: raw}, defaultImportLimits(), time.Unix(0, 0).UTC())
	if errParse != nil {
		t.Fatalf("parseImportUpload() error = %v", errParse)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(result.Candidates))
	}
	candidate := result.Candidates[0]
	if candidate.SourcePath != "$.payload.batches[0].records[0]" || candidate.Email != "nested@example.com" || candidate.AccountID != "nested-account" {
		t.Fatalf("candidate = %#v", candidate)
	}
}

func TestImportMultiDocumentTextJSON(t *testing.T) {
	raw := []byte(`{"email":"first@example.com","account_id":"account-1","access_token":"access-1"}
{"wrapper":{"email":"second@example.com","account_id":"account-2","access_token":"access-2"}}`)

	result, errParse := parseImportUpload(importUpload{Name: "accounts.txt", ContentType: "text/plain", Data: raw}, defaultImportLimits(), time.Unix(0, 0).UTC())
	if errParse != nil {
		t.Fatalf("parseImportUpload() error = %v", errParse)
	}
	if result.SourceFiles != 1 || len(result.Candidates) != 2 {
		t.Fatalf("result = %#v, want one source and two candidates", result)
	}
	if result.Candidates[0].SourcePath != "$document[0]" || result.Candidates[1].SourcePath != "$document[1].wrapper" {
		t.Fatalf("source paths = %q, %q", result.Candidates[0].SourcePath, result.Candidates[1].SourcePath)
	}
	if got := importInputType(importUpload{Name: "accounts.ndjson", ContentType: "application/x-ndjson"}); got != "text" {
		t.Fatalf("importInputType() = %q, want text", got)
	}
}

func TestImportMultiDocumentTextRejectsInvalidTrailingDocument(t *testing.T) {
	raw := []byte("{\"email\":\"valid@example.com\",\"access_token\":\"secret\"}\n{broken")
	result, errParse := parseImportUpload(importUpload{Name: "broken.jsonl", Data: raw}, defaultImportLimits(), time.Unix(0, 0).UTC())
	if errParse == nil || !strings.Contains(errParse.Error(), "invalid JSON") {
		t.Fatalf("error = %v, want invalid JSON", errParse)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("invalid stream retained candidates: %#v", result.Candidates)
	}
}

func TestImportMultiDocumentTextHonorsAggregateNodeLimit(t *testing.T) {
	limits := defaultImportLimits()
	limits.MaxJSONNodes = 2
	_, errParse := parseImportUpload(importUpload{Name: "too-many.jsonl", Data: []byte("{}\n{}\n{}")}, limits, time.Unix(0, 0).UTC())
	if errParse == nil || !strings.Contains(errParse.Error(), "node limit") {
		t.Fatalf("error = %v, want node limit", errParse)
	}
}

func TestImportZIPParsesJSONAndSkipsOtherEntries(t *testing.T) {
	archive := importTestZIP(t, []importTestZIPEntry{
		{Name: "nested/first.json", Content: `{"email":"zip@example.com","account_id":"zip-account","access_token":"zip-access"}`},
		{Name: "notes.txt", Content: "not imported"},
	})

	result, errParse := parseImportUpload(importUpload{Name: "bundle.zip", ContentType: "application/zip", Data: archive}, defaultImportLimits(), time.Unix(0, 0).UTC())
	if errParse != nil {
		t.Fatalf("parseImportUpload() error = %v", errParse)
	}
	if result.SourceFiles != 1 || len(result.Candidates) != 1 || len(result.Skipped) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Candidates[0].SourceName != "nested/first.json" || !strings.Contains(result.Skipped[0].Reason, "JSON") {
		t.Fatalf("unexpected source/skip summary: %#v", result)
	}
}

func TestImportZIPParsesTextJSONDocuments(t *testing.T) {
	archive := importTestZIP(t, []importTestZIPEntry{
		{Name: "nested/accounts.jsonl", Content: "{\"email\":\"one@example.com\",\"account_id\":\"one\",\"access_token\":\"one-secret\"}\n{\"email\":\"two@example.com\",\"account_id\":\"two\",\"access_token\":\"two-secret\"}"},
		{Name: "nested/readme.md", Content: "not imported"},
	})

	result, errParse := parseImportUpload(importUpload{Name: "text-bundle.zip", ContentType: "application/zip", Data: archive}, defaultImportLimits(), time.Unix(0, 0).UTC())
	if errParse != nil {
		t.Fatalf("parseImportUpload() error = %v", errParse)
	}
	if result.SourceFiles != 1 || len(result.Candidates) != 2 || len(result.Skipped) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Candidates[0].SourcePath != "$document[0]" || result.Candidates[1].SourcePath != "$document[1]" {
		t.Fatalf("candidate paths = %#v", result.Candidates)
	}
}

func TestImportZIPRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []importTestZIPEntry
		want    string
	}{
		{
			name:    "path traversal",
			entries: []importTestZIPEntry{{Name: "../escape.json", Content: `{}`}},
			want:    "unsafe path",
		},
		{
			name:    "backslash traversal",
			entries: []importTestZIPEntry{{Name: `..\\escape.json`, Content: `{}`}},
			want:    "unsafe path",
		},
		{
			name:    "symbolic link",
			entries: []importTestZIPEntry{{Name: "link.json", Content: "target", Mode: os.ModeSymlink | 0o777}},
			want:    "symbolic link",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := importTestZIP(t, test.entries)
			_, errParse := parseImportUpload(importUpload{Name: "unsafe.zip", Data: archive}, defaultImportLimits(), time.Unix(0, 0).UTC())
			if errParse == nil || !strings.Contains(errParse.Error(), test.want) {
				t.Fatalf("error = %v, want %q", errParse, test.want)
			}
		})
	}
}

func TestImportZIPRejectsExpansionLimit(t *testing.T) {
	archive := importTestZIP(t, []importTestZIPEntry{{
		Name:    "large.json",
		Content: `{"email":"large@example.com","access_token":"` + strings.Repeat("a", 128) + `"}`,
	}})
	limits := defaultImportLimits()
	limits.MaxEntryBytes = 64
	limits.MaxExpandedBytes = 64

	_, errParse := parseImportUpload(importUpload{Name: "large.zip", Data: archive}, limits, time.Unix(0, 0).UTC())
	if errParse == nil || !strings.Contains(errParse.Error(), "expanded size") {
		t.Fatalf("error = %v, want expanded size limit", errParse)
	}
}

func TestImportParseRejectsExcessiveNesting(t *testing.T) {
	limits := defaultImportLimits()
	limits.MaxJSONDepth = 3
	raw := []byte(`{"a":{"b":{"c":{"d":{"email":"deep@example.com","access_token":"token"}}}}}`)

	_, errParse := parseImportUpload(importUpload{Name: "deep.json", Data: raw}, limits, time.Unix(0, 0).UTC())
	if errParse == nil || !strings.Contains(errParse.Error(), "nesting") {
		t.Fatalf("error = %v, want nesting limit", errParse)
	}
}

func TestImportMultiFilesAggregatesMixedJSONAndZIP(t *testing.T) {
	archive := importTestZIP(t, []importTestZIPEntry{
		{Name: "zip/second.json", Content: `{"email":"second@example.com","account_id":"account-2","access_token":"access-2"}`},
		{Name: "zip/third.json", Content: `[{"email":"third@example.com","account_id":"account-3","access_token":"access-3"}]`},
		{Name: "zip/readme.txt", Content: "ignored"},
	})
	result, errParse := parseImportUploads([]importUpload{
		{Name: "first.json", ContentType: "application/json", Data: []byte(`{"email":"first@example.com","account_id":"account-1","access_token":"access-1"}`)},
		{Name: "bundle.zip", ContentType: "application/zip", Data: archive},
		{Name: "broken.json", ContentType: "application/json", Data: []byte(`{"broken"`)},
	}, defaultImportLimits(), time.Unix(0, 0).UTC())
	if errParse != nil {
		t.Fatalf("parseImportUploads() error = %v", errParse)
	}
	if result.SourceFiles != 3 || len(result.Candidates) != 3 || len(result.Skipped) != 2 {
		t.Fatalf("result = %#v", result)
	}
	gotEmails := []string{result.Candidates[0].Email, result.Candidates[1].Email, result.Candidates[2].Email}
	if strings.Join(gotEmails, ",") != "first@example.com,second@example.com,third@example.com" {
		t.Fatalf("candidate emails = %#v", gotEmails)
	}
	if result.Skipped[0].SourceName != "bundle.zip!/zip/readme.txt" || result.Skipped[1].SourceName != "broken.json" {
		t.Fatalf("skipped = %#v", result.Skipped)
	}
}

func TestImportServicePreviewRedactsAndImports(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{{Name: "codex-existing.json", AuthIndex: "existing", Source: "file", Path: "/auths/codex-existing.json"}},
		details: map[string]cpaapi.HostAuthGetResponse{},
	}
	service := NewImportService(host, NewMutationCoordinator())
	service.now = func() time.Time { return time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC) }
	raw := []byte(`{"accounts":[
		{"email":"first@example.com","account_id":"account-1","access_token":"access-secret-1","refresh_token":"refresh-secret-1"},
		{"email":"second@example.com","account_id":"account-2","access_token":"access-secret-2","id_token":"id-secret-2"}
	]}`)

	preview, errPreview := service.Preview(context.Background(), importUpload{Name: "accounts.json", Data: raw})
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if preview.Total != 2 || preview.SourceFiles != 1 || len(preview.Items) != 2 || preview.ExpiresAt.Sub(preview.CreatedAt) != defaultPreviewTTL {
		t.Fatalf("preview = %#v", preview)
	}
	encodedPreview, _ := json.Marshal(preview)
	for _, secret := range []string{"access-secret", "refresh-secret", "id-secret"} {
		if bytes.Contains(encodedPreview, []byte(secret)) {
			t.Fatalf("preview leaked %q: %s", secret, encodedPreview)
		}
	}

	result, errStart := service.Start(context.Background(), preview.ID)
	if errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	if result.Imported != 2 || result.Failed != 0 || result.Skipped != 0 || len(result.Results) != 2 {
		t.Fatalf("result = %#v", result)
	}
	encodedResult, _ := json.Marshal(result)
	for _, secret := range []string{"access-secret", "refresh-secret", "id-secret"} {
		if bytes.Contains(encodedResult, []byte(secret)) {
			t.Fatalf("result leaked %q: %s", secret, encodedResult)
		}
	}
	if len(host.saves) != 2 {
		t.Fatalf("save calls = %#v, want 2", host.saves)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(host.saves[0].JSON, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if saved["access_token"] != "access-secret-1" || saved["refresh_token"] != "refresh-secret-1" {
		t.Fatalf("saved auth JSON = %#v", saved)
	}
	if _, errRepeat := service.Start(context.Background(), preview.ID); !errors.Is(errRepeat, ErrImportPreviewNotFound) {
		t.Fatalf("second Start() error = %v, want preview not found", errRepeat)
	}
}

func TestImportServiceCollisionDoesNotOverwrite(t *testing.T) {
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}
	service := NewImportService(host, NewMutationCoordinator())
	preview, errPreview := service.Preview(context.Background(), importUpload{
		Name: "race.json",
		Data: []byte(`{"email":"race@example.com","account_id":"race-account","access_token":"race-secret"}`),
	})
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	targetName := preview.Items[0].TargetName
	host.mu.Lock()
	host.entries = append(host.entries, cpaapi.HostAuthFileEntry{Name: targetName, AuthIndex: "raced", Source: "file", Path: "/auths/" + targetName})
	host.mu.Unlock()

	result, errStart := service.Start(context.Background(), preview.ID)
	if errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	if result.Imported != 0 || result.Skipped != 1 || len(host.saves) != 0 {
		t.Fatalf("result = %#v saves=%#v", result, host.saves)
	}
	if result.Results[0].Status != ImportResultSkipped || !strings.Contains(result.Results[0].Error, "already exists") {
		t.Fatalf("collision result = %#v", result.Results[0])
	}
}

func TestImportServiceBusyKeepsPreviewForRetry(t *testing.T) {
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}
	mutations := NewMutationCoordinator()
	service := NewImportService(host, mutations)
	preview, errPreview := service.Preview(context.Background(), importUpload{
		Name: "busy.json",
		Data: []byte(`{"email":"busy@example.com","account_id":"busy-account","access_token":"busy-secret"}`),
	})
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if !mutations.TryAcquire("another-writer") {
		t.Fatal("failed to acquire test mutation owner")
	}
	if _, errStart := service.Start(context.Background(), preview.ID); !errors.Is(errStart, ErrJobBusy) {
		t.Fatalf("Start() error = %v, want busy", errStart)
	}
	mutations.Release("another-writer")
	result, errRetry := service.Start(context.Background(), preview.ID)
	if errRetry != nil || result.Imported != 1 {
		t.Fatalf("retry result = %#v error=%v", result, errRetry)
	}
}

func TestImportPreviewStoreActivelyClearsExpiredCredentials(t *testing.T) {
	ttl := 250 * time.Millisecond
	now := time.Now().UTC()
	store := &importPreviewStore{
		ttl:     ttl,
		entries: make(map[string]storedImportPreview),
		timers:  make(map[string]*time.Timer),
	}
	defer store.clear()
	store.put(storedImportPreview{
		Public: ImportPreview{ID: "expiring-preview", CreatedAt: now, ExpiresAt: now.Add(ttl)},
		Items:  []storedImportItem{{AuthJSON: json.RawMessage(`{"access_token":"expires-secret"}`)}},
	})

	store.mu.Lock()
	retained := store.entries["expiring-preview"].Items[0].AuthJSON
	store.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		_, exists := store.entries["expiring-preview"]
		store.mu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview credentials were not removed after the TTL")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, value := range retained {
		if value != 0 {
			t.Fatalf("expired credential memory was not cleared: %q", retained)
		}
	}
	store.mu.Lock()
	timerCount := len(store.timers)
	store.mu.Unlock()
	if timerCount != 0 {
		t.Fatalf("expiration timers = %d, want 0", timerCount)
	}
}

func TestImportRoutesPreviewAndStartWithoutCredentialLeak(t *testing.T) {
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	raw := []byte(`{"email":"route@example.com","account_id":"route-account","access_token":"route-access-secret","refresh_token":"route-refresh-secret"}`)

	previewResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/import/preview",
		Headers: http.Header{
			"Content-Type":          []string{"application/json"},
			"X-Cpa-Import-Filename": []string{"route.json"},
		},
		Body: raw,
	})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", previewResponse.StatusCode, previewResponse.Body)
	}
	if bytes.Contains(previewResponse.Body, []byte("route-access-secret")) || bytes.Contains(previewResponse.Body, []byte("route-refresh-secret")) {
		t.Fatalf("preview response leaked credentials: %s", previewResponse.Body)
	}
	var preview ImportPreview
	if errDecode := json.Unmarshal(previewResponse.Body, &preview); errDecode != nil {
		t.Fatalf("decode preview: %v", errDecode)
	}
	startBody, _ := json.Marshal(ImportStartRequest{PreviewID: preview.ID})
	startResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/import/start",
		Body:   startBody,
	})
	if startResponse.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d body=%s", startResponse.StatusCode, startResponse.Body)
	}
	if bytes.Contains(startResponse.Body, []byte("route-access-secret")) || bytes.Contains(startResponse.Body, []byte("route-refresh-secret")) {
		t.Fatalf("start response leaked credentials: %s", startResponse.Body)
	}
	if len(host.saves) != 1 {
		t.Fatalf("save calls = %#v", host.saves)
	}

	resourceResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/resource/plugins/cpa-account-config-manager/import/preview",
		Body:   raw,
	})
	if resourceResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("resource import status = %d, want 404", resourceResponse.StatusCode)
	}
}

func TestImportMultipartRouteAggregatesMultipleFilesAndZIPEntries(t *testing.T) {
	host := &fakeAuthHost{details: map[string]cpaapi.HostAuthGetResponse{}}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	archive := importTestZIP(t, []importTestZIPEntry{
		{Name: "nested/two.json", Content: `{"email":"two@example.com","account_id":"account-2","access_token":"zip-secret-2"}`},
		{Name: "nested/three.json", Content: `{"email":"three@example.com","account_id":"account-3","access_token":"zip-secret-3"}`},
	})
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	first, errFirst := writer.CreateFormFile("files", "one.json")
	if errFirst != nil {
		t.Fatalf("CreateFormFile() error = %v", errFirst)
	}
	_, _ = first.Write([]byte(`{"email":"one@example.com","account_id":"account-1","access_token":"json-secret-1"}`))
	bundle, errBundle := writer.CreateFormFile("files", "bundle.zip")
	if errBundle != nil {
		t.Fatalf("CreateFormFile() error = %v", errBundle)
	}
	_, _ = bundle.Write(archive)
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	previewResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method:  http.MethodPost,
		Path:    "/v0/management/plugins/cpa-account-config-manager/import/preview",
		Headers: http.Header{"Content-Type": []string{writer.FormDataContentType()}},
		Body:    requestBody.Bytes(),
	})
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", previewResponse.StatusCode, previewResponse.Body)
	}
	for _, secret := range []string{"json-secret-1", "zip-secret-2", "zip-secret-3"} {
		if bytes.Contains(previewResponse.Body, []byte(secret)) {
			t.Fatalf("multipart preview leaked %q: %s", secret, previewResponse.Body)
		}
	}
	var preview ImportPreview
	if errDecode := json.Unmarshal(previewResponse.Body, &preview); errDecode != nil {
		t.Fatalf("decode preview: %v", errDecode)
	}
	if preview.Total != 3 || preview.SourceFiles != 3 || len(preview.Items) != 3 {
		t.Fatalf("preview = %#v", preview)
	}
	startBody, _ := json.Marshal(ImportStartRequest{PreviewID: preview.ID})
	startResponse := app.HandleManagement(context.Background(), cpaapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-account-config-manager/import/start",
		Body:   startBody,
	})
	if startResponse.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d body=%s", startResponse.StatusCode, startResponse.Body)
	}
	if len(host.saves) != 3 {
		t.Fatalf("save calls = %d, want 3", len(host.saves))
	}
}

type importTestZIPEntry struct {
	Name    string
	Content string
	Mode    os.FileMode
}

func importTestZIP(t *testing.T, entries []importTestZIPEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		if entry.Mode != 0 {
			header.SetMode(entry.Mode)
		}
		handle, errCreate := writer.CreateHeader(header)
		if errCreate != nil {
			t.Fatalf("CreateHeader() error = %v", errCreate)
		}
		if _, errWrite := handle.Write([]byte(entry.Content)); errWrite != nil {
			t.Fatalf("Write() error = %v", errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	return buffer.Bytes()
}

func importTestJWT(payload map[string]any) string {
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	body, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

func decodeImportCandidate(t *testing.T, candidate importCandidate) map[string]any {
	t.Helper()
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(candidate.AuthJSON, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	return decoded
}
