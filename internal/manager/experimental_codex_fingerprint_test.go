package manager

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testCodexFingerprintSeed = "11111111-1111-4111-8111-111111111111"

func fingerprintIDsForTest(t *testing.T, mode codexFingerprintMode, clientSessionID string) *codexFingerprintIDs {
	t.Helper()
	account := Account{ID: "account", Name: "Account", Provider: "codex", Type: "oauth"}
	ids := resolveCodexFingerprintIDs(account, testCodexFingerprintSeed, clientSessionID, mode)
	if mode == codexFingerprintOff {
		if ids != nil {
			t.Fatalf("off mode returned IDs %#v", ids)
		}
		return nil
	}
	if ids == nil || ids.installationID == "" {
		t.Fatalf("mode %q did not return usable IDs", mode)
	}
	return ids
}

func TestDeriveStableUUIDv4FormatAndDeterminism(t *testing.T) {
	first := deriveStableUUIDv4("seed-a")
	second := deriveStableUUIDv4("seed-a")
	if first != second || !isCanonicalUUIDString(first) {
		t.Fatalf("deriveStableUUIDv4() = %q/%q, want deterministic canonical UUIDv4", first, second)
	}
	if deriveStableUUIDv4("seed-b") == first {
		t.Fatal("different seeds produced the same UUID")
	}
}

func TestCanonicalUUIDRejectsMalformedLengthsWithoutPanic(t *testing.T) {
	for _, value := range []string{"", "1", "11111111-1111-4111-8111", "11111111-1111-4111-8111-1111111111110"} {
		if isCanonicalUUIDString(value) {
			t.Fatalf("isCanonicalUUIDString(%q) = true, want false", value)
		}
	}
}

func TestResolveCodexFingerprintModes(t *testing.T) {
	device := fingerprintIDsForTest(t, codexFingerprintDevice, "client-session")
	session := fingerprintIDsForTest(t, codexFingerprintSession, "client-session")
	full := fingerprintIDsForTest(t, codexFingerprintFull, "client-session")

	if device.sessionID != "" || device.threadID != "" || device.windowID != "" || device.turnID != "" {
		t.Fatalf("device IDs = %#v, want only installation identity", device)
	}
	if session.installationID != device.installationID {
		t.Fatal("device and session modes used different installation identities")
	}
	// turn_id is intentionally per attempt; compare stable identity only.
	if session.sessionID != full.sessionID {
		t.Fatalf("stable session identity differs: %q vs %q", session.sessionID, full.sessionID)
	}
	derivedThread := resolveConvergedThreadID(testCodexFingerprintSeed, "client-session")
	if session.threadID != derivedThread || full.threadID != session.sessionID {
		t.Fatalf("thread derivation = session %q full %q, want derived then shared", session.threadID, full.threadID)
	}
	sessionless := fingerprintIDsForTest(t, codexFingerprintSession, "")
	if sessionless.threadID != sessionless.sessionID {
		t.Fatalf("session mode without client session derived thread %q, want session fallback", sessionless.threadID)
	}
}

func TestApplyCodexFingerprintHeadersPreservesUnmanagedFields(t *testing.T) {
	rawMetadata := `{"installation_id":"old-install","session_id":"old-session","sandbox":"seccomp","thread_source":"user"}`
	account := Account{ID: "account", Provider: "codex", Type: "oauth"}
	header := http.Header{}
	header.Set("X-Codex-Installation-Id", "old-install")
	header.Set("X-Codex-Window-Id", "old-window:0")
	header.Set(codexFingerprintHeader, rawMetadata)

	applyCodexFingerprintHeaders(header, fingerprintIDsForTest(t, codexFingerprintDevice, "ignored"))

	if header.Get("X-Codex-Installation-Id") != resolveConvergedInstallationID(account, testCodexFingerprintSeed) {
		t.Fatalf("installation ID was not converged: %q", header.Get("X-Codex-Installation-Id"))
	}
	if header.Get("X-Codex-Window-Id") != "old-window:0" {
		t.Fatalf("window ID changed in device mode: %q", header.Get("X-Codex-Window-Id"))
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(header.Get(codexFingerprintHeader)), &metadata); err != nil {
		t.Fatalf("decode turn metadata: %v", err)
	}
	if metadata["installation_id"] != resolveConvergedInstallationID(account, testCodexFingerprintSeed) ||
		metadata["session_id"] != "old-session" || metadata["sandbox"] != "seccomp" {
		t.Fatalf("device turn metadata = %#v", metadata)
	}
}

func TestApplyCodexFingerprintHeadersAndBodyStayConsistent(t *testing.T) {
	for _, mode := range []codexFingerprintMode{codexFingerprintSession, codexFingerprintFull} {
		ids := fingerprintIDsForTest(t, mode, "client-session")
		header := http.Header{}
		header.Set(codexFingerprintHeader, `{"session_id":"old-session","trace":"keep"}`)
		body := map[string]any{
			"prompt_cache_key": "body-session",
			"client_metadata": map[string]any{
				"x-codex-installation-id": "old-install",
				"session_id":              " body-session ",
				"X-Codex-Turn-Metadata":   `{"turn_id":"old-turn","trace":"keep"}`,
			},
		}
		applyCodexFingerprintHeaders(header, ids)
		// The interceptor captures the original body session before mutation.
		captureOriginalBodySessionID(ids, body["client_metadata"])
		if !applyCodexFingerprintClientMetadata(body, ids) {
			t.Fatalf("mode %q did not modify body", mode)
		}

		var headerMetadata map[string]any
		if err := json.Unmarshal([]byte(header.Get(codexFingerprintHeader)), &headerMetadata); err != nil {
			t.Fatalf("decode header metadata: %v", err)
		}
		clientMetadata, ok := body["client_metadata"].(map[string]any)
		if !ok {
			t.Fatalf("mode %q did not retain client metadata", mode)
		}
		embeddedRaw, _ := clientMetadata[codexFingerprintHeader].(string)
		if embeddedRaw == "" {
			t.Fatalf("mode %q removed embedded turn metadata", mode)
		}
		if headerMetadata["turn_id"] != ids.turnID || clientMetadata["turn_id"] != ids.turnID {
			t.Fatalf("turn IDs diverge: header %v body %v, want %q", headerMetadata["turn_id"], clientMetadata["turn_id"], ids.turnID)
		}
		if headerMetadata["trace"] != "keep" {
			t.Fatal("header metadata did not preserve unmanaged fields")
		}
		if body["prompt_cache_key"] != ids.sessionID {
			t.Fatalf("prompt cache key = %#v, want %q", body["prompt_cache_key"], ids.sessionID)
		}
	}
}

func TestApplyCodexFingerprintPromptCacheKeyConditions(t *testing.T) {
	ids := fingerprintIDsForTest(t, codexFingerprintDevice, "")
	body := map[string]any{"prompt_cache_key": "original"}
	captureOriginalBodySessionID(ids, map[string]any{"session_id": "original"})
	if !applyCodexFingerprintClientMetadata(body, ids) || body["prompt_cache_key"] != "original" {
		t.Fatalf("device mode changed prompt cache key or skipped metadata: %#v", body)
	}
}

func TestEffectiveAndValidationConvergenceMode(t *testing.T) {
	if got := effectiveCodexFingerprintMode(""); got != codexFingerprintOff {
		t.Fatalf("empty mode = %q, want fail-closed off", got)
	}
	if got := effectiveCodexFingerprintMode("bad"); got != codexFingerprintOff {
		t.Fatalf("invalid mode = %q, want fail-closed off", got)
	}
	for _, mode := range []string{"off", "OFF ", "device", "session", "full"} {
		settings := NormalizeExperimentalCodexIdentitySettings(ExperimentalCodexIdentitySettings{ConvergenceMode: mode})
		want := strings.ToLower(strings.TrimSpace(mode))
		if settings.ConvergenceMode != want {
			t.Fatalf("normalize(%q) = %q", mode, settings.ConvergenceMode)
		}
	}
	if err := ValidateExperimentalCodexIdentitySettings(ExperimentalCodexIdentitySettings{ConvergenceMode: "invalid"}); err == nil {
		t.Fatal("invalid convergence mode was accepted")
	}

	settings := NormalizeExperimentalCodexIdentitySettings(ExperimentalCodexIdentitySettings{})
	if settings.ConvergenceMode != "" || effectiveCodexFingerprintMode(settings.ConvergenceMode) != codexFingerprintOff {
		t.Fatalf("legacy empty setting = %#v, want persisted empty and runtime off", settings)
	}

	if ids := resolveCodexFingerprintIDs(
		Account{}, testCodexFingerprintSeed, "client-session", codexFingerprintMode("bad"),
	); ids != nil {
		t.Fatalf("unknown mode produced IDs %#v", ids)
	}
}

func TestResolveCreditModelPricingMultiFamilyAliases(t *testing.T) {
	table, err := parseCreditPricingTable(embeddedCreditPricingJSON, time.Now(), creditPricingSource+" (embedded)")
	if err != nil {
		t.Fatalf("parse embedded pricing: %v", err)
	}
	tests := []struct{ model, expected string }{
		{"anthropic/claude-opus-5-20260101", "claude-opus-4-8"},
		{"claude-opus-4.7-mini", "claude-opus-4-7"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514"},
		{"gemini/vertex/gemini-3.1-pro-custom", "gemini-3.1-pro-preview"},
		{"vertex/gemini-3.6-flash-high", "gemini-3.6-flash"},
		{"deepseek/deepseek-reasoner", "deepseek-v4-flash"},
		{"deepseek-v4-pro-thinking", "deepseek-v4-pro"},
		{"zai/glm-5.2", "glm-5.2"},
		{"zai/glm-5-turbo", "glm-5-turbo"},
		{"zai/glm-4.7-flashx", "glm-4.7-flashx"},
		{"moonshot/kimi-k3", "kimi-k3"},
		{"kimi-for-coding", "kimi-for-coding"},
		{"moonshot/kimi-k2.6", "kimi-k2.6"},
		{"kimi-k2-thinking-xhigh", "kimi-k2-thinking"},
		{"minimax/minimax-m3-20260801", "minimax-m3"},
		{"minimax-m2.7-highspeed", "minimax-m2.7-highspeed"},
		{"volcengine/doubao-embedding-vision-251215", "doubao-embedding-vision"},
		{"xai/grok", "grok-4.6"},
		{"xai/grok-4.20-reasoning", "grok-4.20"},
		{"xai/grok-build", "grok-build-0.1"},
	}
	for _, tt := range tests {
		pricing, ok := resolveCreditModelPricing(table.Models, tt.model)
		if !ok {
			t.Fatalf("%s was not rated", tt.model)
		}
		expected, ok := table.Models[tt.expected]
		if !ok {
			t.Fatalf("expected pricing key %s is absent", tt.expected)
		}
		if pricing != expected {
			t.Fatalf("%s resolved to %#v, want %#v (%s)", tt.model, pricing, expected, tt.expected)
		}
	}
}

func TestResolveCreditModelPricingCommonLegacyModels(t *testing.T) {
	table, err := parseCreditPricingTable(embeddedCreditPricingJSON, time.Now(), creditPricingSource+" (embedded)")
	if err != nil {
		t.Fatalf("parse embedded pricing: %v", err)
	}
	for _, model := range []string{
		"kimi-k2", "moonshot-v1-8k", "deepseek-chat", "deepseek-reasoner",
		"glm-4", "glm-4.5", "claude-sonnet-4-5", "claude-opus-4-6",
		"gemini-2.5-pro", "gemini-2.5-flash",
	} {
		pricing, ok := resolveCreditModelPricing(table.Models, model)
		if !ok || pricing.Input <= 0 || pricing.Output <= 0 {
			t.Fatalf("model %q resolved to %#v, want rated positive pricing", model, pricing)
		}
	}
}
