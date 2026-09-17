package manager

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file implements the state-based trigger of a notification policy. A
// policy cohort is selected by static account attributes (provider, plan type,
// email suffix). A state rule then inspects what the inspection actually
// observed for each account in that cohort, which is what makes notifications
// such as "every account lost its authorization and is disabled" or "an account
// is failing with 429" expressible.

// normalizeInspectionNotificationStateRules coerces the persisted shape of the
// state rules. It never drops a rule: validation rejects an unusable one, and
// matching treats an unknown target as never matching so a stale persisted rule
// can never fire a notification by accident.
func normalizeInspectionNotificationStateRules(rules []InspectionNotificationStateRule) []InspectionNotificationStateRule {
	if len(rules) == 0 {
		return nil
	}
	normalized := make([]InspectionNotificationStateRule, len(rules))
	for index, rule := range rules {
		rule.Match = strings.ToLower(strings.TrimSpace(rule.Match))
		rule.Scope = strings.ToLower(strings.TrimSpace(rule.Scope))
		if rule.Scope == "" {
			rule.Scope = PolicyStateScopeAny
		}
		rule.Value = normalizeInspectionNotificationStateValue(rule.Match, rule.Value)
		if rule.Scope != PolicyStateScopeAtLeast {
			// minimum_count is meaningless outside the at_least scope and is
			// dropped so a reordered scope cannot leave stale state behind.
			rule.MinimumCount = 0
		}
		normalized[index] = rule
	}
	return normalized
}

// normalizeInspectionNotificationStateValue trims the configured value and
// lowercases the textual targets. A status code is canonicalized through
// strconv so a value such as "0429" cannot be stored in a form the matcher
// would compare against "429" and then never match. A value that is not a
// number at all is left untouched so validation can reject it.
func normalizeInspectionNotificationStateValue(match, value string) string {
	value = strings.TrimSpace(value)
	if match == PolicyStateMatchStatusCode {
		if status, errStatus := strconv.Atoi(value); errStatus == nil {
			return strconv.Itoa(status)
		}
		return value
	}
	return strings.ToLower(value)
}

func validateInspectionNotificationStateRules(policyID string, rules []InspectionNotificationStateRule) ([]InspectionNotificationStateRule, error) {
	rules = normalizeInspectionNotificationStateRules(rules)
	if len(rules) > maxInspectionNotificationStateRules {
		return nil, fmt.Errorf("notification policy %s state_rules must contain at most %d entries", policyID, maxInspectionNotificationStateRules)
	}
	for _, rule := range rules {
		if problem := inspectionNotificationStateRuleProblem(rule); problem != "" {
			return nil, fmt.Errorf("notification policy %s state rule %s", policyID, problem)
		}
	}
	return rules, nil
}

// sanitizeInspectionNotificationStateRules drops the state rules this version
// cannot evaluate. It is used on the state-file load path only. An unusable
// rule there must not make the whole persisted inspection state unloadable,
// because the engine then falls back to a fresh default state and writes it
// back over the file, discarding every record and cooldown. Configuration
// submitted through the API is validated strictly instead, so an operator
// still gets an error for a typo.
func sanitizeInspectionNotificationStateRules(rules []InspectionNotificationStateRule) []InspectionNotificationStateRule {
	rules = normalizeInspectionNotificationStateRules(rules)
	if len(rules) == 0 {
		return nil
	}
	sanitized := make([]InspectionNotificationStateRule, 0, min(len(rules), maxInspectionNotificationStateRules))
	for _, rule := range rules {
		if len(sanitized) >= maxInspectionNotificationStateRules {
			break
		}
		if inspectionNotificationStateRuleProblem(rule) != "" {
			continue
		}
		sanitized = append(sanitized, rule)
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

// inspectionNotificationStateRuleProblem describes why a normalized state rule
// cannot be evaluated, or returns an empty string when it can.
func inspectionNotificationStateRuleProblem(rule InspectionNotificationStateRule) string {
	switch rule.Match {
	case PolicyStateMatchHealth:
		if !inspectionHealthAllowed(rule.Value) {
			return "health is invalid"
		}
	case PolicyStateMatchReasonCode, PolicyStateMatchDisableReason:
		if !inspectionReasonCodeAllowed(rule.Value) {
			return rule.Match + " is invalid"
		}
	case PolicyStateMatchStatusCode:
		status, errStatus := strconv.Atoi(rule.Value)
		if errStatus != nil || boundedHTTPStatus(status) == 0 {
			return "status_code must be between 100 and 599"
		}
	case PolicyStateMatchDisabled:
		if rule.Value != "true" && rule.Value != "false" {
			return "disabled must be true or false"
		}
	default:
		return "match must be health, reason_code, status_code, disabled, or disable_reason"
	}
	switch rule.Scope {
	case PolicyStateScopeAny, PolicyStateScopeAll:
	case PolicyStateScopeAtLeast:
		if rule.MinimumCount < 1 || rule.MinimumCount > maxInspectionAccounts {
			return fmt.Sprintf("minimum_count must be between 1 and %d", maxInspectionAccounts)
		}
	default:
		return "scope must be any, all, or at_least"
	}
	return ""
}

func inspectionHealthAllowed(value string) bool {
	switch value {
	case InspectionHealthHealthy, InspectionHealthQuotaLimited, InspectionHealthInvalidCredentials,
		InspectionHealthDeactivated, InspectionHealthReview, InspectionHealthUnavailable,
		InspectionHealthDisabled, InspectionHealthUnknown:
		return true
	default:
		return false
	}
}

// inspectionNotificationStateEvaluation is the result of evaluating every state
// rule of one notification policy against a cohort.
type inspectionNotificationStateEvaluation struct {
	// Reasons are the matched rules rendered as event reasons, for example
	// "state_health_disabled".
	Reasons []string
	// Rules are the matched rules rendered for humans, for example
	// "health=disabled".
	Rules []string
	// Accounts counts the distinct cohort accounts that matched at least one
	// matched rule.
	Accounts int
}

func (evaluation inspectionNotificationStateEvaluation) empty() bool {
	return len(evaluation.Reasons) == 0
}

// inspectionNotificationStateRuleMatches reports whether the observed evidence
// for one account satisfies a state rule.
func inspectionNotificationStateRuleMatches(rule InspectionNotificationStateRule, account Account, record inspectionRecord, hasRecord bool) bool {
	switch rule.Match {
	case PolicyStateMatchDisabled:
		// Manual state is available even before the first scan.
		return strconv.FormatBool(account.Disabled) == rule.Value
	case PolicyStateMatchHealth:
		return hasRecord && record.Result.Health == rule.Value
	case PolicyStateMatchReasonCode:
		return hasRecord && record.Result.ReasonCode == rule.Value
	case PolicyStateMatchStatusCode:
		// Compared as integers so a persisted value that is numerically equal
		// but textually different still matches.
		status, errStatus := strconv.Atoi(rule.Value)
		return hasRecord && errStatus == nil && record.Result.StatusCode == status
	case PolicyStateMatchDisableReason:
		// Only a disable this plugin performed records a reason. A manual
		// disable is reported through the disabled and health targets instead.
		return hasRecord && record.Result.OwnedDisable && record.DisableReason == rule.Value
	default:
		return false
	}
}

// inspectionNotificationStateRuleScopeMatched applies the count semantics of a
// rule scope to the number of matching accounts in the cohort.
func inspectionNotificationStateRuleScopeMatched(rule InspectionNotificationStateRule, matched, total int) bool {
	switch rule.Scope {
	case PolicyStateScopeAll:
		// Every cohort account must match. An account without any evidence yet
		// cannot satisfy an evidence target, which keeps the "all accounts are
		// in this state" claim honest.
		return total > 0 && matched == total
	case PolicyStateScopeAtLeast:
		return matched >= rule.MinimumCount
	default:
		return matched > 0
	}
}

// inspectionNotificationStateRuleLabel renders a rule for humans.
func inspectionNotificationStateRuleLabel(rule InspectionNotificationStateRule) string {
	return rule.Match + "=" + rule.Value
}

// inspectionNotificationStateRuleReason renders a rule as an event reason that
// follows the existing snake_case reason style.
func inspectionNotificationStateRuleReason(rule InspectionNotificationStateRule) string {
	return "state_" + rule.Match + "_" + sanitizeNotificationStateReasonSegment(rule.Value)
}

func sanitizeNotificationStateReasonSegment(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteRune(character - 'A' + 'a')
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

// inspectionNotificationStateEvaluationFor evaluates the state rules of a
// policy against a cohort. A policy without state rules produces an empty
// evaluation, so existing configurations keep their previous behaviour.
func inspectionNotificationStateEvaluationFor(
	policy InspectionNotificationPolicy,
	accounts map[string]Account,
	records map[string]inspectionRecord,
) inspectionNotificationStateEvaluation {
	evaluation := inspectionNotificationStateEvaluation{}
	if len(policy.StateRules) == 0 || len(accounts) == 0 {
		return evaluation
	}
	matchedAccounts := make(map[string]struct{})
	for _, rule := range policy.StateRules {
		matching := make([]string, 0, len(accounts))
		for id, account := range accounts {
			record, hasRecord := records[id]
			if inspectionNotificationStateRuleMatches(rule, account, record, hasRecord) {
				matching = append(matching, id)
			}
		}
		if !inspectionNotificationStateRuleScopeMatched(rule, len(matching), len(accounts)) {
			continue
		}
		evaluation.Reasons = append(evaluation.Reasons, inspectionNotificationStateRuleReason(rule))
		evaluation.Rules = append(evaluation.Rules, inspectionNotificationStateRuleLabel(rule))
		for _, id := range matching {
			matchedAccounts[id] = struct{}{}
		}
	}
	sort.Strings(evaluation.Reasons)
	sort.Strings(evaluation.Rules)
	evaluation.Accounts = len(matchedAccounts)
	return evaluation
}
