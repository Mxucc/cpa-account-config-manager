package manager

import (
	"encoding/json"
	"testing"
	"time"
)

// A passive usage observation creates a record before anything was inspected.
// That placeholder carries no health and no check time, so it is not a result:
// publishing it sent empty enum fields a page rejects as an invalid response.
func TestInspectionResultsSkipPlaceholdersAndPublishNonEmptyEnums(t *testing.T) {
	now := time.Date(2026, time.November, 8, 10, 0, 0, 0, time.UTC)
	engine := NewInspectionEngine(nil, nil, nil)
	engine.now = func() time.Time { return now }
	engine.records["placeholder"] = inspectionRecord{Result: InspectionResult{ID: "placeholder"}}
	engine.records["checked"] = inspectionRecord{Result: InspectionResult{
		ID:            "checked",
		Name:          "codex-thomastaylor.json",
		Provider:      "codex",
		LastCheckedAt: now,
	}}

	list := engine.ListResults(InspectionResultQuery{Page: 1, PageSize: 50})
	if len(list.Results) != 1 {
		t.Fatalf("results = %d, want only the inspected record: %+v", len(list.Results), list.Results)
	}
	if list.Total != 1 {
		t.Fatalf("total = %d, want 1", list.Total)
	}

	raw, errMarshal := json.Marshal(list.Results[0])
	if errMarshal != nil {
		t.Fatalf("marshal result: %v", errMarshal)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal result: %v", errUnmarshal)
	}
	for _, key := range []string{"health", "reason_code", "confidence", "recommendation"} {
		value, found := decoded[key]
		if !found {
			t.Fatalf("%s is missing from %s", key, raw)
		}
		text, isString := value.(string)
		if !isString || text == "" {
			t.Fatalf("%s serialized as %#v, want a non-empty string: %s", key, value, raw)
		}
	}
}

// A record that was inspected but never classified must still answer with a
// usable value rather than an empty string.
func TestPublishInspectionResultNormalizesMissingEnums(t *testing.T) {
	published := publishInspectionResult(InspectionResult{ID: "acct-1", LastCheckedAt: time.Unix(0, 0).UTC()})
	if published.Health != InspectionHealthUnknown {
		t.Fatalf("health = %q, want %q", published.Health, InspectionHealthUnknown)
	}
	if published.ReasonCode != InspectionReasonUnknown {
		t.Fatalf("reason_code = %q, want %q", published.ReasonCode, InspectionReasonUnknown)
	}
	if published.Confidence != InspectionConfidenceLow {
		t.Fatalf("confidence = %q, want %q", published.Confidence, InspectionConfidenceLow)
	}
	if published.Recommendation == "" {
		t.Fatal("recommendation is empty")
	}
}

// The live view is fed by the same client validators, so it must publish the
// same shape even mid-run.
func TestLiveInspectionResultsPublishNonEmptyEnums(t *testing.T) {
	observedAt := time.Date(2026, time.November, 8, 10, 0, 0, 0, time.UTC)
	records := map[string]inspectionRecord{
		"mid-run": {Result: InspectionResult{ID: "mid-run", RunID: "run-1", RunObservedAt: &observedAt}},
	}
	live := liveInspectionResults(records, "run-1", 12)
	if len(live) != 1 {
		t.Fatalf("live results = %d, want 1", len(live))
	}
	if live[0].Health == "" || live[0].ReasonCode == "" || live[0].Confidence == "" || live[0].Recommendation == "" {
		t.Fatalf("live result has an empty enum: %+v", live[0])
	}
}
