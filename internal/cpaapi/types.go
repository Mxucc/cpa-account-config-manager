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
	MethodPluginRegister         = "plugin.register"
	MethodPluginReconfigure      = "plugin.reconfigure"
	MethodManagementRegister     = "management.register"
	MethodManagementHandle       = "management.handle"
	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodUsageHandle            = "usage.handle"
	MethodAuthIdentifier         = "auth.identifier"
	MethodAuthParse              = "auth.parse"
	MethodAuthLoginStart         = "auth.login.start"
	MethodAuthLoginPoll          = "auth.login.poll"
	MethodAuthRefresh            = "auth.refresh"
	MethodModelStatic            = "model.static"
	MethodModelForAuth           = "model.for_auth"
	MethodExecutorIdentifier     = "executor.identifier"
	MethodExecutorExecute        = "executor.execute"
	MethodExecutorExecuteStream  = "executor.execute_stream"
	MethodExecutorCountTokens    = "executor.count_tokens"
	MethodExecutorHTTPRequest    = "executor.http_request"
	MethodHostHTTPDo             = "host.http.do"
	MethodHostHTTPDoStream       = "host.http.do_stream"
	MethodHostHTTPStreamRead     = "host.http.stream_read"
	MethodHostHTTPStreamClose    = "host.http.stream_close"
	MethodHostStreamEmit         = "host.stream.emit"
	MethodHostStreamClose        = "host.stream.close"
	MethodHostAuthList           = "host.auth.list"
	MethodHostAuthGet            = "host.auth.get"
	MethodHostAuthGetRuntime     = "host.auth.get_runtime"
	MethodHostAuthSave           = "host.auth.save"
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

type RequestInterceptRequest struct {
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

type RequestInterceptResponse struct {
	Headers      http.Header `json:"Headers,omitempty"`
	Body         []byte      `json:"Body,omitempty"`
	ClearHeaders []string    `json:"ClearHeaders,omitempty"`
}

type IdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type AuthParseRequest struct {
	Provider string
	Path     string
	FileName string
	RawJSON  []byte
}

type AuthData struct {
	Provider         string
	ID               string
	FileName         string
	Label            string
	Prefix           string
	ProxyURL         string
	Disabled         bool
	StorageJSON      []byte
	Metadata         map[string]any
	Attributes       map[string]string
	NextRefreshAfter time.Time
}

type AuthParseResponse struct {
	Handled bool
	Auth    AuthData
	Auths   []AuthData
}

type AuthLoginStartResponse struct {
	Provider  string
	URL       string
	State     string
	ExpiresAt time.Time
}

type AuthLoginPollResponse struct {
	Status  string
	Message string
	Auth    AuthData
	Auths   []AuthData
}

type AuthRefreshRequest struct {
	AuthID       string
	AuthProvider string
	StorageJSON  []byte
	Metadata     map[string]any
	Attributes   map[string]string
}

type AuthRefreshResponse struct {
	Auth             AuthData
	NextRefreshAfter time.Time
}

type ThinkingSupport struct {
	Min            int
	Max            int
	ZeroAllowed    bool
	DynamicAllowed bool
	Levels         []string
}

type ModelInfo struct {
	ID                         string
	Object                     string
	Created                    int64
	OwnedBy                    string
	Type                       string
	DisplayName                string
	Name                       string
	Version                    string
	Description                string
	InputTokenLimit            int64
	OutputTokenLimit           int64
	SupportedGenerationMethods []string
	ContextLength              int64
	MaxCompletionTokens        int64
	SupportedParameters        []string
	SupportedInputModalities   []string
	SupportedOutputModalities  []string
	Thinking                   *ThinkingSupport
	UserDefined                bool
}

type AuthModelRequest struct {
	AuthID         string
	AuthProvider   string
	StorageJSON    []byte
	Metadata       map[string]any
	Attributes     map[string]string
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type ModelResponse struct {
	Provider   string
	Models     []ModelInfo
	AuthUpdate AuthData
}

type ExecutorRequest struct {
	AuthID          string
	AuthProvider    string
	Model           string
	Format          string
	Stream          bool
	Alt             string
	Headers         http.Header
	Query           url.Values
	OriginalRequest []byte
	SourceFormat    string
	Payload         []byte
	Metadata        map[string]any
	StorageJSON     []byte
	AuthMetadata    map[string]any
	AuthAttributes  map[string]string
	StreamID        string `json:"stream_id,omitempty"`
	HostCallbackID  string `json:"host_callback_id,omitempty"`
}

type ExecutorResponse struct {
	Payload  []byte
	Headers  http.Header
	Metadata map[string]any
}

type ExecutorStreamResponse struct {
	Headers http.Header           `json:"headers,omitempty"`
	Chunks  []ExecutorStreamChunk `json:"chunks,omitempty"`
}

type ExecutorStreamChunk struct {
	Payload []byte
	Error   string `json:"Error,omitempty"`
}

type ExecutorHTTPRequest struct {
	AuthID         string
	AuthProvider   string
	Method         string
	URL            string
	Headers        http.Header
	Body           []byte
	StorageJSON    []byte
	Metadata       map[string]any
	Attributes     map[string]string
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type ExecutorHTTPResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

type HostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method,omitempty"`
	URL            string      `json:"url,omitempty"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type HostHTTPResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	Body       []byte      `json:"body,omitempty"`
}

type HostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type HostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type HostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type HostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type HostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type HostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
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
