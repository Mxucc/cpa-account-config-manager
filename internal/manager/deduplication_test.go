package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

func TestAccountDeduplicationGroupsStableIdentityTransitivelyAndRanksKeep(t *testing.T) {
	now := time.Now().UTC()
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Email: "Person@Example.com ", Disabled: true, Status: "error", Source: "file", Path: "/auths/a.json", UpdatedAt: now.Add(-3 * time.Hour)},
			{AuthIndex: "b", Name: "b.json", Provider: agentIdentityProvider, Email: "bridge@example.com", Status: "ready", Source: "file", Path: "/auths/b.json", UpdatedAt: now},
			{AuthIndex: "c", Name: "c.json", Provider: "codex", Email: "BRIDGE@example.com", Unavailable: true, Status: "unavailable", Source: "file", Path: "/auths/c.json", UpdatedAt: now.Add(-time.Hour)},
			{AuthIndex: "runtime", Name: "runtime.json", Provider: "codex", Email: "person@example.com", RuntimeOnly: true, Source: "runtime"},
			{AuthIndex: "gemini", Name: "gemini.json", Provider: "gemini", Email: "person@example.com", Source: "file", Path: "/auths/gemini.json"},
			{AuthIndex: "unknown", Name: "unknown.json", Provider: "codex", Source: "file", Path: "/auths/unknown.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"a":       {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex","account_id":"UPSTREAM-ACCOUNT","email":"Person@Example.com","access_token":"secret-a"}`)},
			"b":       {AuthIndex: "b", Name: "b.json", Path: "/auths/b.json", JSON: json.RawMessage(`{"type":"codex-agent-identity","chatgpt_account_id":"upstream-account","email":"bridge@example.com","agent_identity":"secret-b"}`)},
			"c":       {AuthIndex: "c", Name: "c.json", Path: "/auths/c.json", JSON: json.RawMessage(`{"type":"codex","account_id":"different-id","email":"bridge@example.com","refresh_token":"secret-c"}`)},
			"gemini":  {AuthIndex: "gemini", Name: "gemini.json", Path: "/auths/gemini.json", JSON: json.RawMessage(`{"type":"gemini","email":"person@example.com","access_token":"secret-gemini"}`)},
			"unknown": {AuthIndex: "unknown", Name: "unknown.json", Path: "/auths/unknown.json", JSON: json.RawMessage(`{"type":"codex","access_token":"secret-unknown"}`)},
		},
	}

	preview, errPreview := NewAccountDeduplicationService(NewAccountService(host)).Preview(t.Context())
	if errPreview != nil {
		t.Fatalf("Preview() error = %v", errPreview)
	}
	if preview.ScannedCredentials != 6 || preview.IdentifiedCredentials != 5 || preview.MissingIdentity != 1 {
		t.Fatalf("identity metrics = %#v", preview)
	}
	if preview.DuplicateGroups != 1 || preview.DuplicateCredentials != 3 || preview.ProposedDeletions != 2 || preview.ReadOnlySkipped != 1 {
		t.Fatalf("duplicate metrics = %#v", preview)
	}
	group := preview.Groups[0]
	if group.Provider != "codex" || group.MatchedBy != "multiple" || group.KeepID != "b" || group.KeepReason != "enabled_account" {
		t.Fatalf("group = %#v", group)
	}
	actions := make(map[string]string, len(group.Members))
	for _, member := range group.Members {
		actions[member.ID] = member.RecommendedAction
	}
	if actions["a"] != "delete" || actions["b"] != "keep" || actions["c"] != "delete" || actions["runtime"] != "skip" {
		t.Fatalf("actions = %#v", actions)
	}
	encoded, errMarshal := json.Marshal(preview)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	for _, forbidden := range []string{"UPSTREAM-ACCOUNT", "upstream-account", "secret-a", "secret-b", "secret-c", "/auths"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestAccountDeduplicationUsesDeterministicIDFingerprintWithoutEmail(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "z", Name: "z.json", Provider: "codex", Source: "file", Path: "/auths/z.json"},
			{AuthIndex: "a", Name: "a.json", Provider: "codex", Source: "file", Path: "/auths/a.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"z": {AuthIndex: "z", Name: "z.json", Path: "/auths/z.json", JSON: json.RawMessage(`{"type":"codex","account_id":"sensitive-upstream-id","access_token":"secret-z"}`)},
			"a": {AuthIndex: "a", Name: "a.json", Path: "/auths/a.json", JSON: json.RawMessage(`{"type":"codex","chatgpt_account_id":"sensitive-upstream-id","access_token":"secret-a"}`)},
		},
	}
	service := NewAccountDeduplicationService(NewAccountService(host))
	first, errFirst := service.Preview(t.Context())
	second, errSecond := service.Preview(t.Context())
	if errFirst != nil || errSecond != nil {
		t.Fatalf("Preview() errors = %v, %v", errFirst, errSecond)
	}
	if len(first.Groups) != 1 || len(second.Groups) != 1 || first.Groups[0].KeepID != "a" || first.Groups[0].MatchedBy != "account_id" {
		t.Fatalf("groups = %#v %#v", first.Groups, second.Groups)
	}
	if first.Groups[0].ID != second.Groups[0].ID || first.Groups[0].IdentityLabel != second.Groups[0].IdentityLabel || strings.Contains(first.Groups[0].IdentityLabel, "sensitive") {
		t.Fatalf("fingerprints are not deterministic and redacted: %#v %#v", first.Groups[0], second.Groups[0])
	}
}

func TestAccountDeduplicationAllowsTenThousandAndRejectsMore(t *testing.T) {
	entries := make([]cpaapi.HostAuthFileEntry, maxDeduplicationAccounts)
	for index := range entries {
		entries[index] = cpaapi.HostAuthFileEntry{
			AuthIndex: fmt.Sprintf("runtime-%05d", index), Provider: "codex", RuntimeOnly: true,
			Email: fmt.Sprintf("account-%05d@example.com", index), Source: "runtime",
		}
	}
	service := NewAccountDeduplicationService(NewAccountService(&fakeAuthHost{entries: entries}))
	preview, errPreview := service.Preview(t.Context())
	if errPreview != nil || preview.ScannedCredentials != maxDeduplicationAccounts || preview.DuplicateGroups != 0 {
		t.Fatalf("10000-account preview = %#v error=%v", preview, errPreview)
	}
	entries = append(entries, cpaapi.HostAuthFileEntry{AuthIndex: "overflow", Provider: "codex", RuntimeOnly: true, Email: "overflow@example.com"})
	_, errOverflow := NewAccountDeduplicationService(NewAccountService(&fakeAuthHost{entries: entries})).Preview(t.Context())
	if !errors.Is(errOverflow, ErrDeduplicationTooLarge) {
		t.Fatalf("overflow error = %v, want ErrDeduplicationTooLarge", errOverflow)
	}
}

func TestAccountDeduplicationPreviewRouteIsRedacted(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "one", Name: "one.json", Provider: "codex", Email: "same@example.com", Source: "file", Path: "/auths/one.json"},
			{AuthIndex: "two", Name: "two.json", Provider: "codex", Email: "SAME@example.com", Source: "file", Path: "/auths/two.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"one": {AuthIndex: "one", Name: "one.json", Path: "/auths/one.json", JSON: json.RawMessage(`{"type":"codex","email":"same@example.com","access_token":"route-secret-one"}`)},
			"two": {AuthIndex: "two", Name: "two.json", Path: "/auths/two.json", JSON: json.RawMessage(`{"type":"codex","email":"same@example.com","access_token":"route-secret-two"}`)},
		},
	}
	app := NewApp(host, []byte("index"))
	defer app.Close()
	response := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
		Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/accounts/deduplicate/preview",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response = %d %s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), "route-secret") || !strings.Contains(string(response.Body), `"duplicate_groups":1`) {
		t.Fatalf("response is invalid or leaked a secret: %s", response.Body)
	}
}
