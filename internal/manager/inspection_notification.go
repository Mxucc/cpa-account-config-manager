package manager

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxAnomalyNotificationURLBytes = 4096
	anomalyNotificationTimeout     = 10 * time.Second
	anomalyNotificationAttempts    = 3
	maxAnomalyNotificationResponse = 4 << 10
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
	URLTemplate string
	Event       string
	Metrics     anomalyNotificationMetrics
	TriggeredAt time.Time
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
	if errValidate := validateAnomalyNotificationDestination(parsed); errValidate != nil {
		return "", errValidate
	}
	return parsed.String(), nil
}

func anomalyNotificationVariableValues(event anomalyNotificationEvent) map[string]string {
	return map[string]string{
		"event":                          event.Event,
		"total_accounts":                 strconv.Itoa(event.Metrics.TotalAccounts),
		"eligible_accounts":              strconv.Itoa(event.Metrics.EligibleAccounts),
		"available_accounts":             strconv.Itoa(event.Metrics.AvailableAccounts),
		"available_percent":              strconv.Itoa(event.Metrics.AvailablePercent),
		"abnormal_accounts":              strconv.Itoa(event.Metrics.AbnormalAccounts),
		"abnormal_percent":               strconv.Itoa(event.Metrics.AbnormalPercent),
		"quota_limited_accounts":         strconv.Itoa(event.Metrics.QuotaLimitedAccounts),
		"invalid_credential_accounts":    strconv.Itoa(event.Metrics.InvalidCredentialAccounts),
		"deactivated_accounts":           strconv.Itoa(event.Metrics.DeactivatedAccounts),
		"unavailable_accounts":           strconv.Itoa(event.Metrics.UnavailableAccounts),
		"disabled_accounts":              strconv.Itoa(event.Metrics.DisabledAccounts),
		"threshold_percent":              strconv.Itoa(event.Metrics.ThresholdPercent),
		"available_accounts_threshold":   strconv.Itoa(event.Metrics.AvailableAccountsThreshold),
		"availability_percent_threshold": strconv.Itoa(event.Metrics.AvailabilityThreshold),
		"triggered_at":                   event.TriggeredAt.UTC().Format(time.RFC3339),
	}
}

func inspectionNotificationEnabled(policy InspectionPolicy) bool {
	return policy.AnomalyNotificationEnabled || policy.NotificationAvailableEnabled || policy.NotificationPercentEnabled
}

func inspectionNotificationPolicyChanged(previous, next InspectionPolicy) bool {
	return previous.AnomalyNotificationEnabled != next.AnomalyNotificationEnabled ||
		previous.AnomalyNotificationURL != next.AnomalyNotificationURL ||
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
	request.URLTemplate = strings.TrimSpace(request.URLTemplate)
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
	e.mu.RUnlock()
	metrics := inspectionAnomalyNotificationMetrics(accountsByID, records)
	metrics.ThresholdPercent = request.ThresholdPercent
	metrics.AvailableAccountsThreshold = request.AvailableAccountsThreshold
	metrics.AvailabilityThreshold = request.AvailabilityPercentThreshold
	triggeredAt := e.currentTime()
	event := anomalyNotificationEvent{
		URLTemplate: request.URLTemplate,
		Event:       eventName,
		Metrics:     metrics,
		TriggeredAt: triggeredAt,
	}
	expanded, errExpand := expandAnomalyNotificationURL(event)
	if errExpand != nil {
		return InspectionNotificationPreview{}, errExpand
	}
	return InspectionNotificationPreview{
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
		URLTemplate: request.URLTemplate,
		Event:       preview.Event,
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
	value, _ := strconv.Atoi(values[key])
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
	succeeded, failed := 0, 1
	if result.ReasonCode == "notification_delivered" {
		status = OperationStatusSucceeded
		succeeded, failed = 1, 0
	}
	journal.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionNotificationTest,
		Status: status, Source: OperationSourceManual, Scope: OperationScopeSystem,
		TargetCount: event.Metrics.TotalAccounts, Succeeded: succeeded, Failed: failed,
		StartedAt: event.TriggeredAt, FinishedAt: e.currentTime(), ReasonCode: result.ReasonCode,
		HTTPStatus: result.StatusCode, Attempts: result.Attempts,
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
	if policy.NotificationPercentEnabled && metrics.AvailableAccounts*100 < metrics.TotalAccounts*policy.NotificationPercentBelow {
		reasons = append(reasons, "availability_percent_low")
	}
	return reasons
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
	metrics := inspectionAnomalyNotificationMetrics(accounts, records)
	reasons := inspectionNotificationReasons(policy, metrics)
	if len(reasons) == 0 {
		return false
	}
	cooldown := time.Duration(policy.NotificationCooldownMinutes) * time.Minute
	e.mu.Lock()
	if !e.lastNotificationAt.IsZero() && now.Before(e.lastNotificationAt.Add(cooldown)) {
		e.mu.Unlock()
		return false
	}
	e.lastNotificationAt = now.UTC()
	e.dirty = true
	e.generation++
	e.mu.Unlock()

	metrics.ThresholdPercent = policy.AnomalyThresholdPercent
	metrics.AvailableAccountsThreshold = policy.NotificationAvailableBelow
	metrics.AvailabilityThreshold = policy.NotificationPercentBelow
	e.queueAnomalyNotification(anomalyNotificationEvent{
		URLTemplate: policy.AnomalyNotificationURL,
		Event:       strings.Join(reasons, ","),
		Metrics:     metrics,
		TriggeredAt: now.UTC(),
	})
	return true
}

func (e *InspectionEngine) queueAnomalyNotification(event anomalyNotificationEvent) {
	if e == nil || e.notificationWake == nil {
		return
	}
	select {
	case e.notificationWake <- event:
	default:
		e.recordAnomalyNotification(event, anomalyNotificationResult{ReasonCode: "notification_queue_full"})
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
	succeeded, failed := 0, 1
	if result.ReasonCode == "notification_delivered" {
		status = OperationStatusSucceeded
		succeeded, failed = 1, 0
	}
	finishedAt := e.currentTime()
	journal.Record(OperationEntry{
		Category: OperationCategoryInspection, Action: OperationActionAnomalyNotification,
		Status: status, Source: OperationSourceInspection, Scope: OperationScopeSystem,
		TargetCount: event.Metrics.TotalAccounts, Succeeded: succeeded, Failed: failed,
		StartedAt: event.TriggeredAt, FinishedAt: finishedAt, ReasonCode: result.ReasonCode,
		HTTPStatus: result.StatusCode, Attempts: result.Attempts,
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
