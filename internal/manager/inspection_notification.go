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
	"abnormal_accounts": {}, "abnormal_percent": {}, "quota_limited_accounts": {},
	"invalid_credential_accounts": {}, "deactivated_accounts": {}, "unavailable_accounts": {},
	"disabled_accounts": {}, "threshold_percent": {}, "triggered_at": {},
}

type anomalyNotificationMetrics struct {
	TotalAccounts             int
	EligibleAccounts          int
	AvailableAccounts         int
	AbnormalAccounts          int
	AbnormalPercent           int
	QuotaLimitedAccounts      int
	InvalidCredentialAccounts int
	DeactivatedAccounts       int
	UnavailableAccounts       int
	DisabledAccounts          int
	ThresholdPercent          int
}

type anomalyNotificationEvent struct {
	URLTemplate string
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
	values := map[string]string{
		"event":                       "anomaly_threshold",
		"total_accounts":              strconv.Itoa(event.Metrics.TotalAccounts),
		"eligible_accounts":           strconv.Itoa(event.Metrics.EligibleAccounts),
		"available_accounts":          strconv.Itoa(event.Metrics.AvailableAccounts),
		"abnormal_accounts":           strconv.Itoa(event.Metrics.AbnormalAccounts),
		"abnormal_percent":            strconv.Itoa(event.Metrics.AbnormalPercent),
		"quota_limited_accounts":      strconv.Itoa(event.Metrics.QuotaLimitedAccounts),
		"invalid_credential_accounts": strconv.Itoa(event.Metrics.InvalidCredentialAccounts),
		"deactivated_accounts":        strconv.Itoa(event.Metrics.DeactivatedAccounts),
		"unavailable_accounts":        strconv.Itoa(event.Metrics.UnavailableAccounts),
		"disabled_accounts":           strconv.Itoa(event.Metrics.DisabledAccounts),
		"threshold_percent":           strconv.Itoa(event.Metrics.ThresholdPercent),
		"triggered_at":                event.TriggeredAt.UTC().Format(time.RFC3339),
	}
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
