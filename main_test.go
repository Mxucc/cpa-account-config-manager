package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/manager"
)

func TestDecodeHostHTTPResponseAcceptsCurrentAndLegacyStatusCodeShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "CPA host PascalCase", raw: `{"StatusCode":200,"Headers":{"Content-Type":["application/json"]},"Body":"eyJvayI6dHJ1ZX0="}`, want: http.StatusOK},
		{name: "plugin snake_case", raw: `{"status_code":201,"headers":{"Content-Type":["application/json"]},"body":"eyJvayI6dHJ1ZX0="}`, want: http.StatusCreated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, errDecode := decodeHostHTTPResponse([]byte(test.raw))
			if errDecode != nil {
				t.Fatalf("decodeHostHTTPResponse() error = %v", errDecode)
			}
			if response.StatusCode != test.want || response.Headers.Get("Content-Type") != "application/json" || string(response.Body) != `{"ok":true}` {
				t.Fatalf("decodeHostHTTPResponse() = %#v", response)
			}
		})
	}
}

func TestDecodeHostHTTPResponseRejectsMissingInvalidAndConflictingStatusCodes(t *testing.T) {
	for _, raw := range []string{
		`{"Headers":{"Content-Type":["application/json"]}}`,
		`{"StatusCode":99}`,
		`{"status_code":1000}`,
		`{"StatusCode":200,"status_code":401}`,
	} {
		if _, errDecode := decodeHostHTTPResponse([]byte(raw)); errDecode == nil {
			t.Fatalf("decodeHostHTTPResponse(%s) succeeded", raw)
		}
	}
}

func TestHandleMethodRegistersManagementCapability(t *testing.T) {
	raw, errHandle := handleMethod(cpaapi.MethodPluginRegister, []byte(`{"config_yaml":"d29ya2VyczogNAo="}`))
	if errHandle != nil {
		t.Fatalf("handleMethod() error = %v", errHandle)
	}
	result, errDecode := decodeEnvelopeResult(raw)
	if errDecode != nil {
		t.Fatalf("decodeEnvelopeResult() error = %v", errDecode)
	}
	var registration manager.Registration
	if errUnmarshal := json.Unmarshal(result, &registration); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if !registration.Capabilities.ManagementAPI {
		t.Fatal("management_api capability is false")
	}
	if !registration.Capabilities.UsagePlugin {
		t.Fatal("usage_plugin capability is false")
	}
	if !registration.Capabilities.RequestInterceptor {
		t.Fatal("request_interceptor capability is false")
	}
	if registration.Metadata.Name != manager.PluginName {
		t.Fatalf("metadata name = %q", registration.Metadata.Name)
	}
}

func TestRequestInterceptorMethodsRemainAvailableWhenExperimentsDisabled(t *testing.T) {
	originalApp := pluginApp
	testApp := manager.NewApp(nil, nil)
	testApp.Configure([]byte("data_dir: " + t.TempDir()))
	pluginApp = testApp
	defer func() {
		testApp.Close()
		pluginApp = originalApp
	}()

	request := cpaapi.RequestInterceptRequest{
		SourceFormat: "responses", ToFormat: "codex", Model: "gpt-5.4", RequestedModel: "gpt-5.4",
		Body: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"continue"}]}`),
	}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatalf("marshal interceptor request: %v", errMarshal)
	}
	for _, method := range []string{cpaapi.MethodRequestInterceptBefore, cpaapi.MethodRequestInterceptAfter} {
		raw, errHandle := handleMethod(method, rawRequest)
		if errHandle != nil {
			t.Fatalf("handleMethod(%q) error = %v", method, errHandle)
		}
		result, errDecode := decodeEnvelopeResult(raw)
		if errDecode != nil {
			t.Fatalf("decode %q result: %v", method, errDecode)
		}
		var response cpaapi.RequestInterceptResponse
		if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
			t.Fatalf("decode %q response: %v", method, errUnmarshal)
		}
		if len(response.Body) != 0 || len(response.Headers) != 0 || len(response.ClearHeaders) != 0 {
			t.Fatalf("disabled experiment changed %q request: %#v", method, response)
		}
	}
}

func TestHandleMethodAcceptsCurrentUsageABIJSON(t *testing.T) {
	originalApp := pluginApp
	testApp := manager.NewApp(nil, nil)
	testApp.Configure([]byte("data_dir: " + t.TempDir()))
	pluginApp = testApp
	defer func() {
		testApp.Close()
		pluginApp = originalApp
	}()

	raw, errHandle := handleMethod(cpaapi.MethodUsageHandle, []byte(`{
		"Provider":"codex",
		"AuthIndex":"auth-index-1",
		"RequestedAt":"2026-07-15T12:00:00Z",
		"Detail":{"InputTokens":12,"OutputTokens":3,"TotalTokens":15},
		"ResponseHeaders":{"X-Codex-Secondary-Used-Percent":["25"]}
	}`))
	if errHandle != nil {
		t.Fatalf("handleMethod() error = %v", errHandle)
	}
	result, errDecode := decodeEnvelopeResult(raw)
	if errDecode != nil {
		t.Fatalf("decodeEnvelopeResult() error = %v", errDecode)
	}
	if string(result) != "{}" {
		t.Fatalf("result = %s, want {}", result)
	}
}

func TestHandleMethodRejectsUnknownMethod(t *testing.T) {
	raw, errHandle := handleMethod("unknown", nil)
	if errHandle != nil {
		t.Fatalf("handleMethod() error = %v", errHandle)
	}
	var response envelope
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if response.OK || response.Error == nil || response.Error.Code != "unknown_method" {
		t.Fatalf("response = %#v", response)
	}
}
