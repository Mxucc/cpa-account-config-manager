package main

import (
	"context"
	"encoding/json"
	"fmt"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/manager"
	"cpa-account-config-manager/internal/web"
)

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

var pluginApp = manager.NewApp(hostAdapter{}, web.IndexHTML())

func main() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case cpaapi.MethodPluginRegister, cpaapi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &lifecycle); errUnmarshal != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
			}
		}
		pluginApp.Configure(lifecycle.ConfigYAML)
		return okEnvelope(pluginApp.Registration())
	case cpaapi.MethodManagementRegister:
		return okEnvelope(pluginApp.ManagementRegistration())
	case cpaapi.MethodManagementHandle:
		var managementRequest cpaapi.ManagementRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &managementRequest); errUnmarshal != nil {
				return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
			}
		}
		return okEnvelope(pluginApp.HandleManagement(context.Background(), managementRequest))
	case cpaapi.MethodUsageHandle:
		var record cpaapi.UsageRecord
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &record); errUnmarshal != nil {
				return nil, fmt.Errorf("decode usage record: %w", errUnmarshal)
			}
		}
		pluginApp.HandleUsage(record)
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}
