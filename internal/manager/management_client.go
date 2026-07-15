package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultManagementBaseURL = "http://127.0.0.1:8317"
	maxManagementResponse    = 64 << 10
)

var ErrManagementBaseURLInvalid = errors.New("management_base_url is invalid; configure an HTTP(S) loopback URL")

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type ManagementWriter interface {
	PatchFields(context.Context, string, BatchPatch) error
	PatchDisabled(context.Context, string, bool) error
}

type managementClient struct {
	baseURL string
	key     string
	doer    HTTPDoer
}

func (c *managementClient) clearSecrets() {
	if c == nil {
		return
	}
	c.key = ""
}

func newManagementClient(baseURL, key string, doer HTTPDoer) (*managementClient, error) {
	validatedBaseURL, errBaseURL := validateManagementBaseURL(baseURL)
	if errBaseURL != nil {
		return nil, errBaseURL
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("management key is unavailable")
	}
	if doer == nil {
		doer = &http.Client{Timeout: 15 * time.Second}
	}
	return &managementClient{baseURL: validatedBaseURL, key: key, doer: doer}, nil
}

func (c *managementClient) PatchFields(ctx context.Context, name string, patch BatchPatch) error {
	if !patch.HasFieldUpdates() {
		return nil
	}
	return c.patch(ctx, "/v0/management/auth-files/fields", patch.FieldPayload(name))
}

func (c *managementClient) PatchDisabled(ctx context.Context, name string, disabled bool) error {
	return c.patch(ctx, "/v0/management/auth-files/status", map[string]any{
		"name":     name,
		"disabled": disabled,
	})
}

func (c *managementClient) patch(ctx context.Context, path string, payload any) error {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return fmt.Errorf("encode management request: %w", errMarshal)
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+path, bytes.NewReader(raw))
	if errRequest != nil {
		return fmt.Errorf("create management request: %w", errRequest)
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, errDo := c.doer.Do(request)
	if errDo != nil {
		return fmt.Errorf("management API request failed: %w", errDo)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	limited := io.LimitReader(response.Body, maxManagementResponse+1)
	body, errRead := io.ReadAll(limited)
	if errRead != nil {
		return fmt.Errorf("read management API response: %w", errRead)
	}
	if len(body) > maxManagementResponse {
		return fmt.Errorf("management API response exceeded the size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("management API returned HTTP %d", response.StatusCode)
	}
	return nil
}

func resolveManagementBaseURL(configured string) string {
	if value := strings.TrimSpace(configured); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("CPA_MANAGEMENT_BASE_URL")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("CPA_BASE_URL")); value != "" {
		if validated, errValidate := validateManagementBaseURL(value); errValidate == nil {
			return validated
		}
	}
	for _, environmentName := range []string{"PORT", "CPA_PORT"} {
		if value := strings.TrimSpace(os.Getenv(environmentName)); value != "" {
			return "http://127.0.0.1:" + value
		}
	}
	return defaultManagementBaseURL
}

func validateManagementBaseURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		value = defaultManagementBaseURL
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: value must be a valid URL", ErrManagementBaseURLInvalid)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme must be http or https", ErrManagementBaseURLInvalid)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: credentials, query parameters, and fragments are not allowed", ErrManagementBaseURLInvalid)
	}
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return "", fmt.Errorf("%w: paths are not allowed", ErrManagementBaseURLInvalid)
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	loopback := strings.EqualFold(hostname, "localhost")
	if address := net.ParseIP(hostname); address != nil {
		loopback = address.IsLoopback()
	}
	if !loopback {
		return "", fmt.Errorf("%w: host must be localhost or a loopback IP", ErrManagementBaseURLInvalid)
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func resolveManagementKey(headers http.Header) string {
	if token := extractBearerToken(headers); token != "" {
		return token
	}
	if token := headerValue(headers, "X-Management-Key"); token != "" {
		return token
	}
	for _, environmentName := range []string{"MANAGEMENT_PASSWORD", "CPA_MANAGEMENT_KEY"} {
		if value := strings.TrimSpace(os.Getenv(environmentName)); value != "" {
			return value
		}
	}
	return ""
}

func extractBearerToken(headers http.Header) string {
	authorization := headerValue(headers, "Authorization")
	if authorization == "" {
		return ""
	}
	const bearerPrefix = "bearer "
	if len(authorization) > len(bearerPrefix) && strings.EqualFold(authorization[:len(bearerPrefix)], bearerPrefix) {
		return strings.TrimSpace(authorization[len(bearerPrefix):])
	}
	return authorization
}

func headerValue(headers http.Header, expected string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(expected)); value != "" {
		return value
	}
	for name, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), expected) || len(values) == 0 {
			continue
		}
		if value := strings.TrimSpace(values[0]); value != "" {
			return value
		}
	}
	return ""
}
