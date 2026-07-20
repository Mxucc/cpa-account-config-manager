package manager

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUpdateCheckIntervalHours = 24
	minUpdateCheckIntervalHours     = 1
	maxUpdateCheckIntervalHours     = 7 * 24
)

type UpdatePolicy struct {
	CheckEnabled       bool `json:"check_enabled"`
	CheckIntervalHours int  `json:"check_interval_hours"`
	AutoUpdate         bool `json:"auto_update"`
}

type UpdatePolicyRequest struct {
	Policy            UpdatePolicy `json:"policy"`
	ConfirmAutoUpdate bool         `json:"confirm_auto_update"`
}

type UpdateSnapshot struct {
	Policy          UpdatePolicy `json:"policy"`
	CurrentVersion  string       `json:"current_version"`
	LatestVersion   string       `json:"latest_version,omitempty"`
	UpdateAvailable bool         `json:"update_available"`
	ReleaseURL      string       `json:"release_url,omitempty"`
	Checking        bool         `json:"checking"`
	Pending         bool         `json:"pending"`
	CheckedAt       time.Time    `json:"checked_at,omitempty"`
	Error           string       `json:"error,omitempty"`
}

type releaseVersion struct {
	major int
	minor int
	patch int
}

func defaultUpdatePolicy() UpdatePolicy {
	return UpdatePolicy{
		CheckEnabled:       true,
		CheckIntervalHours: defaultUpdateCheckIntervalHours,
	}
}

func normalizeUpdatePolicy(policy UpdatePolicy) UpdatePolicy {
	if policy.CheckIntervalHours == 0 {
		policy.CheckIntervalHours = defaultUpdateCheckIntervalHours
	}
	return policy
}

func validateUpdatePolicy(policy UpdatePolicy) (UpdatePolicy, error) {
	policy = normalizeUpdatePolicy(policy)
	if policy.CheckIntervalHours < minUpdateCheckIntervalHours || policy.CheckIntervalHours > maxUpdateCheckIntervalHours {
		return UpdatePolicy{}, fmt.Errorf("check_interval_hours must be between %d and %d", minUpdateCheckIntervalHours, maxUpdateCheckIntervalHours)
	}
	if policy.AutoUpdate && !policy.CheckEnabled {
		return UpdatePolicy{}, fmt.Errorf("auto_update requires check_enabled")
	}
	return policy, nil
}

func parseReleaseVersion(value string) (releaseVersion, string, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	if index := strings.IndexAny(value, "-+"); index >= 0 {
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return releaseVersion{}, "", false
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		if part == "" {
			return releaseVersion{}, "", false
		}
		parsed, errParse := strconv.Atoi(part)
		if errParse != nil || parsed < 0 || parsed > 1_000_000 {
			return releaseVersion{}, "", false
		}
		numbers[index] = parsed
	}
	normalized := fmt.Sprintf("%d.%d.%d", numbers[0], numbers[1], numbers[2])
	return releaseVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, normalized, true
}

func releaseVersionNewer(current, latest string) (bool, string, bool) {
	currentVersion, _, currentOK := parseReleaseVersion(current)
	latestVersion, normalizedLatest, latestOK := parseReleaseVersion(latest)
	if !currentOK || !latestOK {
		return false, "", false
	}
	if latestVersion.major != currentVersion.major {
		return latestVersion.major > currentVersion.major, normalizedLatest, true
	}
	if latestVersion.minor != currentVersion.minor {
		return latestVersion.minor > currentVersion.minor, normalizedLatest, true
	}
	return latestVersion.patch > currentVersion.patch, normalizedLatest, true
}
