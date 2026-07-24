package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-account-config-manager/internal/cpaapi"
)

type anomalyNotificationDoerFunc func(*http.Request) (*http.Response, error)

func (function anomalyNotificationDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAnomalyNotificationTemplateValidationAndExpansion(t *testing.T) {
	valid := "https://notify.example/events?event=${event}&available=${available_accounts}&available_percent=${available_percent}&count_threshold=${available_accounts_threshold}&percent_threshold=${availability_percent_threshold}&abnormal=${abnormal_accounts}&at=${triggered_at}"
	if errValidate := validateAnomalyNotificationTemplate(valid); errValidate != nil {
		t.Fatalf("valid template rejected: %v", errValidate)
	}
	event := anomalyNotificationEvent{
		URLTemplate: valid,
		Event:       "available_accounts_low,availability_percent_low",
		Metrics: anomalyNotificationMetrics{
			AvailableAccounts:          17,
			AvailablePercent:           42,
			AvailableAccountsThreshold: 20,
			AvailabilityThreshold:      50,
			AbnormalAccounts:           5,
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
	if parsed.Query().Get("event") != "available_accounts_low,availability_percent_low" || parsed.Query().Get("available") != "17" ||
		parsed.Query().Get("available_percent") != "42" || parsed.Query().Get("count_threshold") != "20" ||
		parsed.Query().Get("percent_threshold") != "50" || parsed.Query().Get("abnormal") != "5" || parsed.Query().Get("at") != "2026-07-22T00:09:10Z" {
		t.Fatalf("expanded query = %#v", parsed.Query())
	}
	combined := event
	combined.URLTemplate = "https://notify.example/events?message=event:${event},available:${available_accounts}/${total_accounts},rate:${available_percent}%25"
	combined.Metrics.TotalAccounts = 40
	combinedURL, errCombined := expandAnomalyNotificationURL(combined)
	if errCombined != nil {
		t.Fatalf("expand combined detail template: %v", errCombined)
	}
	combinedParsed, errParseCombined := url.Parse(combinedURL)
	if errParseCombined != nil {
		t.Fatalf("parse combined detail URL: %v", errParseCombined)
	}
	if got, want := combinedParsed.Query().Get("message"), "event:available_accounts_low,availability_percent_low,available:17/40,rate:42%"; got != want {
		t.Fatalf("combined detail message = %q, want %q", got, want)
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

func TestInspectionNotificationPreviewUsesCurrentValuesWithoutAppendingFieldsOrSending(t *testing.T) {
	now := time.Date(2026, time.July, 24, 8, 30, 0, 0, time.UTC)
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{
		{AuthIndex: "healthy", Name: "healthy.json", Provider: "codex", Source: "file", Path: "/auths/healthy.json"},
		{AuthIndex: "quota", Name: "quota.json", Provider: "codex", Source: "file", Path: "/auths/quota.json"},
	}}
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	requests := 0
	engine.notificationDoer = anomalyNotificationDoerFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("preview must not send")
	})
	engine.records = map[string]inspectionRecord{
		"healthy": {Result: InspectionResult{ID: "healthy", Health: InspectionHealthHealthy}},
		"quota":   {Result: InspectionResult{ID: "quota", Health: InspectionHealthQuotaLimited}},
	}

	preview, errPreview := engine.PreviewNotification(context.Background(), InspectionNotificationRequest{
		URLTemplate:                  "https://notify.example/publish?message=available:${available_accounts},rate:${available_percent}",
		Scenario:                     InspectionNotificationScenarioAvailableLow,
		ThresholdPercent:             50,
		AvailableAccountsThreshold:   2,
		AvailabilityPercentThreshold: 60,
	})
	if errPreview != nil {
		t.Fatalf("PreviewNotification() error = %v", errPreview)
	}
	if requests != 0 {
		t.Fatalf("preview sent %d network requests", requests)
	}
	parsed, errParse := url.Parse(preview.ExpandedURL)
	if errParse != nil {
		t.Fatalf("parse preview URL: %v", errParse)
	}
	if got, want := parsed.Query().Get("message"), "available:1,rate:50"; got != want {
		t.Fatalf("preview message = %q, want %q", got, want)
	}
	if len(parsed.Query()) != 1 || parsed.Query().Has("event") {
		t.Fatalf("preview appended fields not present in the template: %#v", parsed.Query())
	}
	if preview.Variables["total_accounts"] != "2" || preview.Variables["available_accounts"] != "1" ||
		preview.Variables["available_percent"] != "50" || preview.Variables["available_accounts_threshold"] != "2" {
		t.Fatalf("preview variables = %#v", preview.Variables)
	}
	if preview.Scenario != InspectionNotificationScenarioAvailableLow || preview.Event != InspectionNotificationScenarioAvailableLow || !preview.TriggeredAt.Equal(now) {
		t.Fatalf("preview metadata = %#v", preview)
	}
}

func TestInspectionNotificationTestUsesHardenedDeliveryAndLogsSanitizedManualOutcome(t *testing.T) {
	now := time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)
	host := &fakeAuthHost{entries: []cpaapi.HostAuthFileEntry{{
		AuthIndex: "quota", Name: "quota.json", Provider: "codex", Source: "file", Path: "/auths/quota.json",
	}}}
	journal := NewOperationJournal()
	journal.Configure(Config{DataDir: t.TempDir()})
	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.SetOperationJournal(journal)
	engine.records = map[string]inspectionRecord{
		"quota": {Result: InspectionResult{ID: "quota", Health: InspectionHealthQuotaLimited}},
	}
	var requested string
	engine.notificationDoer = anomalyNotificationDoerFunc(func(request *http.Request) (*http.Response, error) {
		requested = request.URL.String()
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("sensitive response body")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})
	result, errTest := engine.TestNotification(context.Background(), InspectionNotificationRequest{
		URLTemplate:                  "https://secret.notify.example/publish?message=${event}:${available_accounts}",
		Scenario:                     InspectionNotificationScenarioCombined,
		ThresholdPercent:             50,
		AvailableAccountsThreshold:   1,
		AvailabilityPercentThreshold: 20,
	})
	if errTest != nil {
		t.Fatalf("TestNotification() error = %v", errTest)
	}
	if !result.Delivered || result.StatusCode != http.StatusAccepted || result.Attempts != 1 || result.ReasonCode != "notification_delivered" {
		t.Fatalf("test result = %#v", result)
	}
	if requested != result.Preview.ExpandedURL || !strings.Contains(requested, "available_accounts_low") {
		t.Fatalf("requested URL = %q, preview = %q", requested, result.Preview.ExpandedURL)
	}
	operations := journal.List(OperationQuery{Page: 1})
	if len(operations.Operations) != 1 {
		t.Fatalf("operation count = %d", len(operations.Operations))
	}
	entry := operations.Operations[0]
	if entry.Action != OperationActionNotificationTest || entry.Source != OperationSourceManual || entry.Scope != OperationScopeSystem ||
		entry.Status != OperationStatusSucceeded || entry.TargetCount != 1 || entry.HTTPStatus != http.StatusAccepted || entry.Attempts != 1 {
		t.Fatalf("test notification operation = %#v", entry)
	}
	encoded, errEncode := json.Marshal(entry)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	for _, secret := range []string{"secret.notify.example", "message=", "sensitive response body"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("operation log leaked %q: %s", secret, encoded)
		}
	}
}

func TestNotificationSettingChangeImmediatelyEvaluatesZeroAvailabilityAndDisablesQuotaAccount(t *testing.T) {
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	host := inspectionEditableHost(false)
	host.entries[0].Status = "quota exhausted"
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		patchCalls++
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := NewInspectionEngine(NewAccountService(host), host, NewMutationCoordinator())
	engine.now = func() time.Time { return now }
	engine.mu.Lock()
	engine.config = normalizeConfig(Config{DataDir: t.TempDir(), ManagementBaseURL: server.URL})
	engine.store = inspectionStorePath(engine.config.DataDir)
	engine.managementKey = "management-secret"
	engine.mu.Unlock()
	policy := defaultInspectionPolicy()
	policy.Enabled = true
	policy.AutoDisable = true
	policy.NotificationAvailableEnabled = true
	policy.NotificationAvailableBelow = 1
	policy.AnomalyNotificationURL = "https://notify.example/publish?available=${available_accounts}&rate=${available_percent}"
	if _, errSet := engine.SetPolicy(policy); errSet != nil {
		t.Fatalf("SetPolicy() error = %v", errSet)
	}
	engine.scan(context.Background())

	if patchCalls != 1 {
		t.Fatalf("automatic disable calls = %d, want 1", patchCalls)
	}
	if len(engine.notificationWake) != 1 {
		t.Fatalf("queued notifications = %d, want 1", len(engine.notificationWake))
	}
	event := <-engine.notificationWake
	if event.Event != InspectionNotificationScenarioAvailableLow || event.Metrics.AvailableAccounts != 0 || event.Metrics.AvailablePercent != 0 {
		t.Fatalf("immediate notification event = %#v", event)
	}
	if engine.notificationPending {
		t.Fatal("notification pending flag was not cleared after the immediate native scan")
	}
	if snapshot := engine.Snapshot(); snapshot.LastRun.AutoDisabled != 1 {
		t.Fatalf("last run = %#v", snapshot.LastRun)
	}
	engine.scan(context.Background())
	if len(engine.notificationWake) != 0 {
		t.Fatal("notification cooldown allowed a duplicate from a subsequent native scan")
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
	if !engine.evaluateInspectionNotification(policy, accounts, records, now, true) {
		t.Fatal("exact anomaly threshold did not queue a notification")
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
	if engine.evaluateInspectionNotification(policy, accounts, records, now.Add(59*time.Minute), true) {
		t.Fatal("notification cooldown allowed a duplicate notification")
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

func TestInspectionNotificationCombinesAvailabilityConditionsWithStrictBoundaries(t *testing.T) {
	now := time.Date(2026, time.July, 22, 16, 30, 0, 0, time.UTC)
	policy := defaultInspectionPolicy()
	policy.AnomalyTriggerEnabled = true
	policy.AnomalyNotificationEnabled = true
	policy.AnomalyThresholdPercent = 50
	policy.AnomalyMinimumAccounts = 4
	policy.NotificationAvailableEnabled = true
	policy.NotificationAvailableBelow = 3
	policy.NotificationPercentEnabled = true
	policy.NotificationPercentBelow = 60
	policy.NotificationCooldownMinutes = 60
	policy.AnomalyNotificationURL = "https://notify.example/hook?event=${event}&available=${available_accounts}&rate=${available_percent}"
	accounts := map[string]Account{
		"healthy-a": {ID: "healthy-a"}, "healthy-b": {ID: "healthy-b"},
		"quota": {ID: "quota"}, "invalid": {ID: "invalid"},
	}
	records := map[string]inspectionRecord{
		"healthy-a": {Result: InspectionResult{Health: InspectionHealthHealthy}},
		"healthy-b": {Result: InspectionResult{Health: InspectionHealthHealthy}},
		"quota":     {Result: InspectionResult{Health: InspectionHealthQuotaLimited}},
		"invalid":   {Result: InspectionResult{Health: InspectionHealthInvalidCredentials}},
	}
	engine := NewInspectionEngine(nil, nil, nil)
	if !engine.evaluateInspectionNotification(policy, accounts, records, now, true) {
		t.Fatal("combined notification conditions did not trigger")
	}
	if queued := len(engine.notificationWake); queued != 1 {
		t.Fatalf("queued notifications = %d, want 1", queued)
	}
	event := <-engine.notificationWake
	if event.Event != "anomaly_threshold,available_accounts_low,availability_percent_low" {
		t.Fatalf("notification event = %q", event.Event)
	}
	if event.Metrics.TotalAccounts != 4 || event.Metrics.AvailableAccounts != 2 || event.Metrics.AvailablePercent != 50 {
		t.Fatalf("notification metrics = %#v", event.Metrics)
	}
	if engine.evaluateInspectionNotification(policy, accounts, records, now.Add(59*time.Minute), true) {
		t.Fatal("notification cooldown allowed an early duplicate")
	}

	boundary := policy
	boundary.AnomalyNotificationEnabled = false
	boundary.NotificationAvailableBelow = 2
	boundary.NotificationPercentBelow = 50
	boundaryEngine := NewInspectionEngine(nil, nil, nil)
	if boundaryEngine.evaluateInspectionNotification(boundary, accounts, records, now, true) {
		t.Fatal("values equal to notification thresholds must not trigger")
	}
	if boundaryEngine.evaluateInspectionNotification(boundary, map[string]Account{}, map[string]inspectionRecord{}, now, true) {
		t.Fatal("an empty account pool must not trigger")
	}
}

func TestInspectionNotificationCooldownSurvivesRestart(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.July, 22, 17, 0, 0, 0, time.UTC)
	policy := defaultInspectionPolicy()
	policy.NotificationAvailableEnabled = true
	policy.NotificationAvailableBelow = 2
	policy.NotificationCooldownMinutes = 60
	policy.AnomalyNotificationURL = "https://notify.example/hook?available=${available_accounts}"
	accounts := map[string]Account{"quota": {ID: "quota"}}
	records := map[string]inspectionRecord{
		"quota": {Result: InspectionResult{ID: "quota", Health: InspectionHealthQuotaLimited}},
	}

	first := NewInspectionEngine(nil, nil, nil)
	first.notificationDoer = successfulNotificationDoer()
	first.Configure(Config{DataDir: dataDir, InspectionPolicy: &policy})
	if !first.evaluateInspectionNotification(policy, accounts, records, now, true) {
		first.Shutdown()
		t.Fatal("initial low-account notification did not trigger")
	}
	first.persist()
	first.Shutdown()

	loaded, errLoad := loadInspectionState(inspectionStorePath(dataDir))
	if errLoad != nil {
		t.Fatalf("load persisted notification state: %v", errLoad)
	}
	if !loaded.LastNotificationAt.Equal(now) {
		t.Fatalf("persisted notification time = %s, want %s", loaded.LastNotificationAt, now)
	}

	restarted := NewInspectionEngine(nil, nil, nil)
	restarted.notificationDoer = successfulNotificationDoer()
	restarted.Configure(Config{DataDir: dataDir})
	t.Cleanup(restarted.Shutdown)
	if snapshot := restarted.Snapshot(); snapshot.LastNotificationAt == nil || !snapshot.LastNotificationAt.Equal(now) {
		t.Fatalf("restarted notification time = %#v, want %s", snapshot.LastNotificationAt, now)
	}
	if restarted.evaluateInspectionNotification(policy, accounts, records, now.Add(59*time.Minute), true) {
		t.Fatal("restarted notification ignored its persisted cooldown")
	}
	if !restarted.evaluateInspectionNotification(policy, accounts, records, now.Add(60*time.Minute), true) {
		t.Fatal("notification did not trigger at the persisted cooldown boundary")
	}
}

func successfulNotificationDoer() anomalyNotificationDoerFunc {
	return func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
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
