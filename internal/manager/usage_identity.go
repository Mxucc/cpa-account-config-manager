package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

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
	Key      string
	Identity usageIdentityFingerprint
	Disabled bool
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
			Key:      usageEmailKeyPrefix + usageIdentityDigest(provider+"\x00"+email),
			Identity: identity,
			Disabled: entry.Disabled,
		}, true
	}
	if identity.AccountIDHash != "" {
		return usageBinding{
			Key:      usageAccountKeyPrefix + usageIdentityDigest(provider+"\x00"+identity.AccountIDHash),
			Identity: identity,
			Disabled: entry.Disabled,
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

func mergeUsageIdentity(primary, secondary usageIdentityFingerprint) usageIdentityFingerprint {
	if primary.EmailHash == "" {
		primary.EmailHash = secondary.EmailHash
	}
	if primary.AccountIDHash == "" {
		primary.AccountIDHash = secondary.AccountIDHash
	}
	return primary
}
