package main

import (
	"context"
	"encoding/json"
	"fmt"

	"cpa-account-config-manager/internal/cpaapi"
)

type hostAdapter struct{}

func (hostAdapter) ListAuth(context.Context) ([]cpaapi.HostAuthFileEntry, error) {
	result, errCall := callHost(cpaapi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	var response cpaapi.HostAuthListResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth list: %w", errUnmarshal)
	}
	return response.Files, nil
}

func (hostAdapter) GetAuth(_ context.Context, authIndex string) (cpaapi.HostAuthGetResponse, error) {
	result, errCall := callHost(cpaapi.MethodHostAuthGet, cpaapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return cpaapi.HostAuthGetResponse{}, errCall
	}
	var response cpaapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return cpaapi.HostAuthGetResponse{}, fmt.Errorf("decode host auth get: %w", errUnmarshal)
	}
	return response, nil
}

func (hostAdapter) SaveAuth(_ context.Context, name string, rawJSON json.RawMessage) (cpaapi.HostAuthSaveResponse, error) {
	result, errCall := callHost(cpaapi.MethodHostAuthSave, cpaapi.HostAuthSaveRequest{Name: name, JSON: rawJSON})
	if errCall != nil {
		return cpaapi.HostAuthSaveResponse{}, errCall
	}
	var response cpaapi.HostAuthSaveResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return cpaapi.HostAuthSaveResponse{}, fmt.Errorf("decode host auth save: %w", errUnmarshal)
	}
	return response, nil
}

func (hostAdapter) AgentIdentityDo(_ context.Context, callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPResponse, error) {
	request.HostCallbackID = callbackID
	result, errCall := callHost(cpaapi.MethodHostHTTPDo, request)
	if errCall != nil {
		return cpaapi.HostHTTPResponse{}, errCall
	}
	var response cpaapi.HostHTTPResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return cpaapi.HostHTTPResponse{}, fmt.Errorf("decode host HTTP response: %w", errUnmarshal)
	}
	return response, nil
}

func (hostAdapter) AgentIdentityDoStream(_ context.Context, callbackID string, request cpaapi.HostHTTPRequest) (cpaapi.HostHTTPStreamResponse, error) {
	request.HostCallbackID = callbackID
	result, errCall := callHost(cpaapi.MethodHostHTTPDoStream, request)
	if errCall != nil {
		return cpaapi.HostHTTPStreamResponse{}, errCall
	}
	var response cpaapi.HostHTTPStreamResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return cpaapi.HostHTTPStreamResponse{}, fmt.Errorf("decode host HTTP stream response: %w", errUnmarshal)
	}
	return response, nil
}

func (hostAdapter) AgentIdentityReadStream(_ context.Context, streamID string) (cpaapi.HostHTTPStreamReadResponse, error) {
	result, errCall := callHost(cpaapi.MethodHostHTTPStreamRead, cpaapi.HostHTTPStreamReadRequest{StreamID: streamID})
	if errCall != nil {
		return cpaapi.HostHTTPStreamReadResponse{}, errCall
	}
	var response cpaapi.HostHTTPStreamReadResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		return cpaapi.HostHTTPStreamReadResponse{}, fmt.Errorf("decode host HTTP stream chunk: %w", errUnmarshal)
	}
	return response, nil
}

func (hostAdapter) AgentIdentityCloseHTTPStream(_ context.Context, streamID string) error {
	_, errCall := callHost(cpaapi.MethodHostHTTPStreamClose, cpaapi.HostHTTPStreamCloseRequest{StreamID: streamID})
	return errCall
}

func (hostAdapter) AgentIdentityEmitStream(_ context.Context, request cpaapi.HostStreamEmitRequest) error {
	_, errCall := callHost(cpaapi.MethodHostStreamEmit, request)
	return errCall
}

func (hostAdapter) AgentIdentityCloseStream(_ context.Context, request cpaapi.HostStreamCloseRequest) error {
	_, errCall := callHost(cpaapi.MethodHostStreamClose, request)
	return errCall
}
