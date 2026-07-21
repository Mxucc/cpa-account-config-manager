package cpaapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	ABIVersion    uint32 = 1
	SchemaVersion uint32 = 1
)

const (
	MethodPluginRegister     = "plugin.register"
	MethodPluginReconfigure  = "plugin.reconfigure"
	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
	MethodUsageHandle        = "usage.handle"
	MethodHostAuthList       = "host.auth.list"
	MethodHostAuthGet        = "host.auth.get"
	MethodHostAuthGetRuntime = "host.auth.get_runtime"
	MethodHostAuthSave       = "host.auth.save"
)

type Metadata struct {
	Name             string
	Version          string
	Author           string
	GitHubRepository string
	Logo             string
	ConfigFields     []ConfigField
}

type ConfigFieldType string

const (
	ConfigFieldTypeString  ConfigFieldType = "string"
	ConfigFieldTypeInteger ConfigFieldType = "integer"
)

type ConfigField struct {
	Name        string
	Type        ConfigFieldType
	EnumValues  []string
	Description string
}

type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute
	Resources []ResourceRoute
}

type ManagementRoute struct {
	Method      string
	Path        string
	Menu        string
	Description string
}

type ResourceRoute struct {
	Path        string
	Menu        string
	Description string
}

type ManagementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

type ManagementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

type UsageRecord struct {
	Provider        string        `json:"Provider"`
	ExecutorType    string        `json:"ExecutorType"`
	Model           string        `json:"Model"`
	Alias           string        `json:"Alias"`
	APIKey          string        `json:"APIKey"`
	AuthID          string        `json:"AuthID"`
	AuthIndex       string        `json:"AuthIndex"`
	AuthType        string        `json:"AuthType"`
	Source          string        `json:"Source"`
	ReasoningEffort string        `json:"ReasoningEffort"`
	ServiceTier     string        `json:"ServiceTier"`
	RequestedAt     time.Time     `json:"RequestedAt"`
	Latency         time.Duration `json:"Latency"`
	TTFT            time.Duration `json:"TTFT"`
	Failed          bool          `json:"Failed"`
	Failure         UsageFailure  `json:"Failure"`
	Detail          UsageDetail   `json:"Detail"`
	ResponseHeaders http.Header   `json:"ResponseHeaders"`
}

type UsageFailure struct {
	StatusCode int    `json:"StatusCode"`
	Body       string `json:"Body"`
}

type UsageDetail struct {
	InputTokens         int64 `json:"InputTokens"`
	OutputTokens        int64 `json:"OutputTokens"`
	ReasoningTokens     int64 `json:"ReasoningTokens"`
	CachedTokens        int64 `json:"CachedTokens"`
	CacheReadTokens     int64 `json:"CacheReadTokens"`
	CacheCreationTokens int64 `json:"CacheCreationTokens"`
	TotalTokens         int64 `json:"TotalTokens"`
}

type HostRecentRequestEntry struct {
	Time    string `json:"time"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

type HostAuthFileEntry struct {
	ID             string                   `json:"id,omitempty"`
	AuthIndex      string                   `json:"auth_index,omitempty"`
	Name           string                   `json:"name"`
	Type           string                   `json:"type,omitempty"`
	Provider       string                   `json:"provider,omitempty"`
	Label          string                   `json:"label,omitempty"`
	Status         string                   `json:"status,omitempty"`
	StatusMessage  string                   `json:"status_message,omitempty"`
	Disabled       bool                     `json:"disabled,omitempty"`
	Unavailable    bool                     `json:"unavailable,omitempty"`
	RuntimeOnly    bool                     `json:"runtime_only,omitempty"`
	Source         string                   `json:"source,omitempty"`
	Path           string                   `json:"path,omitempty"`
	Size           int64                    `json:"size,omitempty"`
	ModTime        time.Time                `json:"modtime,omitempty"`
	UpdatedAt      time.Time                `json:"updated_at,omitempty"`
	CreatedAt      time.Time                `json:"created_at,omitempty"`
	LastRefresh    time.Time                `json:"last_refresh,omitempty"`
	NextRetryAfter time.Time                `json:"next_retry_after,omitempty"`
	Email          string                   `json:"email,omitempty"`
	ProjectID      string                   `json:"project_id,omitempty"`
	AccountType    string                   `json:"account_type,omitempty"`
	Account        string                   `json:"account,omitempty"`
	Priority       int                      `json:"priority,omitempty"`
	Note           string                   `json:"note,omitempty"`
	Websockets     bool                     `json:"websockets,omitempty"`
	Success        int64                    `json:"success,omitempty"`
	Failed         int64                    `json:"failed,omitempty"`
	RecentRequests []HostRecentRequestEntry `json:"recent_requests,omitempty"`
}

type HostAuthListResponse struct {
	Files []HostAuthFileEntry `json:"files"`
}

type HostAuthGetRequest struct {
	AuthIndex string `json:"auth_index"`
}

type HostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

type HostAuthSaveRequest struct {
	Name string          `json:"name"`
	JSON json.RawMessage `json:"json"`
}

type HostAuthSaveResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
