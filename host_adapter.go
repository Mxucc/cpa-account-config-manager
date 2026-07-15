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
