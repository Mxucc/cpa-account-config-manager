package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
)

// The Cline Pass gateway routes one subscription model across several upstreams.
// Prompt caches are per upstream, so a conversation that switches upstream
// between steps re-reads its whole context and can cool what the previous
// upstream had cached. The switch pins the two request-body fields the gateway
// reads, and the tests below cover that injection, its off state and every
// request it must leave alone.

func newPinnedClinePassService(t *testing.T, pin bool) *ClinePassService {
	t.Helper()
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	if _, errSave := service.SaveAPIKeyAccount("", "pinned", "", "sk-cline-pin"); errSave != nil {
		t.Fatalf("SaveAPIKeyAccount() error = %v", errSave)
	}
	if errSet := service.SetDeepseekUpstreamConsistency(pin); errSet != nil {
		t.Fatalf("SetDeepseekUpstreamConsistency(%v) error = %v", pin, errSet)
	}
	return service
}

// chatBody is a request body that already carries its own formatting, so the
// tests can prove the injection preserves every other byte.
const chatBody = "{\n  \"model\": \"cline-pass/deepseek-v4.1-flash\",\n  \"messages\": [{\"role\": \"user\", \"content\": \"hi\"}],\n  \"temperature\": 0.25\n}"

func decodedBody(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &fields); errDecode != nil {
		t.Fatalf("the injected body is not valid JSON: %v (%s)", errDecode, raw)
	}
	return fields
}

func TestClinePassUpstreamPinnerPinsDeepseekRequests(t *testing.T) {
	service := newPinnedClinePassService(t, true)
	pinner := NewClinePassUpstreamPinner(service)
	if !pinner.RequestInterceptionActive() {
		t.Fatal("the pinner is inactive while the switch is on and an account exists")
	}
	if !pinner.RequestInterceptionBeforeActive() {
		t.Fatal("the pinner must mark itself as a before-path transformer")
	}

	response, changed := pinner.InterceptRequest(cpaapi.RequestInterceptRequest{
		Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(chatBody),
	})
	if !changed || len(response.Body) == 0 {
		t.Fatal("a Cline Pass DeepSeek chat request was not pinned")
	}
	fields := decodedBody(t, response.Body)
	var providerOptions struct {
		Gateway struct {
			Only []string `json:"only"`
		} `json:"gateway"`
	}
	if errDecode := json.Unmarshal(fields["providerOptions"], &providerOptions); errDecode != nil {
		t.Fatalf("providerOptions = %s", fields["providerOptions"])
	}
	if len(providerOptions.Gateway.Only) != 1 || providerOptions.Gateway.Only[0] != "deepseek" {
		t.Fatalf("providerOptions.gateway.only = %#v", providerOptions.Gateway.Only)
	}
	var provider struct {
		Only []string `json:"only"`
	}
	if errDecode := json.Unmarshal(fields["provider"], &provider); errDecode != nil {
		t.Fatalf("provider = %s", fields["provider"])
	}
	if len(provider.Only) != 1 || provider.Only[0] != "deepseek" {
		t.Fatalf("provider.only = %#v", provider.Only)
	}
	// Everything the caller sent is still there, byte for byte: only the two
	// fields were added.
	for _, fragment := range []string{"\"temperature\": 0.25", "\"content\": \"hi\"", "{\"role\": \"user\""} {
		if !strings.Contains(string(response.Body), fragment) {
			t.Fatalf("the injected body lost %q: %s", fragment, response.Body)
		}
	}

	// Running the transformer again over its own output changes nothing, because
	// both fields are present now.
	if _, changedAgain := pinner.InterceptRequest(cpaapi.RequestInterceptRequest{
		Model: "cline-pass/deepseek-v4.1-flash", Body: response.Body,
	}); changedAgain {
		t.Fatal("the injection is not idempotent")
	}
}

func TestClinePassUpstreamPinnerPinsStrippedModelID(t *testing.T) {
	// The published rows may drop the literal prefix, and CPA reports the model
	// the client asked for, so the stripped spelling has to be accepted too.
	service := newPinnedClinePassService(t, true)
	pinner := NewClinePassUpstreamPinner(service)
	response, changed := pinner.InterceptRequest(cpaapi.RequestInterceptRequest{
		RequestedModel: "deepseek-v4.1-flash",
		Body:           []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	if !changed {
		t.Fatal("the stripped Cline Pass DeepSeek id was not pinned")
	}
	if fields := decodedBody(t, response.Body); len(fields["providerOptions"]) == 0 {
		t.Fatal("the stripped id did not receive providerOptions")
	}
}

func TestClinePassUpstreamPinnerLeavesOtherRequestsAlone(t *testing.T) {
	openAI := `{"model":"%s","messages":[{"role":"user","content":"hi"}]}`
	cases := map[string]struct {
		service *ClinePassService
		request cpaapi.RequestInterceptRequest
	}{
		"another Cline Pass model": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/glm-5.3", Body: []byte(`{"model":"cline-pass/glm-5.3","messages":[]}`)},
		},
		"a DeepSeek model outside the Cline Pass catalog": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "deepseek-chat", Body: []byte(`{"model":"deepseek-chat","messages":[]}`)},
		},
		"the switch is off": {
			newPinnedClinePassService(t, false),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(`{"model":"cline-pass/deepseek-v4.1-flash","messages":[]}`)},
		},
		"the caller already pinned providerOptions": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(`{"providerOptions":{"gateway":{"only":["anthropic"]}},"messages":[]}`)},
		},
		"the caller already pinned provider": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(`{"provider":{"only":["anthropic"]},"messages":[]}`)},
		},
		"a body that is not a chat payload": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(`{"input":"hello"}`)},
		},
		"a body that is not JSON": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(`not json`)},
		},
		"an empty body": {
			newPinnedClinePassService(t, true),
			cpaapi.RequestInterceptRequest{Model: "cline-pass/deepseek-v4.1-flash"},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			pinner := NewClinePassUpstreamPinner(testCase.service)
			response, changed := pinner.InterceptRequest(testCase.request)
			if changed || len(response.Body) != 0 {
				t.Fatalf("the request was modified: %s", response.Body)
			}
		})
	}
	// A DeepSeek model the catalog knows under another provider namespace is not
	// pinned, so the switch stays inside the channel this plugin owns.
	if clinePassPinsDeepseekUpstream("deepseek/deepseek-chat") {
		t.Fatal("an unknown DeepSeek id was treated as a Cline Pass model")
	}
	if !clinePassPinsDeepseekUpstream("cline-pass/deepseek-v4-pro") {
		t.Fatal("a catalog DeepSeek id was not recognised")
	}
	_ = openAI
}

func TestClinePassUpstreamPinnerWithoutAccounts(t *testing.T) {
	// An installation with no stored account has no Cline Pass traffic to pin.
	service, _ := newConfiguredClinePassService(t, t.TempDir())
	if errSet := service.SetDeepseekUpstreamConsistency(true); errSet != nil {
		t.Fatalf("SetDeepseekUpstreamConsistency(true) error = %v", errSet)
	}
	pinner := NewClinePassUpstreamPinner(service)
	if pinner.RequestInterceptionActive() {
		t.Fatal("the pinner is active without a stored account")
	}
	if _, changed := pinner.InterceptRequest(cpaapi.RequestInterceptRequest{
		Model: "cline-pass/deepseek-v4.1-flash", Body: []byte(chatBody),
	}); changed {
		t.Fatal("a request was pinned without a stored account")
	}
}

// The switch is additive in the store file: a deployment updating from a build
// that had no such field keeps loading, and the switch defaults to off.
func TestClinePassUpstreamConsistencyPersistsAndDefaultsOff(t *testing.T) {
	dataDir := t.TempDir()
	service, _ := newConfiguredClinePassService(t, dataDir)
	if service.DeepseekUpstreamConsistency() {
		t.Fatal("deepseek_upstream_consistency must default to off")
	}
	if errSet := service.SetDeepseekUpstreamConsistency(true); errSet != nil {
		t.Fatalf("SetDeepseekUpstreamConsistency(true) error = %v", errSet)
	}
	raw, errRead := os.ReadFile(filepath.Join(dataDir, clinePassStoreFileName))
	if errRead != nil {
		t.Fatalf("read store: %v", errRead)
	}
	if !strings.Contains(string(raw), `"deepseek_upstream_consistency":true`) {
		t.Fatalf("the store does not hold the switch: %s", raw)
	}

	reloaded, _ := newConfiguredClinePassService(t, dataDir)
	if !reloaded.DeepseekUpstreamConsistency() {
		t.Fatal("the switch did not survive a reload")
	}

	// A store file without the field reads as the documented default.
	legacy := `{"version":1,"accounts":[],"strip_model_prefix":true}`
	if errWrite := os.WriteFile(filepath.Join(dataDir, clinePassStoreFileName), []byte(legacy), 0o600); errWrite != nil {
		t.Fatalf("write legacy store: %v", errWrite)
	}
	legacyLoaded, _ := newConfiguredClinePassService(t, dataDir)
	if legacyLoaded.DeepseekUpstreamConsistency() {
		t.Fatal("a store without the field did not default to off")
	}
}

// The before path must run only the transformers that rewrite the outgoing
// request: the after-path transformers observe, block or account for a request
// that already resolved, and running those twice would double their effect.
func TestRequestHookBeforeRunsOnlyBeforeTransformers(t *testing.T) {
	afterOnly := &recordingTransformer{}
	before := &recordingBeforeTransformer{}
	hook := NewRequestHook(afterOnly, before)

	if !hook.BeforeActive() {
		t.Fatal("BeforeActive() = false with an active before transformer")
	}
	hook.InterceptBefore(cpaapi.RequestInterceptRequest{Body: []byte(`{"messages":[]}`)})
	if before.beforeCalls != 1 {
		t.Fatalf("the before transformer ran %d times", before.beforeCalls)
	}
	if afterOnly.calls != 0 {
		t.Fatalf("an after-only transformer ran on the before path %d times", afterOnly.calls)
	}

	// The after path is unchanged: it still runs every registered transformer,
	// including the before-path one.
	before.beforeCalls = 0
	hook.InterceptAfter(cpaapi.RequestInterceptRequest{Body: []byte(`{"messages":[]}`)})
	if afterOnly.calls != 1 || before.beforeCalls != 1 {
		t.Fatalf("after path calls: afterOnly=%d before=%d", afterOnly.calls, before.beforeCalls)
	}

	// A hook with no before transformer keeps the payload out of the plugin.
	if NewRequestHook(afterOnly).BeforeActive() {
		t.Fatal("BeforeActive() = true without a before transformer")
	}
}

type recordingTransformer struct{ calls int }

func (r *recordingTransformer) InterceptRequest(cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	r.calls++
	return cpaapi.RequestInterceptResponse{}, false
}

type recordingBeforeTransformer struct {
	calls       int
	beforeCalls int
}

func (r *recordingBeforeTransformer) RequestInterceptionBeforeActive() bool { return true }

func (r *recordingBeforeTransformer) InterceptRequest(cpaapi.RequestInterceptRequest) (cpaapi.RequestInterceptResponse, bool) {
	r.calls++
	r.beforeCalls++
	return cpaapi.RequestInterceptResponse{}, false
}
