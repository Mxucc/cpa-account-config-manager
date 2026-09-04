package manager

import (
	"fmt"
	"sort"
	"strings"
)

const (
	PolicyConditionAll = "all"
	PolicyConditionAny = "any"

	PolicyConditionProvider    = "provider"
	PolicyConditionAccountType = "account_type"
	PolicyConditionEmailSuffix = "email_suffix"

	maxConditionalPolicyRules      = 100
	maxPolicyConditionDepth        = 5
	maxPolicyConditionsPerRule     = 32
	maxConditionalPolicyIdentifier = 64
	maxConditionalPolicyName       = 128
	maxPolicyConditionValue        = 256
	minConditionalPolicyPriority   = -10000
	maxConditionalPolicyPriority   = 10000
)

type PolicyCondition struct {
	Field string `json:"field" yaml:"field"`
	Value string `json:"value" yaml:"value"`
}

type PolicyConditionGroup struct {
	Operator   string                 `json:"operator" yaml:"operator"`
	Conditions []PolicyCondition      `json:"conditions,omitempty" yaml:"conditions,omitempty"`
	Groups     []PolicyConditionGroup `json:"groups,omitempty" yaml:"groups,omitempty"`
}

type ConditionalPolicyActions struct {
	NewAccountModelProbe     *bool                  `json:"new_account_model_probe,omitempty" yaml:"new_account_model_probe,omitempty"`
	Priority                 *int                   `json:"priority,omitempty" yaml:"priority,omitempty"`
	Disabled                 *bool                  `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	ConcurrencyLimit         *int                   `json:"concurrency_limit,omitempty" yaml:"concurrency_limit,omitempty"`
	Concurrency15sLimit      *int                   `json:"concurrency_15s_limit,omitempty" yaml:"concurrency_15s_limit,omitempty"`
	ConcurrencyWindowSeconds *int                   `json:"concurrency_window_seconds,omitempty" yaml:"concurrency_window_seconds,omitempty"`
	QuotaPolicy              *AccountQuotaPolicy    `json:"quota_policy,omitempty" yaml:"quota_policy,omitempty"`
	Note                     *string                `json:"note,omitempty" yaml:"note,omitempty"`
	Prefix                   *string                `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	ProxyURL                 *string                `json:"proxy_url,omitempty" yaml:"proxy_url,omitempty"`
	Websockets               *bool                  `json:"websockets,omitempty" yaml:"websockets,omitempty"`
	Headers                  *HeaderPatch           `json:"headers,omitempty" yaml:"headers,omitempty"`
	ModelPolicy              *ModelPolicyPatch      `json:"model_policy,omitempty" yaml:"model_policy,omitempty"`
	CodexIdentity            *CodexIdentityOverride `json:"codex_identity,omitempty" yaml:"codex_identity,omitempty"`
	ProxyProfileID           *string                `json:"proxy_profile_id,omitempty" yaml:"proxy_profile_id,omitempty"`
	AIProviderProxyProfileID *string                `json:"ai_provider_proxy_profile_id,omitempty" yaml:"ai_provider_proxy_profile_id,omitempty"`
}

type ConditionalPolicyRule struct {
	ID         string                   `json:"id" yaml:"id"`
	Name       string                   `json:"name" yaml:"name"`
	Enabled    bool                     `json:"enabled" yaml:"enabled"`
	Priority   int                      `json:"priority" yaml:"priority"`
	Conditions PolicyConditionGroup     `json:"conditions" yaml:"conditions"`
	Actions    ConditionalPolicyActions `json:"actions" yaml:"actions"`
}

type ResolvedConditionalPolicy struct {
	NewAccountModelProbe     *bool
	Priority                 *int
	Disabled                 *bool
	ConcurrencyLimit         *int
	Concurrency15sLimit      *int
	ConcurrencyWindowSeconds *int
	QuotaPolicy              *AccountQuotaPolicy
	Note                     *string
	Prefix                   *string
	ProxyURL                 *string
	Websockets               *bool
	Headers                  *HeaderPatch
	ModelPolicy              *ModelPolicyPatch
	CodexIdentity            *CodexIdentityOverride
	MatchedRuleIDs           []string
	PriorityFromRule         bool
	WebsocketsFromRule       bool
	ModelPolicyFromRule      bool
	DisabledFromRule         bool
	ConcurrencyFromRule      bool
	QuotaPolicyFromRule      bool
	NoteFromRule             bool
	PrefixFromRule           bool
	ProxyURLFromRule         bool
	HeadersFromRule          bool
	CodexIdentityFromRule    bool
	ProxyProfileID           *string
	AIProviderProxyProfileID *string
	ProxyProfileFromRule     bool
	AIProviderProxyFromRule  bool
}

func validateConditionalPolicyRules(rules []ConditionalPolicyRule) ([]ConditionalPolicyRule, error) {
	if len(rules) > maxConditionalPolicyRules {
		return nil, fmt.Errorf("conditional policy exceeds %d rules", maxConditionalPolicyRules)
	}
	normalized := make([]ConditionalPolicyRule, 0, len(rules))
	ids := make(map[string]struct{}, len(rules))
	for index, rule := range rules {
		rule.ID = strings.ToLower(strings.TrimSpace(rule.ID))
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("rule-%d", index+1)
		}
		if !validConditionalPolicyIdentifier(rule.ID) {
			return nil, fmt.Errorf("conditional policy rule id is invalid")
		}
		if _, duplicate := ids[rule.ID]; duplicate {
			return nil, fmt.Errorf("conditional policy rule ids must be unique")
		}
		ids[rule.ID] = struct{}{}
		rule.Name = strings.TrimSpace(rule.Name)
		if len(rule.Name) > maxConditionalPolicyName || hasUnsafeControl(rule.Name, true) {
			return nil, fmt.Errorf("conditional policy rule name is invalid")
		}
		if rule.Priority < minConditionalPolicyPriority || rule.Priority > maxConditionalPolicyPriority {
			return nil, fmt.Errorf("conditional policy rule priority is out of range")
		}
		conditionCount := 0
		conditions, errConditions := normalizePolicyConditionGroup(rule.Conditions, 1, &conditionCount)
		if errConditions != nil {
			return nil, fmt.Errorf("conditional policy rule %s: %w", rule.ID, errConditions)
		}
		if conditionCount == 0 {
			return nil, fmt.Errorf("conditional policy rule %s requires at least one condition", rule.ID)
		}
		rule.Conditions = conditions
		actions, errActions := validateConditionalPolicyActions(rule.Actions)
		if errActions != nil {
			return nil, fmt.Errorf("conditional policy rule %s: %w", rule.ID, errActions)
		}
		rule.Actions = actions
		normalized = append(normalized, rule)
	}
	return normalized, nil
}

func validConditionalPolicyIdentifier(value string) bool {
	if value == "" || len(value) > maxConditionalPolicyIdentifier {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && (char == '-' || char == '_') {
			continue
		}
		return false
	}
	return true
}

func normalizePolicyConditionGroup(group PolicyConditionGroup, depth int, count *int) (PolicyConditionGroup, error) {
	if depth > maxPolicyConditionDepth {
		return PolicyConditionGroup{}, fmt.Errorf("condition nesting exceeds %d levels", maxPolicyConditionDepth)
	}
	group.Operator = strings.ToLower(strings.TrimSpace(group.Operator))
	if group.Operator == "" {
		group.Operator = PolicyConditionAll
	}
	if group.Operator != PolicyConditionAll && group.Operator != PolicyConditionAny {
		return PolicyConditionGroup{}, fmt.Errorf("condition operator must be all or any")
	}
	conditions := make([]PolicyCondition, 0, len(group.Conditions))
	for _, condition := range group.Conditions {
		(*count)++
		if *count > maxPolicyConditionsPerRule {
			return PolicyConditionGroup{}, fmt.Errorf("condition count exceeds %d", maxPolicyConditionsPerRule)
		}
		normalized, errCondition := normalizePolicyCondition(condition)
		if errCondition != nil {
			return PolicyConditionGroup{}, errCondition
		}
		conditions = append(conditions, normalized)
	}
	groups := make([]PolicyConditionGroup, 0, len(group.Groups))
	for _, child := range group.Groups {
		normalized, errGroup := normalizePolicyConditionGroup(child, depth+1, count)
		if errGroup != nil {
			return PolicyConditionGroup{}, errGroup
		}
		if len(normalized.Conditions)+len(normalized.Groups) == 0 {
			return PolicyConditionGroup{}, fmt.Errorf("condition group cannot be empty")
		}
		groups = append(groups, normalized)
	}
	return PolicyConditionGroup{Operator: group.Operator, Conditions: conditions, Groups: groups}, nil
}

func normalizePolicyCondition(condition PolicyCondition) (PolicyCondition, error) {
	condition.Field = strings.ToLower(strings.TrimSpace(condition.Field))
	condition.Value = strings.ToLower(strings.TrimSpace(condition.Value))
	if condition.Value == "" || len(condition.Value) > maxPolicyConditionValue || hasUnsafeControl(condition.Value, true) {
		return PolicyCondition{}, fmt.Errorf("condition value is invalid")
	}
	switch condition.Field {
	case PolicyConditionProvider:
		condition.Value = deduplicationProviderFamily(condition.Value)
		if condition.Value == "" || !validPolicyMatchValue(condition.Value) {
			return PolicyCondition{}, fmt.Errorf("provider condition is invalid")
		}
	case PolicyConditionAccountType:
		condition.Value = safeAccountPlanType(condition.Value)
		if condition.Value == "" {
			return PolicyCondition{}, fmt.Errorf("account type condition is invalid")
		}
	case PolicyConditionEmailSuffix:
		condition.Value = strings.TrimPrefix(condition.Value, "@")
		condition.Value = strings.TrimPrefix(condition.Value, ".")
		if !validPolicyEmailSuffix(condition.Value) {
			return PolicyCondition{}, fmt.Errorf("email suffix condition is invalid")
		}
	default:
		return PolicyCondition{}, fmt.Errorf("condition field must be provider, account_type, or email_suffix")
	}
	return condition, nil
}

func validPolicyMatchValue(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validPolicyEmailSuffix(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validateConditionalPolicyActions(actions ConditionalPolicyActions) (ConditionalPolicyActions, error) {
	patch := BatchPatch{Disabled: actions.Disabled, Priority: actions.Priority, Note: actions.Note, Prefix: actions.Prefix, ProxyURL: actions.ProxyURL, ProxyProfileID: actions.ProxyProfileID, Websockets: actions.Websockets, Headers: actions.Headers, ModelPolicy: actions.ModelPolicy, ConcurrencyLimit: actions.ConcurrencyLimit, Concurrency15sLimit: actions.Concurrency15sLimit, ConcurrencyWindowSeconds: actions.ConcurrencyWindowSeconds, QuotaPolicy: actions.QuotaPolicy, CodexIdentity: actions.CodexIdentity}
	if !patch.Empty() {
		validated, errValidate := patch.Validate()
		if errValidate != nil {
			return ConditionalPolicyActions{}, errValidate
		}
		actions.Disabled, actions.Priority, actions.Note, actions.Prefix, actions.ProxyURL = validated.Disabled, validated.Priority, validated.Note, validated.Prefix, validated.ProxyURL
		actions.ProxyProfileID, actions.Websockets, actions.Headers, actions.ModelPolicy = validated.ProxyProfileID, validated.Websockets, validated.Headers, validated.ModelPolicy
		actions.ConcurrencyLimit, actions.Concurrency15sLimit, actions.ConcurrencyWindowSeconds, actions.QuotaPolicy, actions.CodexIdentity = validated.ConcurrencyLimit, validated.Concurrency15sLimit, validated.ConcurrencyWindowSeconds, validated.QuotaPolicy, validated.CodexIdentity
	}
	if actions.NewAccountModelProbe == nil && patch.Empty() {
		return ConditionalPolicyActions{}, fmt.Errorf("conditional policy requires at least one action")
	}
	for _, id := range []*string{actions.ProxyProfileID, actions.AIProviderProxyProfileID} {
		if id != nil && !validConditionalPolicyIdentifier(strings.ToLower(strings.TrimSpace(*id))) {
			return ConditionalPolicyActions{}, fmt.Errorf("proxy profile id is invalid")
		}
	}
	if actions.ProxyProfileID != nil {
		id := strings.ToLower(strings.TrimSpace(*actions.ProxyProfileID))
		actions.ProxyProfileID = &id
	}
	if actions.AIProviderProxyProfileID != nil {
		id := strings.ToLower(strings.TrimSpace(*actions.AIProviderProxyProfileID))
		actions.AIProviderProxyProfileID = &id
	}
	return cloneConditionalPolicyActions(actions), nil
}

func conditionalPolicyGroupMatches(group PolicyConditionGroup, account Account) bool {
	results := make([]bool, 0, len(group.Conditions)+len(group.Groups))
	for _, condition := range group.Conditions {
		results = append(results, conditionalPolicyConditionMatches(condition, account))
	}
	for _, child := range group.Groups {
		results = append(results, conditionalPolicyGroupMatches(child, account))
	}
	if len(results) == 0 {
		return false
	}
	if group.Operator == PolicyConditionAny {
		for _, result := range results {
			if result {
				return true
			}
		}
		return false
	}
	for _, result := range results {
		if !result {
			return false
		}
	}
	return true
}

func conditionalPolicyConditionMatches(condition PolicyCondition, account Account) bool {
	switch condition.Field {
	case PolicyConditionProvider:
		return deduplicationProviderFamily(firstNonEmpty(account.Provider, account.Type)) == condition.Value
	case PolicyConditionAccountType:
		for _, value := range []string{account.PlanType, account.AccountType, account.Type} {
			if strings.EqualFold(strings.TrimSpace(value), condition.Value) {
				return true
			}
		}
	case PolicyConditionEmailSuffix:
		email := normalizeDeduplicationEmail(account.Email)
		at := strings.LastIndexByte(email, '@')
		if at < 0 || at == len(email)-1 {
			return false
		}
		domain := email[at+1:]
		return domain == condition.Value || strings.HasSuffix(domain, "."+condition.Value)
	}
	return false
}

func resolveConditionalPolicy(policy DefaultPolicy, account Account) ResolvedConditionalPolicy {
	resolved := ResolvedConditionalPolicy{NewAccountModelProbe: conditionalBoolPointer(policy.NewAccountModelProbeEnabled)}
	if policy.Enabled {
		resolved.Priority = cloneIntPointer(policy.Priority)
		resolved.Disabled = cloneBoolPointer(policy.Disabled)
		resolved.ConcurrencyLimit = cloneIntPointer(policy.ConcurrencyLimit)
		resolved.Concurrency15sLimit = cloneIntPointer(policy.Concurrency15sLimit)
		resolved.ConcurrencyWindowSeconds = cloneIntPointer(policy.ConcurrencyWindowSeconds)
		if policy.QuotaPolicy != nil {
			value := *policy.QuotaPolicy
			resolved.QuotaPolicy = &value
		}
		resolved.Note = cloneStringPointer(policy.Note)
		resolved.Prefix = cloneStringPointer(policy.Prefix)
		resolved.ProxyURL = cloneStringPointer(policy.ProxyURL)
		resolved.Websockets = cloneBoolPointer(policy.Websockets)
		if policy.Headers != nil {
			value := cloneHeaderPatch(*policy.Headers)
			resolved.Headers = &value
		}
		if policy.CodexIdentity != nil {
			value := cloneCodexIdentityOverride(*policy.CodexIdentity)
			resolved.CodexIdentity = &value
		}
		if policy.ModelPolicy != nil {
			value := cloneModelPolicyPatch(*policy.ModelPolicy)
			resolved.ModelPolicy = &value
		}
	}
	type indexedRule struct {
		index int
		rule  ConditionalPolicyRule
	}
	matching := make([]indexedRule, 0, len(policy.ConditionalRules))
	for index, rule := range policy.ConditionalRules {
		if rule.Enabled && conditionalPolicyGroupMatches(rule.Conditions, account) {
			matching = append(matching, indexedRule{index: index, rule: rule})
		}
	}
	sort.SliceStable(matching, func(left, right int) bool {
		if matching[left].rule.Priority == matching[right].rule.Priority {
			return matching[left].index < matching[right].index
		}
		return matching[left].rule.Priority < matching[right].rule.Priority
	})
	for _, match := range matching {
		actions := match.rule.Actions
		if actions.NewAccountModelProbe != nil {
			resolved.NewAccountModelProbe = cloneBoolPointer(actions.NewAccountModelProbe)
		}
		if actions.Priority != nil {
			resolved.Priority = cloneIntPointer(actions.Priority)
			resolved.PriorityFromRule = true
		}
		if actions.Disabled != nil {
			resolved.Disabled = cloneBoolPointer(actions.Disabled)
			resolved.DisabledFromRule = true
		}
		if actions.ConcurrencyLimit != nil {
			resolved.ConcurrencyLimit = cloneIntPointer(actions.ConcurrencyLimit)
			resolved.ConcurrencyFromRule = true
		}
		if actions.Concurrency15sLimit != nil {
			resolved.Concurrency15sLimit = cloneIntPointer(actions.Concurrency15sLimit)
			resolved.ConcurrencyFromRule = true
		}
		if actions.ConcurrencyWindowSeconds != nil {
			resolved.ConcurrencyWindowSeconds = cloneIntPointer(actions.ConcurrencyWindowSeconds)
			resolved.ConcurrencyFromRule = true
		}
		if actions.QuotaPolicy != nil {
			value := *actions.QuotaPolicy
			resolved.QuotaPolicy = &value
			resolved.QuotaPolicyFromRule = true
		}
		if actions.Note != nil {
			resolved.Note = cloneStringPointer(actions.Note)
			resolved.NoteFromRule = true
		}
		if actions.Prefix != nil {
			resolved.Prefix = cloneStringPointer(actions.Prefix)
			resolved.PrefixFromRule = true
		}
		if actions.ProxyURL != nil {
			resolved.ProxyURL = cloneStringPointer(actions.ProxyURL)
			resolved.ProxyURLFromRule = true
		}
		if actions.Websockets != nil {
			resolved.Websockets = cloneBoolPointer(actions.Websockets)
			resolved.WebsocketsFromRule = true
		}
		if actions.Headers != nil {
			value := cloneHeaderPatch(*actions.Headers)
			resolved.Headers = &value
			resolved.HeadersFromRule = true
		}
		if actions.ModelPolicy != nil {
			modelPolicy := cloneModelPolicyPatch(*actions.ModelPolicy)
			resolved.ModelPolicy = &modelPolicy
			resolved.ModelPolicyFromRule = true
		}
		if actions.CodexIdentity != nil {
			value := cloneCodexIdentityOverride(*actions.CodexIdentity)
			resolved.CodexIdentity = &value
			resolved.CodexIdentityFromRule = true
		}
		if actions.ProxyProfileID != nil {
			id := strings.ToLower(strings.TrimSpace(*actions.ProxyProfileID))
			resolved.ProxyProfileID = &id
			resolved.ProxyProfileFromRule = true
		}
		if actions.AIProviderProxyProfileID != nil {
			id := strings.ToLower(strings.TrimSpace(*actions.AIProviderProxyProfileID))
			resolved.AIProviderProxyProfileID = &id
			resolved.AIProviderProxyFromRule = true
		}
		resolved.MatchedRuleIDs = append(resolved.MatchedRuleIDs, match.rule.ID)
	}
	return resolved
}

func cloneConditionalPolicyRules(rules []ConditionalPolicyRule) []ConditionalPolicyRule {
	cloned := make([]ConditionalPolicyRule, len(rules))
	for index, rule := range rules {
		cloned[index] = rule
		cloned[index].Conditions = clonePolicyConditionGroup(rule.Conditions)
		cloned[index].Actions = cloneConditionalPolicyActions(rule.Actions)
	}
	return cloned
}

func clonePolicyConditionGroup(group PolicyConditionGroup) PolicyConditionGroup {
	clone := group
	clone.Conditions = append([]PolicyCondition(nil), group.Conditions...)
	clone.Groups = make([]PolicyConditionGroup, len(group.Groups))
	for index := range group.Groups {
		clone.Groups[index] = clonePolicyConditionGroup(group.Groups[index])
	}
	return clone
}

func cloneConditionalPolicyActions(actions ConditionalPolicyActions) ConditionalPolicyActions {
	clone := actions
	clone.NewAccountModelProbe = cloneBoolPointer(actions.NewAccountModelProbe)
	clone.Priority = cloneIntPointer(actions.Priority)
	clone.Disabled = cloneBoolPointer(actions.Disabled)
	clone.ConcurrencyLimit = cloneIntPointer(actions.ConcurrencyLimit)
	clone.Concurrency15sLimit = cloneIntPointer(actions.Concurrency15sLimit)
	clone.ConcurrencyWindowSeconds = cloneIntPointer(actions.ConcurrencyWindowSeconds)
	clone.Note = cloneStringPointer(actions.Note)
	clone.Prefix = cloneStringPointer(actions.Prefix)
	clone.ProxyURL = cloneStringPointer(actions.ProxyURL)
	clone.Websockets = cloneBoolPointer(actions.Websockets)
	if actions.Headers != nil {
		headers := HeaderPatch{Set: map[string]string{}, Remove: append([]string(nil), actions.Headers.Remove...)}
		for key, value := range actions.Headers.Set {
			headers.Set[key] = value
		}
		clone.Headers = &headers
	}
	if actions.QuotaPolicy != nil {
		value := *actions.QuotaPolicy
		clone.QuotaPolicy = &value
	}
	if actions.CodexIdentity != nil {
		value := cloneCodexIdentityOverride(*actions.CodexIdentity)
		clone.CodexIdentity = &value
	}
	if actions.ProxyProfileID != nil {
		id := *actions.ProxyProfileID
		clone.ProxyProfileID = &id
	}
	if actions.AIProviderProxyProfileID != nil {
		id := *actions.AIProviderProxyProfileID
		clone.AIProviderProxyProfileID = &id
	}
	if actions.ModelPolicy != nil {
		modelPolicy := cloneModelPolicyPatch(*actions.ModelPolicy)
		clone.ModelPolicy = &modelPolicy
	}
	return clone
}

func cloneHeaderPatch(patch HeaderPatch) HeaderPatch {
	clone := HeaderPatch{Set: map[string]string{}, Remove: append([]string(nil), patch.Remove...)}
	for key, value := range patch.Set {
		clone.Set[key] = value
	}
	return clone
}

func cloneModelPolicyPatch(patch ModelPolicyPatch) ModelPolicyPatch {
	patch.Models = append([]string(nil), patch.Models...)
	return patch
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func conditionalBoolPointer(value bool) *bool {
	return &value
}
