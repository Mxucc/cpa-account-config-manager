package manager

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderInspectionExportSupportsSanitizedFormatsAndProtectsCSV(t *testing.T) {
	now := time.Date(2026, time.July, 21, 11, 0, 0, 0, time.UTC)
	results := []InspectionResult{{
		ID: "review-account", Name: "=formula.json", Provider: "codex", Type: "oauth", PlanType: "k12",
		Health: InspectionHealthReview, ReasonCode: "authentication_review", StatusCode: 401,
		Confidence: InspectionConfidenceLow, Recommendation: InspectionRecommendationReview,
		ReviewStatus: InspectionReviewPending, LastCheckedAt: now,
	}}
	for _, format := range []string{"json", "jsonl", "csv"} {
		download, errRender := renderInspectionExport(format, results, now)
		if errRender != nil {
			t.Fatalf("render %s: %v", format, errRender)
		}
		if download.Count != 1 || len(download.Body) == 0 || !strings.HasSuffix(download.Filename, "."+format) {
			t.Fatalf("%s download = %#v", format, download)
		}
		for _, secret := range []string{"access_token", "management-secret", "authorization"} {
			if bytes.Contains(bytes.ToLower(download.Body), []byte(secret)) {
				t.Fatalf("%s export leaked %q: %s", format, secret, download.Body)
			}
		}
	}
	csvDownload, _ := renderInspectionExport("csv", results, now)
	rows, errRead := csv.NewReader(bytes.NewReader(csvDownload.Body)).ReadAll()
	if errRead != nil {
		t.Fatalf("read csv: %v", errRead)
	}
	if rows[1][1] != "'=formula.json" {
		t.Fatalf("CSV formula cell = %q", rows[1][1])
	}

	jsonDownload, _ := renderInspectionExport("json", results, now)
	var payload inspectionExportPayload
	if errDecode := json.Unmarshal(jsonDownload.Body, &payload); errDecode != nil || len(payload.Results) != 1 || payload.Results[0].StatusCode != 401 {
		t.Fatalf("JSON payload = %#v, error=%v", payload, errDecode)
	}
}

func TestRenderInspectionExportRejectsUnknownFormat(t *testing.T) {
	if _, errRender := renderInspectionExport("xlsx", nil, time.Now()); errRender == nil {
		t.Fatal("unsupported inspection export format was accepted")
	}
}
