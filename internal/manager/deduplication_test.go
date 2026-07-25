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

func TestAccountDeduplicationOptionsAvoidSharedTeamAccountIDs(t *testing.T) {
	host := &fakeAuthHost{
		entries: []cpaapi.HostAuthFileEntry{
			{AuthIndex: "team-a", Name: "team-a.json", Provider: "codex", Email: "team-a@example.com", Source: "file", Path: "/auths/team-a.json"},
			{AuthIndex: "team-b", Name: "team-b.json", Provider: "codex", Email: "team-b@example.com", Source: "file", Path: "/auths/team-b.json"},
			{AuthIndex: "plus-a", Name: "plus-a.json", Provider: "codex", Email: "plus-a@example.com", Source: "file", Path: "/auths/plus-a.json"},
			{AuthIndex: "plus-b", Name: "plus-b.json", Provider: "codex", Email: "plus-b@example.com", Source: "file", Path: "/auths/plus-b.json"},
		},
		details: map[string]cpaapi.HostAuthGetResponse{
			"team-a": {AuthIndex: "team-a", Name: "team-a.json", Path: "/auths/team-a.json", JSON: json.RawMessage(`{"type":"codex","account_id":"shared-team","email":"team-a@example.com","plan_type":"plus","id_token":"{\"chatgpt_plan_type\":\"k12\"}"}`)},
			"team-b": {AuthIndex: "team-b", Name: "team-b.json", Path: "/auths/team-b.json", JSON: json.RawMessage(`{"type":"codex","account_id":"shared-team","email":"team-b@example.com","id_token":"{\"chatgpt_plan_type\":\"team\"}"}`)},
			"plus-a": {AuthIndex: "plus-a", Name: "plus-a.json", Path: "/auths/plus-a.json", JSON: json.RawMessage(`{"type":"codex","account_id":"shared-plus","email":"plus-a@example.com","plan_type":"plus"}`)},
			"plus-b": {AuthIndex: "plus-b", Name: "plus-b.json", Path: "/auths/plus-b.json", JSON: json.RawMessage(`{"type":"codex","account_id":"shared-plus","email":"plus-b@example.com","plan_type":"plus"}`)},
		},
	}
	service := NewAccountDeduplicationService(NewAccountService(host))

	standard, errStandard := service.Preview(t.Context())
	if errStandard != nil || standard.DuplicateGroups != 2 || standard.ExcludedCredentials != 0 {
		t.Fatalf("standard Preview() = %#v, error = %v", standard, errStandard)
	}
	ignored, errIgnored := service.Preview(t.Context(), AccountDeduplicationOptions{IgnoreAccountID: true})
	if errIgnored != nil || ignored.DuplicateGroups != 0 || ignored.IdentifiedCredentials != 4 || ignored.MissingIdentity != 0 || !ignored.Options.IgnoreAccountID {
		t.Fatalf("ignore-account-ID Preview() = %#v, error = %v", ignored, errIgnored)
	}
	excluded, errExcluded := service.Preview(t.Context(), AccountDeduplicationOptions{ExcludeTeamAccounts: true})
	if errExcluded != nil || excluded.DuplicateGroups != 1 || excluded.ExcludedCredentials != 2 || !excluded.Options.ExcludeTeamAccounts {
		t.Fatalf("exclude-team Preview() = %#v, error = %v", excluded, errExcluded)
	}
	if len(excluded.Groups) != 1 || excluded.Groups[0].KeepID != "plus-a" {
		t.Fatalf("exclude-team groups = %#v", excluded.Groups)
	}
	combined, errCombined := service.Preview(t.Context(), AccountDeduplicationOptions{IgnoreAccountID: true, ExcludeTeamAccounts: true})
	if errCombined != nil || combined.DuplicateGroups != 0 || combined.ExcludedCredentials != 2 || !combined.Options.IgnoreAccountID || !combined.Options.ExcludeTeamAccounts {
		t.Fatalf("combined Preview() = %#v, error = %v", combined, errCombined)
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
		Body: []byte(`{"ignore_account_id":false,"exclude_team_accounts":false}`),
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response = %d %s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), "route-secret") || !strings.Contains(string(response.Body), `"duplicate_groups":1`) {
		t.Fatalf("response is invalid or leaked a secret: %s", response.Body)
	}

	for _, body := range [][]byte{
		[]byte(`{"ignore_account_id":true,"unknown":true}`),
		[]byte(`{"ignore_account_id":true} {}`),
	} {
		invalid := app.HandleManagement(t.Context(), cpaapi.ManagementRequest{
			Method: http.MethodPost, Path: "/v0/management/plugins/cpa-account-config-manager/accounts/deduplicate/preview", Body: body,
		})
		if invalid.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid body %q status = %d body = %s", body, invalid.StatusCode, invalid.Body)
		}
	}
}
