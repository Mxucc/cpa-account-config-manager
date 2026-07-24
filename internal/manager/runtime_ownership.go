package manager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	runtimeClaimVersion       = 1
	runtimeBootstrapVersion   = 1
	runtimeHeartbeatInterval  = time.Second
	runtimeClaimTimeout       = 10 * time.Second
	runtimeTakeoverDelay      = 2 * time.Second
	runtimeClaimFutureSkew    = time.Minute
	maxRuntimeClaimsPerScope  = 1024
	runtimeOwnershipStoreName = "runtime-instances"
	runtimeBootstrapStoreName = "runtime-ownership.json"
	runtimeProtocolVersion    = "0.3.1203"
)

type BackgroundWorkOwner interface {
	AllowsBackgroundWork() bool
}

type RuntimeOwnershipSnapshot struct {
	Active          bool   `json:"active"`
	Superseded      bool   `json:"superseded"`
	InstanceVersion string `json:"instance_version"`
	OwnerVersion    string `json:"owner_version,omitempty"`
	ProcessScope    string `json:"process_scope,omitempty"`
	StorageError    string `json:"storage_error,omitempty"`
	RestartRequired bool   `json:"restart_required"`
}

type persistedRuntimeBootstrap struct {
	Version             int       `json:"version"`
	Ready               bool      `json:"ready"`
	PendingProcessScope string    `json:"pending_process_scope,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type runtimeClaim struct {
	Version       int       `json:"version"`
	InstanceID    string    `json:"instance_id"`
	PluginVersion string    `json:"plugin_version"`
	ProcessScope  string    `json:"process_scope"`
	StartedAt     time.Time `json:"started_at"`
	HeartbeatAt   time.Time `json:"heartbeat_at"`
}

type RuntimeOwnership struct {
	lifecycleMu      sync.Mutex
	mu               sync.RWMutex
	wait             sync.WaitGroup
	instanceID       string
	version          string
	scope            string
	directory        string
	claimPath        string
	startedAt        time.Time
	takeoverAt       time.Time
	active           bool
	owner            runtimeClaim
	storageErr       string
	bootstrapErr     string
	restartRequired  bool
	bootstrapEnabled bool
	retired          bool
	onSuperseded     func()
	cancel           context.CancelFunc
	started          bool
	closed           bool
	now              func() time.Time
	heartbeat        time.Duration
	timeout          time.Duration
	takeover         time.Duration
}

func (o *RuntimeOwnership) SetOnSuperseded(callback func()) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.onSuperseded = callback
	o.mu.Unlock()
}

func NewRuntimeOwnership(version string) *RuntimeOwnership {
	return &RuntimeOwnership{
		instanceID:       newRuntimeInstanceID(),
		version:          strings.TrimSpace(version),
		scope:            runtimeProcessScope(),
		now:              time.Now,
		heartbeat:        runtimeHeartbeatInterval,
		timeout:          runtimeClaimTimeout,
		takeover:         runtimeTakeoverDelay,
		bootstrapEnabled: runtimeBootstrapApplies(version),
	}
}

func (o *RuntimeOwnership) Configure(config Config) {
	if o == nil {
		return
	}
	config = normalizeConfig(config)
	directory := filepath.Join(config.DataDir, runtimeOwnershipStoreName, o.scope)
	claimPath := filepath.Join(directory, o.instanceID+".json")

	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	o.mu.RLock()
	sameStore := o.started && !o.closed && o.claimPath == claimPath
	o.mu.RUnlock()
	if sameStore {
		o.refresh()
		return
	}
	o.stopLoop(true)

	now := o.currentTime()
	restartRequired, bootstrapErr := runtimeBootstrapStatus(config.DataDir, o.scope, now, o.bootstrapEnabled)
	o.mu.Lock()
	if o.closed || o.retired {
		o.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.directory = directory
	o.claimPath = claimPath
	o.startedAt = now
	o.takeoverAt = time.Time{}
	o.active = false
	o.owner = runtimeClaim{}
	o.storageErr = ""
	o.bootstrapErr = bootstrapErr
	o.restartRequired = restartRequired
	o.cancel = cancel
	o.started = true
	o.mu.Unlock()
	o.refresh()
	if o.isRetired() {
		return
	}
	o.wait.Add(1)
	go o.run(ctx)
}

func (o *RuntimeOwnership) AllowsBackgroundWork() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.started && !o.closed && o.active && o.storageErr == ""
}

func (o *RuntimeOwnership) Snapshot() RuntimeOwnershipSnapshot {
	if o == nil {
		return RuntimeOwnershipSnapshot{}
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	ownerVersion := strings.TrimSpace(o.owner.PluginVersion)
	return RuntimeOwnershipSnapshot{
		Active:          o.started && !o.closed && o.active && o.storageErr == "",
		Superseded:      o.started && o.owner.InstanceID != "" && o.owner.InstanceID != o.instanceID,
		InstanceVersion: o.version,
		OwnerVersion:    ownerVersion,
		ProcessScope:    o.scope,
		StorageError:    o.storageErr,
		RestartRequired: o.restartRequired,
	}
}

func (o *RuntimeOwnership) Shutdown() {
	if o == nil {
		return
	}
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	o.mu.Unlock()
	o.stopLoop(true)
}

func (o *RuntimeOwnership) run(ctx context.Context) {
	defer o.wait.Done()
	interval := o.heartbeatInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.refresh()
			if o.isRetired() {
				return
			}
		}
	}
}

func (o *RuntimeOwnership) refresh() {
	if o == nil {
		return
	}
	now := o.currentTime()
	o.mu.RLock()
	if !o.started || o.closed {
		o.mu.RUnlock()
		return
	}
	claim := runtimeClaim{
		Version: runtimeClaimVersion, InstanceID: o.instanceID, PluginVersion: o.version,
		ProcessScope: o.scope, StartedAt: o.startedAt, HeartbeatAt: now,
	}
	claimPath := o.claimPath
	directory := o.directory
	timeout := o.timeout
	takeover := o.takeover
	bootstrapErr := o.bootstrapErr
	restartRequired := o.restartRequired
	o.mu.RUnlock()
	if bootstrapErr != "" {
		o.setRefreshResult(false, runtimeClaim{}, bootstrapErr)
		return
	}

	if errSave := savePrivateJSON(claimPath, claim); errSave != nil {
		o.setRefreshResult(false, runtimeClaim{}, "runtime ownership state could not be persisted")
		return
	}
	claims, errClaims := loadRuntimeClaims(directory, claim.ProcessScope, now, timeout)
	if errClaims != nil {
		o.setRefreshResult(false, runtimeClaim{}, "runtime ownership state could not be loaded")
		return
	}
	winner := selectRuntimeOwner(claims)
	retired := false
	var onSuperseded func()
	o.mu.Lock()
	if o.started && !o.closed {
		active := false
		if winner.InstanceID == claim.InstanceID {
			if len(claims) > 1 && o.takeoverAt.IsZero() {
				o.takeoverAt = now.Add(takeover)
			}
			active = !restartRequired && (o.takeoverAt.IsZero() || !now.Before(o.takeoverAt))
		} else {
			o.takeoverAt = time.Time{}
			o.retired = true
			retired = true
			onSuperseded = o.onSuperseded
		}
		o.active = active
		o.owner = winner
		o.storageErr = ""
	}
	o.mu.Unlock()
	if retired {
		_ = os.Remove(claimPath)
		if onSuperseded != nil {
			go onSuperseded()
		}
	}
}

func (o *RuntimeOwnership) setRefreshResult(active bool, owner runtimeClaim, storageErr string) {
	o.mu.Lock()
	if o.started && !o.closed {
		o.active = active
		o.owner = owner
		o.storageErr = storageErr
	}
	o.mu.Unlock()
}

func (o *RuntimeOwnership) stopLoop(removeClaim bool) {
	o.mu.Lock()
	cancel := o.cancel
	claimPath := o.claimPath
	o.cancel = nil
	o.started = false
	o.active = false
	o.takeoverAt = time.Time{}
	o.owner = runtimeClaim{}
	o.mu.Unlock()
	if cancel != nil {
		cancel()
		o.wait.Wait()
	}
	if removeClaim && strings.TrimSpace(claimPath) != "" {
		_ = os.Remove(claimPath)
	}
}

func (o *RuntimeOwnership) currentTime() time.Time {
	o.mu.RLock()
	now := o.now
	o.mu.RUnlock()
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

func (o *RuntimeOwnership) heartbeatInterval() time.Duration {
	o.mu.RLock()
	interval := o.heartbeat
	o.mu.RUnlock()
	if interval <= 0 {
		return runtimeHeartbeatInterval
	}
	return interval
}

func (o *RuntimeOwnership) isRetired() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.retired
}

func loadRuntimeClaims(directory, scope string, now time.Time, timeout time.Duration) ([]runtimeClaim, error) {
	entries, errRead := os.ReadDir(directory)
	if errRead != nil {
		return nil, errRead
	}
	if len(entries) > maxRuntimeClaimsPerScope {
		return nil, errors.New("too many runtime ownership claims")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	claims := make([]runtimeClaim, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		raw, errReadFile := os.ReadFile(path)
		if errReadFile != nil {
			continue
		}
		var claim runtimeClaim
		if errDecode := json.Unmarshal(raw, &claim); errDecode != nil || !validRuntimeClaim(claim, scope, now, timeout) {
			continue
		}
		claims = append(claims, claim)
	}
	if len(claims) == 0 {
		return nil, errors.New("no valid runtime ownership claims")
	}
	return claims, nil
}

func validRuntimeClaim(claim runtimeClaim, scope string, now time.Time, timeout time.Duration) bool {
	if claim.Version != runtimeClaimVersion || claim.ProcessScope != scope ||
		strings.TrimSpace(claim.InstanceID) == "" || strings.TrimSpace(claim.PluginVersion) == "" ||
		claim.StartedAt.IsZero() || claim.HeartbeatAt.IsZero() {
		return false
	}
	if timeout <= 0 {
		timeout = runtimeClaimTimeout
	}
	if claim.HeartbeatAt.After(now.Add(runtimeClaimFutureSkew)) || now.Sub(claim.HeartbeatAt) > timeout {
		return false
	}
	return true
}

func selectRuntimeOwner(claims []runtimeClaim) runtimeClaim {
	var winner runtimeClaim
	for _, claim := range claims {
		if winner.InstanceID == "" || compareRuntimeClaims(claim, winner) > 0 {
			winner = claim
		}
	}
	return winner
}

func compareRuntimeClaims(left, right runtimeClaim) int {
	leftVersion, _, leftOK := parseReleaseVersion(left.PluginVersion)
	rightVersion, _, rightOK := parseReleaseVersion(right.PluginVersion)
	if leftOK != rightOK {
		if leftOK {
			return 1
		}
		return -1
	}
	if leftOK {
		if leftVersion.major != rightVersion.major {
			return compareInt(leftVersion.major, rightVersion.major)
		}
		if leftVersion.minor != rightVersion.minor {
			return compareInt(leftVersion.minor, rightVersion.minor)
		}
		if leftVersion.patch != rightVersion.patch {
			return compareInt(leftVersion.patch, rightVersion.patch)
		}
	} else if compared := strings.Compare(left.PluginVersion, right.PluginVersion); compared != 0 {
		return compared
	}
	if !left.StartedAt.Equal(right.StartedAt) {
		if left.StartedAt.After(right.StartedAt) {
			return 1
		}
		return -1
	}
	return strings.Compare(left.InstanceID, right.InstanceID)
}

func compareInt(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func newRuntimeInstanceID() string {
	var raw [16]byte
	if _, errRead := rand.Read(raw[:]); errRead == nil {
		return hex.EncodeToString(raw[:])
	}
	fallback := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixNano())))
	return hex.EncodeToString(fallback[:16])
}

func runtimeProcessScope() string {
	hostname, errHostname := os.Hostname()
	if errHostname != nil {
		hostname = "unknown-host"
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(hostname) + "\x00" + strconv.Itoa(os.Getpid())))
	return hex.EncodeToString(sum[:12])
}

func runtimeBootstrapApplies(version string) bool {
	current, _, currentOK := parseReleaseVersion(version)
	minimum, _, minimumOK := parseReleaseVersion(runtimeProtocolVersion)
	if !currentOK || !minimumOK {
		return false
	}
	if current.major != minimum.major {
		return current.major > minimum.major
	}
	if current.minor != minimum.minor {
		return current.minor > minimum.minor
	}
	return current.patch >= minimum.patch
}

func runtimeBootstrapStatus(dataDir, processScope string, now time.Time, enabled bool) (bool, string) {
	if !enabled {
		return false, ""
	}
	path := filepath.Join(dataDir, runtimeBootstrapStoreName)
	raw, errRead := os.ReadFile(path)
	if errors.Is(errRead, os.ErrNotExist) {
		state := persistedRuntimeBootstrap{
			Version: runtimeBootstrapVersion, PendingProcessScope: processScope, UpdatedAt: now,
		}
		if errSave := savePrivateJSON(path, state); errSave != nil {
			return false, "runtime ownership state could not be persisted"
		}
		return true, ""
	}
	if errRead != nil {
		return false, "runtime ownership state could not be loaded"
	}
	var state persistedRuntimeBootstrap
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil || state.Version != runtimeBootstrapVersion {
		return false, "runtime ownership state could not be loaded"
	}
	if state.Ready {
		return false, ""
	}
	if strings.TrimSpace(state.PendingProcessScope) == "" {
		state.PendingProcessScope = processScope
		state.UpdatedAt = now
		if errSave := savePrivateJSON(path, state); errSave != nil {
			return false, "runtime ownership state could not be persisted"
		}
		return true, ""
	}
	if state.PendingProcessScope == processScope {
		return true, ""
	}
	state.Ready = true
	state.PendingProcessScope = ""
	state.UpdatedAt = now
	if errSave := savePrivateJSON(path, state); errSave != nil {
		return false, "runtime ownership state could not be persisted"
	}
	return false, ""
}

func backgroundWorkAllowed(owner BackgroundWorkOwner) bool {
	return owner == nil || owner.AllowsBackgroundWork()
}

func contextWithBackgroundOwnership(parent context.Context, owner BackgroundWorkOwner) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if owner == nil {
		return ctx, cancel
	}
	if !owner.AllowsBackgroundWork() {
		cancel()
		return ctx, cancel
	}
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !owner.AllowsBackgroundWork() {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
