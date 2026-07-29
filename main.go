package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/manager"
	"cpa-account-config-manager/internal/web"
)

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

var pluginApp = manager.NewApp(hostAdapter{}, web.IndexHTML())

var emptyRequestInterceptEnvelope = []byte(`{"ok":true,"result":{}}`)

func main() {}

type methodHandler func(string, []byte) ([]byte, error)

func callMethodSafely(handler methodHandler, method string, request []byte) (raw []byte, callCode int) {
	defer func() {
		if recover() != nil {
			raw = errorEnvelope("plugin_panic", "plugin request failed; restart CPA and retry")
			callCode = 1
		}
	}()
	if handler == nil {
		return errorEnvelope("plugin_unavailable", "plugin request handler is unavailable"), 1
	}
	raw, errHandle := handler(method, request)
	if errHandle != nil {
		return errorEnvelopeFor(errHandle), 1
	}
	if len(raw) == 0 {
		return errorEnvelope("empty_response", "plugin produced an empty response; restart CPA and retry"), 1
	}
	return raw, 0
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case cpaapi.MethodPluginRegister, cpaapi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &lifecycle); errUnmarshal != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
			}
		}
		pluginApp.ConfigureHost(lifecycle.ConfigYAML, lifecycle.SchemaVersion)
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
		var loginRequest cpaapi.AuthLoginStartRequest
		if errUnmarshal := json.Unmarshal(request, &loginRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity login start input: %w", errUnmarshal)
		}
		response, errStart := pluginApp.HandleAgentIdentityLoginStart(loginRequest)
		if errStart != nil {
			return nil, errStart
		}
		return okEnvelope(response)
	case cpaapi.MethodAuthLoginPoll:
		var pollRequest cpaapi.AuthLoginPollRequest
		if errUnmarshal := json.Unmarshal(request, &pollRequest); errUnmarshal != nil {
			return nil, fmt.Errorf("decode Agent Identity login poll input: %w", errUnmarshal)
		}
		response, errPoll := pluginApp.HandleAgentIdentityLoginPoll(pollRequest)
		if errPoll != nil {
			return nil, errPoll
		}
		return okEnvelope(response)
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
	case cpaapi.MethodRequestInterceptBefore:
		return emptyRequestInterceptEnvelope, nil
	case cpaapi.MethodRequestInterceptAfter:
		if !pluginApp.RequestInterceptionActive() {
			return emptyRequestInterceptEnvelope, nil
		}
		if len(request) == 0 {
			return emptyRequestInterceptEnvelope, nil
		}
		format, errFormat := requestInterceptFormat(request)
		if errFormat != nil {
			return nil, fmt.Errorf("decode request interceptor format: %w", errFormat)
		}
		if !pluginApp.RequestInterceptionAcceptsFormat(format) {
			return emptyRequestInterceptEnvelope, nil
		}
		var interceptRequest cpaapi.RequestInterceptRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &interceptRequest); errUnmarshal != nil {
				return nil, fmt.Errorf("decode request interceptor input: %w", errUnmarshal)
			}
		}
		return okEnvelope(pluginApp.HandleRequestAfter(interceptRequest))
	case cpaapi.MethodRequestComplete:
		if !pluginApp.RequestCompletionActive() || len(request) == 0 {
			return okEnvelope(struct{}{})
		}
		var completion cpaapi.RequestCompletion
		if errUnmarshal := json.Unmarshal(request, &completion); errUnmarshal != nil {
			return nil, fmt.Errorf("decode request completion input: %w", errUnmarshal)
		}
		pluginApp.HandleRequestComplete(completion)
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func requestInterceptFormat(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, errToken := decoder.Token()
	if errToken != nil {
		return "", errToken
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return "", fmt.Errorf("request interceptor input must be an object")
	}
	for decoder.More() {
		keyToken, errKey := decoder.Token()
		if errKey != nil {
			return "", errKey
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", fmt.Errorf("request interceptor field name must be a string")
		}
		if key == "ToFormat" {
			var format string
			if errDecode := decoder.Decode(&format); errDecode != nil {
				return "", errDecode
			}
			return format, nil
		}
		var ignored json.RawMessage
		if errDecode := decoder.Decode(&ignored); errDecode != nil {
			return "", errDecode
		}
	}
	return "", nil
}

func methodNeedsRequestPayload(method string) bool {
	switch method {
	case cpaapi.MethodRequestInterceptBefore:
		return false
	case cpaapi.MethodRequestInterceptAfter:
		return pluginApp.RequestInterceptionActive()
	case cpaapi.MethodRequestComplete:
		return pluginApp.RequestCompletionActive()
	default:
		return true
	}
}
