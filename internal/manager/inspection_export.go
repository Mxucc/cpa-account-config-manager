package manager

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type inspectionExportPayload struct {
	ExportedAt time.Time          `json:"exported_at"`
	Count      int                `json:"count"`
	Results    []InspectionResult `json:"results"`
}

type inspectionDownload struct {
	Filename    string
	ContentType string
	Body        []byte
	Count       int
}

func renderInspectionExport(format string, results []InspectionResult, now time.Time) (inspectionDownload, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "json" && format != "csv" && format != "jsonl" {
		return inspectionDownload{}, fmt.Errorf("inspection export format must be json, csv, or jsonl")
	}
	if len(results) > maxInspectionAccounts {
		results = results[:maxInspectionAccounts]
	}
	results = cloneInspectionResults(results)
	prefix := "cpa-account-inspection-" + now.UTC().Format("20060102-150405")
	download := inspectionDownload{Filename: prefix + "." + format, Count: len(results)}
	switch format {
	case "json":
		raw, errEncode := json.MarshalIndent(inspectionExportPayload{ExportedAt: now.UTC(), Count: len(results), Results: results}, "", "  ")
		if errEncode != nil {
			return inspectionDownload{}, errEncode
		}
		download.ContentType = "application/json; charset=utf-8"
		download.Body = raw
	case "jsonl":
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		for _, result := range results {
			if errEncode := encoder.Encode(result); errEncode != nil {
				return inspectionDownload{}, errEncode
			}
		}
		download.ContentType = "application/x-ndjson; charset=utf-8"
		download.Body = buffer.Bytes()
	case "csv":
		var buffer bytes.Buffer
		writer := csv.NewWriter(&buffer)
		header := []string{"id", "name", "provider", "type", "plan_type", "health", "reason_code", "status_code", "confidence", "recommendation", "disabled", "editable", "review_status", "last_checked_at", "probe_status", "probe_reason_code", "probe_model", "probe_tested_at", "probe_latency_ms", "signal_source"}
		if errWrite := writer.Write(header); errWrite != nil {
			return inspectionDownload{}, errWrite
		}
		for _, result := range results {
			probeTestedAt := ""
			if result.ProbeTestedAt != nil {
				probeTestedAt = result.ProbeTestedAt.UTC().Format(time.RFC3339Nano)
			}
			row := []string{
				inspectionCSVCell(result.ID), inspectionCSVCell(result.Name), inspectionCSVCell(result.Provider), inspectionCSVCell(result.Type), inspectionCSVCell(result.PlanType),
				result.Health, result.ReasonCode, strconv.Itoa(result.StatusCode), result.Confidence, result.Recommendation,
				strconv.FormatBool(result.Disabled), strconv.FormatBool(result.Editable), result.ReviewStatus,
				result.LastCheckedAt.UTC().Format(time.RFC3339Nano), result.ProbeStatus, result.ProbeReasonCode,
				inspectionCSVCell(result.ProbeModel), probeTestedAt, strconv.FormatInt(result.ProbeLatencyMS, 10), result.SignalSource,
			}
			if errWrite := writer.Write(row); errWrite != nil {
				return inspectionDownload{}, errWrite
			}
		}
		writer.Flush()
		if errWrite := writer.Error(); errWrite != nil {
			return inspectionDownload{}, errWrite
		}
		download.ContentType = "text/csv; charset=utf-8"
		download.Body = buffer.Bytes()
	}
	return download, nil
}

func cloneInspectionResults(results []InspectionResult) []InspectionResult {
	out := make([]InspectionResult, 0, len(results))
	for _, result := range results {
		out = append(out, cloneInspectionResult(result))
	}
	return out
}

func inspectionCSVCell(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + value
	default:
		return value
	}
}
