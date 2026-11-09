package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
	"cpa-account-config-manager/internal/magpiecompat"
)

// Signing in to a community provider.
//
// The market lists Magpie/OpenCode provider plugins. Those plugins carry their own
// sign-in in their package, so the honest answer to "add an account" is to install
// that package and run its sign-in here, not to send the operator to another app.
// The sign-in runs in a bounded sidecar process (see magpiecompat), and the
// credential it produces becomes a CPA channel so the provider's models are
// routable like any other.
//
// What this file does NOT do: it never proxies a request for the provider and
// never keeps the credential anywhere but the CPA channel row CPA itself owns.

const (
	// communityLoginTTL bounds how long a started sign-in is kept for polling.
	communityLoginTTL = 10 * time.Minute
	// communityLoginMax bounds how many sign-ins one plugin runtime tracks.
	communityLoginMax = 16
)

// communityLogin is one running community sign-in.
type communityLogin struct {
	id        string
	provider  string
	name      string
	directory string
	// baseURL is the provider's API base, which the channel needs to route models.
	baseURL   string
	session   *magpiecompat.LoginSession
	startedAt time.Time
	// finished records the credential once, so a repeated poll cannot create a
	// second channel from the same sign-in.
	finished bool
	channel  string
}

// communityLogins tracks the sign-ins this runtime started.
var communityLogins = struct {
	mu      sync.Mutex
	entries map[string]*communityLogin
	// key is the management key the operator used to start a sign-in, held for the
	// poll that writes the channel. Memory only, never logged.
	key string
}{entries: map[string]*communityLogin{}}

// communityChannelCredentials remembers, per channel label, the credentials this
// plugin published. A v8 channel group keeps no name, so a row is matched by its
// base URL and credential; this record is what lets a re-sign-in recognise the row
// it wrote last time even though the provider rotated the credential. Only
// digests are kept, never the credential itself.
var communityChannelCredentials = struct {
	mu      sync.Mutex
	entries map[string][]string
}{entries: map[string][]string{}}

// communityChannelCredentialLimit bounds how many past credentials one label
// keeps, so a provider that rotates on every sign-in cannot grow the record.
const communityChannelCredentialLimit = 8

// communityChannelCredentials returns the digests this label published before.
func (a *App) communityChannelCredentials(label string) []string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return nil
	}
	communityChannelCredentials.mu.Lock()
	defer communityChannelCredentials.mu.Unlock()
	stored := communityChannelCredentials.entries[trimmed]
	out := make([]string, len(stored))
	copy(out, stored)
	return out
}

// rememberCommunityChannelCredential records one credential digest for a label.
func (a *App) rememberCommunityChannelCredential(label, credential string) {
	trimmed := strings.TrimSpace(label)
	digest := communityCredentialDigest(credential)
	if trimmed == "" || digest == "" {
		return
	}
	communityChannelCredentials.mu.Lock()
	defer communityChannelCredentials.mu.Unlock()
	stored := communityChannelCredentials.entries[trimmed]
	for _, existing := range stored {
		if existing == digest {
			return
		}
	}
	stored = append(stored, digest)
	if len(stored) > communityChannelCredentialLimit {
		stored = stored[len(stored)-communityChannelCredentialLimit:]
	}
	communityChannelCredentials.entries[trimmed] = stored
}

// communityCredentialDigest hashes one credential so the record never holds it.
func communityCredentialDigest(credential string) string {
	trimmed := strings.TrimSpace(credential)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

// communityChannelCredentialsByProvider remembers, per provider id, the digests of
// the credentials this plugin published for it. A repair pass finds a provider's
// channel by that record: the package keeps its endpoint in its own module, so the
// plugin must not have to hardcode a base URL to find a row it already wrote.
// the credentials this plugin published for it. A repair pass finds a provider's
// channel by that record: the package keeps its endpoint in its own module, so the
// plugin must not have to hardcode a base URL to find a row it already wrote.
var communityChannelCredentialsByProvider = struct {
	mu      sync.Mutex
	entries map[string][]string
	// bases is where each provider's channel points, learned from the sign-in that
	// wrote it. A package keeps its endpoint in its own module, so this is the only
	// way to find that provider's channel again without a hardcoded table.
	bases map[string]string
}{entries: map[string][]string{}, bases: map[string]string{}}

// rememberCommunityChannelBase records the API base a provider's channel uses.
func (a *App) rememberCommunityChannelBase(providerID, baseURL string) {
	provider := strings.ToLower(strings.TrimSpace(providerID))
	trimmed := strings.TrimSpace(baseURL)
	if provider == "" || trimmed == "" {
		return
	}
	communityChannelCredentialsByProvider.mu.Lock()
	communityChannelCredentialsByProvider.bases[provider] = trimmed
	communityChannelCredentialsByProvider.mu.Unlock()
}

// communityChannelBase returns the API base recorded for a provider, if any.
func (a *App) communityChannelBase(providerID string) string {
	provider := strings.ToLower(strings.TrimSpace(providerID))
	if provider == "" {
		return ""
	}
	communityChannelCredentialsByProvider.mu.Lock()
	defer communityChannelCredentialsByProvider.mu.Unlock()
	return communityChannelCredentialsByProvider.bases[provider]
}

// rememberCommunityChannelCredentialForProvider records one published credential
// digest against a provider id. Only digests are kept.
func (a *App) rememberCommunityChannelCredentialForProvider(providerID, credential string) {
	provider := strings.ToLower(strings.TrimSpace(providerID))
	digest := communityCredentialDigest(credential)
	if provider == "" || digest == "" {
		return
	}
	communityChannelCredentialsByProvider.mu.Lock()
	defer communityChannelCredentialsByProvider.mu.Unlock()
	stored := communityChannelCredentialsByProvider.entries[provider]
	for _, existing := range stored {
		if existing == digest {
			return
		}
	}
	stored = append(stored, digest)
	if len(stored) > communityChannelCredentialLimit {
		stored = stored[len(stored)-communityChannelCredentialLimit:]
	}
	communityChannelCredentialsByProvider.entries[provider] = stored
}

// communityChannelCredentialsFor returns the digests published for a provider.
func (a *App) communityChannelCredentialsFor(providerID string) []string {
	provider := strings.ToLower(strings.TrimSpace(providerID))
	if provider == "" {
		return nil
	}
	communityChannelCredentialsByProvider.mu.Lock()
	defer communityChannelCredentialsByProvider.mu.Unlock()
	stored := communityChannelCredentialsByProvider.entries[provider]
	out := make([]string, len(stored))
	copy(out, stored)
	return out
}

// isImportedProvider reports whether id belongs to an imported community plugin.
func (a *App) isImportedProvider(id string) bool {
	if a == nil {
		return false
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false
	}
	for _, entry := range a.providerImportStore().List() {
		if strings.EqualFold(strings.TrimSpace(entry.ID), trimmed) {
			return true
		}
	}
	return false
}

// communityLoginStatusDir is where a running sign-in reports its progress.
func (a *App) communityLoginStatusDir() string {
	if a == nil {
		return ""
	}
	dataDir := strings.TrimSpace(a.configSnapshot().DataDir)
	if dataDir == "" {
		return ""
	}
	return dataDir + "/provider-logins"
}

// startCommunityLogin installs the package if needed and starts its sign-in.
func (a *App) startCommunityLogin(ctx context.Context, req cpaapi.ManagementRequest, providerID string) cpaapi.ManagementResponse {
	// The credential this sign-in produces becomes a CPA channel, and writing one
	// needs the operator's own key. It is held in memory for the poll that
	// finishes the sign-in and is never logged or persisted.
	a.rememberCommunityManagementKey(resolveManagementKey(req.Headers))
	directory, name, errDirectory := a.importedProviderDirectory(ctx, providerID)
	if errDirectory != nil {
		return jsonResponse(http.StatusOK, map[string]any{
			"error":    "community_sign_in_unavailable",
			"provider": providerID,
			"reason":   errDirectory.Error(),
		})
	}
	if !a.pluginRuntimeAvailable() {
		return jsonResponse(http.StatusOK, map[string]any{
			"error":    "community_sign_in_unavailable",
			"provider": providerID,
			"reason":   "the JavaScript runtime (Bun) is not installed on this host, so the plugin's own sign-in cannot run",
		})
	}
	runner := magpiecompat.New("", "", magpiecompat.Options{})
	if runner == nil || !runner.Available() {
		return jsonResponse(http.StatusOK, map[string]any{
			"error":    "community_sign_in_unavailable",
			"provider": providerID,
			"reason":   "the JavaScript runtime (Bun) is not installed on this host, so the plugin's own sign-in cannot run",
		})
	}
	var request accountLoginStartRequest
	if len(req.Body) > 0 {
		_ = json.Unmarshal(req.Body, &request)
	}
	session, status, errStart := runner.StartLogin(ctx, directory, request.Method, request.Inputs, a.communityLoginStatusDir())
	if errStart != nil {
		return jsonResponse(http.StatusOK, map[string]any{
			"error":    "community_sign_in_failed",
			"provider": providerID,
			"reason":   errStart.Error(),
		})
	}
	login := &communityLogin{
		id:        fmt.Sprintf("community-%d", time.Now().UnixNano()),
		provider:  providerID,
		name:      name,
		directory: directory,
		baseURL:   status.BaseURL,
		session:   session,
		startedAt: time.Now(),
	}
	communityLogins.mu.Lock()
	pruneCommunityLoginsLocked()
	communityLogins.entries[login.id] = login
	communityLogins.mu.Unlock()

	answer := accountLoginFlow{
		Provider:     providerID,
		State:        login.id,
		URL:          status.URL,
		Flow:         "community",
		Instructions: status.Instructions,
		Mode:         status.Mode,
		Status:       status.Phase,
	}
	if status.Phase == "failed" {
		answer.Status = "failed"
		answer.Message = status.Message
	}
	// A key-based method has no browser step: the page asks for the key and starts
	// the sign-in again with it.
	if status.Mode == "key" && strings.TrimSpace(status.URL) == "" {
		answer.Mode = "key"
	}
	return jsonResponse(http.StatusOK, answer)
}

// communityLoginSession returns the tracked sign-in for a state, if any.
func (a *App) communityLoginSession(state string) *communityLogin {
	trimmed := strings.TrimSpace(state)
	if trimmed == "" {
		return nil
	}
	communityLogins.mu.Lock()
	defer communityLogins.mu.Unlock()
	login, found := communityLogins.entries[trimmed]
	if !found {
		return nil
	}
	if time.Since(login.startedAt) > communityLoginTTL {
		delete(communityLogins.entries, trimmed)
		login.session.Close()
		return nil
	}
	return login
}

// pollCommunityLogin reports a community sign-in and, once it succeeds, turns the
// credential into a CPA channel.
func (a *App) pollCommunityLogin(state string) cpaapi.ManagementResponse {
	login := a.communityLoginSession(state)
	if login == nil {
		return jsonResponse(http.StatusOK, map[string]any{"state": state, "status": "expired", "done": false})
	}
	status := login.session.Poll()
	result := accountLoginStatus{State: state, Status: status.Phase, Done: false}
	switch status.Phase {
	case "success":
		result.Done = true
		result.Status = "ok"
		if status.Credential != nil && !login.finished {
			login.finished = true
			channel, errChannel := a.publishCommunityChannel(login, status.Credential)
			if errChannel != nil {
				result.Done = false
				result.Status = "failed"
				result.Message = errChannel.Error()
				break
			}
			login.channel = channel
		}
		result.Message = login.channel
	case "failed":
		result.Status = "failed"
		result.Message = status.Message
	default:
		result.Status = "wait"
	}
	return jsonResponse(http.StatusOK, result)
}

// publishCommunityChannel writes the CPA channel that routes the provider's
// models, so the account this sign-in created is usable like any other.
func (a *App) publishCommunityChannel(login *communityLogin, credential *magpiecompat.LoginCredential) (string, error) {
	if a == nil || a.accounts == nil {
		return "", fmt.Errorf("the account service is unavailable")
	}
	name := strings.TrimSpace(login.name)
	if name == "" {
		name = login.provider
	}
	// The channel name is what an operator sees in CPA's channel list, so it names
	// the provider and the account the sign-in produced.
	label := fmt.Sprintf("%s · %s", name, firstNonEmptyText(credential.AccountID, login.provider))
	baseURL := strings.TrimSpace(login.baseURL)
	if baseURL == "" {
		// A provider that keeps its endpoint in a module constant publishes none of
		// it through a hook. The plugin knows the hosts the community providers
		// serve, so the channel can still be written instead of failing the sign-in
		// after the credential was already obtained.
		baseURL = communityProviderBaseURL(login.provider)
	}
	if baseURL == "" {
		return "", fmt.Errorf("this provider published no base URL, so no channel can route it")
	}
	// CPA routes a channel's models from the models list on that row. A channel
	// without one registers no model at all — CPA unregisters the models of a
	// compat auth whose list is empty — so the account would stay unroutable
	// however healthy its credential is. The catalog is the one this provider
	// publishes, read the same way the account row reads it.
	models := a.communityChannelModels(login.provider)
	if len(models) == 0 {
		return "", fmt.Errorf(
			"this provider published no model list, so its channel could not serve any model; the sign-in itself succeeded",
		)
	}
	body := map[string]any{
		"name":     label,
		"base-url": baseURL,
		"api-key-entries": []map[string]any{
			{"api-key": credential.Access},
		},
		"models":   models,
		"disabled": false,
	}
	if errSave := a.writeCommunityChannel(label, body); errSave != nil {
		return "", errSave
	}
	// Record what this provider published, so a later repair can find its channel
	// without the plugin having to know the endpoint the package keeps to itself.
	a.rememberCommunityChannelCredentialForProvider(login.provider, credential.Access)
	a.rememberCommunityChannelBase(login.provider, baseURL)
	return label, nil
}

// communityChannelModels reads the model catalog an imported provider publishes,
// in the shape a CPA channel row takes. An empty answer means the plugin
// published none, which the caller reports instead of writing an unroutable row.
func (a *App) communityChannelModels(providerID string) []map[string]any {
	if a == nil || strings.TrimSpace(providerID) == "" {
		return nil
	}
	if !a.pluginRuntimeAvailable() {
		return nil
	}
	directory, _, errDirectory := a.importedProviderDirectory(context.Background(), providerID)
	if errDirectory != nil || strings.TrimSpace(directory) == "" {
		return nil
	}
	input, errProbe := a.pluginHooks().Probe(directory)
	if errProbe != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(input.Models))
	for _, model := range input.Models {
		id := strings.TrimSpace(model)
		if id == "" {
			continue
		}
		// A CPA compat model names the upstream model and the name a client asks
		// for; publishing the id as both is what makes the model routable.
		out = append(out, map[string]any{"name": id, "alias": id})
	}
	return out
}

// publishCommunityChannelModelRefresh republishes the model list of the channels
// this plugin created for a provider. CPA registers a compat channel's models from
// that field, so a channel written without one — or written before the catalog was
// published — routes nothing until it is filled in.
func (a *App) publishCommunityChannelModelRefresh(providerID string) (int, error) {
	if a == nil {
		return 0, fmt.Errorf("the plugin is unavailable")
	}
	models := a.communityChannelModels(providerID)
	if len(models) == 0 {
		return 0, fmt.Errorf("this provider published no model list")
	}
	baseURL := strings.TrimSpace(communityProviderBaseURL(providerID))
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" {
		return 0, fmt.Errorf("a management key is required to update the channel")
	}

	client, errClient := a.newWriteManagementClient(managementKey)
	if errClient != nil {
		return 0, errClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const kind = "openai-compatibility"
	existing, supported, errRead := client.getAIProviderChannel(ctx, kind)
	if errRead != nil {
		return 0, fmt.Errorf("read the channel list: %w", errRead)
	}
	if !supported {
		return 0, fmt.Errorf("this host does not accept OpenAI-compatible channels")
	}
	// A channel is found by the credential this plugin published for the provider,
	// not by a base URL the plugin would have to hardcode: a community package
	// keeps its endpoint in its own module, so only the row it already wrote knows
	// where it points.
	known := map[string]bool{}
	for _, credential := range a.communityChannelCredentialsFor(providerID) {
		known[credential] = true
	}
	wanted := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	items := make([]map[string]any, 0, len(existing))
	updated := 0
	for _, entry := range existing {
		matched := false
		if live := aiProviderChannelCredential(entry); live != "" && len(known) > 0 {
			_, matched = known[communityCredentialDigest(live)]
		}
		if !matched && wanted != "" {
			live, _ := entry["base-url"].(string)
			matched = strings.TrimRight(strings.TrimSpace(live), "/") == wanted
		}
		if matched {
			entry["models"] = models
			updated++
		}
		items = append(items, entry)
	}
	if updated == 0 {
		return 0, fmt.Errorf("no channel of this provider was found")
	}
	if errWrite := client.putAIProviderChannel(ctx, kind, items); errWrite != nil {
		return 0, errWrite
	}
	return updated, nil
}

// writeCommunityChannel appends one channel row through CPA's own AI-provider
// route, which is where every other channel this plugin creates lives.
//
// The row is matched by what the v8 tree actually keeps, which is the base URL and
// the credential: a v8 channel group carries no name, so a name comparison would
// never match its own earlier row and every sign-in would pile up another one. A
// row is this provider's when it publishes this provider's base URL and either the
// credential this sign-in just obtained or the one this provider published last
// time (the provider rotated it, which is the same account).
func (a *App) writeCommunityChannel(label string, body map[string]any) error {
	managementKey := strings.TrimSpace(a.communityManagementKey())
	if managementKey == "" {
		return fmt.Errorf("a management key is required to create the channel")
	}
	client, errClient := a.newWriteManagementClient(managementKey)
	if errClient != nil {
		return errClient
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const kind = "openai-compatibility"
	existing, supported, errRead := client.getAIProviderChannel(ctx, kind)
	if errRead != nil {
		return fmt.Errorf("read the channel list: %w", errRead)
	}
	if !supported {
		return fmt.Errorf("this host does not accept OpenAI-compatible channels")
	}
	baseURL, _ := body["base-url"].(string)
	credential := aiProviderChannelCredential(body)
	// A row this provider wrote before this session carries an older credential.
	// Only the provider itself knows that mapping, so it is recorded beside the
	// sign-in state instead of being guessed from the list.
	previous := a.communityChannelCredentials(label)
	items := make([]map[string]any, 0, len(existing)+1)
	replaced := false
	for _, entry := range existing {
		if !replaced && communityChannelIsOwn(entry, baseURL, credential, previous) {
			items = append(items, body)
			replaced = true
			continue
		}
		items = append(items, entry)
	}
	if !replaced {
		items = append(items, body)
	}
	if errWrite := client.putAIProviderChannel(ctx, kind, items); errWrite != nil {
		return errWrite
	}
	a.rememberCommunityChannelCredential(label, credential)
	return nil
}

// communityChannelIsOwn reports whether one live row belongs to this provider.
func communityChannelIsOwn(entry map[string]any, baseURL, credential string, previous []string) bool {
	if entry == nil || strings.TrimSpace(baseURL) == "" {
		return false
	}
	live, _ := entry["base-url"].(string)
	if strings.TrimRight(strings.TrimSpace(live), "/") != strings.TrimRight(strings.TrimSpace(baseURL), "/") {
		return false
	}
	liveCredential := aiProviderChannelCredential(entry)
	if liveCredential == "" {
		return false
	}
	if liveCredential == credential {
		return true
	}
	// previous holds digests, so the live credential is hashed before comparing:
	// the record never keeps a credential, not even a rotated one.
	liveDigest := communityCredentialDigest(liveCredential)
	for _, candidate := range previous {
		if candidate != "" && candidate == liveDigest {
			return true
		}
	}
	return false
}

// pruneCommunityLoginsLocked drops expired entries. Callers hold the mutex.
func pruneCommunityLoginsLocked() {
	for id, login := range communityLogins.entries {
		if time.Since(login.startedAt) > communityLoginTTL {
			delete(communityLogins.entries, id)
			login.session.Close()
		}
	}
	for len(communityLogins.entries) >= communityLoginMax {
		var oldestID string
		var oldest time.Time
		for id, login := range communityLogins.entries {
			if oldestID == "" || login.startedAt.Before(oldest) {
				oldestID, oldest = id, login.startedAt
			}
		}
		if oldestID == "" {
			return
		}
		communityLogins.entries[oldestID].session.Close()
		delete(communityLogins.entries, oldestID)
	}
}

// rememberCommunityManagementKey keeps the management key the operator used to
// start a community sign-in. The channel write happens on a later poll, which has
// no key of its own; the value is held in memory only.
func (a *App) rememberCommunityManagementKey(managementKey string) {
	if a == nil {
		return
	}
	trimmed := strings.TrimSpace(managementKey)
	if trimmed == "" {
		return
	}
	communityLogins.mu.Lock()
	communityLogins.key = trimmed
	communityLogins.mu.Unlock()
}

// communityManagementKey returns the held key.
func (a *App) communityManagementKey() string {
	communityLogins.mu.Lock()
	defer communityLogins.mu.Unlock()
	return communityLogins.key
}

// communityProviderBaseURL maps a community provider onto the API base its plugin
// serves, for the plugins that keep that base in a module constant no hook exposes.
// An unknown provider returns "" so the caller reports the gap instead of guessing.
func communityProviderBaseURL(providerID string) string {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "zen-free":
		// OpenCode's free Zen access, as the zen-free plugin's own default.
		return "https://opencode.ai/inference/v1"
	case "zcode":
		return "https://api.z.ai/api/anthropic"
	}
	return ""
}
