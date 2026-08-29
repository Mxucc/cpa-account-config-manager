package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	PluginID                = "cpa-account-config-manager"
	PluginName              = "CPA Account Config Manager"
	DefaultPluginRepository = "https://github.com/Mxucc/cpa-account-config-manager"

	managementRoutePrefix = "/plugins/" + PluginID
	resourceRoutePrefix   = "/v0/resource/plugins/" + PluginID
)

var (
	// PluginVersion is replaced by release builds through the Go linker.
	PluginVersion    = "0.0.0-dev"
	PluginRepository = DefaultPluginRepository
)

type Registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      cpaapi.Metadata          `json:"metadata"`
	Capabilities  RegistrationCapabilities `json:"capabilities"`
}

type RegistrationCapabilities struct {
	ManagementAPI          bool     `json:"management_api"`
	UsagePlugin            bool     `json:"usage_plugin"`
	RequestInterceptor     bool     `json:"request_interceptor"`
	RequestLifecyclePlugin bool     `json:"request_lifecycle_plugin"`
	AuthProvider           bool     `json:"auth_provider"`
	ModelProvider          bool     `json:"model_provider"`
	Executor               bool     `json:"executor"`
	ExecutorModelScope     string   `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats   []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats  []string `json:"executor_output_formats,omitempty"`
}

type App struct {
	mu                     sync.RWMutex
	config                 Config
	configErr              string
	accounts               *AccountService
	deduplication          *AccountDeduplicationService
	deletions              *AccountDeleteService
	tokenRefresh           *AccountTokenRefreshService
	previews               *PreviewService
	jobs                   *JobEngine
	policies               *PolicyEngine
	inspection             *InspectionEngine
	updates                *UpdateChecker
	force                  *ForceSyncEngine
	imports                *ImportService
	usage                  *UsageTracker
	creditUsage            *Sub2APICreditUsage
	operations             *OperationJournal
	modelTests             *ModelTestService
	newAccountProbe        *newAccountModelProbeEngine
	quotaBootstrap         *accountQuotaMetadataBootstrap
	managementDoer         HTTPDoer
	requestHooks           *RequestHook
	concurrency            *AccountConcurrencyService
	providerRuntime        *ProviderRuntimeTracker
	hostSchema             uint32
	runtime                *RuntimeOwnership
	experiments            *ExperimentalSettingsService
	agentIdentity          *AgentIdentityExperiment
	opencode               *OpenCodeQuotaService
	opencodeZen            *OpenCodeZenService
	proxyProfiles          *ProxyProfileService
	quotaPolicies          *QuotaPolicyService
	codexIdentityOverrides *CodexIdentityOverrideService
	indexHTML              []byte
	quiesceOnce            sync.Once
	quotaResetLocks        [64]sync.Mutex
}

func NewApp(host AuthHost, indexHTML []byte) *App {
	usage := NewUsageTracker()
	creditUsage := NewSub2APICreditUsage()
	usage.SetCreditCalculator(creditUsage)
	accounts := NewAccountService(host, usage)
	concurrency := NewAccountConcurrencyService()
	accounts.SetAccountConcurrency(concurrency)
	mutations := NewMutationCoordinator()
	jobs := NewJobEngineWithCoordinator(accounts, mutations)
	jobs.SetAccountConcurrency(concurrency)
	policies := NewPolicyEngineWithCoordinator(host, mutations)
	inspection := NewInspectionEngine(accounts, host, mutations)
	modelTests := NewModelTestService(accounts, usage)
	deletions := NewAccountDeleteService(accounts, mutations)
	operations := NewOperationJournal()
	experiments := NewExperimentalSettingsService()
	newAccountProbe := NewAccountModelProbeEngine(func() bool {
		return policies.Snapshot().Policy.ManagesNewAccountProbe()
	})
	quotaBootstrap := NewAccountQuotaMetadataBootstrap()
	opencode := NewOpenCodeQuotaService()
	opencodeZen := NewOpenCodeZenService()
	proxyProfiles := NewProxyProfileService()
	quotaPolicies := NewQuotaPolicyService()
	codexIdentityOverrides := NewCodexIdentityOverrideService()
	var identityTransport AgentIdentityTransport
	if transport, ok := host.(AgentIdentityTransport); ok {
		identityTransport = transport
	}
	agentIdentity := NewAgentIdentityExperiment(experiments.AgentIdentityEnabled, identityTransport)
	modelTests.SetAgentIdentityExperiment(agentIdentity)
	imports := NewImportService(host, mutations)
	imports.SetAgentIdentityExperiment(agentIdentity)
	weeklyOverdraft := NewWeeklyOverdraftExperiment(experiments.WeeklyOverdraftEnabled).WithOverdraftGate(usage)
	codexIdentity := NewCodexIdentityExperiment(experiments, accounts)
	codexIdentity.SetOverrides(codexIdentityOverrides)
	setCodexIdentitySettingsProvider(experiments.codexIdentitySnapshot)
	modelTests.SetCodexIdentityExperiment(codexIdentity)
	providerRuntime := NewProviderRuntimeTracker(creditUsage)
	providerRuntime.SetAccountConcurrency(concurrency)
	quotaGuard := NewAccountQuotaGuard(usage, quotaPolicies)
	requestHooks := NewRequestHook(quotaGuard, providerRuntime, concurrency, weeklyOverdraft, codexIdentity)
	runtimeMarker := ""
	if provider, ok := host.(interface{ RuntimeProcessMarker() string }); ok {
		runtimeMarker = provider.RuntimeProcessMarker()
	}
	runtime := NewRuntimeOwnershipWithMarker(PluginVersion, runtimeMarker)
	updates := NewUpdateChecker(PluginVersion)
	updates.SetRuntimeOwnership(runtime)
	force := NewForceSyncEngine(accounts, host, policies, mutations)
	modelTests.SetExperimentalTransformer(weeklyOverdraft)
	inspection.RegisterAutomaticDisableGuard(weeklyOverdraft)
	inspection.SetModelTestService(modelTests)
	inspection.SetOverdraftCycleTracker(usage)
	inspection.SetDeleteService(deletions)
	inspection.SetOperationJournal(operations)
	app := &App{
		config:                 normalizeConfig(Config{}),
		accounts:               accounts,
		deduplication:          NewAccountDeduplicationService(accounts),
		deletions:              deletions,
		tokenRefresh:           NewAccountTokenRefreshService(accounts, host),
		previews:               NewPreviewService(accounts),
		jobs:                   jobs,
		policies:               policies,
		inspection:             inspection,
		updates:                updates,
		force:                  force,
		imports:                imports,
		usage:                  usage,
		creditUsage:            creditUsage,
		operations:             operations,
		modelTests:             modelTests,
		newAccountProbe:        newAccountProbe,
		quotaBootstrap:         quotaBootstrap,
		requestHooks:           requestHooks,
		concurrency:            concurrency,
		providerRuntime:        providerRuntime,
		hostSchema:             cpaapi.SchemaVersion,
		runtime:                runtime,
		experiments:            experiments,
		agentIdentity:          agentIdentity,
		opencode:               opencode,
		opencodeZen:            opencodeZen,
		proxyProfiles:          proxyProfiles,
		quotaPolicies:          quotaPolicies,
		codexIdentityOverrides: codexIdentityOverrides,
		indexHTML:              append([]byte(nil), indexHTML...),
	}
	app.previews.SetAccountConcurrency(concurrency)
	app.previews.SetProxyProfiles(proxyProfiles)
	jobs.SetProxyProfiles(proxyProfiles)
	jobs.SetQuotaPolicies(quotaPolicies)
	jobs.SetCodexIdentityOverrides(codexIdentityOverrides)
	accounts.SetQuotaPolicies(quotaPolicies)
	accounts.SetCodexIdentityOverrides(codexIdentityOverrides)
	accounts.SetObserver(accountObserverGroup{newAccountProbe, quotaBootstrap})
	policies.SetObserver(newAccountProbe)
	policies.SetModelPolicyApplier(app.applyConditionalModelPolicy)
	policies.SetProxyProfiles(proxyProfiles)
	policies.SetAIProviderProxyApplier(app.applyAIProviderProxyPolicy)
	policies.SetQuotaMetadataProbe(app.runPolicyQuotaMetadataProbe)
	newAccountProbe.SetEligibility(func(account Account) bool {
		resolved := resolveConditionalPolicy(policies.Snapshot().Policy, account)
		return resolved.NewAccountModelProbe != nil && *resolved.NewAccountModelProbe
	})
	newAccountProbe.SetHandler(app.runNewAccountModelProbe)
	quotaBootstrap.SetHandler(app.runNewAccountQuotaMetadata)
	runtime.SetOnSuperseded(app.quiesceRetiredInstance)
	return app
}

func (a *App) Configure(raw []byte) {
	a.ConfigureHost(raw, cpaapi.SchemaVersion)
}

func (a *App) ConfigureHost(raw []byte, hostSchema uint32) {
	if a == nil {
		return
	}
	config, errConfig := ParseConfigStrict(raw)
	if errConfig != nil {
		a.mu.Lock()
		a.configErr = "plugin configuration is invalid"
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.config = config
	a.configErr = ""
	a.hostSchema = normalizeHostSchemaVersion(hostSchema)
	hostSchema = a.hostSchema
	a.mu.Unlock()
	a.concurrency.Configure(config, hostSchema)
	a.runtime.Configure(config)
	if a.runtime.Snapshot().Superseded {
		a.quiesceRetiredInstance()
		return
	}
	a.jobs.SetBackgroundWorkOwner(a.runtime)
	a.policies.SetBackgroundWorkOwner(a.runtime)
	a.inspection.SetBackgroundWorkOwner(a.runtime)
	a.newAccountProbe.SetBackgroundWorkOwner(a.runtime)
	a.quotaBootstrap.SetBackgroundWorkOwner(a.runtime)
	a.force.SetBackgroundWorkOwner(a.runtime)
	a.operations.Configure(config)
	a.opencode.Configure(config)
	a.opencodeZen.Configure(config)
	a.proxyProfiles.Configure(config)
	a.quotaPolicies.Configure(config)
	a.codexIdentityOverrides.Configure(config)
	a.proxyProfiles.SetBindingApplier(a.applyProxyProfileBindings)
	a.experiments.Configure(config)
	a.creditUsage.Configure(config, a.experiments.Sub2APICreditUsageEnabled())
	a.newAccountProbe.Configure(config)
	a.quotaBootstrap.Start()
	a.jobs.Configure(config)
	a.policies.Configure(config)
	a.inspection.Configure(config)
	a.updates.Configure(config)
	a.force.Configure(config)
	a.usage.Configure(config)
	a.reconcileOperationSources()
}

func (a *App) configSnapshot() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config
}

func (a *App) configError() string {
	if a == nil {
		return "plugin configuration is invalid"
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.configErr
}

func (a *App) HandleUsage(record cpaapi.UsageRecord) {
	if a == nil || a.usage == nil {
		return
	}
	if a.runtimeSuperseded() {
		return
	}
	a.usage.Observe(record)
	a.providerRuntime.ObserveUsage(record)
	a.inspection.Observe(record)
}

func (a *App) Close() {
	if a == nil {
		return
	}
	// Reconcile the last in-memory operation snapshots before shutting down the
	// journal.  The previous order closed the journal first and then attempted
	// to upsert those snapshots, which made shutdown entries silently disappear
	// (and could leave a stale "running" operation on the next startup).
	a.reconcileOperationSources()
	a.quiesceRetiredInstance()
	a.runtime.Shutdown()
}

func (a *App) quiesceRetiredInstance() {
	if a == nil {
		return
	}
	a.quiesceOnce.Do(func() {
		superseded := a.runtime != nil && a.runtime.Snapshot().Superseded
		a.force.Shutdown()
		a.inspection.Shutdown()
		a.newAccountProbe.Shutdown()
		a.quotaBootstrap.Shutdown()
		a.updates.Shutdown()
		a.policies.Shutdown()
		a.jobs.Shutdown()
		a.deletions.Clear()
		a.previews.Clear()
		a.imports.Shutdown()
		a.agentIdentity.Shutdown()
		a.agentIdentity.Clear()
		a.concurrency.Shutdown()
		a.providerRuntime.Shutdown()
		a.creditUsage.Close()
		a.usage.Close()
		// Workers can finish with an interrupted/failed terminal snapshot while
		// they are being quiesced. Reconcile once more after all producers have
		// stopped, but before closing the journal, so the final operation status
		// is durable instead of leaving a stale "running" entry after restart.
		a.reconcileOperationSources()
		a.operations.Close()
		if superseded {
			debug.FreeOSMemory()
		}
	})
}

func (a *App) Registration() Registration {
	a.mu.RLock()
	hostSchema := normalizeHostSchemaVersion(a.hostSchema)
	a.mu.RUnlock()
	registrationSchema := cpaapi.LegacySchemaVersion
	requestLifecycle := false
	if hostSchema >= cpaapi.SchemaVersion {
		registrationSchema = cpaapi.SchemaVersion
		requestLifecycle = true
	}
	return Registration{
		SchemaVersion: registrationSchema,
		Metadata: cpaapi.Metadata{
			Name:             PluginName,
			Version:          PluginVersion,
			Author:           "cpa-account-config-manager contributors",
			GitHubRepository: PluginRepository,
			ConfigFields: []cpaapi.ConfigField{
				{Name: "workers", Type: cpaapi.ConfigFieldTypeInteger, Description: "Optional maximum concurrent account mutations (default 6, range 1-16)."},
				{Name: "data_dir", Type: cpaapi.ConfigFieldTypeString, Description: "Optional writable directory for sanitized job, policy, usage, inspection, update, and operation-journal state."},
				{Name: "management_base_url", Type: cpaapi.ConfigFieldTypeString, Description: "Optional loopback CLIProxyAPI Management API base URL; defaults to http://127.0.0.1:8317."},
			},
		},
		Capabilities: RegistrationCapabilities{
			ManagementAPI: true, UsagePlugin: true, RequestInterceptor: true, RequestLifecyclePlugin: requestLifecycle,
			AuthProvider: true, ModelProvider: true, Executor: true,
			ExecutorModelScope:    "oauth",
			ExecutorInputFormats:  []string{"codex"},
			ExecutorOutputFormats: []string{"codex"},
		},
	}
}

func (a *App) HandleRequestBefore(request cpaapi.RequestInterceptRequest) cpaapi.RequestInterceptResponse {
	if a == nil || a.requestHooks == nil || a.runtimeSuperseded() {
		return cpaapi.RequestInterceptResponse{}
	}
	return a.requestHooks.InterceptBefore(request)
}

func (a *App) RequestInterceptionActive() bool {
	return a != nil && !a.runtimeSuperseded() && a.requestLifecycleAvailable() && a.requestHooks != nil && a.requestHooks.Active()
}

func (a *App) RequestInterceptionAcceptsFormat(format string) bool {
	return a != nil && !a.runtimeSuperseded() && a.requestLifecycleAvailable() && a.requestHooks != nil && a.requestHooks.AcceptsFormat(format)
}

func (a *App) runtimeSuperseded() bool {
	return a != nil && a.runtime != nil && a.runtime.Snapshot().Superseded
}

func (a *App) HandleRequestAfter(request cpaapi.RequestInterceptRequest) cpaapi.RequestInterceptResponse {
	if a == nil || a.requestHooks == nil || a.runtimeSuperseded() {
		return cpaapi.RequestInterceptResponse{}
	}
	return a.requestHooks.InterceptAfter(request)
}

func (a *App) HandleRequestComplete(completion cpaapi.RequestCompletion) {
	if a == nil || a.runtimeSuperseded() {
		return
	}
	if a.concurrency != nil {
		a.concurrency.Complete(completion)
	}
	if a.providerRuntime != nil {
		a.providerRuntime.Complete(completion)
	}
}

func (a *App) RequestCompletionActive() bool {
	return a != nil && !a.runtimeSuperseded() && a.requestLifecycleAvailable() && ((a.concurrency != nil && a.concurrency.RequestInterceptionActive()) || a.providerRuntime != nil)
}

func (a *App) requestLifecycleAvailable() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return normalizeHostSchemaVersion(a.hostSchema) >= cpaapi.SchemaVersion
}

func (a *App) HandleAgentIdentityAuthParse(request cpaapi.AuthParseRequest) (cpaapi.AuthParseResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.AuthParseResponse{}, nil
	}
	return a.agentIdentity.ParseAuth(request.RawJSON)
}

func (a *App) HandleAgentIdentityAuthRefresh(request cpaapi.AuthRefreshRequest) (cpaapi.AuthRefreshResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.AuthRefreshResponse{}, fmt.Errorf("Agent Identity experiment is unavailable")
	}
	return a.agentIdentity.RefreshAuth(request)
}

func (a *App) HandleAgentIdentityLoginStart(request cpaapi.AuthLoginStartRequest) (cpaapi.AuthLoginStartResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.AuthLoginStartResponse{}, fmt.Errorf("Agent Identity experiment is unavailable")
	}
	return a.agentIdentity.StartLogin(request)
}

func (a *App) HandleAgentIdentityLoginPoll(request cpaapi.AuthLoginPollRequest) (cpaapi.AuthLoginPollResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.AuthLoginPollResponse{Status: "error", Message: "Agent Identity experiment is unavailable"}, nil
	}
	return a.agentIdentity.PollLogin(request), nil
}

func (a *App) HandleAgentIdentityModels(request cpaapi.AuthModelRequest) (cpaapi.ModelResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.ModelResponse{Provider: agentIdentityProvider}, nil
	}
	return a.agentIdentity.ModelsForAuth(request)
}

func (a *App) HandleAgentIdentityExecute(ctx context.Context, request cpaapi.ExecutorRequest) (cpaapi.ExecutorResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.ExecutorResponse{}, fmt.Errorf("Agent Identity experiment is unavailable")
	}
	return a.agentIdentity.Execute(ctx, request)
}

func (a *App) HandleAgentIdentityExecuteStream(ctx context.Context, request cpaapi.ExecutorRequest) (cpaapi.ExecutorStreamResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.ExecutorStreamResponse{}, fmt.Errorf("Agent Identity experiment is unavailable")
	}
	return a.agentIdentity.ExecuteStream(ctx, request)
}

func (a *App) HandleAgentIdentityHTTPRequest(ctx context.Context, request cpaapi.ExecutorHTTPRequest) (cpaapi.ExecutorHTTPResponse, error) {
	if a == nil || a.agentIdentity == nil {
		return cpaapi.ExecutorHTTPResponse{}, fmt.Errorf("Agent Identity experiment is unavailable")
	}
	return a.agentIdentity.HTTPRequest(ctx, request)
}

func (a *App) ManagementRegistration() cpaapi.ManagementRegistrationResponse {
	return cpaapi.ManagementRegistrationResponse{
		Routes: []cpaapi.ManagementRoute{
			{Method: http.MethodGet, Path: managementRoutePrefix + "/accounts", Description: "List redacted CLIProxyAPI accounts."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/config", Description: "Read one editable account's current allow-listed configuration."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/quota-policies", Description: "Read persisted account and AI provider quota/concurrency policies."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/quota-policies/account", Description: "Save one account's 5-hour and 7-day quota limits."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/quota-policies/provider", Description: "Save one AI provider's plugin-managed budget, percentage, and concurrency limits."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/codex-identity-overrides", Description: "Read plugin-managed account and AI-provider Codex identity overrides."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/codex-identity-overrides/account", Description: "Save or clear one account's Codex identity override."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/codex-identity-overrides/provider", Description: "Save or clear one AI provider's Codex identity override."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/quota-metadata/refresh", Description: "Refresh one Codex account's CPA-native plan and active reset metadata."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/quota-metadata/reset", Description: "Consume one explicitly confirmed Codex active reset credit and refresh quota metadata."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/models", Description: "Load the common effective model catalog for an editable account scope."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/deduplicate/preview", Description: "Find duplicate upstream accounts and return a redacted review plan."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/model-test", Description: "Run one bounded account-specific model availability probe through CLIProxyAPI."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/token/refresh", Description: "Refresh one editable account through CPA's native credential refresh coordinator."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/delete/preview", Description: "Preview deletion of one editable physical Auth file."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/accounts/delete/start", Description: "Delete one confirmed unchanged physical Auth file."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/preview", Description: "Preview a batch account configuration patch."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/start", Description: "Start an approved batch account configuration patch."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/delete/preview", Description: "Preview deletion of editable physical Auth files in a selected or filtered scope."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/delete/start", Description: "Start an explicitly confirmed batch Auth-file deletion."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/batch/status", Description: "Read current or last batch progress."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/batch/retry", Description: "Retry the failed subset of the last in-memory batch."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/accounts", Description: "Export filtered account credentials for an explicitly selected target format."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/export/accounts", Description: "Export selected account credentials for an explicitly selected target format."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/export/results", Description: "Export sanitized batch results as JSON, CSV, or JSON Lines."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/import/preview", Description: "Preview JSON or ZIP conversion into CPA Auth files."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/import/start", Description: "Start importing a confirmed converted Auth-file preview in the background."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/import/status", Description: "Read current or last background import progress."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/defaults", Description: "Read the default Auth-file policy and safe scan status."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/defaults", Description: "Validate and save the default Auth-file policy."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/scan", Description: "Request an immediate missing-only Auth-file scan."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/force/preview", Description: "Preview force-syncing managed policy fields."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/defaults/force/start", Description: "Start an approved default-policy force sync."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/defaults/force/status", Description: "Read current or last force-sync progress."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection", Description: "Read the persistent account inspection policy and scan status."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/live", Description: "Read uncached live inspection progress and newly completed account results."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/inspection", Description: "Validate and save the account inspection policy."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/scan", Description: "Request an immediate account inspection scan."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/scan/native", Description: "Request an immediate full CPA-native account census without model probes."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/notification/preview", Description: "Preview an exact expanded external notification URL with current aggregate values."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/notification/test", Description: "Send one authenticated external notification test through the hardened GET delivery path."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/run", Description: "Start a full, incremental, retry, or scoped active inspection."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/stop", Description: "Stop the current manual active inspection."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/results", Description: "List redacted account inspection results."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/export", Description: "Export filtered sanitized inspection results as JSON, CSV, or JSON Lines."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/review", Description: "Resolve, ignore, or reopen one sanitized inspection review result."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/inspection/actions", Description: "List sanitized automatic action history."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/delete", Description: "Delete explicitly confirmed high-confidence inspection recommendations after physical revision revalidation."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/inspection/auto-delete", Description: "Execute due opt-in deletion candidates with the current Management credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/updates", Description: "Read plugin release and update-check status."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/updates", Description: "Validate and save plugin update-check settings."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/updates/check", Description: "Record an immediate CPA plugin-store update check."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/experiments", Description: "Read removable experimental feature settings."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/experiments", Description: "Persist removable experimental feature settings."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/experiments/agent-identity/session-login", Description: "Convert one explicitly submitted ChatGPT Session JSON into a pending Agent Identity login credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/operations", Description: "List the persistent sanitized account-manager operation journal."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/operations/export", Description: "Export the sanitized operation journal as JSON, CSV, or JSON Lines."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/operations/settings", Description: "Read operation-journal retention settings."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/operations/settings", Description: "Persist operation-journal retention settings."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/operations", Description: "Clear the operation journal while retaining a clear audit event."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/operations/record", Description: "Record a strict browser-owned plugin-store update outcome."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/quota", Description: "Read cached OpenCode Go quota results for every bound workspace."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/refresh", Description: "Force refresh OpenCode Go quota results for every bound workspace."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/refresh-account", Description: "Refresh one OpenCode Go workspace quota result by account ID."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/accounts", Description: "List redacted bound OpenCode Go workspaces."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/accounts", Description: "Save one OpenCode Go workspace credential and refresh its quota."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/opencode/accounts", Description: "Remove one bound OpenCode Go workspace credential."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/probe", Description: "Query one OpenCode Go workspace quota without saving its credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/zen/accounts", Description: "List redacted bound OpenCode Zen credentials."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/zen/accounts", Description: "Save or update one OpenCode Zen credential and probe its endpoint."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/opencode/zen/accounts", Description: "Remove one bound OpenCode Zen credential."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/zen/probe", Description: "Probe one OpenCode Zen or opencode-cc bridge endpoint without saving its credential."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/zen/probe-account", Description: "Probe one saved OpenCode Zen account with its stored key."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/ai-providers/test", Description: "Probe one AI provider channel endpoint with the submitted credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/ai-providers/runtime", Description: "Read redacted AI provider concurrency, token, and model cost metrics."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/proxy-profiles", Description: "List redacted reusable proxy profiles."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/proxy-profiles", Description: "Create a reusable proxy profile."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/proxy-profiles", Description: "Update a reusable proxy profile."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/proxy-profiles", Description: "Delete a reusable proxy profile."},
		},
		Resources: []cpaapi.ResourceRoute{
			{
				Path:        "/index.html",
				Menu:        "CPA-A Manager",
				Description: "List, filter, and safely batch-edit CLIProxyAPI account configuration.",
			},
		},
	}
}

func (a *App) HandleManagement(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "plugin runtime is unavailable"})
	}
	if a.runtime != nil && a.runtime.Snapshot().Superseded {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{
			"error": "plugin runtime has been superseded; restart CPA and retry",
		})
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	path := normalizedRequestPath(req.Path)
	if method == http.MethodGet && path == resourceRoutePrefix+"/index.html" {
		return cpaapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       append([]byte(nil), a.indexHTML...),
		}
	}
	if configErr := a.configError(); configErr != "" {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": configErr})
	}
	if strings.HasPrefix(path, "/v0/management"+managementRoutePrefix) {
		managementKey := resolveManagementKey(req.Headers)
		a.policies.Arm(managementKey)
		if a.policies.Snapshot().Policy.ManagesNewAccountProbe() {
			a.newAccountProbe.SetManagementKey(managementKey, req.HostCallbackID)
		}
		managementKey = ""
	}

	switch {
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/accounts":
		return a.handleListAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/config":
		return a.handleAccountConfig(ctx, req)
	case (method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/quota-policies") ||
		(method == http.MethodPut && (path == "/v0/management"+managementRoutePrefix+"/quota-policies/account" || path == "/v0/management"+managementRoutePrefix+"/quota-policies/provider")):
		return a.handleQuotaPolicies(req)
	case (method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/codex-identity-overrides") ||
		(method == http.MethodPut && (path == "/v0/management"+managementRoutePrefix+"/codex-identity-overrides/account" || path == "/v0/management"+managementRoutePrefix+"/codex-identity-overrides/provider")):
		return a.handleCodexIdentityOverrides(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/quota-metadata/refresh":
		return a.handleAccountQuotaMetadata(ctx, req, false)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/quota-metadata/reset":
		return a.handleAccountQuotaMetadata(ctx, req, true)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/models":
		return a.handleAccountModels(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/deduplicate/preview":
		return a.handleAccountDeduplicationPreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/model-test":
		return a.handleAccountModelTest(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/token/refresh":
		return a.handleAccountTokenRefresh(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/delete/preview":
		return a.handleAccountDeletePreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/accounts/delete/start":
		return a.handleAccountDeleteStart(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/preview":
		return a.handlePreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/start":
		return a.handleStart(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/delete/preview":
		return a.handleBatchDeletePreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/delete/start":
		return a.handleBatchDeleteStart(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/batch/status":
		return jsonResponse(http.StatusOK, a.jobs.Snapshot(statusWantsResults(req.Query)))
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/batch/retry":
		return a.handleRetry(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/export/accounts":
		return a.handleExportAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/export/accounts":
		return a.handleExportAccounts(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/export/results":
		return a.handleExportResults(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/import/preview":
		return a.handleImportPreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/import/start":
		return a.handleImportStart(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/import/status":
		return jsonResponse(http.StatusOK, a.imports.Status())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/defaults":
		return jsonResponse(http.StatusOK, a.defaultPolicySnapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/defaults":
		return a.handlePutDefaultPolicy(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/defaults/scan":
		a.policies.RequestScan()
		return jsonResponse(http.StatusAccepted, a.defaultPolicySnapshot())
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/defaults/force/preview":
		return a.handleForcePreview(ctx)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/defaults/force/start":
		return a.handleForceStart(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/defaults/force/status":
		return jsonResponse(http.StatusOK, a.force.Snapshot(statusWantsResults(req.Query)))
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection":
		return jsonResponse(http.StatusOK, a.inspection.Snapshot())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/live":
		response := jsonResponse(http.StatusOK, a.inspection.Snapshot())
		response.Headers.Set("Cache-Control", "no-store")
		return response
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/inspection":
		return a.handlePutInspectionPolicy(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/scan":
		managementKey := resolveManagementKey(req.Headers)
		snapshot := a.inspection.RequestScanWithModelProbes(managementKey)
		managementKey = ""
		return jsonResponse(http.StatusAccepted, snapshot)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/scan/native":
		managementKey := resolveManagementKey(req.Headers)
		if managementKey != "" {
			a.inspection.ArmModelProbes(managementKey)
			managementKey = ""
		}
		return jsonResponse(http.StatusAccepted, a.inspection.RequestScan())
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/notification/preview":
		return a.handleInspectionNotificationPreview(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/notification/test":
		return a.handleInspectionNotificationTest(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/run":
		return a.handleInspectionRun(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/stop":
		return jsonResponse(http.StatusAccepted, a.inspection.StopRun())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/results":
		return a.handleListInspectionResults(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/export":
		return a.handleExportInspection(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/review":
		return a.handleInspectionReview(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/inspection/actions":
		return a.handleListInspectionActions(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/delete":
		return a.handleInspectionManualDelete(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/inspection/auto-delete":
		return a.handleInspectionAutoDelete(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/updates":
		return jsonResponse(http.StatusOK, a.updates.Snapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/updates":
		return a.handlePutUpdatePolicy(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/updates/check":
		return jsonResponse(http.StatusAccepted, a.updates.RequestCheck())
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/experiments":
		return jsonResponse(http.StatusOK, a.experiments.Snapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/experiments":
		return a.handlePutExperimentalSettings(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/experiments/agent-identity/session-login":
		return a.handleAgentIdentitySessionLogin(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/operations":
		return a.handleListOperations(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/operations/export":
		return a.handleExportOperations(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/operations/settings":
		return jsonResponse(http.StatusOK, a.operations.RetentionSettings())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/operations/settings":
		return a.handlePutOperationRetentionSettings(req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/operations":
		return a.handleClearOperations()
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/operations/record":
		return a.handleRecordOperation(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/quota":
		return a.handleOpenCodeQuota(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/refresh":
		return a.handleOpenCodeRefresh(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/refresh-account":
		return a.handleOpenCodeRefreshAccount(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/accounts":
		return a.handleOpenCodeAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/accounts":
		return a.handleOpenCodeAccounts(ctx, req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/opencode/accounts":
		return a.handleOpenCodeAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/probe":
		return a.handleOpenCodeProbe(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/proxy-profiles":
		return a.handleProxyProfilesList(ctx)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/proxy-profiles":
		return a.handleProxyProfileCreate(req)
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/proxy-profiles":
		return a.handleProxyProfileUpdate(req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/proxy-profiles":
		return a.handleProxyProfileDelete(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/zen/accounts":
		return a.handleOpenCodeZenAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/zen/accounts":
		return a.handleOpenCodeZenAccounts(ctx, req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/opencode/zen/accounts":
		return a.handleOpenCodeZenAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/zen/probe":
		return a.handleOpenCodeZenProbe(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/zen/probe-account":
		return a.handleOpenCodeZenProbeAccount(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/ai-providers/test":
		return a.handleAIProviderProbe(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/ai-providers/runtime":
		if resolveManagementKey(req.Headers) == "" {
			return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
		}
		return a.handleAIProviderRuntime()
	case method == http.MethodGet && path == opencodeStatusResourcePath:
		return a.handleOpenCodeStatusPage(ctx, req)
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{
			"error":  "not found",
			"method": method,
			"path":   path,
		})
	}
}

func (a *App) handleAccountDeduplicationPreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var options AccountDeduplicationOptions
	if errDecode := decodeJSONRequest(req.Body, &options); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.deduplication.Preview(ctx, options)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrDeduplicationTooLarge):
			return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": errPreview.Error()})
		default:
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to analyze account identities"})
		}
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleExportInspection(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format := firstQuery(req.Query, "format")
	if format == "" {
		format = "json"
	}
	query := InspectionResultQuery{Page: 1, PageSize: maxInspectionResultPageSize, Health: firstQuery(req.Query, "health"), Search: firstQuery(req.Query, "search")}
	response := a.inspection.ListResults(query)
	results := append([]InspectionResult(nil), response.Results...)
	for page := 2; page <= response.Pages && len(results) < maxInspectionAccounts; page++ {
		query.Page = page
		results = append(results, a.inspection.ListResults(query).Results...)
	}
	download, errRender := renderInspectionExport(format, results, time.Now().UTC())
	if errRender != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errRender.Error()})
	}
	return cpaapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":                  []string{download.ContentType},
			"Content-Disposition":           []string{fmt.Sprintf(`attachment; filename="%s"`, download.Filename)},
			"X-Exported-Inspection-Results": []string{strconv.Itoa(download.Count)},
		},
		Body: download.Body,
	}
}

func (a *App) handleListOperations(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	query := operationQueryFromRequest(req, operationPageSize)
	return jsonResponse(http.StatusOK, a.operations.List(query))
}

func (a *App) handleExportOperations(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format := firstQuery(req.Query, "format")
	if format == "" {
		format = "json"
	}
	query := operationQueryFromRequest(req, operationPageSize)
	query.Page = 1
	query.PageSize = operationPageSize
	entries, errSnapshot := a.operations.ExportSnapshot(query)
	if errSnapshot != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "operation journal could not be exported"})
	}
	download, errRender := renderOperationExport(format, entries, time.Now().UTC())
	if errRender != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errRender.Error()})
	}
	return cpaapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":          []string{download.ContentType},
			"Content-Disposition":   []string{fmt.Sprintf(`attachment; filename="%s"`, download.Filename)},
			"X-Exported-Operations": []string{strconv.Itoa(download.Count)},
		},
		Body: download.Body,
	}
}

func (a *App) handlePutOperationRetentionSettings(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request OperationRetentionUpdateRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if request.ExtendedHistory == nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "extended_history is required"})
	}
	settings, errUpdate := a.operations.UpdateRetentionSettings(*request.ExtendedHistory)
	if errUpdate != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "operation journal settings could not be persisted"})
	}
	return jsonResponse(http.StatusOK, settings)
}

func operationQueryFromRequest(req cpaapi.ManagementRequest, pageSize int) OperationQuery {
	return OperationQuery{
		Page:     intQuery(req.Query, "page", 1),
		PageSize: intQuery(req.Query, "page_size", pageSize),
		Category: firstQuery(req.Query, "category"),
		Status:   firstQuery(req.Query, "status"),
		Source:   firstQuery(req.Query, "source"),
		Search:   firstQuery(req.Query, "search"),
	}
}

func (a *App) handleClearOperations() cpaapi.ManagementResponse {
	entry, errClear := a.operations.ClearWithError()
	if errClear != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "operation journal could not be cleared"})
	}
	return jsonResponse(http.StatusOK, map[string]any{"operation": entry, "retained": 1})
}

func (a *App) handleRecordOperation(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request OperationRecordRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	entry, errValidate := validateBrowserOperationRecord(request)
	if errValidate != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errValidate.Error()})
	}
	now := time.Now().UTC()
	entry.StartedAt = now
	entry.FinishedAt = now
	recorded := a.operations.Record(entry)
	if recorded.ID == "" {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{
			"error": "operation journal is unavailable",
		})
	}
	return jsonResponse(http.StatusCreated, recorded)
}

func (a *App) handleImportPreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	uploads, multipartUpload, errUploads := importUploadsFromRequest(req, a.imports.limits)
	if errUploads != nil {
		status := http.StatusBadRequest
		if strings.Contains(errUploads.Error(), "exceeds") || strings.Contains(errUploads.Error(), "more than") {
			status = http.StatusRequestEntityTooLarge
		}
		return jsonResponse(status, map[string]any{"error": errUploads.Error()})
	}
	var preview ImportPreview
	var errPreview error
	if multipartUpload {
		preview, errPreview = a.imports.PreviewMany(ctx, uploads)
	} else {
		preview, errPreview = a.imports.Preview(ctx, uploads[0])
	}
	if errPreview != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(errPreview, ErrImportNoAccounts):
			status = http.StatusUnprocessableEntity
		case errors.Is(errPreview, ErrImportAuthUnavailable):
			status = http.StatusBadGateway
		case strings.Contains(errPreview.Error(), "exceeds") || strings.Contains(errPreview.Error(), "more than"):
			status = http.StatusRequestEntityTooLarge
		}
		return jsonResponse(status, map[string]any{"error": errPreview.Error()})
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleImportStart(_ context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	startedAt := time.Now().UTC()
	var request ImportStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	managementKey := resolveManagementKey(req.Headers)
	backgroundKey := managementKey
	result, errStart := a.imports.StartAsync(request.PreviewID, func(started ImportResult) {
		a.operations.Upsert("import:"+started.ID, OperationEntry{
			Category: OperationCategoryImport, Action: OperationActionImport, Status: OperationStatusRunning,
			Source: OperationSourceImport, Scope: OperationScopeAll, TargetCount: started.Total, StartedAt: started.StartedAt,
			ReasonCode: "running",
		})
	}, func(ctx context.Context, completed ImportResult, errRun error) ImportResult {
		defer func() { backgroundKey = "" }()
		return a.completeImport(ctx, completed, errRun, backgroundKey)
	})
	managementKey = ""
	if errStart != nil {
		backgroundKey = ""
		a.operations.Record(OperationEntry{
			Category: OperationCategoryImport, Action: OperationActionImport, Status: OperationStatusFailed,
			Source: OperationSourceImport, Scope: OperationScopeAll, Failed: 1, StartedAt: startedAt,
			FinishedAt: time.Now().UTC(), ReasonCode: "operation_failed",
		})
		status := http.StatusInternalServerError
		switch {
		case errors.Is(errStart, ErrImportPreviewExpired):
			status = http.StatusGone
		case errors.Is(errStart, ErrImportPreviewNotFound):
			status = http.StatusNotFound
		case errors.Is(errStart, ErrJobBusy):
			status = http.StatusConflict
		case errors.Is(errStart, ErrImportAuthUnavailable):
			status = http.StatusBadGateway
		}
		return jsonResponse(status, map[string]any{"error": errStart.Error()})
	}
	return jsonResponse(http.StatusAccepted, result)
}

func (a *App) completeImport(ctx context.Context, result ImportResult, errRun error, managementKey string) ImportResult {
	if errRun == nil && result.Imported > 0 && ctx.Err() == nil && strings.TrimSpace(managementKey) != "" {
		accountIDs := a.importedAccountIDs(ctx, result)
		result.UsageCollectionTargets = len(accountIDs)
		if result.UsageCollectionTargets > 0 {
			_, errInspect := a.inspection.RequestRun(InspectionRunRequest{
				Mode: InspectionRunModeScoped, Selected: accountIDs,
			}, managementKey)
			result.UsageCollectionStarted = errInspect == nil
		}
	}
	a.operations.Upsert("import:"+result.ID, OperationEntry{
		Category: OperationCategoryImport, Action: OperationActionImport, Status: operationStatusFromJobState(result.State),
		Source: OperationSourceImport, Scope: OperationScopeAll, TargetCount: result.Total, Succeeded: result.Imported,
		Failed: result.Failed, Skipped: result.Skipped, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		ReasonCode: operationReasonFromJobState(result.State),
	})
	return result
}

func (a *App) importedAccountIDs(ctx context.Context, result ImportResult) []string {
	if a == nil || a.accounts == nil || result.Imported == 0 {
		return nil
	}
	targets := make(map[string]struct{}, result.Imported)
	for _, item := range result.Results {
		if item.Status == ImportResultImported {
			targets[strings.ToLower(strings.TrimSpace(item.TargetName))] = struct{}{}
		}
	}
	accounts, errList := a.accounts.baseAccounts(ctx)
	if errList != nil {
		return nil
	}
	ids := make([]string, 0, len(targets))
	for _, account := range accounts {
		if _, exists := targets[strings.ToLower(strings.TrimSpace(account.Name))]; exists {
			ids = append(ids, account.ID)
		}
	}
	return sanitizeInspectionSweepTargets(ids)
}

func (a *App) handlePutDefaultPolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var policy DefaultPolicy
	if errDecode := decodeJSONRequest(req.Body, &policy); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	saved, errSave := a.policies.SetPolicy(policy)
	if errSave != nil {
		a.recordPolicyChange(OperationCategoryDefaultPolicy, OperationActionPolicySave, OperationSourceManual, OperationStatusFailed)
		if errors.Is(errSave, ErrPolicyStorageUnavailable) {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": ErrPolicyStorageUnavailable.Error()})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	a.recordPolicyChange(OperationCategoryDefaultPolicy, OperationActionPolicySave, OperationSourceManual, OperationStatusSucceeded)
	snapshot := a.defaultPolicySnapshot()
	snapshot.Policy = saved
	if saved.ManagesNewAccountProbe() {
		managementKey := resolveManagementKey(req.Headers)
		a.newAccountProbe.Arm(managementKey, req.HostCallbackID)
		managementKey = ""
	}
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) defaultPolicySnapshot() PolicySnapshot {
	snapshot := a.policies.Snapshot()
	if a != nil && a.newAccountProbe != nil {
		snapshot.NewAccountModelProbeStorageError = a.newAccountProbe.StorageError()
	}
	return snapshot
}

func (a *App) handleForcePreview(ctx context.Context) cpaapi.ManagementResponse {
	preview, errPreview := a.force.Preview(ctx)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrForcePolicyEmpty):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrForcePolicyEmpty.Error()})
		case strings.Contains(errPreview.Error(), "resolve force-sync targets"):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve force-sync targets"})
		default:
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "no auth files are available for force sync"})
		}
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleForceStart(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request StartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	snapshot, errStart := a.force.Start(request.PreviewID)
	if errStart != nil {
		switch {
		case errors.Is(errStart, ErrForcePreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": ErrForcePreviewExpired.Error()})
		case errors.Is(errStart, ErrForcePreviewNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrForcePreviewNotFound.Error()})
		case errors.Is(errStart, ErrForcePreviewStale), errors.Is(errStart, ErrJobBusy):
			return jsonResponse(http.StatusConflict, map[string]any{"error": errStart.Error()})
		case errors.Is(errStart, ErrForceNoEligible):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrForceNoEligible.Error()})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to start force-sync job"})
		}
	}
	a.operations.Upsert("force:"+snapshot.ID, operationFromForceSync(snapshot))
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleListAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	managementKey := resolveManagementKey(req.Headers)
	a.quotaBootstrap.SetManagementKey(managementKey)
	if a.policies.Snapshot().Policy.NewAccountModelProbeEnabled {
		a.newAccountProbe.SetManagementKey(managementKey, req.HostCallbackID)
	}
	managementKey = ""
	query, errQuery := listQueryFromValues(req.Query)
	if errQuery != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errQuery.Error()})
	}
	response, errList := a.accounts.List(ctx, query)
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to load accounts"})
	}
	summaries := a.inspection.AccountAutomationSummaries(response.Accounts)
	for index := range response.Accounts {
		if summary, exists := summaries[response.Accounts[index].ID]; exists {
			response.Accounts[index].Automation = &summary
		}
	}
	return jsonResponse(http.StatusOK, response)
}

func (a *App) handleAccountModels(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request AccountModelCatalogRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	scope, errScope := request.Scope.Validate()
	if errScope != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errScope.Error()})
	}
	resolved, errResolve := a.accounts.ResolveTargets(ctx, scope)
	if errResolve != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve target accounts"})
	}
	if len(resolved.Accounts)+len(resolved.MissingIDs) == 0 {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "scope matched no accounts"})
	}
	if len(resolved.Accounts) > maxModelCatalogTargets {
		return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": fmt.Sprintf("model catalog scope exceeds %d accounts", maxModelCatalogTargets)})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	client, errClient := newManagementClient(resolveManagementBaseURL(config.ManagementBaseURL), managementKey, a.managementDoer)
	managementKey = ""
	if errClient != nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "account model catalog is unavailable"})
	}
	defer client.clearSecrets()

	eligible := make([]Account, 0, len(resolved.Accounts))
	for _, account := range resolved.Accounts {
		if account.Editable {
			eligible = append(eligible, account)
		}
	}
	readOnly := len(resolved.Accounts) - len(eligible)
	response := AccountModelCatalogResponse{
		Models:   []AccountModelOption{},
		Total:    len(resolved.Accounts) + len(resolved.MissingIDs),
		Eligible: len(eligible),
		ReadOnly: readOnly,
		Missing:  len(resolved.MissingIDs),
	}
	if len(eligible) == 1 && len(resolved.Accounts) == 1 && len(resolved.MissingIDs) == 0 {
		response.CurrentPolicy = eligible[0].ModelPolicy
		if response.CurrentPolicy == nil {
			response.CurrentPolicy = &AccountModelPolicySummary{Mode: ModelPolicyModeAll}
		}
	}
	if readOnly > 0 {
		response.Warnings = append(response.Warnings, fmt.Sprintf("%d read-only target(s) were skipped", readOnly))
	}
	if len(resolved.MissingIDs) > 0 {
		response.Warnings = append(response.Warnings, fmt.Sprintf("%d missing target(s) were skipped", len(resolved.MissingIDs)))
	}
	if len(eligible) == 0 {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "scope contains no editable accounts"})
	}
	catalogs, failed := loadCommonAccountModels(ctx, a.accounts, eligible, client, config.Workers)
	response.Loaded = len(catalogs)
	response.Failed = failed
	if failed > 0 {
		response.Warnings = append(response.Warnings, fmt.Sprintf("%d account model catalog(s) could not be loaded", failed))
	}
	if len(catalogs) == 0 {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to load account model catalogs"})
	}
	response.Models = commonAccountModels(catalogs)
	if len(response.Models) == 0 {
		response.Warnings = append(response.Warnings, "the loaded accounts have no common models")
	}
	return jsonResponse(http.StatusOK, response)
}

func (a *App) handleAccountDeletePreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request AccountDeletePreviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if strings.TrimSpace(request.ID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "account id is required"})
	}
	preview, errPreview := a.deletions.Preview(ctx, request)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrAccountDeleteTargetNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrAccountDeleteTargetNotFound.Error()})
		case errors.Is(errPreview, ErrAccountDeleteTargetReadOnly):
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": ErrAccountDeleteTargetReadOnly.Error()})
		case strings.Contains(errPreview.Error(), "resolve account for deletion"):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve account for deletion"})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to create delete preview"})
		}
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleAccountDeleteStart(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request AccountDeleteStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if strings.TrimSpace(request.PreviewID) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "preview_id is required"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	result, errDelete := a.deletions.Start(ctx, request.PreviewID, config.ManagementBaseURL, managementKey)
	managementKey = ""
	if errDelete != nil {
		now := time.Now().UTC()
		a.operations.Record(OperationEntry{
			Category: OperationCategoryAccount, Action: OperationActionDelete, Status: OperationStatusFailed,
			Source: OperationSourceManual, Scope: OperationScopeSingle, TargetCount: 1, Failed: 1,
			StartedAt: now, FinishedAt: now, ReasonCode: "operation_failed",
		})
		switch {
		case errors.Is(errDelete, ErrAccountDeletePreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": "delete preview expired; create a new preview"})
		case errors.Is(errDelete, ErrAccountDeletePreviewNotFound):
			return jsonResponse(http.StatusNotFound, map[string]any{"error": ErrAccountDeletePreviewNotFound.Error()})
		case errors.Is(errDelete, ErrAccountDeletePreviewStale), errors.Is(errDelete, ErrAccountDeleteBusy):
			return jsonResponse(http.StatusConflict, map[string]any{"error": errDelete.Error()})
		case errors.Is(errDelete, ErrManagementBaseURLInvalid):
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": ErrManagementBaseURLInvalid.Error()})
		case errors.Is(errDelete, ErrAccountDeleteFailed):
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": ErrAccountDeleteFailed.Error()})
		default:
			return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to delete account"})
		}
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryAccount, Action: OperationActionDelete, Status: OperationStatusSucceeded,
		Source: OperationSourceManual, Scope: OperationScopeSingle, TargetID: result.Account.ID, TargetCount: 1,
		Succeeded: 1, StartedAt: result.DeletedAt, FinishedAt: result.DeletedAt, ReasonCode: "completed",
	})
	return jsonResponse(http.StatusOK, result)
}

func (a *App) handlePreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request PreviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.previews.Create(ctx, request)
	if errPreview != nil {
		if strings.Contains(errPreview.Error(), "resolve target accounts") || strings.Contains(errPreview.Error(), "account service is unavailable") {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve target accounts"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errPreview.Error()})
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleStart(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request StartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.previews.Get(request.PreviewID)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrPreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": "preview expired; create a new preview"})
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "preview not found"})
		}
	}
	if preview.Operation != BatchOperationPatch {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "preview is not a batch configuration patch"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errStart := a.jobs.Start(preview, managementKey, "")
	managementKey = ""
	if errStart != nil {
		status := jobHTTPStatus(errStart)
		return jsonResponse(status, map[string]any{"error": publicJobStartError(errStart, "failed to start batch job")})
	}
	entry := operationFromJob(snapshot)
	entry.Scope = normalizeOperationScope(preview.Public.ScopeMode)
	a.operations.Upsert("batch:"+snapshot.ID, entry)
	a.previews.Delete(request.PreviewID)
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleBatchDeletePreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request BatchDeletePreviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.previews.CreateDelete(ctx, request)
	if errPreview != nil {
		if strings.Contains(errPreview.Error(), "resolve target accounts") || strings.Contains(errPreview.Error(), "account service is unavailable") {
			return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to resolve target accounts"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errPreview.Error()})
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleBatchDeleteStart(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request BatchDeleteStartRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	if !request.Confirm {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "batch deletion requires explicit confirmation"})
	}
	preview, errPreview := a.previews.Get(request.PreviewID)
	if errPreview != nil {
		switch {
		case errors.Is(errPreview, ErrPreviewExpired):
			return jsonResponse(http.StatusGone, map[string]any{"error": "delete preview expired; create a new preview"})
		default:
			return jsonResponse(http.StatusNotFound, map[string]any{"error": "delete preview not found"})
		}
	}
	if preview.Operation != BatchOperationDelete {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "preview is not a batch deletion"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errStart := a.jobs.Start(preview, managementKey, "")
	managementKey = ""
	if errStart != nil {
		return jsonResponse(jobHTTPStatus(errStart), map[string]any{"error": publicJobStartError(errStart, "failed to start batch delete job")})
	}
	entry := operationFromJob(snapshot)
	entry.Scope = normalizeOperationScope(preview.Public.ScopeMode)
	a.operations.Upsert("batch:"+snapshot.ID, entry)
	a.previews.Delete(request.PreviewID)
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleRetry(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	scope, patch, operation, parentJobID, errIntent := a.jobs.RetryIntent()
	if errIntent != nil {
		return jsonResponse(jobHTTPStatus(errIntent), map[string]any{"error": errIntent.Error()})
	}
	var preview previewSnapshot
	var errPreview error
	if operation == BatchOperationDelete {
		preview, errPreview = a.previews.BuildDeleteTransient(ctx, scope)
	} else {
		preview, errPreview = a.previews.BuildTransient(ctx, scope, patch)
	}
	if errPreview != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to refresh failed targets"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errStart := a.jobs.Start(preview, managementKey, parentJobID)
	managementKey = ""
	if errStart != nil {
		status := jobHTTPStatus(errStart)
		return jsonResponse(status, map[string]any{"error": publicJobStartError(errStart, "failed to start retry job")})
	}
	entry := operationFromJob(snapshot)
	entry.Scope = OperationScopeSelected
	a.operations.Upsert("batch:"+snapshot.ID, entry)
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleExportAccounts(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format, errFormat := credentialExportFormatFromValues(req.Query)
	if errFormat != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errFormat.Error()})
	}
	var collection credentialExportCollection
	var errExport error
	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodPost) {
		var request CredentialExportRequest
		if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
		}
		scope, errScope := request.Scope.Validate()
		if errScope != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errScope.Error()})
		}
		if scope.Mode != "selected" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "credential export scope must be selected"})
		}
		collection, errExport = a.accounts.ExportSelectedCredentialSources(ctx, scope)
	} else {
		query, errQuery := listQueryFromValues(req.Query)
		if errQuery != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": errQuery.Error()})
		}
		collection, errExport = a.accounts.ExportCredentialSources(ctx, query.Filters)
	}
	if errExport != nil {
		if errors.Is(errExport, ErrCredentialExportTooLarge) {
			return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": ErrCredentialExportTooLarge.Error()})
		}
		if errors.Is(errExport, ErrCredentialExportNoAccounts) {
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrCredentialExportNoAccounts.Error()})
		}
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": "failed to export accounts"})
	}
	defer clearCredentialExportCollection(&collection)
	download, errRender := renderCredentialExport(format, collection, time.Now().UTC())
	if errRender != nil {
		if errors.Is(errRender, ErrCredentialExportTooLarge) {
			return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": ErrCredentialExportTooLarge.Error()})
		}
		if errors.Is(errRender, ErrCredentialExportNoCompatible) {
			return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"error": ErrCredentialExportNoCompatible.Error()})
		}
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to encode account export"})
	}
	now := time.Now().UTC()
	scope := OperationScopeFiltered
	if strings.EqualFold(strings.TrimSpace(req.Method), http.MethodPost) {
		scope = OperationScopeSelected
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryExport, Action: OperationActionExportAccounts, Status: OperationStatusSucceeded,
		Source: OperationSourceManual, Scope: scope, TargetCount: download.Exported + download.Skipped,
		Succeeded: download.Exported, Skipped: download.Skipped, StartedAt: now, FinishedAt: now,
		ReasonCode: "completed", Format: format,
	})
	return exportDownloadResponse(download)
}

func (a *App) handleExportResults(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	format, errFormat := resultExportFormatFromValues(req.Query)
	if errFormat != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errFormat.Error()})
	}
	download, errRender := renderResultExport(format, a.jobs.Snapshot(true))
	if errRender != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]any{"error": "failed to encode result export"})
	}
	now := time.Now().UTC()
	a.operations.Record(OperationEntry{
		Category: OperationCategoryExport, Action: OperationActionExportResults, Status: OperationStatusSucceeded,
		Source: OperationSourceManual, Scope: OperationScopeSystem, TargetCount: download.Exported,
		Succeeded: download.Exported, Skipped: download.Skipped, StartedAt: now, FinishedAt: now,
		ReasonCode: "completed", Format: format,
	})
	return exportDownloadResponse(download)
}

func (a *App) handlePutInspectionPolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionPolicyUpdateRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	current := a.inspection.Snapshot().Policy
	if request.AutoDelete && !current.AutoDelete && !request.ConfirmAutoDelete {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "enabling auto_delete requires explicit confirmation"})
	}
	if request.AutoDeleteInvalidCredentials && !current.AutoDeleteInvalidCredentials && !request.ConfirmDeleteInvalidCredentials {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "enabling auto_delete_invalid_credentials requires explicit confirmation"})
	}
	if request.ModelProbeEnabled || request.AnomalyTriggerEnabled || request.AutoDisable || request.AutoEnable || request.AutoDelete {
		managementKey := resolveManagementKey(req.Headers)
		if managementKey != "" {
			a.inspection.ArmModelProbes(managementKey)
			managementKey = ""
		}
	}
	snapshot, errSave := a.inspection.SetPolicy(request.InspectionPolicy)
	if errSave != nil {
		a.recordPolicyChange(OperationCategoryInspection, OperationActionInspectionSave, OperationSourceManual, OperationStatusFailed)
		if strings.Contains(errSave.Error(), "save inspection policy") || strings.Contains(errSave.Error(), "storage is unavailable") {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "inspection policy could not be persisted"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	a.recordPolicyChange(OperationCategoryInspection, OperationActionInspectionSave, OperationSourceManual, OperationStatusSucceeded)
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) handleInspectionNotificationPreview(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request InspectionNotificationRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	preview, errPreview := a.inspection.PreviewNotification(ctx, request)
	if errPreview != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errPreview.Error()})
	}
	return jsonResponse(http.StatusOK, preview)
}

func (a *App) handleInspectionNotificationTest(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if resolveManagementKey(req.Headers) == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	var request InspectionNotificationRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	result, errTest := a.inspection.TestNotification(ctx, request)
	if errTest != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errTest.Error()})
	}
	return jsonResponse(http.StatusOK, result)
}

func (a *App) handleListInspectionResults(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	query := InspectionResultQuery{
		Page:     intQuery(req.Query, "page", 1),
		PageSize: intQuery(req.Query, "page_size", 50),
		Health:   firstQuery(req.Query, "health"),
		Search:   firstQuery(req.Query, "search"),
	}
	return jsonResponse(http.StatusOK, a.inspection.ListResults(query))
}

func (a *App) handleInspectionReview(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionReviewRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	result, errUpdate := a.inspection.UpdateReview(request)
	if errUpdate != nil {
		status := http.StatusBadRequest
		if strings.Contains(errUpdate.Error(), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(errUpdate.Error(), "does not require review") {
			status = http.StatusConflict
		}
		return jsonResponse(status, map[string]any{"error": errUpdate.Error()})
	}
	a.reconcileOperationSources()
	return jsonResponse(http.StatusOK, result)
}

func (a *App) handleInspectionRun(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionRunRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	snapshot, errRun := a.inspection.RequestRun(request, managementKey)
	managementKey = ""
	if errRun != nil {
		status := http.StatusBadRequest
		if strings.Contains(errRun.Error(), "already running") {
			status = http.StatusConflict
		}
		return jsonResponse(status, map[string]any{"error": errRun.Error()})
	}
	return jsonResponse(http.StatusAccepted, snapshot)
}

func (a *App) handleListInspectionActions(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	return jsonResponse(http.StatusOK, map[string]any{
		"actions": a.inspection.Actions(intQuery(req.Query, "limit", 50)),
	})
}

func (a *App) handleInspectionManualDelete(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request InspectionManualDeleteRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	startedAt := time.Now().UTC()
	run, errDelete := a.inspection.ExecuteManualDeletes(ctx, a.deletions, config.ManagementBaseURL, managementKey, request)
	managementKey = ""
	if errDelete != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(errDelete, ErrInspectionDeleteConfirmation) || errors.Is(errDelete, ErrInspectionDeleteIDsRequired) || errors.Is(errDelete, ErrInspectionDeleteTooMany) {
			status = http.StatusBadRequest
		}
		return jsonResponse(status, map[string]any{"error": errDelete.Error()})
	}
	status := OperationStatusSucceeded
	reason := "completed"
	if run.Failed > 0 || run.Skipped > 0 {
		status = OperationStatusPartial
		reason = "partial_failure"
		if run.Succeeded == 0 && run.Failed > 0 {
			status = OperationStatusFailed
			reason = "operation_failed"
		}
	}
	finishedAt := time.Now().UTC()
	a.operations.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionInspectionManualDelete, Status: status,
		Source: OperationSourceManual, Scope: OperationScopeSelected, TargetCount: run.Attempted,
		Succeeded: run.Succeeded, Failed: run.Failed, Skipped: run.Skipped,
		StartedAt: startedAt, FinishedAt: finishedAt, ReasonCode: reason,
	})
	return jsonResponse(http.StatusOK, run)
}

func (a *App) handleInspectionAutoDelete(ctx context.Context, req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if !a.inspection.Snapshot().Policy.AutoDelete {
		return jsonResponse(http.StatusConflict, map[string]any{"error": "auto_delete is disabled"})
	}
	managementKey := resolveManagementKey(req.Headers)
	if managementKey == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
	}
	config := a.configSnapshot()
	run := a.inspection.ExecutePendingDeletes(ctx, a.deletions, config.ManagementBaseURL, managementKey)
	managementKey = ""
	now := time.Now().UTC()
	status := OperationStatusSucceeded
	reason := "completed"
	if run.Failed > 0 {
		status = OperationStatusFailed
		reason = "operation_failed"
		if run.Succeeded > 0 {
			status = OperationStatusPartial
			reason = "partial_failure"
		}
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionAutoDelete, Status: status,
		Source: OperationSourceInspection, Scope: OperationScopeScheduled, TargetCount: run.Attempted,
		Succeeded: run.Succeeded, Failed: run.Failed, Skipped: run.Skipped, StartedAt: now,
		FinishedAt: now, ReasonCode: reason,
	})
	return jsonResponse(http.StatusOK, run)
}

func (a *App) handlePutUpdatePolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var request UpdatePolicyRequest
	if errDecode := decodeJSONRequest(req.Body, &request); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	current := a.updates.Snapshot().Policy
	if request.Policy.AutoUpdate && !current.AutoUpdate && !request.ConfirmAutoUpdate {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "enabling auto_update requires explicit confirmation"})
	}
	snapshot, errSave := a.updates.SetPolicy(request.Policy)
	if errSave != nil {
		a.recordPolicyChange(OperationCategoryUpdate, OperationActionUpdateSave, OperationSourceManual, OperationStatusFailed)
		if strings.Contains(errSave.Error(), "save update policy") || strings.Contains(errSave.Error(), "storage is unavailable") {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "update policy could not be persisted"})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
	}
	a.recordPolicyChange(OperationCategoryUpdate, OperationActionUpdateSave, OperationSourceManual, OperationStatusSucceeded)
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) handlePutExperimentalSettings(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	var settings ExperimentalSettings
	if errDecode := decodeJSONRequest(req.Body, &settings); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	snapshot, errSave := a.experiments.Set(settings)
	if errSave != nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "experimental settings could not be persisted"})
	}
	a.creditUsage.SetEnabled(snapshot.Settings.Sub2APICreditUsageEnabled)
	if a.policies.Snapshot().Policy.NewAccountModelProbeEnabled {
		managementKey := resolveManagementKey(req.Headers)
		a.newAccountProbe.Arm(managementKey, req.HostCallbackID)
		managementKey = ""
	}
	return jsonResponse(http.StatusOK, snapshot)
}

func (a *App) recordPolicyChange(category, action, source, status string) {
	now := time.Now().UTC()
	reason := "completed"
	failed := 0
	succeeded := 1
	if status == OperationStatusFailed {
		reason = "operation_failed"
		failed = 1
		succeeded = 0
	}
	a.operations.Record(OperationEntry{
		Category: category, Action: action, Status: status, Source: source, Scope: OperationScopeSystem,
		TargetCount: 1, Succeeded: succeeded, Failed: failed, StartedAt: now, FinishedAt: now, ReasonCode: reason,
	})
}

func listQueryFromValues(values map[string][]string) (ListQuery, error) {
	query := ListQuery{}
	query.Page = intQuery(values, "page", 1)
	query.PageSize = intQuery(values, "page_size", defaultPageSize)
	query.Filters.Provider = firstQuery(values, "provider")
	query.Filters.Type = firstQuery(values, "type")
	query.Filters.Status = firstQuery(values, "status")
	query.Filters.Editability = firstQuery(values, "editability")
	query.Filters.Source = firstQuery(values, "source")
	query.Filters.Search = firstQuery(values, "search")
	query.SortBy = AccountSortField(strings.ToLower(firstQuery(values, "sort_by")))
	if query.SortBy == "" {
		query.SortBy = AccountSortAccount
	}
	if !validAccountSortField(query.SortBy) {
		return ListQuery{}, fmt.Errorf("unsupported account sort field")
	}
	query.SortOrder = AccountSortOrder(strings.ToLower(firstQuery(values, "sort_order")))
	if query.SortOrder == "" {
		query.SortOrder = AccountSortAscending
	}
	if query.SortOrder != AccountSortAscending && query.SortOrder != AccountSortDescending {
		return ListQuery{}, fmt.Errorf("sort_order must be asc or desc")
	}
	if rawDisabled := firstQuery(values, "disabled"); rawDisabled != "" {
		disabled, errParse := strconv.ParseBool(rawDisabled)
		if errParse != nil {
			return ListQuery{}, fmt.Errorf("disabled must be true or false")
		}
		query.Filters.Disabled = &disabled
	}
	return query, nil
}

func firstQuery(values map[string][]string, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}

func intQuery(values map[string][]string, key string, fallback int) int {
	raw := firstQuery(values, key)
	if raw == "" {
		return fallback
	}
	parsed, errParse := strconv.Atoi(raw)
	if errParse != nil {
		return fallback
	}
	return parsed
}

func normalizedRequestPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	if strings.HasPrefix(path, managementRoutePrefix+"/") {
		return "/v0/management" + path
	}
	return path
}

func jsonResponse(statusCode int, payload any) cpaapi.ManagementResponse {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		statusCode = http.StatusInternalServerError
		raw = []byte(`{"error":"failed to encode response"}`)
	}
	return cpaapi.ManagementResponse{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       raw,
	}
}

func decodeJSONRequest(raw []byte, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("request body is required")
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("request body exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(destination); errDecode != nil {
		return fmt.Errorf("invalid request body: %w", errDecode)
	}
	var trailing any
	if errTrailing := decoder.Decode(&trailing); !errors.Is(errTrailing, io.EOF) {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

func publicJobStartError(err error, fallback string) string {
	switch {
	case errors.Is(err, ErrJobStorageUnavailable):
		return ErrJobStorageUnavailable.Error()
	case errors.Is(err, ErrManagementBaseURLInvalid):
		return ErrManagementBaseURLInvalid.Error()
	case jobHTTPStatus(err) != http.StatusInternalServerError:
		return err.Error()
	default:
		return fallback
	}
}
