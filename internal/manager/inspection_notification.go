package manager

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxAnomalyNotificationURLBytes     = 4096
	maxInspectionNotificationEndpoints = 20
	maxInspectionNotificationPolicies  = 100
	maxInspectionNotificationNameBytes = 80
	anomalyNotificationTimeout         = 10 * time.Second
	anomalyNotificationAttempts        = 3
	maxAnomalyNotificationResponse     = 4 << 10
)

var anomalyNotificationVariablePattern = regexp.MustCompile(`\$\{([a-z_]+)\}`)

var blockedAnomalyNotificationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

var anomalyNotificationVariables = map[string]struct{}{
	"event": {}, "total_accounts": {}, "eligible_accounts": {}, "available_accounts": {},
	"available_percent": {}, "abnormal_accounts": {}, "abnormal_percent": {}, "quota_limited_accounts": {},
	"invalid_credential_accounts": {}, "deactivated_accounts": {}, "unavailable_accounts": {},
	"disabled_accounts": {}, "threshold_percent": {}, "available_accounts_threshold": {},
	"availability_percent_threshold": {}, "triggered_at": {},
	"notification_policy_id": {}, "notification_policy_name": {},
}

const (
	InspectionNotificationScenarioManualTest       = "manual_test"
	InspectionNotificationScenarioAnomalyThreshold = "anomaly_threshold"
	InspectionNotificationScenarioAvailableLow     = "available_accounts_low"
	InspectionNotificationScenarioAvailabilityLow  = "availability_percent_low"
	InspectionNotificationScenarioCombined         = "combined"
)

type anomalyNotificationMetrics struct {
	TotalAccounts              int
	EligibleAccounts           int
	AvailableAccounts          int
	AvailablePercent           int
	AvailabilitySamples        int
	AbnormalAccounts           int
	AbnormalPercent            int
	QuotaLimitedAccounts       int
	InvalidCredentialAccounts  int
	DeactivatedAccounts        int
	UnavailableAccounts        int
	DisabledAccounts           int
	ThresholdPercent           int
	AvailableAccountsThreshold int
	AvailabilityThreshold      int
}

type anomalyNotificationEvent struct {
	EndpointID   string
	EndpointName string
	URLTemplate  string
	Event        string
	Metrics      anomalyNotificationMetrics
	TriggeredAt  time.Time
	PolicyID     string
	PolicyName   string
}

var inspectionNotificationEndpointIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func normalizeInspectionNotificationEndpoints(endpoints []InspectionNotificationEndpoint, legacyURL string) []InspectionNotificationEndpoint {
	if len(endpoints) == 0 && strings.TrimSpace(legacyURL) != "" {
		endpoints = []InspectionNotificationEndpoint{{ID: "legacy", URL: legacyURL, Enabled: true}}
	}
	normalized := make([]InspectionNotificationEndpoint, 0, len(endpoints))
	for index, endpoint := range endpoints {
		endpoint.ID = strings.TrimSpace(endpoint.ID)
		if endpoint.ID == "" {
			endpoint.ID = fmt.Sprintf("notification-%d", index+1)
		}
		endpoint.Name = strings.TrimSpace(endpoint.Name)
		endpoint.URL = strings.TrimSpace(endpoint.URL)
		endpoint.NotificationPolicyID = strings.ToLower(strings.TrimSpace(endpoint.NotificationPolicyID))
		normalized = append(normalized, endpoint)
	}
	return normalized
}

func normalizeInspectionNotificationPolicies(policies []InspectionNotificationPolicy) []InspectionNotificationPolicy {
	normalized := make([]InspectionNotificationPolicy, len(policies))
	for index, policy := range policies {
		policy.ID = strings.ToLower(strings.TrimSpace(policy.ID))
		if policy.ID == "" {
			policy.ID = fmt.Sprintf("notification-policy-%d", index+1)
		}
		policy.Name = strings.TrimSpace(policy.Name)
		policy.ThresholdOperator = strings.ToLower(strings.TrimSpace(policy.ThresholdOperator))
		if policy.ThresholdOperator == "" {
			policy.ThresholdOperator = PolicyConditionAll
		}
		if policy.AvailableAccountsBelow == 0 {
			policy.AvailableAccountsBelow = defaultAvailableThreshold
		}
		if policy.AvailabilityPercentBelow == 0 {
			policy.AvailabilityPercentBelow = defaultAvailabilityPercent
		}
		policy.Conditions = clonePolicyConditionGroup(policy.Conditions)
		normalized[index] = policy
	}
	return normalized
}

func cloneInspectionNotificationPolicies(policies []InspectionNotificationPolicy) []InspectionNotificationPolicy {
	return normalizeInspectionNotificationPolicies(policies)
}

func validateInspectionNotificationPolicies(policies []InspectionNotificationPolicy) ([]InspectionNotificationPolicy, error) {
	policies = normalizeInspectionNotificationPolicies(policies)
	if len(policies) > maxInspectionNotificationPolicies {
		return nil, fmt.Errorf("notification_policies must contain at most %d entries", maxInspectionNotificationPolicies)
	}
	ids := make(map[string]struct{}, len(policies))
	for index := range policies {
		policy := &policies[index]
		if !validConditionalPolicyIdentifier(policy.ID) {
			return nil, fmt.Errorf("notification policy id is invalid")
		}
		if _, duplicate := ids[policy.ID]; duplicate {
			return nil, fmt.Errorf("notification policy ids must be unique")
		}
		ids[policy.ID] = struct{}{}
		if policy.Name == "" || len(policy.Name) > maxConditionalPolicyName || hasUnsafeControl(policy.Name, true) {
			return nil, fmt.Errorf("notification policy %s name is invalid", policy.ID)
		}
		conditionCount := 0
		conditions, errConditions := normalizePolicyConditionGroup(policy.Conditions, 1, &conditionCount)
		if errConditions != nil {
			return nil, fmt.Errorf("notification policy %s: %w", policy.ID, errConditions)
		}
		if conditionCount == 0 {
			return nil, fmt.Errorf("notification policy %s requires at least one account condition", policy.ID)
		}
		policy.Conditions = conditions
		if policy.ThresholdOperator != PolicyConditionAll && policy.ThresholdOperator != PolicyConditionAny {
			return nil, fmt.Errorf("notification policy %s threshold_operator must be all or any", policy.ID)
		}
		if !policy.AvailableAccountsEnabled && !policy.AvailabilityPercentEnabled {
			return nil, fmt.Errorf("notification policy %s requires at least one threshold", policy.ID)
		}
		if policy.AvailableAccountsBelow < 1 || policy.AvailableAccountsBelow > maxInspectionAccounts {
			return nil, fmt.Errorf("notification policy %s available_accounts_below must be between 1 and %d", policy.ID, maxInspectionAccounts)
		}
		if policy.AvailabilityPercentBelow < 1 || policy.AvailabilityPercentBelow > 100 {
			return nil, fmt.Errorf("notification policy %s availability_percent_below must be between 1 and 100", policy.ID)
		}
	}
	return policies, nil
}

func inspectionNotificationPolicyMap(policies []InspectionNotificationPolicy) map[string]InspectionNotificationPolicy {
	indexed := make(map[string]InspectionNotificationPolicy, len(policies))
	for _, policy := range policies {
		indexed[policy.ID] = policy
	}
	return indexed
}

func hasEnabledInspectionNotificationPolicy(policies []InspectionNotificationPolicy) bool {
	for _, policy := range policies {
		if policy.Enabled {
			return true
		}
	}
	return false
}

func validateInspectionNotificationEndpoints(endpoints []InspectionNotificationEndpoint) (int, error) {
	if len(endpoints) > maxInspectionNotificationEndpoints {
		return 0, fmt.Errorf("notification_endpoints must contain at most %d entries", maxInspectionNotificationEndpoints)
	}
	ids := make(map[string]struct{}, len(endpoints))
	urls := make(map[string]struct{}, len(endpoints))
	enabled := 0
	for _, endpoint := range endpoints {
		if !inspectionNotificationEndpointIDPattern.MatchString(endpoint.ID) {
			return 0, fmt.Errorf("notification endpoint id is invalid")
		}
		if _, duplicate := ids[endpoint.ID]; duplicate {
			return 0, fmt.Errorf("notification endpoint id %q is duplicated", endpoint.ID)
		}
		ids[endpoint.ID] = struct{}{}
		if endpoint.NotificationPolicyID != "" && !validConditionalPolicyIdentifier(endpoint.NotificationPolicyID) {
			return 0, fmt.Errorf("notification endpoint %q policy id is invalid", endpoint.ID)
		}
		if len(endpoint.Name) > maxInspectionNotificationNameBytes || strings.IndexFunc(endpoint.Name, func(character rune) bool {
			return character < 0x20 || character == 0x7f
		}) >= 0 {
			return 0, fmt.Errorf("notification endpoint name is invalid")
		}
		if _, duplicate := urls[endpoint.URL]; duplicate {
			return 0, fmt.Errorf("notification endpoint URL is duplicated")
		}
		urls[endpoint.URL] = struct{}{}
		if errTemplate := validateAnomalyNotificationTemplate(endpoint.URL); errTemplate != nil {
			return 0, fmt.Errorf("notification endpoint %q: %w", endpoint.ID, errTemplate)
		}
		if endpoint.Enabled {
			enabled++
		}
	}
	return enabled, nil
}

func inspectionNotificationEndpointsEqual(left, right []InspectionNotificationEndpoint) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type anomalyNotificationResult struct {
	StatusCode int
	Attempts   int
	ReasonCode string
}

func validateAnomalyNotificationTemplate(template string) error {
	template = strings.TrimSpace(template)
	if template == "" {
		return fmt.Errorf("anomaly_notification_url is required when notifications are enabled")
	}
	if len(template) > maxAnomalyNotificationURLBytes {
		return fmt.Errorf("anomaly_notification_url exceeds %d bytes", maxAnomalyNotificationURLBytes)
	}
	for _, character := range template {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("anomaly_notification_url contains control characters")
		}
	}
	queryStart := strings.IndexByte(template, '?')
	matches := anomalyNotificationVariablePattern.FindAllStringSubmatchIndex(template, -1)
	for _, match := range matches {
		if queryStart < 0 || match[0] <= queryStart {
			return fmt.Errorf("anomaly notification variables are only allowed in URL query parameters")
		}
		name := template[match[2]:match[3]]
		if _, allowed := anomalyNotificationVariables[name]; !allowed {
			return fmt.Errorf("unsupported anomaly notification variable %s", name)
		}
	}
	withoutKnownVariables := anomalyNotificationVariablePattern.ReplaceAllString(template, "value")
	if strings.Contains(withoutKnownVariables, "${") {
		return fmt.Errorf("anomaly_notification_url contains an invalid variable")
	}
	parsed, errParse := url.Parse(withoutKnownVariables)
	if errParse != nil {
		return fmt.Errorf("anomaly_notification_url is invalid")
	}
	return validateAnomalyNotificationDestination(parsed)
}

func validateAnomalyNotificationDestination(parsed *url.URL) error {
	if parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("anomaly_notification_url must use HTTPS with a valid host")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("anomaly_notification_url must not contain user credentials or a fragment")
	}
	if port := parsed.Port(); port != "" {
		value, errPort := strconv.Atoi(port)
		if errPort != nil || value < 1 || value > 65535 {
			return fmt.Errorf("anomaly_notification_url contains an invalid port")
		}
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return fmt.Errorf("anomaly_notification_url must target a public host")
	}
	if address := net.ParseIP(hostname); address != nil && !publicNotificationIP(address) {
		return fmt.Errorf("anomaly_notification_url must target a public host")
	}
	return nil
}

func expandAnomalyNotificationURL(event anomalyNotificationEvent) (string, error) {
	if errValidate := validateAnomalyNotificationTemplate(event.URLTemplate); errValidate != nil {
		return "", errValidate
	}
	values := anomalyNotificationVariableValues(event)
	expanded := anomalyNotificationVariablePattern.ReplaceAllStringFunc(event.URLTemplate, func(variable string) string {
		match := anomalyNotificationVariablePattern.FindStringSubmatch(variable)
		return url.QueryEscape(values[match[1]])
	})
	if len(expanded) > maxAnomalyNotificationURLBytes {
		return "", fmt.Errorf("expanded anomaly notification URL exceeds %d bytes", maxAnomalyNotificationURLBytes)
	}
	parsed, errParse := url.Parse(expanded)
	if errParse != nil {
		return "", fmt.Errorf("expanded anomaly notification URL is invalid")
	}
	parsed.RawQuery = escapeUnsafeNotificationQuery(parsed.RawQuery)
	if errValidate := validateAnomalyNotificationDestination(parsed); errValidate != nil {
		return "", errValidate
	}
	return parsed.String(), nil
}

func escapeUnsafeNotificationQuery(query string) string {
	var escaped strings.Builder
	escaped.Grow(len(query))
	for index := 0; index < len(query); index++ {
		character := query[index]
		if character == '%' && index+2 < len(query) && isHexDigit(query[index+1]) && isHexDigit(query[index+2]) {
			escaped.WriteString(query[index : index+3])
			index += 2
			continue
		}
		if isNotificationQueryCharacter(character) {
			escaped.WriteByte(character)
			continue
		}
		escaped.WriteByte('%')
		escaped.WriteByte("0123456789ABCDEF"[character>>4])
		escaped.WriteByte("0123456789ABCDEF"[character&0x0f])
	}
	return escaped.String()
}

func isHexDigit(character byte) bool {
	return character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F'
}

func isNotificationQueryCharacter(character byte) bool {
	if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
		return true
	}
	return strings.ContainsRune("-._~!$&'()*+,;=:@/?", rune(character))
}

func anomalyNotificationVariableValues(event anomalyNotificationEvent) map[string]string {
	return map[string]string{
		"event":                          event.Event,
		"total_accounts":                 strconv.Itoa(event.Metrics.TotalAccounts),
		"eligible_accounts":              strconv.Itoa(event.Metrics.EligibleAccounts),
		"available_accounts":             strconv.Itoa(event.Metrics.AvailableAccounts),
		"available_percent":              notificationPercentValue(event.Metrics.AvailablePercent),
		"abnormal_accounts":              strconv.Itoa(event.Metrics.AbnormalAccounts),
		"abnormal_percent":               notificationPercentValue(event.Metrics.AbnormalPercent),
		"quota_limited_accounts":         strconv.Itoa(event.Metrics.QuotaLimitedAccounts),
		"invalid_credential_accounts":    strconv.Itoa(event.Metrics.InvalidCredentialAccounts),
		"deactivated_accounts":           strconv.Itoa(event.Metrics.DeactivatedAccounts),
		"unavailable_accounts":           strconv.Itoa(event.Metrics.UnavailableAccounts),
		"disabled_accounts":              strconv.Itoa(event.Metrics.DisabledAccounts),
		"threshold_percent":              notificationPercentValue(event.Metrics.ThresholdPercent),
		"available_accounts_threshold":   strconv.Itoa(event.Metrics.AvailableAccountsThreshold),
		"availability_percent_threshold": strconv.Itoa(event.Metrics.AvailabilityThreshold),
		"triggered_at":                   event.TriggeredAt.UTC().Format(time.RFC3339),
		"notification_policy_id":         event.PolicyID,
		"notification_policy_name":       event.PolicyName,
	}
}

func notificationPercentValue(value int) string {
	return strconv.Itoa(value) + "%"
}

func inspectionNotificationEnabled(policy InspectionPolicy) bool {
	policies := inspectionNotificationPolicyMap(policy.NotificationPolicies)
	for _, endpoint := range normalizeInspectionNotificationEndpoints(policy.NotificationEndpoints, policy.AnomalyNotificationURL) {
		if !endpoint.Enabled {
			continue
		}
		if endpoint.NotificationPolicyID == "" && (policy.AnomalyNotificationEnabled || policy.NotificationAvailableEnabled || policy.NotificationPercentEnabled) {
			return true
		}
		if notificationPolicy, exists := policies[endpoint.NotificationPolicyID]; exists && notificationPolicy.Enabled {
			return true
		}
	}
	return false
}

func inspectionNotificationPolicyChanged(previous, next InspectionPolicy) bool {
	return previous.AnomalyNotificationEnabled != next.AnomalyNotificationEnabled ||
		previous.AnomalyNotificationURL != next.AnomalyNotificationURL ||
		!inspectionNotificationEndpointsEqual(previous.NotificationEndpoints, next.NotificationEndpoints) ||
		!reflect.DeepEqual(previous.NotificationPolicies, next.NotificationPolicies) ||
		previous.AnomalyThresholdPercent != next.AnomalyThresholdPercent ||
		previous.AnomalyMinimumAccounts != next.AnomalyMinimumAccounts ||
		previous.NotificationAvailableEnabled != next.NotificationAvailableEnabled ||
		previous.NotificationAvailableBelow != next.NotificationAvailableBelow ||
		previous.NotificationPercentEnabled != next.NotificationPercentEnabled ||
		previous.NotificationPercentBelow != next.NotificationPercentBelow ||
		previous.NotificationCooldownMinutes != next.NotificationCooldownMinutes
}

func normalizeInspectionNotificationScenario(value string) (string, string, error) {
	scenario := strings.ToLower(strings.TrimSpace(value))
	switch scenario {
	case InspectionNotificationScenarioManualTest:
		return scenario, InspectionNotificationScenarioManualTest, nil
	case InspectionNotificationScenarioAnomalyThreshold:
		return scenario, InspectionNotificationScenarioAnomalyThreshold, nil
	case InspectionNotificationScenarioAvailableLow:
		return scenario, InspectionNotificationScenarioAvailableLow, nil
	case InspectionNotificationScenarioAvailabilityLow:
		return scenario, InspectionNotificationScenarioAvailabilityLow, nil
	case InspectionNotificationScenarioCombined:
		return scenario, strings.Join([]string{
			InspectionNotificationScenarioAnomalyThreshold,
			InspectionNotificationScenarioAvailableLow,
			InspectionNotificationScenarioAvailabilityLow,
		}, ","), nil
	default:
		return "", "", fmt.Errorf("unsupported notification scenario")
	}
}

func validateInspectionNotificationRequest(request InspectionNotificationRequest) (InspectionNotificationRequest, string, error) {
	request.EndpointID = strings.TrimSpace(request.EndpointID)
	request.EndpointName = strings.TrimSpace(request.EndpointName)
	request.URLTemplate = strings.TrimSpace(request.URLTemplate)
	request.NotificationPolicyID = strings.ToLower(strings.TrimSpace(request.NotificationPolicyID))
	if request.EndpointID != "" && !inspectionNotificationEndpointIDPattern.MatchString(request.EndpointID) {
		return InspectionNotificationRequest{}, "", fmt.Errorf("notification endpoint id is invalid")
	}
	if request.NotificationPolicyID != "" && !validConditionalPolicyIdentifier(request.NotificationPolicyID) {
		return InspectionNotificationRequest{}, "", fmt.Errorf("notification policy id is invalid")
	}
	if len(request.EndpointName) > maxInspectionNotificationNameBytes || strings.IndexFunc(request.EndpointName, func(character rune) bool {
		return character < 0x20 || character == 0x7f
	}) >= 0 {
		return InspectionNotificationRequest{}, "", fmt.Errorf("notification endpoint name is invalid")
	}
	scenario, event, errScenario := normalizeInspectionNotificationScenario(request.Scenario)
	if errScenario != nil {
		return InspectionNotificationRequest{}, "", errScenario
	}
	request.Scenario = scenario
	if request.ThresholdPercent < 1 || request.ThresholdPercent > 100 {
		return InspectionNotificationRequest{}, "", fmt.Errorf("threshold_percent must be between 1 and 100")
	}
	if request.AvailableAccountsThreshold < 1 || request.AvailableAccountsThreshold > maxInspectionAccounts {
		return InspectionNotificationRequest{}, "", fmt.Errorf("available_accounts_threshold must be between 1 and %d", maxInspectionAccounts)
	}
	if request.AvailabilityPercentThreshold < 1 || request.AvailabilityPercentThreshold > 100 {
		return InspectionNotificationRequest{}, "", fmt.Errorf("availability_percent_threshold must be between 1 and 100")
	}
	if errValidate := validateAnomalyNotificationTemplate(request.URLTemplate); errValidate != nil {
		return InspectionNotificationRequest{}, "", errValidate
	}
	return request, event, nil
}

func (e *InspectionEngine) PreviewNotification(ctx context.Context, request InspectionNotificationRequest) (InspectionNotificationPreview, error) {
	if e == nil || e.accounts == nil {
		return InspectionNotificationPreview{}, fmt.Errorf("inspection engine is unavailable")
	}
	request, eventName, errValidate := validateInspectionNotificationRequest(request)
	if errValidate != nil {
		return InspectionNotificationPreview{}, errValidate
	}
	accounts, errAccounts := e.accounts.baseAccounts(ctx)
	if errAccounts != nil {
		return InspectionNotificationPreview{}, fmt.Errorf("list accounts for notification preview: %w", errAccounts)
	}
	if len(accounts) > maxInspectionAccounts {
		accounts = accounts[:maxInspectionAccounts]
	}
	accountsByID := make(map[string]Account, len(accounts))
	for _, account := range accounts {
		if id := strings.TrimSpace(account.ID); id != "" {
			accountsByID[id] = account
		}
	}
	e.mu.RLock()
	records := cloneInspectionRecords(e.records)
	policy := e.policy
	e.mu.RUnlock()
	policyName := ""
	if request.NotificationPolicyID != "" {
		notificationPolicy, exists := inspectionNotificationPolicyMap(policy.NotificationPolicies)[request.NotificationPolicyID]
		if !exists {
			return InspectionNotificationPreview{}, fmt.Errorf("notification policy is unavailable")
		}
		accountsByID = inspectionNotificationCohort(accountsByID, notificationPolicy.Conditions)
		request.AvailableAccountsThreshold = notificationPolicy.AvailableAccountsBelow
		request.AvailabilityPercentThreshold = notificationPolicy.AvailabilityPercentBelow
		policyName = notificationPolicy.Name
		eventName += ":" + notificationPolicy.ID
	}
	metrics := inspectionAnomalyNotificationMetrics(accountsByID, records)
	metrics.ThresholdPercent = request.ThresholdPercent
	metrics.AvailableAccountsThreshold = request.AvailableAccountsThreshold
	metrics.AvailabilityThreshold = request.AvailabilityPercentThreshold
	triggeredAt := e.currentTime()
	event := anomalyNotificationEvent{
		EndpointID:   request.EndpointID,
		EndpointName: request.EndpointName,
		URLTemplate:  request.URLTemplate,
		Event:        eventName,
		Metrics:      metrics,
		TriggeredAt:  triggeredAt,
		PolicyID:     request.NotificationPolicyID,
		PolicyName:   policyName,
	}
	expanded, errExpand := expandAnomalyNotificationURL(event)
	if errExpand != nil {
		return InspectionNotificationPreview{}, errExpand
	}
	return InspectionNotificationPreview{
		EndpointID: request.EndpointID, EndpointName: request.EndpointName,
		Scenario: request.Scenario, Event: eventName, ExpandedURL: expanded,
		Variables: anomalyNotificationVariableValues(event), TriggeredAt: triggeredAt,
	}, nil
}

func (e *InspectionEngine) TestNotification(ctx context.Context, request InspectionNotificationRequest) (InspectionNotificationTestResult, error) {
	preview, errPreview := e.PreviewNotification(ctx, request)
	if errPreview != nil {
		return InspectionNotificationTestResult{}, errPreview
	}
	event := anomalyNotificationEvent{
		EndpointID:   request.EndpointID,
		EndpointName: request.EndpointName,
		URLTemplate:  request.URLTemplate,
		Event:        preview.Event,
		Metrics: anomalyNotificationMetrics{
			TotalAccounts:              parseNotificationMetric(preview.Variables, "total_accounts"),
			EligibleAccounts:           parseNotificationMetric(preview.Variables, "eligible_accounts"),
			AvailableAccounts:          parseNotificationMetric(preview.Variables, "available_accounts"),
			AvailablePercent:           parseNotificationMetric(preview.Variables, "available_percent"),
			AbnormalAccounts:           parseNotificationMetric(preview.Variables, "abnormal_accounts"),
			AbnormalPercent:            parseNotificationMetric(preview.Variables, "abnormal_percent"),
			QuotaLimitedAccounts:       parseNotificationMetric(preview.Variables, "quota_limited_accounts"),
			InvalidCredentialAccounts:  parseNotificationMetric(preview.Variables, "invalid_credential_accounts"),
			DeactivatedAccounts:        parseNotificationMetric(preview.Variables, "deactivated_accounts"),
			UnavailableAccounts:        parseNotificationMetric(preview.Variables, "unavailable_accounts"),
			DisabledAccounts:           parseNotificationMetric(preview.Variables, "disabled_accounts"),
			ThresholdPercent:           parseNotificationMetric(preview.Variables, "threshold_percent"),
			AvailableAccountsThreshold: parseNotificationMetric(preview.Variables, "available_accounts_threshold"),
			AvailabilityThreshold:      parseNotificationMetric(preview.Variables, "availability_percent_threshold"),
		},
		TriggeredAt: preview.TriggeredAt,
		PolicyID:    request.NotificationPolicyID,
		PolicyName:  preview.Variables["notification_policy_name"],
	}
	result := e.deliverAnomalyNotification(ctx, event)
	delivered := result.ReasonCode == "notification_delivered"
	e.recordNotificationTest(event, result)
	return InspectionNotificationTestResult{
		Preview: preview, Delivered: delivered, StatusCode: result.StatusCode,
		Attempts: result.Attempts, ReasonCode: result.ReasonCode,
	}, nil
}

func parseNotificationMetric(values map[string]string, key string) int {
	raw := strings.TrimSpace(values[key])
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "%"))
	value, _ := strconv.Atoi(raw)
	return value
}

func (e *InspectionEngine) recordNotificationTest(event anomalyNotificationEvent, result anomalyNotificationResult) {
	if e == nil {
		return
	}
	e.mu.RLock()
	journal := e.operations
	e.mu.RUnlock()
	if journal == nil {
		return
	}
	status := OperationStatusFailed
	succeeded, failed, skipped := 0, 1, 0
	switch result.ReasonCode {
	case "notification_delivered":
		status = OperationStatusSucceeded
		succeeded, failed = 1, 0
	case "notification_superseded":
		// A retired instance deliberately cancels its queued work. This is not
		// a delivery failure and should not inflate failure counts.
		status = OperationStatusSkipped
		failed, skipped = 0, 1
	}
	journal.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionNotificationTest,
		Status: status, Source: OperationSourceManual, Scope: OperationScopeSystem,
		TargetID: event.EndpointID, TargetCount: event.Metrics.TotalAccounts, Succeeded: succeeded, Failed: failed, Skipped: skipped,
		StartedAt: event.TriggeredAt, FinishedAt: e.currentTime(), ReasonCode: result.ReasonCode,
		RelatedActionID: event.PolicyID, HTTPStatus: result.StatusCode, Attempts: result.Attempts,
	})
}

func inspectionNotificationReasons(policy InspectionPolicy, metrics anomalyNotificationMetrics) []string {
	if metrics.TotalAccounts <= 0 {
		return nil
	}
	reasons := make([]string, 0, 3)
	if policy.AnomalyNotificationEnabled && inspectionAnomalyTriggered(
		metrics.EligibleAccounts, metrics.AbnormalAccounts, policy.AnomalyMinimumAccounts, policy.AnomalyThresholdPercent,
	) {
		reasons = append(reasons, "anomaly_threshold")
	}
	if policy.NotificationAvailableEnabled && metrics.AvailableAccounts < policy.NotificationAvailableBelow {
		reasons = append(reasons, "available_accounts_low")
	}
	if policy.NotificationPercentEnabled && availabilityPercentTriggerable(metrics) && metrics.AvailablePercent < policy.NotificationPercentBelow {
		reasons = append(reasons, "availability_percent_low")
	}
	return reasons
}

func inspectionNotificationPolicyReasons(policy InspectionNotificationPolicy, metrics anomalyNotificationMetrics) []string {
	results := make([]struct {
		reason  string
		matched bool
	}, 0, 2)
	if policy.AvailableAccountsEnabled {
		results = append(results, struct {
			reason  string
			matched bool
		}{"available_accounts_low", metrics.AvailableAccounts < policy.AvailableAccountsBelow})
	}
	if policy.AvailabilityPercentEnabled {
		results = append(results, struct {
			reason  string
			matched bool
		}{"availability_percent_low", availabilityPercentTriggerable(metrics) && metrics.AvailablePercent < policy.AvailabilityPercentBelow})
	}
	if len(results) == 0 {
		return nil
	}
	matched := policy.ThresholdOperator == PolicyConditionAll
	if policy.ThresholdOperator == PolicyConditionAny {
		matched = false
		for _, result := range results {
			matched = matched || result.matched
		}
	} else {
		for _, result := range results {
			matched = matched && result.matched
		}
	}
	if !matched {
		return nil
	}
	reasons := make([]string, 0, len(results))
	for _, result := range results {
		if result.matched {
			reasons = append(reasons, result.reason)
		}
	}
	return reasons
}

// availabilityPercentTriggerable distinguishes a real zero-availability
// state from an initial state where no account has reported quota data yet.
// Once the inspection has classified accounts and none of the enabled
// accounts has a usable quota sample, their effective availability is 0% and
// a low-availability notification must be allowed to fire.
func availabilityPercentTriggerable(metrics anomalyNotificationMetrics) bool {
	if metrics.AvailabilitySamples > 0 {
		return true
	}
	return metrics.EligibleAccounts > 0 && metrics.AvailableAccounts == 0
}

func inspectionNotificationCohort(accounts map[string]Account, conditions PolicyConditionGroup) map[string]Account {
	cohort := make(map[string]Account)
	for id, account := range accounts {
		if conditionalPolicyGroupMatches(conditions, account) {
			cohort[id] = account
		}
	}
	return cohort
}

func (e *InspectionEngine) evaluateInspectionNotification(
	policy InspectionPolicy,
	accounts map[string]Account,
	records map[string]inspectionRecord,
	now time.Time,
	evaluate bool,
) bool {
	if e == nil || !evaluate {
		return false
	}
	endpoints := normalizeInspectionNotificationEndpoints(policy.NotificationEndpoints, policy.AnomalyNotificationURL)
	if len(endpoints) == 0 {
		return false
	}
	genericMetrics := inspectionAnomalyNotificationMetrics(accounts, records)
	genericReasons := inspectionNotificationReasons(policy, genericMetrics)
	policyByID := inspectionNotificationPolicyMap(policy.NotificationPolicies)
	type policyEvaluation struct {
		metrics anomalyNotificationMetrics
		reasons []string
	}
	evaluations := make(map[string]policyEvaluation, len(policyByID))
	cooldown := time.Duration(policy.NotificationCooldownMinutes) * time.Minute
	events := make([]anomalyNotificationEvent, 0, len(endpoints))
	e.mu.Lock()
	if e.lastNotificationByEndpoint == nil {
		e.lastNotificationByEndpoint = make(map[string]time.Time)
	}
	legacyCooldown := len(e.lastNotificationByEndpoint) == 0 && !e.lastNotificationAt.IsZero()
	for _, endpoint := range endpoints {
		if !endpoint.Enabled {
			continue
		}
		metrics := genericMetrics
		reasons := genericReasons
		policyID, policyName := endpoint.NotificationPolicyID, ""
		if policyID != "" {
			notificationPolicy, exists := policyByID[policyID]
			if !exists || !notificationPolicy.Enabled {
				continue
			}
			policyName = notificationPolicy.Name
			evaluation, exists := evaluations[policyID]
			if !exists {
				cohort := inspectionNotificationCohort(accounts, notificationPolicy.Conditions)
				metrics = inspectionAnomalyNotificationMetrics(cohort, records)
				reasons = inspectionNotificationPolicyReasons(notificationPolicy, metrics)
				evaluation = policyEvaluation{metrics: metrics, reasons: reasons}
				evaluations[policyID] = evaluation
			} else {
				metrics, reasons = evaluation.metrics, evaluation.reasons
			}
		}
		if len(reasons) == 0 {
			continue
		}
		lastTriggered := e.lastNotificationByEndpoint[endpoint.ID]
		if lastTriggered.IsZero() && legacyCooldown {
			lastTriggered = e.lastNotificationAt
		}
		if !lastTriggered.IsZero() && now.Before(lastTriggered.Add(cooldown)) {
			continue
		}
		if policyID == "" {
			metrics.ThresholdPercent = policy.AnomalyThresholdPercent
			metrics.AvailableAccountsThreshold = policy.NotificationAvailableBelow
			metrics.AvailabilityThreshold = policy.NotificationPercentBelow
		} else {
			notificationPolicy := policyByID[policyID]
			metrics.AvailableAccountsThreshold = notificationPolicy.AvailableAccountsBelow
			metrics.AvailabilityThreshold = notificationPolicy.AvailabilityPercentBelow
		}
		events = append(events, anomalyNotificationEvent{
			EndpointID: endpoint.ID, EndpointName: endpoint.Name, URLTemplate: endpoint.URL,
			Event: strings.Join(reasons, ","), Metrics: metrics, TriggeredAt: now.UTC(),
			PolicyID: policyID, PolicyName: policyName,
		})
		e.lastNotificationByEndpoint[endpoint.ID] = now.UTC()
	}
	if len(events) > 0 {
		e.lastNotificationAt = now.UTC()
		e.dirty = true
		e.generation++
	}
	e.mu.Unlock()
	return e.queueInspectionNotificationEvents(events, now, e.queueAnomalyNotification)
}

func (e *InspectionEngine) queueInspectionNotificationEvents(
	events []anomalyNotificationEvent,
	now time.Time,
	queue func(anomalyNotificationEvent) bool,
) bool {
	if e == nil || queue == nil {
		return false
	}
	queued := false
	for _, event := range events {
		if queue(event) {
			queued = true
			continue
		}
		// Do not consume this endpoint's cooldown when it could not be queued
		// (for example, a full queue or a retired runtime). Keep the global
		// cooldown until all endpoints have been attempted: an early failure
		// must not clear it when a later endpoint was queued successfully.
		e.mu.Lock()
		if last := e.lastNotificationByEndpoint[event.EndpointID]; last.Equal(event.TriggeredAt) {
			delete(e.lastNotificationByEndpoint, event.EndpointID)
		}
		e.mu.Unlock()
	}
	if !queued {
		e.mu.Lock()
		if e.lastNotificationAt.Equal(now.UTC()) {
			e.lastNotificationAt = time.Time{}
		}
		e.mu.Unlock()
	}
	return queued
}

func (e *InspectionEngine) queueAnomalyNotification(event anomalyNotificationEvent) bool {
	if e == nil || e.notificationWake == nil {
		return false
	}
	e.mu.RLock()
	owner := e.backgroundOwner
	e.mu.RUnlock()
	if !backgroundWorkAllowed(owner) {
		return false
	}
	select {
	case e.notificationWake <- event:
		return true
	default:
		e.recordAnomalyNotification(event, anomalyNotificationResult{ReasonCode: "notification_queue_full"})
		return false
	}
}

func (e *InspectionEngine) notificationLoop(ctx context.Context) {
	defer e.wait.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-e.notificationWake:
			result := e.deliverAnomalyNotification(ctx, event)
			e.recordAnomalyNotification(event, result)
		}
	}
}

func (e *InspectionEngine) deliverAnomalyNotification(ctx context.Context, event anomalyNotificationEvent) anomalyNotificationResult {
	e.mu.RLock()
	owner := e.backgroundOwner
	e.mu.RUnlock()
	if !backgroundWorkAllowed(owner) {
		return anomalyNotificationResult{ReasonCode: "notification_superseded"}
	}
	ownedCtx, cancelOwnership := contextWithBackgroundOwnership(ctx, owner)
	defer cancelOwnership()
	ctx = ownedCtx
	target, errExpand := expandAnomalyNotificationURL(event)
	if errExpand != nil {
		return anomalyNotificationResult{ReasonCode: "notification_rejected"}
	}
	e.mu.RLock()
	doer := e.notificationDoer
	retryDelay := e.notificationRetryDelay
	e.mu.RUnlock()
	if doer == nil {
		doer = newAnomalyNotificationHTTPClient()
	}
	if retryDelay == nil {
		retryDelay = func(attempt int) time.Duration { return time.Duration(attempt) * time.Second }
	}
	result := anomalyNotificationResult{ReasonCode: "notification_failed"}
	for attempt := 1; attempt <= anomalyNotificationAttempts; attempt++ {
		result.Attempts = attempt
		result.StatusCode = 0
		requestCtx, cancel := context.WithTimeout(ctx, anomalyNotificationTimeout)
		request, errRequest := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
		if errRequest != nil {
			cancel()
			return anomalyNotificationResult{Attempts: attempt, ReasonCode: "notification_rejected"}
		}
		request.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.1")
		request.Header.Set("User-Agent", PluginID+"/"+PluginVersion)
		response, errDo := doer.Do(request)
		if errDo == nil && response != nil {
			result.StatusCode = boundedHTTPStatus(response.StatusCode)
			if response.Body != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAnomalyNotificationResponse))
				_ = response.Body.Close()
			}
		}
		cancel()
		if errDo == nil && response != nil && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			result.ReasonCode = "notification_delivered"
			return result
		}
		if errDo == nil && response != nil && response.StatusCode < http.StatusInternalServerError && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests {
			return result
		}
		if attempt < anomalyNotificationAttempts {
			delay := retryDelay(attempt)
			if delay <= 0 {
				continue
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return result
			case <-timer.C:
			}
		}
	}
	return result
}

func (e *InspectionEngine) recordAnomalyNotification(event anomalyNotificationEvent, result anomalyNotificationResult) {
	if e == nil {
		return
	}
	e.mu.RLock()
	journal := e.operations
	e.mu.RUnlock()
	if journal == nil {
		return
	}
	status := OperationStatusFailed
	succeeded, failed, skipped := 0, 1, 0
	switch result.ReasonCode {
	case "notification_delivered":
		status = OperationStatusSucceeded
		succeeded, failed = 1, 0
	case "notification_superseded":
		// A retired instance deliberately cancels its queued work. This is not
		// a delivery failure and should not inflate failure counts.
		status = OperationStatusSkipped
		failed, skipped = 0, 1
	}
	finishedAt := e.currentTime()
	journal.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionAnomalyNotification,
		Status: status, Source: OperationSourceInspection, Scope: OperationScopeSystem,
		TargetID: event.EndpointID, TargetCount: event.Metrics.TotalAccounts, Succeeded: succeeded, Failed: failed, Skipped: skipped,
		StartedAt: event.TriggeredAt, FinishedAt: finishedAt, ReasonCode: result.ReasonCode,
		RelatedActionID: event.PolicyID, HTTPStatus: result.StatusCode, Attempts: result.Attempts,
	})
}

func newAnomalyNotificationHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = 4
	transport.MaxIdleConnsPerHost = 2
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, errSplit := net.SplitHostPort(address)
		if errSplit != nil {
			return nil, fmt.Errorf("notification destination is invalid")
		}
		addresses, errLookup := net.DefaultResolver.LookupIPAddr(ctx, host)
		if errLookup != nil {
			return nil, fmt.Errorf("resolve notification destination: %w", errLookup)
		}
		for _, candidate := range addresses {
			if !publicNotificationIP(candidate.IP) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		}
		return nil, fmt.Errorf("notification destination did not resolve to a public address")
	}
	return &http.Client{
		Transport: transport,
		Timeout:   anomalyNotificationTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func publicNotificationIP(address net.IP) bool {
	parsed, ok := netip.AddrFromSlice(address)
	if !ok {
		return false
	}
	parsed = parsed.Unmap()
	if !parsed.IsGlobalUnicast() || parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() || parsed.IsMulticast() || parsed.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedAnomalyNotificationPrefixes {
		if prefix.Contains(parsed) {
			return false
		}
	}
	return true
}
