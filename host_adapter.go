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
