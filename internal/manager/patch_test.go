package manager

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBatchPatchValidationNormalizesWithoutExposingValues(t *testing.T) {
	disabled := true
	proxyURL := "socks5://proxy-user:proxy-password@127.0.0.1:1080"
	patch, errValidate := (BatchPatch{
		Disabled: &disabled,
		ProxyURL: &proxyURL,
		Headers: &HeaderPatch{
			Set: map[string]string{
				"authorization": "Bearer header-secret",
				"X-Remove":      "",
			},
			Remove: []string{"X-Old"},
		},
	}).Validate()
	if errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	if patch.Headers == nil || patch.Headers.Set["authorization"] != "Bearer header-secret" {
		t.Fatalf("headers = %#v", patch.Headers)
	}
	if len(patch.Headers.Remove) != 2 || patch.Headers.Remove[0] != "X-Old" || patch.Headers.Remove[1] != "X-Remove" {
		t.Fatalf("header removals = %#v", patch.Headers.Remove)
	}
	summaryJSON, errMarshal := json.Marshal(patch.Summary())
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	for _, secret := range []string{"proxy-password", "Bearer header-secret", "proxy-user"} {
		if strings.Contains(string(summaryJSON), secret) {
			t.Fatalf("summary leaked %q: %s", secret, summaryJSON)
		}
	}
}

func TestBatchPatchValidationRejectsUnsafeInputsWithoutEchoingSecrets(t *testing.T) {
	invalidProxy := "ftp://user:do-not-echo@example.com/proxy"
	_, errProxy := (BatchPatch{ProxyURL: &invalidProxy}).Validate()
	if errProxy == nil {
		t.Fatal("expected proxy validation error")
	}
	if strings.Contains(errProxy.Error(), "do-not-echo") {
		t.Fatalf("proxy validation leaked input: %v", errProxy)
	}

	_, errHeader := (BatchPatch{Headers: &HeaderPatch{
		Set: map[string]string{"X-Test": "secret\r\nInjected: true"},
	}}).Validate()
	if errHeader == nil || strings.Contains(errHeader.Error(), "secret") {
		t.Fatalf("header validation error = %v", errHeader)
	}

	_, errForbidden := (BatchPatch{Headers: &HeaderPatch{
		Set: map[string]string{"Content-Length": "10"},
	}}).Validate()
	if errForbidden == nil {
		t.Fatal("expected forbidden header error")
	}
}

func TestBatchPatchValidationRejectsDuplicateCaseInsensitiveHeaders(t *testing.T) {
	_, errValidate := (BatchPatch{Headers: &HeaderPatch{
		Set: map[string]string{
			"X-Team": "one",
			"x-team": "two",
		},
	}}).Validate()
	if errValidate == nil {
		t.Fatal("expected duplicate header error")
	}
}

func TestTargetScopeValidationSeparatesSelectedAndFilteredModes(t *testing.T) {
	scope, errSelected := (TargetScope{Mode: "selected", IDs: []string{" a ", "a", "b"}}).Validate()
	if errSelected != nil {
		t.Fatalf("Validate() error = %v", errSelected)
	}
	if len(scope.IDs) != 2 || scope.IDs[0] != "a" || scope.IDs[1] != "b" {
		t.Fatalf("selected IDs = %#v", scope.IDs)
	}

	filtered, errFiltered := (TargetScope{
		Mode: "filtered",
		IDs:  []string{"ignored"},
		Filters: AccountFilters{
			Provider: "codex",
		},
	}).Validate()
	if errFiltered != nil {
		t.Fatalf("Validate() error = %v", errFiltered)
	}
	if len(filtered.IDs) != 0 || filtered.Filters.Provider != "codex" {
		t.Fatalf("filtered scope = %#v", filtered)
	}
}
