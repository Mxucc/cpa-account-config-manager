package manager

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type anomalyNotificationDoerFunc func(*http.Request) (*http.Response, error)

func (function anomalyNotificationDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAnomalyNotificationTemplateValidationAndExpansion(t *testing.T) {
	valid := "https://notify.example/events?available=${available_accounts}&abnormal=${abnormal_accounts}&at=${triggered_at}"
	if errValidate := validateAnomalyNotificationTemplate(valid); errValidate != nil {
		t.Fatalf("valid template rejected: %v", errValidate)
	}
	event := anomalyNotificationEvent{
		URLTemplate: valid,
		Metrics: anomalyNotificationMetrics{
			AvailableAccounts: 17,
			AbnormalAccounts:  5,
		},
		TriggeredAt: time.Date(2026, time.July, 22, 8, 9, 10, 0, time.FixedZone("UTC+8", 8*60*60)),
	}
	expanded, errExpand := expandAnomalyNotificationURL(event)
	if errExpand != nil {
		t.Fatalf("expand template: %v", errExpand)
	}
	parsed, errParse := url.Parse(expanded)
	if errParse != nil {
		t.Fatalf("parse expanded URL: %v", errParse)
	}
	if parsed.Query().Get("available") != "17" || parsed.Query().Get("abnormal") != "5" || parsed.Query().Get("at") != "2026-07-22T00:09:10Z" {
		t.Fatalf("expanded query = %#v", parsed.Query())
	}

	for name, template := range map[string]string{
		"HTTP":             "http://notify.example/events?total=${total_accounts}",
		"loopback":         "https://127.0.0.1/events?total=${total_accounts}",
		"private address":  "https://10.0.0.8/events?total=${total_accounts}",
		"localhost":        "https://localhost/events?total=${total_accounts}",
		"userinfo":         "https://user:secret@notify.example/events?total=${total_accounts}",
		"path variable":    "https://notify.example/${event}?total=${total_accounts}",
		"host variable":    "https://${event}.example/events?total=${total_accounts}",
		"unknown variable": "https://notify.example/events?value=${account_email}",
		"broken variable":  "https://notify.example/events?value=${available-accounts}",
	} {
		t.Run(name, func(t *testing.T) {
			if errValidate := validateAnomalyNotificationTemplate(template); errValidate == nil {
				t.Fatalf("unsafe template was accepted: %s", template)
			}
		})
	}
}

func TestInspectionAnomalyNotificationSendsAggregateGETOnceAndLogsSanitizedOutcome(t *testing.T) {
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.AnomalyTriggerEnabled = true
	policy.AnomalyThresholdPercent = 50
	policy.AnomalyMinimumAccounts = 2
	policy.AnomalyCooldownMinutes = 60
	policy.AnomalyNotificationEnabled = true
	policy.AnomalyNotificationURL = "https://notify.example/hook?event=${event}&total=${total_accounts}&eligible=${eligible_accounts}&available=${available_accounts}&abnormal=${abnormal_accounts}&percent=${abnormal_percent}&quota=${quota_limited_accounts}&invalid=${invalid_credential_accounts}&disabled=${disabled_accounts}&threshold=${threshold_percent}"

	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: t.TempDir()})
	requestURLs := make(chan string, 2)
	engine := NewInspectionEngine(nil, nil, nil)
	engine.SetOperationJournal(journal)
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		requestURLs <- request.URL.String()
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("private notification response")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})
	engine.Configure(Config{DataDir: t.TempDir(), InspectionPolicy: &policy})
	t.Cleanup(engine.Shutdown)

	accounts := map[string]Account{
		"healthy":          {ID: "healthy", Editable: true},
		"quota":            {ID: "quota", Editable: true},
		"invalid-disabled": {ID: "invalid-disabled", Editable: true, Disabled: true},
		"manual-disabled":  {ID: "manual-disabled", Editable: true, Disabled: true},
	}
	records := map[string]inspectionRecord{
		"healthy": {Result: InspectionResult{ID: "healthy", Health: InspectionHealthHealthy}},
		"quota":   {Result: InspectionResult{ID: "quota", Health: InspectionHealthQuotaLimited}},
		"invalid-disabled": {Result: InspectionResult{
			ID: "invalid-disabled", Health: InspectionHealthInvalidCredentials, OwnedDisable: true,
		}},
		"manual-disabled": {Result: InspectionResult{ID: "manual-disabled", Health: InspectionHealthUnavailable}},
	}
	now := time.Date(2026, time.July, 22, 8, 30, 0, 0, time.UTC)
	triggered, _ := engine.evaluateAnomalyTrigger(policy, accounts, records, now, true, true)
	if !triggered {
		t.Fatal("exact anomaly threshold did not trigger")
	}

	var requested string
	select {
	case requested = <-requestURLs:
	case <-time.After(2 * time.Second):
		t.Fatal("notification request was not sent")
	}
	parsed, errParse := url.Parse(requested)
	if errParse != nil {
		t.Fatalf("parse requested URL: %v", errParse)
	}
	wantQuery := map[string]string{
		"event": "anomaly_threshold", "total": "4", "eligible": "3", "available": "1",
		"abnormal": "2", "percent": "66", "quota": "1", "invalid": "1", "disabled": "2", "threshold": "50",
	}
	for key, want := range wantQuery {
		if got := parsed.Query().Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}

	triggered, _ = engine.evaluateAnomalyTrigger(policy, accounts, records, now.Add(59*time.Minute), true, true)
	if triggered {
		t.Fatal("cooldown allowed a duplicate anomaly notification")
	}
	select {
	case duplicate := <-requestURLs:
		t.Fatalf("duplicate notification request = %s", duplicate)
	case <-time.After(50 * time.Millisecond):
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		operations := journal.List(OperationQuery{Page: 1})
		if len(operations.Operations) > 0 {
			entry := operations.Operations[0]
			if entry.Action != OperationActionAnomalyNotification || entry.Status != OperationStatusSucceeded || entry.ReasonCode != "notification_delivered" || entry.HTTPStatus != http.StatusNoContent || entry.Attempts != 1 {
				t.Fatalf("notification operation = %#v", entry)
			}
			encoded := strings.Join([]string{entry.TargetID, entry.ReasonCode, entry.Model, entry.Format}, " ")
			for _, private := range []string{"notify.example", "private notification response", "hook?event"} {
				if strings.Contains(encoded, private) {
					t.Fatalf("notification operation leaked %q: %#v", private, entry)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification operation was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnomalyNotificationQueueFullRecordsSanitizedFailure(t *testing.T) {
	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: t.TempDir()})
	engine := NewInspectionEngine(nil, nil, nil)
	engine.SetOperationJournal(journal)
	event := anomalyNotificationEvent{
		URLTemplate: "https://notify.example/hook?available=${available_accounts}",
		Metrics:     anomalyNotificationMetrics{TotalAccounts: 25, AvailableAccounts: 2},
		TriggeredAt: time.Now().UTC(),
	}
	for index := 0; index <= cap(engine.notificationWake); index++ {
		engine.queueAnomalyNotification(event)
	}
	operations := journal.List(OperationQuery{Page: 1})
	if len(operations.Operations) != 1 {
		t.Fatalf("queue-full operation count = %d", len(operations.Operations))
	}
	entry := operations.Operations[0]
	if entry.Status != OperationStatusFailed || entry.ReasonCode != "notification_queue_full" || entry.Attempts != 0 || entry.HTTPStatus != 0 || entry.TargetCount != 25 {
		t.Fatalf("queue-full notification operation = %#v", entry)
	}
	encoded := strings.Join([]string{entry.TargetID, entry.ReasonCode, entry.Model, entry.Format}, " ")
	if strings.Contains(encoded, "notify.example") || strings.Contains(encoded, "available=") {
		t.Fatalf("queue-full notification operation leaked its URL: %#v", entry)
	}
}

func TestAnomalyNotificationDestinationRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "::", "::1", "169.254.169.254", "100.100.100.200", "100.64.0.1",
		"192.0.2.1", "192.168.1.2", "198.18.0.1", "203.0.113.1", "224.0.0.1", "2001:db8::1",
	} {
		if publicNotificationIP(net.ParseIP(raw)) {
			t.Errorf("publicNotificationIP(%q) = true", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicNotificationIP(net.ParseIP(raw)) {
			t.Errorf("publicNotificationIP(%q) = false", raw)
		}
	}
}

func TestAnomalyNotificationRedirectIsNotFollowed(t *testing.T) {
	client := newAnomalyNotificationHTTPClient()
	request, errRequest := http.NewRequest(http.MethodGet, "https://notify.example/next", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	if errRedirect := client.CheckRedirect(request, nil); !errors.Is(errRedirect, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v", errRedirect)
	}

	attempts := 0
	engine := NewInspectionEngine(nil, nil, nil)
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       io.NopCloser(strings.NewReader("redirect response")),
			Header:     http.Header{"Location": []string{"https://other.example/hook"}},
			Request:    request,
		}, nil
	})
	result := engine.deliverAnomalyNotification(context.Background(), anomalyNotificationEvent{
		URLTemplate: "https://notify.example/hook?available=${available_accounts}",
		Metrics:     anomalyNotificationMetrics{AvailableAccounts: 3},
	})
	if attempts != 1 || result.ReasonCode != "notification_failed" || result.StatusCode != http.StatusFound || result.Attempts != 1 {
		t.Fatalf("attempts=%d result=%#v", attempts, result)
	}
}

func TestAnomalyNotificationShutdownCancelsBlockingRequest(t *testing.T) {
	engine := NewInspectionEngine(nil, nil, nil)
	started := make(chan struct{})
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	engine.Configure(Config{DataDir: t.TempDir()})
	engine.queueAnomalyNotification(anomalyNotificationEvent{
		URLTemplate: "https://notify.example/hook?available=${available_accounts}",
		Metrics:     anomalyNotificationMetrics{AvailableAccounts: 3},
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		engine.Shutdown()
		t.Fatal("notification request did not start")
	}

	stopped := make(chan struct{})
	go func() {
		engine.Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("inspection shutdown did not cancel the notification request")
	}
}

func TestAnomalyNotificationRejectedTemplateRecordsSanitizedFailure(t *testing.T) {
	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: t.TempDir()})
	engine := NewInspectionEngine(nil, nil, nil)
	engine.SetOperationJournal(journal)
	engine.Configure(Config{DataDir: t.TempDir()})
	t.Cleanup(engine.Shutdown)
	engine.queueAnomalyNotification(anomalyNotificationEvent{
		URLTemplate: "https://user:private-token@notify.example/hook?account=${account_email}",
		Metrics:     anomalyNotificationMetrics{TotalAccounts: 12},
		TriggeredAt: time.Now().UTC(),
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		operations := journal.List(OperationQuery{Page: 1})
		if len(operations.Operations) > 0 {
			entry := operations.Operations[0]
			if entry.Status != OperationStatusFailed || entry.ReasonCode != "notification_rejected" || entry.Attempts != 0 || entry.TargetCount != 12 {
				t.Fatalf("rejected notification operation = %#v", entry)
			}
			encoded := strings.Join([]string{entry.TargetID, entry.ReasonCode, entry.Model, entry.Format}, " ")
			for _, private := range []string{"private-token", "notify.example", "account_email"} {
				if strings.Contains(encoded, private) {
					t.Fatalf("rejected notification operation leaked %q: %#v", private, entry)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("rejected notification operation was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnomalyNotificationFailureRetriesAndRecordsHTTPStatusWithoutResponse(t *testing.T) {
	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: t.TempDir()})
	engine := NewInspectionEngine(nil, nil, nil)
	engine.SetOperationJournal(journal)
	attempts := 0
	engine.notificationRetryDelay = func(int) time.Duration { return 0 }
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(strings.NewReader("upstream notification private failure")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})
	engine.Configure(Config{DataDir: t.TempDir()})
	t.Cleanup(engine.Shutdown)
	event := anomalyNotificationEvent{
		URLTemplate: "https://notify.example/hook?available=${available_accounts}",
		Metrics:     anomalyNotificationMetrics{TotalAccounts: 40, AvailableAccounts: 3},
		TriggeredAt: time.Now().UTC(),
	}
	engine.queueAnomalyNotification(event)

	deadline := time.Now().Add(2 * time.Second)
	for {
		operations := journal.List(OperationQuery{Page: 1})
		if len(operations.Operations) > 0 {
			entry := operations.Operations[0]
			if attempts != anomalyNotificationAttempts || entry.Status != OperationStatusFailed || entry.ReasonCode != "notification_failed" || entry.HTTPStatus != http.StatusBadGateway || entry.Attempts != anomalyNotificationAttempts {
				t.Fatalf("attempts=%d operation=%#v", attempts, entry)
			}
			encoded := strings.Join([]string{entry.TargetID, entry.ReasonCode, entry.Model, entry.Format}, " ")
			for _, private := range []string{"notify.example", "upstream notification private failure", "available="} {
				if strings.Contains(encoded, private) {
					t.Fatalf("failure operation leaked %q: %#v", private, entry)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed notification operation was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnomalyNotificationFailureDoesNotReportStaleHTTPStatus(t *testing.T) {
	engine := NewInspectionEngine(nil, nil, nil)
	engine.notificationRetryDelay = func(int) time.Duration { return 0 }
	attempts := 0
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("temporary failure")),
				Header:     make(http.Header),
				Request:    request,
			}, nil
		}
		return nil, context.DeadlineExceeded
	})
	result := engine.deliverAnomalyNotification(context.Background(), anomalyNotificationEvent{
		URLTemplate: "https://notify.example/hook?available=${available_accounts}",
		Metrics:     anomalyNotificationMetrics{AvailableAccounts: 3},
	})
	if attempts != anomalyNotificationAttempts || result.ReasonCode != "notification_failed" || result.StatusCode != 0 || result.Attempts != anomalyNotificationAttempts {
		t.Fatalf("attempts=%d result=%#v", attempts, result)
	}
}
