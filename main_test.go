package main

import (
	"encoding/json"
	"testing"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/manager"
)

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
	if registration.Metadata.Name != manager.PluginName {
		t.Fatalf("metadata name = %q", registration.Metadata.Name)
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
