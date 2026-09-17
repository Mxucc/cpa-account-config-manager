package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Scheduler              bool     `json:"scheduler"`
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
	mu              sync.RWMutex
	config          Config
	configErr       string
	configureErr    string
	accounts        *AccountService
	deduplication   *AccountDeduplicationService
	deletions       *AccountDeleteService
	tokenRefresh    *AccountTokenRefreshService
	previews        *PreviewService
	jobs            *JobEngine
	policies        *PolicyEngine
	inspection      *InspectionEngine
	updates         *UpdateChecker
	force           *ForceSyncEngine
	imports         *ImportService
	usage           *UsageTracker
	creditUsage     *Sub2APICreditUsage
	operations      *OperationJournal
	modelTests      *ModelTestService
	newAccountProbe *newAccountModelProbeEngine
	quotaBootstrap  *accountQuotaMetadataBootstrap
	managementDoer  HTTPDoer
	authDir         string
	// stateAdoptionNote is the latest sanitized state-adoption notice, guarded by mu.
	stateAdoptionNote        string
	requestHooks             *RequestHook
	quotaGuard               *AccountQuotaGuard
	concurrency              *AccountConcurrencyService
	providerRuntime          *ProviderRuntimeTracker
	hostSchema               uint32
	runtime                  *RuntimeOwnership
	experiments              *ExperimentalSettingsService
	agentIdentity            *AgentIdentityExperiment
	opencode                 *OpenCodeQuotaService
	opencodeZen              *OpenCodeZenService
	clinePass                *ClinePassService
	autoRetry                *AutoRetryService
	modelRetry               *modelRetryExhaustionTracker
	opencodePricing          *OpenCodePricingService
	selfUpdate               *SelfUpdateService
	codexFingerprints        *CodexFingerprintProfileService
	codexModelControl        *CodexModelControlService
	opencodeModelControl     *OpenCodeModelControlService
	opencodeModelControlGate *OpenCodeModelControl
	opencodeSession          *OpenCodeSessionRouter
	opencodeSessionSyncedAt  atomic.Int64
	opencodeSessionPrimedAt  atomic.Int64
	proxyProfiles            *ProxyProfileService
	quotaPolicies            *QuotaPolicyService
	aiProviderNames          *AIProviderNameService
	codexIdentityOverrides   *CodexIdentityOverrideService
	globalPolicy             *GlobalPolicyService
	riskControl              *RiskControlService
	indexHTML                []byte
	quiesceOnce              sync.Once
	quotaResetLocks          [64]sync.Mutex
	// State-directory reconfigure coalescing. DiscoverAuthStorage can run on a stack that already
	// holds a service lock (the inspection scan holds scanMu while it reads accounts), so the
	// service reconfiguration is applied by one background worker instead of inline.
	reconfigureMu      sync.Mutex
	reconfigurePending configReconfigureRequest
	reconfigureQueued  bool
	// Cline Pass auto-bind throttling: a page load that finds an account unbound publishes
	// its channel once, so a credential rotation never leaves the operator clicking a bind
	// button, while a dead credential costs one attempt per cooldown instead of one per read.
	clinePassAutoBindMu sync.Mutex
	clinePassAutoBindAt map[string]time.Time
	reconfigureRunning  bool
	reconfigureCycle    chan struct{}
	// configApplyMu serializes every service configuration, so a deferred configure and a host
	// reconfigure never mutate the services at the same time.
	configApplyMu sync.Mutex
	// effectiveDataDir is the state directory the services were last configured with. It is
	// guarded by configApplyMu, so adoption can tell a real directory change from a repeat.
	effectiveDataDir string
	// testServiceConfigurer replaces the service configuration applier in tests.
	testServiceConfigurer serviceConfigureApplier
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
	clinePass := NewClinePassService()
	autoRetry := NewAutoRetryService()
	modelRetry := newModelRetryExhaustionTracker()
	opencodePricing := NewOpenCodePricingService()
	selfUpdate := NewSelfUpdateService(PluginVersion)
	codexFingerprints := NewCodexFingerprintProfileService()
	codexModelControl := NewCodexModelControlService()
	opencodeModelControl := NewOpenCodeModelControlService()
	opencodeModelControlGate := NewOpenCodeModelControl(opencodeModelControl)
	opencodeSession := NewOpenCodeSessionRouter()
	opencodeSession.SetEnabled(true)
	proxyProfiles := NewProxyProfileService()
	quotaPolicies := NewQuotaPolicyService()
	aiProviderNames := NewAIProviderNameService()
	codexIdentityOverrides := NewCodexIdentityOverrideService()
	globalPolicy := NewGlobalPolicyService()
	riskControl := NewRiskControlService()
	var identityTransport AgentIdentityTransport
	if transport, ok := host.(AgentIdentityTransport); ok {
		identityTransport = transport
	}
	riskControl.SetAuditTransport(identityTransport)
	if executor, ok := host.(CPAModelExecutor); ok {
		riskControl.SetModelExecutor(executor)
	}
	agentIdentity := NewAgentIdentityExperiment(experiments.AgentIdentityEnabled, identityTransport)
	modelTests.SetAgentIdentityExperiment(agentIdentity)
	imports := NewImportService(host, mutations)
	imports.SetAgentIdentityExperiment(agentIdentity)
	weeklyOverdraft := NewWeeklyOverdraftExperiment(experiments.WeeklyOverdraftEnabled).WithOverdraftGate(usage)
	codexIdentity := NewCodexIdentityExperiment(experiments, accounts)
	codexIdentity.SetOverrides(codexIdentityOverrides)
	// Codex client identity has exactly one source: the global experimental
	// settings edited under Other settings. The permanent global policy used to
	// carry a competing copy that silently became authoritative whenever that
	// policy was enabled, which made the setting look like it stopped working
	// after an unrelated policy change.
	setCodexIdentitySettingsProvider(experiments.codexIdentitySnapshot)
	modelTests.SetCodexIdentityExperiment(codexIdentity)
	providerRuntime := NewProviderRuntimeTracker(creditUsage)
	providerRuntime.SetQuotaPolicies(quotaPolicies)
	accounts.AddUsageStorageDiscoverer(providerRuntime)
	quotaGuard := NewAccountQuotaGuard(usage, quotaPolicies)
	// Admission must run before observational trackers. A saturated account can
	// block in the concurrency transformer; recording it as active before that
	// wait would skew provider runtime metrics and rolling request windows.
	requestHooks := NewRequestHook(riskControl, quotaGuard, concurrency, providerRuntime, weeklyOverdraft, codexIdentity, opencodeModelControlGate, opencodeSession, NewCodexModelControl(codexModelControl))
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
		config:                   normalizeConfig(Config{}),
		accounts:                 accounts,
		deduplication:            NewAccountDeduplicationService(accounts),
		deletions:                deletions,
		tokenRefresh:             NewAccountTokenRefreshService(accounts, host),
		previews:                 NewPreviewService(accounts),
		jobs:                     jobs,
		policies:                 policies,
		inspection:               inspection,
		updates:                  updates,
		force:                    force,
		imports:                  imports,
		usage:                    usage,
		creditUsage:              creditUsage,
		operations:               operations,
		modelTests:               modelTests,
		newAccountProbe:          newAccountProbe,
		quotaBootstrap:           quotaBootstrap,
		requestHooks:             requestHooks,
		quotaGuard:               quotaGuard,
		concurrency:              concurrency,
		providerRuntime:          providerRuntime,
		hostSchema:               cpaapi.SchemaVersion,
		runtime:                  runtime,
		experiments:              experiments,
		agentIdentity:            agentIdentity,
		opencode:                 opencode,
		opencodeZen:              opencodeZen,
		clinePass:                clinePass,
		autoRetry:                autoRetry,
		modelRetry:               modelRetry,
		opencodePricing:          opencodePricing,
		selfUpdate:               selfUpdate,
		codexFingerprints:        codexFingerprints,
		codexModelControl:        codexModelControl,
		opencodeModelControl:     opencodeModelControl,
		opencodeModelControlGate: opencodeModelControlGate,
		opencodeSession:          opencodeSession,
		proxyProfiles:            proxyProfiles,
		quotaPolicies:            quotaPolicies,
		aiProviderNames:          aiProviderNames,
		codexIdentityOverrides:   codexIdentityOverrides,
		globalPolicy:             globalPolicy,
		riskControl:              riskControl,
		indexHTML:                append([]byte(nil), indexHTML...),
	}
	// OpenCode traffic is valued with OpenCode's own published prices, and the
	// periodic catalog sync only runs for installations that use OpenCode.
	creditUsage.SetOpenCodePricing(opencodePricing, aiProviderNames.BaseURLForRuntimeIdentity)
	opencodePricing.SetActivityCheck(func() bool {
		return len(opencode.ListAccounts()) > 0 || len(opencodeZen.ListAccounts()) > 0
	})
	app.previews.SetAccountConcurrency(concurrency)
	app.previews.SetProxyProfiles(proxyProfiles)
	jobs.SetProxyProfiles(proxyProfiles)
	jobs.SetQuotaPolicies(quotaPolicies)
	jobs.SetCodexIdentityOverrides(codexIdentityOverrides)
	accounts.SetQuotaPolicies(quotaPolicies)
	accounts.SetCodexIdentityOverrides(codexIdentityOverrides)
	accounts.SetObserver(accountObserverGroup{newAccountProbe, quotaBootstrap})
	// The plugin's own state follows the discovered CPA auth directory, exactly like the durable
	// usage snapshot, so an implicit relative data directory cannot hide it after a restart.
	accounts.AddUsageStorageDiscoverer(app)
	policies.SetObserver(newAccountProbe)
	policies.SetModelPolicyApplier(app.applyConditionalModelPolicy)
	policies.SetGlobalPolicy(globalPolicy)
	policies.SetAccountConcurrency(concurrency)
	policies.SetQuotaPolicies(quotaPolicies)
	policies.SetCodexIdentityOverrides(codexIdentityOverrides)
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

// configReconfigureRequest carries one queued state-directory reconfiguration. previousDir is the
// directory that was effective before the change, so the worker can adopt state that was already
// written there instead of leaving it behind.
type configReconfigureRequest struct {
	config      Config
	previousDir string
}

// serviceConfigureApplier runs the service side of one host configuration. Production code uses
// the App itself; tests substitute a deliberately blocking collaborator to exercise the bounded
// ConfigureHostBounded path without driving the real services.
type serviceConfigureApplier interface {
	applyServiceConfig(config Config, hostSchema uint32)
}

// DiscoverAuthStorage keeps the plugin's private state beside CPA's auth files. The host gives
// the plugin absolute auth file paths as soon as an account list is read, which is far more
// stable than the implicit relative data directory that follows the working directory of
// whoever started CPA. An operator-pinned `data_dir` is never overridden.
//
// This runs on AccountService.baseAccounts, which the inspection scan calls while it already
// holds InspectionEngine.scanMu. The expensive service reconfiguration is therefore never applied
// on the caller's stack: re-entering InspectionEngine.Configure here would self-deadlock on the
// non-reentrant mutex and wedge CPA startup (GitHub issue #7). Only the resolved directories are
// recorded inline, and one coalesced worker applies the rest off this stack.
func (a *App) DiscoverAuthStorage(entries []cpaapi.HostAuthFileEntry) {
	if a == nil {
		return
	}
	authDir := discoverUsageAuthDir(entries)
	if authDir == "" {
		return
	}
	a.mu.RLock()
	previousDir := a.authDir
	configured := a.config
	reconfigured := a.configErr == "" && configured.DataDir != ""
	a.mu.RUnlock()
	if !reconfigured || (previousDir == authDir && !isImplicitStateDir(configured)) {
		return
	}
	a.mu.Lock()
	a.authDir = authDir
	a.mu.Unlock()
	if !isImplicitStateDir(configured) {
		// The operator pinned a data directory, so only remember the auth dir for reporting.
		return
	}
	resolved := a.resolveStateDirectories(configured, authDir)
	if resolved.DataDir == configured.DataDir && len(resolved.DataDirAlternates) == len(configured.DataDirAlternates) {
		return
	}
	// Record the resolved directories immediately so the change is visible to every reader of the
	// config snapshot, then let the coalesced worker re-open the stores from the new paths.
	a.mu.Lock()
	a.config = resolved
	a.mu.Unlock()
	a.scheduleResolvedConfigReconfigure(resolved, configured.DataDir)
}

// scheduleResolvedConfigReconfigure queues one state-directory reconfiguration and starts the
// single worker when none is running. Repeated calls coalesce into one pending apply, so there is
// never more than one reconfigure goroutine per app.
func (a *App) scheduleResolvedConfigReconfigure(config Config, previousDir string) {
	if a == nil {
		return
	}
	a.reconfigureMu.Lock()
	// Keep the directory of the first queued change: coalesced changes are applied in one hop,
	// and the state that must be adopted comes from the directory that was effective first.
	if !a.reconfigureQueued {
		a.reconfigurePending.previousDir = previousDir
	}
	a.reconfigurePending.config = config
	a.reconfigureQueued = true
	if a.reconfigureRunning {
		a.reconfigureMu.Unlock()
		return
	}
	a.reconfigureRunning = true
	cycle := make(chan struct{})
	a.reconfigureCycle = cycle
	a.reconfigureMu.Unlock()
	go a.runReconfigureWorker(cycle)
}

// runReconfigureWorker applies the latest queued configuration until the queue drains, then
// reports idle. It is the only goroutine that runs applyResolvedConfig.
func (a *App) runReconfigureWorker(cycle chan struct{}) {
	defer close(cycle)
	for {
		a.reconfigureMu.Lock()
		config := a.reconfigurePending.config
		previousDir := a.reconfigurePending.previousDir
		queued := a.reconfigureQueued
		a.reconfigureQueued = false
		if !queued {
			a.reconfigureRunning = false
			a.reconfigureCycle = nil
			a.reconfigureMu.Unlock()
			return
		}
		a.reconfigureMu.Unlock()
		a.applyResolvedConfigSafely(config, previousDir)
	}
}

// applyResolvedConfigSafely contains a panic so a failed reconfiguration can never take down CPA,
// and records the sanitized diagnostic.
func (a *App) applyResolvedConfigSafely(config Config, previousDir string) {
	defer func() {
		if recover() != nil {
			a.noteConfigureFailure()
		}
	}()
	a.applyResolvedConfig(config, previousDir)
}

// ReconfigurePending reports whether a state-directory reconfiguration is queued or in flight.
func (a *App) ReconfigurePending() bool {
	if a == nil {
		return false
	}
	a.reconfigureMu.Lock()
	defer a.reconfigureMu.Unlock()
	return a.reconfigureRunning || a.reconfigureQueued
}

// WaitForReconfigure waits until no state-directory reconfiguration is queued or in flight, or
// until timeout elapses. It is the bounded observability hook for the asynchronous worker.
func (a *App) WaitForReconfigure(timeout time.Duration) bool {
	if a == nil {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		a.reconfigureMu.Lock()
		cycle := a.reconfigureCycle
		running := a.reconfigureRunning
		a.reconfigureMu.Unlock()
		if !running || cycle == nil {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		select {
		case <-cycle:
		case <-time.After(remaining):
			return false
		}
	}
}

// applyResolvedConfig re-configures every store after the state directory was resolved again,
// without touching the host schema or the background start-up sequence. It runs on the coalesced
// reconfigure worker, never on a caller stack that may already hold a service lock.
func (a *App) applyResolvedConfig(config Config, previousDir string) {
	a.configApplyMu.Lock()
	defer a.configApplyMu.Unlock()
	// Adopt the state of the previously effective directory before any store re-opens there, so
	// a directory change never makes already-written credentials look deleted.
	adoption := a.adoptStateDirectoryChange(previousDir, config)
	a.mu.Lock()
	a.config = config
	a.mu.Unlock()
	a.operations.Configure(config)
	// The journal now points at the target directory, so the adoption entry is durable there.
	a.recordStateAdoptionResult(adoption)
	a.opencode.Configure(config)
	a.opencodeZen.Configure(config)
	a.clinePass.Configure(config)
	a.autoRetry.Configure(config)
	a.opencodeModelControl.Configure(config)
	a.opencodeSession.Configure(config)
	a.codexFingerprints.Configure(config)
	a.codexModelControl.Configure(config)
	a.selfUpdate.Configure(config)
	a.creditUsage.Configure(config, a.experiments.Sub2APICreditUsageEnabled())
	a.providerRuntime.Configure(config)
	a.usage.Configure(config)
	a.jobs.Configure(config)
	a.policies.Configure(config)
	a.updates.Configure(config)
	a.proxyProfiles.Configure(config)
	a.quotaPolicies.Configure(config)
	a.aiProviderNames.Configure(config)
	a.codexIdentityOverrides.Configure(config)
	a.experiments.Configure(config)
	a.globalPolicy.Configure(config)
	a.riskControl.Configure(config)
	a.inspection.Configure(config)
	a.force.Configure(config)
	a.newAccountProbe.Configure(config)
	a.scheduleAutoRetryApply()
}

// stateDirUnderAuthDir is where plugin state lives when the data directory is implicit: the
// same hidden folder the durable usage snapshot already uses, inside CPA's auth directory.
func stateDirUnderAuthDir(authDir string) string {
	trimmed := strings.TrimSpace(authDir)
	if trimmed == "" {
		return ""
	}
	return filepath.Join(trimmed, usageDurableDirName)
}

// isImplicitStateDir reports whether this config still uses the working-directory default.
func isImplicitStateDir(config Config) bool {
	return strings.TrimSpace(config.DataDir) == "" || filepath.Clean(config.DataDir) == filepath.Clean(implicitDataDirName)
}

// resolveStateDirectories decides the effective state directory and the fallbacks to look in.
// With an implicit data directory the state follows CPA's auth directory; the working-directory
// default and the directories beside the plugin library stay as fallbacks so existing state is
// adopted rather than left behind.
func (a *App) resolveStateDirectories(config Config, authDir string) Config {
	resolved := config
	alternates := make([]string, 0, 6)
	if isImplicitStateDir(config) {
		if underAuth := stateDirUnderAuthDir(authDir); underAuth != "" {
			resolved.DataDir = underAuth
		}
	}
	alternates = append(alternates, config.DataDir)
	alternates = append(alternates, a.dataDirAlternates(resolved.DataDir)...)
	if !isImplicitStateDir(config) {
		// A pinned directory is authoritative and needs no fallbacks.
		alternates = nil
	}
	resolved.DataDirAlternates = dedupeDirectories(alternates)
	return resolved
}

// dedupeDirectories keeps the first occurrence of each cleaned, absolute directory.
func dedupeDirectories(directories []string) []string {
	cleaned := make([]string, 0, len(directories))
	seen := map[string]struct{}{}
	for _, directory := range directories {
		trimmed := strings.TrimSpace(directory)
		if trimmed == "" {
			continue
		}
		if absolute, errAbs := filepath.Abs(trimmed); errAbs == nil {
			trimmed = absolute
		}
		trimmed = filepath.Clean(trimmed)
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		cleaned = append(cleaned, trimmed)
	}
	return cleaned
}

// dataDirAlternates lists other directories that may hold this plugin's state. The implicit
// data directory is a relative path, so it follows the working directory of whoever started
// CPA; looking beside the plugin library and the CPA binary keeps stored credentials visible
// after a restart from a different directory.
func (a *App) dataDirAlternates(primary string) []string {
	if a == nil {
		return nil
	}
	alternates := make([]string, 0, 4)
	seen := map[string]struct{}{}
	appendAlternate := func(directory string) {
		trimmed := strings.TrimSpace(directory)
		if trimmed == "" {
			return
		}
		if absolute, errAbs := filepath.Abs(trimmed); errAbs == nil {
			trimmed = absolute
		}
		trimmed = filepath.Clean(trimmed)
		if trimmed == filepath.Clean(primary) {
			return
		}
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		alternates = append(alternates, trimmed)
	}
	// The plugin library lives where the host installed it, which does not move when CPA is
	// restarted from another working directory.
	if a.selfUpdate != nil {
		if pluginFile, errFile := a.selfUpdatePluginFile(); errFile == nil && pluginFile != "" {
			appendAlternate(filepath.Join(filepath.Dir(pluginFile), implicitDataDirName))
		}
	}
	if executable, errExecutable := os.Executable(); errExecutable == nil {
		appendAlternate(filepath.Join(filepath.Dir(executable), implicitDataDirName))
	}
	return alternates
}

// selfUpdatePluginFile reports the located plugin library, if any.
func (a *App) selfUpdatePluginFile() (string, error) {
	if a == nil || a.selfUpdate == nil {
		return "", nil
	}
	snapshot := a.selfUpdate.Snapshot()
	return snapshot.PluginFile, nil
}

// ConfigureHost applies a host lifecycle configuration synchronously. CPA's register and
// reconfigure path uses ConfigureHostBounded so a wedged service can never block the host.
func (a *App) ConfigureHost(raw []byte, hostSchema uint32) {
	if a == nil {
		return
	}
	config, resolvedSchema, ok := a.applyHostIdentity(raw, hostSchema)
	if !ok {
		return
	}
	a.applyServiceConfig(config, resolvedSchema)
}

// applyHostIdentity parses the lifecycle configuration and records the host identity (config and
// host schema) so Registration reports the correct capabilities even when the service side is
// deferred. It never touches a service lock, so it is safe on any caller stack.
func (a *App) applyHostIdentity(raw []byte, hostSchema uint32) (Config, uint32, bool) {
	config, errConfig := ParseConfigStrict(raw)
	if errConfig != nil {
		a.mu.Lock()
		a.configErr = "plugin configuration is invalid"
		a.mu.Unlock()
		return Config{}, hostSchema, false
	}
	// Resolve CPA's auth directory deterministically from its own configuration before any host
	// auth-list read; the later discovery may still refine it with a directory verified on disk.
	resolvedAuthDir := resolveAuthDirectory()
	a.mu.RLock()
	authDir := a.authDir
	a.mu.RUnlock()
	if resolvedAuthDir != "" {
		authDir = resolvedAuthDir
	}
	config = a.resolveStateDirectories(config, authDir)
	a.mu.Lock()
	if resolvedAuthDir != "" {
		a.authDir = resolvedAuthDir
	}
	a.config = config
	a.configErr = ""
	a.configureErr = ""
	a.hostSchema = normalizeHostSchemaVersion(hostSchema)
	resolvedSchema := a.hostSchema
	a.mu.Unlock()
	return config, resolvedSchema, true
}

// applyServiceConfig runs the full service configuration for one host identity. It serializes on
// configApplyMu so a deferred configure and a host reconfigure never mutate the services at once.
func (a *App) applyServiceConfig(config Config, hostSchema uint32) {
	a.configApplyMu.Lock()
	defer a.configApplyMu.Unlock()
	// A host reconfigure can resolve a different state directory without passing through the
	// coalesced worker, so it runs the same adoption step before the first service Configure.
	adoption := a.adoptStateDirectoryChange(a.effectiveDataDir, config)
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
	// The journal already points at the target directory, so the adoption entry is durable there.
	a.recordStateAdoptionResult(adoption)
	a.opencode.Configure(config)
	a.opencodeZen.Configure(config)
	a.clinePass.Configure(config)
	a.autoRetry.Configure(config)
	a.opencodePricing.Configure(config)
	a.selfUpdate.SetManagementDoer(a.managementDoer)
	a.selfUpdate.Configure(config)
	a.codexFingerprints.Configure(config)
	a.codexModelControl.Configure(config)
	a.opencodeModelControl.Configure(config)
	a.opencodeSession.Configure(config)
	a.refreshOpenCodeSessionTargets()
	a.proxyProfiles.Configure(config)
	a.quotaPolicies.Configure(config)
	a.aiProviderNames.Configure(config)
	a.codexIdentityOverrides.Configure(config)
	a.proxyProfiles.SetBindingApplier(a.applyProxyProfileBindings)
	a.experiments.Configure(config)
	a.globalPolicy.Configure(config)
	// Migrate the Codex identity copy that earlier releases stored in the global
	// policy: keep the value that was effective, then drop the duplicate and any
	// account overrides this plugin derived from it automatically.
	if legacyIdentity, legacyEnabled := a.globalPolicy.LegacyCodexIdentity(); !globalIdentityEmpty(legacyIdentity) {
		if legacyEnabled {
			if errAdopt := a.experiments.AdoptCodexIdentity(legacyIdentity); errAdopt != nil {
				a.experiments.noteStorageError("experimental settings could not be persisted")
			}
			a.mergeLegacyCodexIdentityOverrides(legacyIdentity)
		}
	}
	a.riskControl.Configure(config)
	a.creditUsage.Configure(config, a.experiments.Sub2APICreditUsageEnabled())
	a.providerRuntime.Configure(config)
	a.newAccountProbe.Configure(config)
	a.quotaBootstrap.Start()
	a.jobs.Configure(config)
	a.policies.Configure(config)
	a.inspection.Configure(config)
	a.updates.Configure(config)
	a.force.Configure(config)
	a.usage.Configure(config)
	a.reconcileOperationSources()
	a.scheduleAutoRetryApply()
	// A completed configure clears the diagnostic recorded by an earlier bounded-configure
	// timeout or panic, so the status output stops reporting a degraded lifecycle.
	a.mu.Lock()
	a.configureErr = ""
	a.mu.Unlock()
}

// configureHostBound is the default upper bound ConfigureHostBounded gives the service
// configuration. It is a package var so tests can shrink it; a healthy configure finishes far
// sooner and is therefore unaffected.
var configureHostBound = 25 * time.Second

// configureTimeoutMessage and configureFailureMessage are fixed, sanitized, operator-visible
// diagnostics. They never contain configuration values, credentials, headers or tokens.
const (
	configureTimeoutMessage = "plugin configuration did not finish within the startup deadline"
	configureFailureMessage = "plugin configuration aborted unexpectedly"
)

// ConfigureHostBounded applies a host lifecycle configuration without blocking the caller for
// longer than bound. The host identity (config and schema version) is always applied first, so
// Registration reports the correct capabilities; the service configuration then runs on a
// panic-safe background goroutine that keeps going even after the bound elapses, so a later
// reconfigure or the finishing work converges. A bound <= 0 selects configureHostBound. The
// boolean reports whether the service configuration finished within the bound.
func (a *App) ConfigureHostBounded(raw []byte, hostSchema uint32, bound time.Duration) bool {
	if a == nil {
		return false
	}
	config, resolvedSchema, ok := a.applyHostIdentity(raw, hostSchema)
	if !ok {
		return false
	}
	if bound <= 0 {
		bound = configureHostBound
	}
	return a.applyServiceConfigBounded(config, resolvedSchema, bound)
}

// applyServiceConfigBounded runs one service configuration on a background goroutine and waits at
// most bound for it. A panic is contained and recorded instead of crashing the host.
func (a *App) applyServiceConfigBounded(config Config, hostSchema uint32, bound time.Duration) bool {
	if a == nil {
		return false
	}
	done := make(chan struct{})
	go func() {
		// Contain a panic before signalling done, so the wait below still returns and a goroutine
		// panic can never reach the host process.
		defer func() {
			if recover() != nil {
				a.noteConfigureFailure()
			}
			close(done)
		}()
		a.serviceConfigurer().applyServiceConfig(config, hostSchema)
	}()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		a.noteConfigureTimeout()
		return false
	}
}

// serviceConfigurer returns the object that runs the service configuration, honouring the test
// seam when one is installed.
func (a *App) serviceConfigurer() serviceConfigureApplier {
	if a == nil {
		return nil
	}
	if a.testServiceConfigurer != nil {
		return a.testServiceConfigurer
	}
	return a
}

// noteConfigureTimeout records the sanitized diagnostic for a bounded configure that timed out.
func (a *App) noteConfigureTimeout() {
	a.noteConfigureDiagnostic(configureTimeoutMessage, "plugin_configure_timeout")
}

// noteConfigureFailure records the sanitized diagnostic for a service configuration that panicked.
func (a *App) noteConfigureFailure() {
	a.noteConfigureDiagnostic(configureFailureMessage, "operation_failed")
}

// noteConfigureDiagnostic publishes the operator-visible config error and journals the event. Both
// strings are fixed and allow-listed, so no configuration value or secret can leak through here.
func (a *App) noteConfigureDiagnostic(message, reason string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.configureErr = message
	a.mu.Unlock()
	if a.operations == nil {
		return
	}
	a.operations.Record(OperationEntry{
		Category: OperationCategoryPlugin, Action: OperationActionPluginConfigure,
		Status: OperationStatusFailed, Source: OperationSourceBackground, Scope: OperationScopeSystem,
		ReasonCode: reason,
	})
}

// mergeLegacyCodexIdentityOverrides removes per-account identity overrides that
// this plugin previously derived from the global-policy copy, so the single
// global switch becomes effective for those accounts again. Overrides that do
// not match the derived value are preserved.
func (a *App) mergeLegacyCodexIdentityOverrides(legacy ExperimentalCodexIdentitySettings) {
	if a == nil || a.codexIdentityOverrides == nil {
		return
	}
	derived := codexIdentityOverrideFromGlobal(legacy)
	if derived == nil {
		return
	}
	a.codexIdentityOverrides.DropMatchingAccounts(*derived)
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
	if a.configErr != "" {
		return a.configErr
	}
	return a.configureErr
}

// isAIProviderUsageRecord distinguishes API-key provider traffic from native
// OAuth account traffic before it reaches the provider-only runtime dashboard.
// CPA delivers both through the same usage callback.
func (a *App) isKnownAccountUsageRecord(record cpaapi.UsageRecord) bool {
	if a == nil || a.usage == nil {
		return false
	}
	for _, identifier := range []string{record.AuthIndex, record.AuthID} {
		resolved := a.usage.ResolveAuthIndex(identifier)
		if resolved != "" && a.usage.UsageIdentity(resolved) != "" {
			return true
		}
	}
	return false
}

func isAIProviderUsageRecord(record cpaapi.UsageRecord) bool {
	switch strings.ToLower(strings.TrimSpace(record.AuthType)) {
	case "oauth", "oauth2":
		return false
	case "apikey", "api_key", "api-key":
		return true
	}
	// Legacy hosts may omit AuthType. An API key without a selected OAuth
	// identity is the only safe fallback; records carrying only AuthIndex/AuthID
	// are treated as account telemetry.
	return strings.TrimSpace(record.APIKey) != "" && strings.TrimSpace(record.AuthID) == "" && strings.TrimSpace(record.AuthIndex) == ""
}

func (a *App) HandleUsage(record cpaapi.UsageRecord) {
	if a == nil || a.usage == nil {
		return
	}
	if a.runtimeSuperseded() {
		return
	}
	// Resolve the channel credential BEFORE any consumer sees the record. CPA omits the API
	// key from a provider channel's callback, so a consumer that only looks for one - the
	// Cline Pass ledger does exactly that - attributes nothing while the traffic keeps
	// arriving, which is why the documented Cline Pass windows stayed at $0.
	record = a.attributeProviderChannelCredential(record)
	if a.clinePass != nil {
		// Cline Pass is an OpenAI-compatible channel, so its traffic arrives on
		// this same usage callback. The record feeds the reference-priced quota
		// windows; no additional host callback or observer is registered, and the
		// tracker itself decides whether the record is Cline Pass traffic.
		a.clinePass.ObserveUsage(record)
	}
	if !a.isKnownAccountUsageRecord(record) && isAIProviderUsageRecord(record) {
		// Provider credentials and native OAuth accounts share CPA's usage
		// callback. Never put provider traffic into the account usage store: an
		// API-key channel can expose an auth index, and that index may collide
		// with an account identity after a provider edit.
		a.providerRuntime.ObserveUsage(record)
	} else {
		a.usage.Observe(record)
		a.inspection.Observe(record)
	}
}

// attributeProviderChannelCredential resolves the credential of a CPA provider
// channel from the auth index a usage callback carries. CPA's callback for an
// openai-compatibility or codex-api-key channel names the auth index CPA assigned
// to the channel row but omits the API key, so the record has no credential
// identity: the runtime dashboard refuses such a snapshot and pricing cannot
// resolve the channel. The index is only filled in when the plugin verified that
// the auth index belongs to a live provider channel row, which is provenance
// enough to attribute the record. A record that already carries a key, one that
// names an index no channel row owns, and native account telemetry are all
// returned unchanged. The key stays inside this process and is hashed by the
// tracker immediately: it is never logged or persisted.
func (a *App) attributeProviderChannelCredential(record cpaapi.UsageRecord) cpaapi.UsageRecord {
	if a == nil || a.providerRuntime == nil || strings.TrimSpace(record.APIKey) != "" {
		return record
	}
	switch strings.ToLower(strings.TrimSpace(record.AuthType)) {
	case "oauth", "oauth2":
		// Native account telemetry stays account telemetry.
		return record
	}
	apiKey, provider := a.providerRuntime.ProviderCredentialForAuthIndex(record.AuthIndex)
	if apiKey == "" {
		return record
	}
	record.APIKey = apiKey
	// The provider name is the namespace the tracker and the pricing lookup both
	// key the credential identity by, so the channel's name wins over whatever a
	// callback reports: the identity must be the one the channel is known under.
	if provider != "" {
		record.Provider = provider
	}
	// Keep the record on the provider path even when the host omitted AuthType.
	record.AuthType = "api_key"
	return record
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
		a.opencodePricing.Close()
		a.selfUpdate.Close()
		a.updates.Shutdown()
		a.policies.Shutdown()
		a.jobs.Shutdown()
		a.deletions.Clear()
		a.previews.Clear()
		a.imports.Shutdown()
		a.agentIdentity.Shutdown()
		a.agentIdentity.Clear()
		a.concurrency.Shutdown()
		a.riskControl.Shutdown()
		a.providerRuntime.Shutdown()
		a.clinePass.Shutdown()
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
			Logo:             pluginLogoDataURI,
			ConfigFields: []cpaapi.ConfigField{
				{Name: "workers", Type: cpaapi.ConfigFieldTypeInteger, Description: "Optional maximum concurrent account mutations (default 6, range 1-16)."},
				{Name: "data_dir", Type: cpaapi.ConfigFieldTypeString, Description: "Optional writable directory for sanitized job, policy, usage, inspection, update, and operation-journal state."},
				{Name: "management_base_url", Type: cpaapi.ConfigFieldTypeString, Description: "Optional loopback CLIProxyAPI Management API base URL; defaults to http://127.0.0.1:8317."},
			},
		},
		Capabilities: RegistrationCapabilities{
			ManagementAPI: true, UsagePlugin: true, Scheduler: hostSchema >= cpaapi.SchemaVersion, RequestInterceptor: true, RequestLifecyclePlugin: requestLifecycle,
			AuthProvider: true, ModelProvider: true, Executor: true,
			ExecutorModelScope:    "oauth",
			ExecutorInputFormats:  []string{"codex"},
			ExecutorOutputFormats: []string{"codex"},
		},
	}
}

func (a *App) HandleSchedulerPick(request cpaapi.SchedulerPickRequest) cpaapi.SchedulerPickResponse {
	if a == nil || a.concurrency == nil || a.runtimeSuperseded() {
		return cpaapi.SchedulerPickResponse{}
	}
	if a.quotaGuard != nil {
		// Scheduler admission can precede the first account-list request. Prime
		// credential-ID/auth-index aliases so a quota policy is not bypassed when
		// CPA sends the credential ID as candidate.ID. A short bounded lookup is
		// preferable to silently scheduling an account already over its limit.
		if a.accounts != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			a.accounts.EnsureUsageStorageBindings(ctx)
			cancel()
		}
		filtered, changed := a.quotaGuard.FilterSchedulerCandidates(request)
		if changed {
			// The scheduler protocol only returns one selected AuthID; it does not
			// let a plugin return a rewritten candidate list. When quota filtering
			// leaves no eligible accounts, handle the pick with an empty AuthID so
			// CPA sticky routing cannot fall back to a quota-limited candidate.
			if len(filtered) == 0 {
				return cpaapi.SchedulerPickResponse{Handled: true}
			}
			// When quota filtering leaves exactly one account, select it explicitly
			// so CPA cannot fall back to the original, quota-limited sticky candidate.
			if len(filtered) == 1 {
				return cpaapi.SchedulerPickResponse{AuthID: filtered[0].ID, Handled: true}
			}
			request.Candidates = filtered
		}
	}
	return a.concurrency.PickAuth(request)
}

func (a *App) HandleRequestBefore(request cpaapi.RequestInterceptRequest) cpaapi.RequestInterceptResponse {
	if a == nil || a.requestHooks == nil || a.runtimeSuperseded() {
		return cpaapi.RequestInterceptResponse{}
	}
	return a.requestHooks.InterceptBefore(request)
}

// openCodeSessionTargetTTL keeps the session router's model set current without
// rebuilding it on every request.
const openCodeSessionTargetTTL = 30 * time.Second

// refreshOpenCodeSessionTargetsIfStale rebuilds the OpenCode model set at most
// once per TTL, so a newly loaded catalog becomes session-targeted quickly.
func (a *App) refreshOpenCodeSessionTargetsIfStale() {
	if a == nil || a.opencodeSession == nil {
		return
	}
	last := a.opencodeSessionSyncedAt.Load()
	if last != 0 && time.Since(time.Unix(0, last)) < openCodeSessionTargetTTL {
		return
	}
	a.opencodeSessionSyncedAt.Store(time.Now().UnixNano())
	a.refreshOpenCodeSessionTargets()
}

// refreshOpenCodeSessionTargets publishes the union of the official OpenCode
// model ids and every account's cached catalog to the session router and the
// model-control gate, so both make their attribution decision from the same
// data and x-opencode-session is only ever sent for OpenCode models.
func (a *App) refreshOpenCodeSessionTargets() {
	if a == nil || a.opencodeSession == nil {
		return
	}
	authIndexes := []string(nil)
	if a.aiProviderNames != nil {
		authIndexes = a.aiProviderNames.OpenCodeAuthIndexes()
		a.opencodeSession.SetAuthIndexes(authIndexes)
	}
	targets := make([]string, 0, 256)
	if a.opencodePricing != nil {
		targets = append(targets, a.opencodePricing.ModelIDs(openCodeKindGoValue)...)
		targets = append(targets, a.opencodePricing.ModelIDs(openCodeKindZenValue)...)
	}
	if a.opencode != nil {
		for _, account := range a.opencode.ListAccounts() {
			targets = append(targets, account.Models...)
		}
	}
	if a.opencodeZen != nil {
		for _, account := range a.opencodeZen.ListAccounts() {
			targets = append(targets, account.Models...)
		}
	}
	a.opencodeSession.SetTargets(targets)
	if a.opencodeModelControlGate != nil {
		// The gate shares the router's attribution data, so a disabled model is
		// blocked exactly on OpenCode traffic and never on another channel.
		a.opencodeModelControlGate.SetAuthIndexes(authIndexes)
		a.opencodeModelControlGate.SetTargets(targets)
	}
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
	if a == nil || a.runtimeSuperseded() {
		return cpaapi.RequestInterceptResponse{}
	}
	response := cpaapi.RequestInterceptResponse{}
	if a.requestHooks != nil {
		// This is the interception path the host actually invokes, so the session
		// router's OpenCode model set is refreshed here.
		a.refreshOpenCodeSessionTargetsIfStale()
		response = a.requestHooks.InterceptAfter(request)
		if response.Terminate {
			// Another transformer answered the request, so this attempt never reaches
			// upstream and must not consume the request's retry counter.
			return response
		}
	}
	if exhausted, terminate := a.modelRetryExhaustedResponse(request); terminate {
		// The attempt after the operator's retry budget ends here instead of going
		// upstream, so the client sees a 503 instead of the upstream error.
		return exhausted
	}
	return response
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
	// Keep the dedicated model-error journal fed without letting a journal problem
	// reach CPA's completion path.
	a.recordModelError(completion)
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
			{Method: http.MethodGet, Path: managementRoutePrefix + "/ai-provider-names", Description: "Read plugin-stored AI provider display names keyed by stable channel characteristics."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/ai-provider-names", Description: "Save or clear plugin-stored AI provider display names for one channel."},
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
			{Method: http.MethodGet, Path: managementRoutePrefix + "/global-policy", Description: "Read the permanent plugin-wide baseline policy."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/global-policy", Description: "Validate and save the permanent plugin-wide baseline policy."},
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
			{Method: http.MethodGet, Path: managementRoutePrefix + "/experiments/auto-model-whitelist", Description: "Read the Codex automatic model allow-list experiment status and recent detections."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/experiments/agent-identity/session-login", Description: "Convert one explicitly submitted ChatGPT Session JSON into a pending Agent Identity login credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/risk-control", Description: "Read the redacted plugin-native risk-control configuration, status, and events."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/risk-control", Description: "Validate and persist plugin-native risk-control settings."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/risk-control/events", Description: "Clear the redacted risk-control event history."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/risk-control/hashes", Description: "Clear remembered risk-control input hashes."},
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
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/models", Description: "Read the upstream model catalog for one OpenCode Go workspace or Zen credential."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/model-test", Description: "Probe one OpenCode model through a stored Go or Zen credential."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/bind", Description: "Create or update the OpenAI-compatible CPA channel that routes one OpenCode credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/pricing", Description: "Read the official OpenCode Zen and Go model prices with their sync provenance."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/pricing/refresh", Description: "Revalidate the official OpenCode price catalog and report whether it changed."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/session", Description: "Read the per-conversation x-opencode-session routing status."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/channels", Description: "List the CPA AI-provider channels that belong to OpenCode."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/import", Description: "Import the credential of one existing OpenCode AI-provider channel."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/storage", Description: "Report the private state directory that holds the OpenCode credentials."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/model-control", Description: "List the OpenCode models and the globally disabled set."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/opencode/model-control", Description: "Replace the globally disabled OpenCode model set."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/cline-pass/accounts", Description: "List redacted bound Cline Pass accounts."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/accounts", Description: "Save or update one Cline Pass account credential."},
			{Method: http.MethodDelete, Path: managementRoutePrefix + "/opencode/cline-pass/accounts", Description: "Remove one bound Cline Pass account."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/cline-pass/catalog", Description: "Read the allow-listed Cline Pass model catalog."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/login/start", Description: "Start a Cline Pass sign-in: browser device flow, Cline CLI reuse or API key."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/login/poll", Description: "Poll one pending Cline Pass sign-in."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/login/cancel", Description: "Cancel one pending Cline Pass sign-in."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/refresh", Description: "Rotate one Cline Pass OAuth token and optionally republish its CPA channel."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/models", Description: "Validate one Cline Pass credential against the gateway model catalog."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/model-test", Description: "Probe one Cline Pass model through a stored credential."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/opencode/cline-pass/bind", Description: "Create or update the OpenAI-compatible CPA channel that routes one Cline Pass account."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/cline-pass/settings", Description: "Read the Cline Pass model publishing settings."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/opencode/cline-pass/settings", Description: "Persist the Cline Pass model publishing settings and republish every stored account."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/opencode/cline-pass/models", Description: "List the Cline Pass models with their client-facing ids and publication state."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/self-update", Description: "Read the direct GitHub self-update state."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/codex/overview", Description: "Read the Codex workspace counts: host Codex accounts, Codex channels, and effective convergence mode."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/codex/fingerprint", Description: "Read every editable Codex fingerprint field with its default."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/codex/fingerprint", Description: "Update Codex fingerprint fields; an empty value restores a field default."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/codex/fingerprint/reset", Description: "Restore Codex fingerprint fields to their defaults."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/codex/test-targets", Description: "List the credentials the Codex model page can probe, including AI-provider channels."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/codex/model-test", Description: "Probe one model through a saved Codex AI-provider channel."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/codex/models", Description: "List the Codex models and the globally disabled set."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/codex/models", Description: "Replace the globally disabled Codex model set."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/self-update/check", Description: "Resolve the latest release from GitHub for the direct self-update path."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/self-update/install", Description: "Download, verify and apply the selected release to the plugin library file."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/self-update/settings", Description: "Record the plugin library path used by the direct self-update."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/self-update/reload", Description: "Ask CPA to reinstall and reload this plugin so a replaced library applies without a restart."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/ai-providers/test", Description: "Probe one AI provider channel endpoint with the submitted credential."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/ai-providers/runtime", Description: "Read redacted AI provider concurrency, token, and model cost metrics."},
			{Method: http.MethodPost, Path: managementRoutePrefix + "/usage/reset", Description: "Reset locally recorded usage for one account or AI provider."},
			{Method: http.MethodGet, Path: managementRoutePrefix + "/auto-retry", Description: "Read the automatic transparent retry budget applied to every managed credential."},
			{Method: http.MethodPut, Path: managementRoutePrefix + "/auto-retry", Description: "Set the automatic retry budget (0..10) and apply it to every managed credential."},
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
		// A self-update may have staged a newer interface next to the private state; serving it
		// lets interface-only updates take effect on a page refresh instead of a CPA restart.
		body := a.indexHTML
		if dataDir := a.configSnapshot().DataDir; dataDir != "" {
			if staged, ok := readUIOverride(dataDir); ok {
				body = staged
			}
		}
		return cpaapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       append([]byte(nil), body...),
		}
	}
	if configErr := a.configError(); configErr != "" {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": configErr})
	}
	if strings.HasPrefix(path, "/v0/management"+managementRoutePrefix) {
		managementKey := resolveManagementKey(req.Headers)
		if a.riskControl != nil {
			a.riskControl.SetManagementCredentials(resolveManagementBaseURL(a.configSnapshot().ManagementBaseURL), managementKey, a.managementDoer)
		}
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
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/global-policy":
		if a.globalPolicy == nil {
			return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "global policy service is unavailable"})
		}
		return jsonResponse(http.StatusOK, a.globalPolicy.Snapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/global-policy":
		return a.handlePutGlobalPolicy(req)
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
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/experiments/auto-model-whitelist":
		return a.handleAutoModelWhitelistStatus(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/experiments/agent-identity/session-login":
		return a.handleAgentIdentitySessionLogin(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/risk-control":
		return jsonResponse(http.StatusOK, a.riskControl.Snapshot())
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/risk-control":
		return a.handleRiskControlUpdate(req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/risk-control/events":
		return a.handleRiskControlClear(false)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/risk-control/hashes":
		return a.handleRiskControlClear(true)
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
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/models":
		return a.handleOpenCodeModels(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/model-test":
		return a.handleOpenCodeModelTest(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/bind":
		return a.handleOpenCodeBind(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/pricing":
		return a.handleOpenCodePricing(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/pricing/refresh":
		return a.handleOpenCodePricingRefresh(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/session":
		return a.handleOpenCodeSession(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/channels":
		return a.handleOpenCodeChannels(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/import":
		return a.handleOpenCodeImport(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/storage":
		return a.handleOpenCodeStorage(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/model-control":
		return a.handleOpenCodeModelControl(ctx, req)
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/opencode/model-control":
		return a.handleOpenCodeModelControlUpdate(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/accounts":
		return a.handleClinePassAccounts(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/accounts":
		return a.handleClinePassAccounts(ctx, req)
	case method == http.MethodDelete && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/accounts":
		return a.handleClinePassAccounts(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/catalog":
		return a.handleClinePassCatalog(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/login/start":
		return a.handleClinePassLoginStart(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/login/poll":
		return a.handleClinePassLoginPoll(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/login/cancel":
		return a.handleClinePassLoginCancel(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/refresh":
		return a.handleClinePassRefresh(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/models":
		return a.handleClinePassModels(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/model-test":
		return a.handleClinePassModelTest(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/bind":
		return a.handleClinePassBind(ctx, req)
	case (method == http.MethodGet || method == http.MethodPut) && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/settings":
		return a.handleClinePassSettings(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/opencode/cline-pass/models":
		return a.handleClinePassModelPage(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/self-update":
		return a.handleSelfUpdate(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/self-update/check":
		return a.handleSelfUpdateCheck(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/self-update/install":
		return a.handleSelfUpdateInstall(ctx, req)
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/self-update/settings":
		return a.handleSelfUpdateSettings(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/auto-retry":
		return a.handleAutoRetryGet(req)
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/auto-retry":
		return a.handleAutoRetryUpdate(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/self-update/reload":
		return a.handleSelfUpdateReload(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/codex/overview":
		return a.handleCodexOverview(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/codex/fingerprint":
		return a.handleCodexFingerprint(req)
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/codex/fingerprint":
		return a.handleCodexFingerprintUpdate(req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/codex/fingerprint/reset":
		return a.handleCodexFingerprintReset(req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/codex/test-targets":
		return a.handleCodexTestTargets(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/codex/model-test":
		return a.handleCodexModelTest(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/codex/models":
		return a.handleCodexModels(ctx, req)
	case method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/codex/models":
		return a.handleCodexModelsUpdate(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/opencode/zen/probe-account":
		return a.handleOpenCodeZenProbeAccount(ctx, req)
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/ai-providers/test":
		return a.handleAIProviderProbe(ctx, req)
	case method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/ai-providers/runtime":
		if resolveManagementKey(req.Headers) == "" {
			return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
		}
		return a.handleAIProviderRuntime()
	case method == http.MethodPost && path == "/v0/management"+managementRoutePrefix+"/usage/reset":
		if resolveManagementKey(req.Headers) == "" {
			return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "management key is unavailable"})
		}
		return a.handleUsageReset(req)
	case (method == http.MethodGet && path == "/v0/management"+managementRoutePrefix+"/ai-provider-names") ||
		(method == http.MethodPut && path == "/v0/management"+managementRoutePrefix+"/ai-provider-names"):
		return a.handleAIProviderNames(ctx, req)
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

func (a *App) handlePutGlobalPolicy(req cpaapi.ManagementRequest) cpaapi.ManagementResponse {
	if a == nil || a.globalPolicy == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "global policy service is unavailable"})
	}
	var policy GlobalPolicy
	if errDecode := decodeJSONRequest(req.Body, &policy); errDecode != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()})
	}
	snapshot, errSave := a.globalPolicy.Set(policy)
	if errSave != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errSave.Error()})
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
