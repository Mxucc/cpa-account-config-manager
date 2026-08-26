package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	usageEmailKeyPrefix   = "email:"
	usageAccountKeyPrefix = "account:"
	usagePendingKeyPrefix = "auth-index:"
)

type usageIdentityFingerprint struct {
	EmailHash     string `json:"email_hash,omitempty"`
	AccountIDHash string `json:"account_id_hash,omitempty"`
}

type usageBinding struct {
	Key       string
	AuthID    string
	Identity  usageIdentityFingerprint
	Disabled  bool
	CreatedAt time.Time
	UpdatedAt time.Time
	ModTime   time.Time
}

func usageBindingForEntry(entry cpaapi.HostAuthFileEntry) (usageBinding, bool) {
	provider := deduplicationProviderFamily(firstNonEmpty(entry.Provider, entry.Type))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(firstNonEmpty(entry.Provider, entry.Type)))
	}
	email := firstValidUsageEmail(entry.Email, entry.Account, entry.Name)
	accountIDHash := strings.ToLower(strings.TrimSpace(entry.IDToken.AccountFingerprint))
	if len(accountIDHash) != sha256.Size*2 {
		accountIDHash = ""
	}
	identity := usageIdentityFingerprint{
		EmailHash:     usageIdentityDigest(email),
		AccountIDHash: accountIDHash,
	}
	if identity.EmailHash != "" {
		return usageBinding{
			Key:       usageEmailKeyPrefix + usageIdentityDigest(provider+"\x00"+email),
			AuthID:    strings.TrimSpace(entry.ID),
			Identity:  identity,
			Disabled:  entry.Disabled,
			CreatedAt: entry.CreatedAt,
			UpdatedAt: entry.UpdatedAt,
			ModTime:   entry.ModTime,
		}, true
	}
	if identity.AccountIDHash != "" {
		return usageBinding{
			Key:       usageAccountKeyPrefix + usageIdentityDigest(provider+"\x00"+identity.AccountIDHash),
			AuthID:    strings.TrimSpace(entry.ID),
			Identity:  identity,
			Disabled:  entry.Disabled,
			CreatedAt: entry.CreatedAt,
			UpdatedAt: entry.UpdatedAt,
			ModTime:   entry.ModTime,
		}, true
	}
	return usageBinding{}, false
}

func buildUsageBindings(entries []cpaapi.HostAuthFileEntry) map[string]usageBinding {
	indexCounts := make(map[string]int, len(entries))
	for _, entry := range entries {
		if authIndex := strings.TrimSpace(entry.AuthIndex); authIndex != "" {
			indexCounts[authIndex]++
		}
	}
	type candidate struct {
		authIndex string
		binding   usageBinding
	}
	candidates := make([]candidate, 0, len(entries))
	identities := make(map[string]usageIdentityFingerprint, len(entries))
	conflictedKeys := make(map[string]struct{})
	for _, entry := range entries {
		authIndex := strings.TrimSpace(entry.AuthIndex)
		if authIndex == "" || indexCounts[authIndex] != 1 {
			continue
		}
		binding, ok := usageBindingForEntry(entry)
		if !ok {
			continue
		}
		if existing, exists := identities[binding.Key]; exists {
			if usageIdentitiesConflict(existing, binding.Identity) {
				conflictedKeys[binding.Key] = struct{}{}
			} else {
				identities[binding.Key] = mergeUsageIdentity(existing, binding.Identity)
			}
		} else {
			identities[binding.Key] = binding.Identity
		}
		candidates = append(candidates, candidate{authIndex: authIndex, binding: binding})
	}
	bindings := make(map[string]usageBinding, len(candidates))
	for _, item := range candidates {
		if _, conflicted := conflictedKeys[item.binding.Key]; conflicted {
			continue
		}
		bindings[item.authIndex] = item.binding
	}
	return bindings
}

func firstValidUsageEmail(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if strings.HasSuffix(strings.ToLower(value), ".json") {
			value = strings.TrimSpace(value[:len(value)-len(".json")])
		}
		if email := normalizeDeduplicationEmail(value); email != "" {
			return email
		}
	}
	return ""
}

func usageIdentityDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func usagePendingKey(authIndex string) string {
	authIndex = safeOperationIdentifier(authIndex, 256)
	if authIndex == "" {
		return ""
	}
	return usagePendingKeyPrefix + authIndex
}

func validUsageStorageKey(key string) bool {
	key = strings.TrimSpace(key)
	for _, prefix := range []string{usageEmailKeyPrefix, usageAccountKeyPrefix} {
		if strings.HasPrefix(key, prefix) {
			digest := strings.TrimPrefix(key, prefix)
			decoded, errDecode := hex.DecodeString(digest)
			return errDecode == nil && len(decoded) == sha256.Size
		}
	}
	if strings.HasPrefix(key, usagePendingKeyPrefix) {
		authIndex := strings.TrimPrefix(key, usagePendingKeyPrefix)
		return safeOperationIdentifier(authIndex, 256) == authIndex
	}
	return false
}

func usageIdentitiesConflict(left, right usageIdentityFingerprint) bool {
	return left.EmailHash != "" && right.EmailHash != "" && left.EmailHash != right.EmailHash ||
		left.AccountIDHash != "" && right.AccountIDHash != "" && left.AccountIDHash != right.AccountIDHash
}

func usageIdentityCanRebindByEmail(current, replacement usageIdentityFingerprint) bool {
	return current.EmailHash != "" && current.EmailHash == replacement.EmailHash
}

func rebindUsageIdentity(current, replacement usageIdentityFingerprint) usageIdentityFingerprint {
	if !usageIdentityCanRebindByEmail(current, replacement) {
		return mergeUsageIdentity(current, replacement)
	}
	current.EmailHash = replacement.EmailHash
	if replacement.AccountIDHash != "" {
		current.AccountIDHash = replacement.AccountIDHash
	}
	return current
}

func observeAccountLifecycle(current *accountLifecycleState, binding usageBinding, now time.Time) *accountLifecycleState {
	now = now.UTC()
	createdAt := boundedLifecycleSourceTime(binding.CreatedAt, now)
	if createdAt.IsZero() {
		createdAt = now
	}
	stateAt := latestLifecycleSourceTime(now, binding.UpdatedAt, binding.ModTime)
	if stateAt.Before(createdAt) {
		stateAt = createdAt
	}
	if current == nil {
		state := &accountLifecycleState{
			CreatedAt: createdAt, Disabled: binding.Disabled, StateChangedAt: stateAt,
		}
		if binding.Disabled {
			state.DisabledAt = stateAt
		}
		return state
	}
	state := sanitizeAccountLifecycle(current)
	if state == nil {
		return observeAccountLifecycle(nil, binding, now)
	}
	if createdAt.Before(state.CreatedAt) {
		state.CreatedAt = createdAt
	}
	if state.Disabled == binding.Disabled {
		return state
	}
	if !stateAt.After(state.StateChangedAt) {
		stateAt = now
	}
	state.Disabled = binding.Disabled
	state.StateChangedAt = stateAt
	if binding.Disabled {
		state.DisabledAt = stateAt
	} else {
		state.DisabledAt = time.Time{}
	}
	return state
}

func latestLifecycleSourceTime(now time.Time, values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if bounded := boundedLifecycleSourceTime(value, now); bounded.After(latest) {
			latest = bounded
		}
	}
	if !latest.IsZero() {
		return latest
	}
	return now.UTC()
}

func boundedLifecycleSourceTime(value, now time.Time) time.Time {
	value = value.UTC()
	now = now.UTC()
	if value.IsZero() || value.Before(time.Unix(0, 0).UTC()) || value.After(now.Add(24*time.Hour)) {
		return time.Time{}
	}
	return value
}

func sameAccountLifecycle(left, right *accountLifecycleState) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.CreatedAt.Equal(right.CreatedAt) && left.Disabled == right.Disabled &&
		left.DisabledAt.Equal(right.DisabledAt) && left.StateChangedAt.Equal(right.StateChangedAt)
}

func mergeUsageIdentity(primary, secondary usageIdentityFingerprint) usageIdentityFingerprint {
	if primary.EmailHash == "" {
		primary.EmailHash = secondary.EmailHash
	}
	if primary.AccountIDHash == "" {
		primary.AccountIDHash = secondary.AccountIDHash
	}
	return primary
}
