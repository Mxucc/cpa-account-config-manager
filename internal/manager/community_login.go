package manager

import (
	"context"
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
		return "", fmt.Errorf("this provider published no base URL, so no channel can route it")
	}
	body := map[string]any{
		"name":     label,
		"base-url": baseURL,
		"api-key-entries": []map[string]any{
			{"api-key": credential.Access},
		},
		"disabled": false,
	}
	if errSave := a.writeCommunityChannel(label, body); errSave != nil {
		return "", errSave
	}
	return label, nil
}

// writeCommunityChannel appends one channel row through CPA's own AI-provider
// route, which is where every other channel this plugin creates lives.
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
	// Replace a row this provider already created, so signing in twice updates the
	// account instead of piling up channels.
	items := make([]map[string]any, 0, len(existing)+1)
	replaced := false
	for _, entry := range existing {
		if name, _ := entry["name"].(string); name == label {
			items = append(items, body)
			replaced = true
			continue
		}
		items = append(items, entry)
	}
	if !replaced {
		items = append(items, body)
	}
	return client.putAIProviderChannel(ctx, kind, items)
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
