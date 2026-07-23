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
	case cpaapi.MethodAuthIdentifier, cpaapi.MethodExecutorIdentifier:
		return okEnvelope(cpaapi.IdentifierResponse{Identifier: "codex-agent-identity"})
	case cpaapi.MethodAuthParse:
		var authRequest cpaapi.AuthParseRequest
		if errUnmarshal := json.Unmarshal(request, &authRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity auth parse input: %w", errUnmarshal)
		}
		response, errParse := pluginApp.HandleAgentIdentityAuthParse(authRequest)
		if errParse != nil {
			return nil, errParse
		}
		return okEnvelope(response)
	case cpaapi.MethodAuthLoginStart:
		return okEnvelope(cpaapi.AuthLoginStartResponse{Provider: "codex-agent-identity"})
	case cpaapi.MethodAuthLoginPoll:
		return okEnvelope(cpaapi.AuthLoginPollResponse{Status: "error", Message: "Agent Identity supports file import only"})
	case cpaapi.MethodAuthRefresh:
		var refreshRequest cpaapi.AuthRefreshRequest
		if errUnmarshal := json.Unmarshal(request, &refreshRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity auth refresh input: %w", errUnmarshal)
		}
		response, errRefresh := pluginApp.HandleAgentIdentityAuthRefresh(refreshRequest)
		if errRefresh != nil {
			return nil, errRefresh
		}
		return okEnvelope(response)
	case cpaapi.MethodModelStatic:
		return okEnvelope(cpaapi.ModelResponse{Provider: "codex-agent-identity"})
	case cpaapi.MethodModelForAuth:
		var modelRequest cpaapi.AuthModelRequest
		if errUnmarshal := json.Unmarshal(request, &modelRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity model input: %w", errUnmarshal)
		}
		response, errModels := pluginApp.HandleAgentIdentityModels(modelRequest)
		if errModels != nil {
			return nil, errModels
		}
		return okEnvelope(response)
	case cpaapi.MethodExecutorExecute, cpaapi.MethodExecutorExecuteStream:
		var executorRequest cpaapi.ExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &executorRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity executor input: %w", errUnmarshal)
		}
		if method == cpaapi.MethodExecutorExecuteStream {
			response, errExecute := pluginApp.HandleAgentIdentityExecuteStream(context.Background(), executorRequest)
			if errExecute != nil {
				return nil, errExecute
			}
			return okEnvelope(response)
		}
		response, errExecute := pluginApp.HandleAgentIdentityExecute(context.Background(), executorRequest)
		if errExecute != nil {
			return nil, errExecute
		}
		return okEnvelope(response)
	case cpaapi.MethodExecutorCountTokens:
		return nil, fmt.Errorf("experimental Codex token counting is not supported")
	case cpaapi.MethodExecutorHTTPRequest:
		var httpRequest cpaapi.ExecutorHTTPRequest
		if errUnmarshal := json.Unmarshal(request, &httpRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity HTTP executor input: %w", errUnmarshal)
		}
		response, errExecute := pluginApp.HandleAgentIdentityHTTPRequest(context.Background(), httpRequest)
		if errExecute != nil {
			return nil, errExecute
		}
		return okEnvelope(response)
	case cpaapi.MethodRequestInterceptBefore, cpaapi.MethodRequestInterceptAfter:
		var interceptRequest cpaapi.RequestInterceptRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &interceptRequest); errUnmarshal != nil {
				return nil, fmt.Errorf("decode request interceptor input: %w", errUnmarshal)
			}
		}
		if method == cpaapi.MethodRequestInterceptBefore {
			return okEnvelope(pluginApp.HandleRequestBefore(interceptRequest))
		}
		return okEnvelope(pluginApp.HandleRequestAfter(interceptRequest))
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}
