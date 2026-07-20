package manager

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type operationExportPayload struct {
	ExportedAt time.Time        `json:"exported_at"`
	Count      int              `json:"count"`
	Operations []OperationEntry `json:"operations"`
}

type operationDownload struct {
	Filename    string
	ContentType string
	Body        []byte
	Count       int
}

func renderOperationExport(format string, entries []OperationEntry, now time.Time) (operationDownload, error) {
	format = safeOperationFormat(format)
	if format != "json" && format != "csv" && format != "jsonl" {
		return operationDownload{}, fmt.Errorf("operation export format must be json, csv, or jsonl")
	}
	prefix := "cpa-account-operations-" + now.UTC().Format("20060102-150405")
	download := operationDownload{Filename: prefix + "." + format, Count: len(entries)}
	switch format {
	case "json":
		raw, errEncode := json.MarshalIndent(operationExportPayload{ExportedAt: now.UTC(), Count: len(entries), Operations: entries}, "", "  ")
		if errEncode != nil {
			return operationDownload{}, errEncode
		}
		download.ContentType = "application/json; charset=utf-8"
		download.Body = raw
	case "jsonl":
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		for _, entry := range entries {
			if errEncode := encoder.Encode(entry); errEncode != nil {
				return operationDownload{}, errEncode
			}
		}
		download.ContentType = "application/x-ndjson; charset=utf-8"
		download.Body = buffer.Bytes()
	case "csv":
		var buffer bytes.Buffer
		writer := csv.NewWriter(&buffer)
		header := []string{"id", "category", "action", "status", "source", "scope", "target_id", "target_count", "succeeded", "failed", "skipped", "started_at", "finished_at", "reason_code", "related_job_id", "related_action_id", "version", "format", "model"}
		if errWrite := writer.Write(header); errWrite != nil {
			return operationDownload{}, errWrite
		}
		for _, entry := range entries {
			finishedAt := ""
			if !entry.FinishedAt.IsZero() {
				finishedAt = entry.FinishedAt.UTC().Format(time.RFC3339Nano)
			}
			row := []string{
				entry.ID, entry.Category, entry.Action, entry.Status, entry.Source, entry.Scope, entry.TargetID,
				strconv.Itoa(entry.TargetCount), strconv.Itoa(entry.Succeeded), strconv.Itoa(entry.Failed), strconv.Itoa(entry.Skipped),
				entry.StartedAt.UTC().Format(time.RFC3339Nano), finishedAt, entry.ReasonCode, entry.RelatedJobID,
				entry.RelatedActionID, entry.Version, entry.Format, entry.Model,
			}
			if errWrite := writer.Write(row); errWrite != nil {
				return operationDownload{}, errWrite
			}
		}
		writer.Flush()
		if errWrite := writer.Error(); errWrite != nil {
			return operationDownload{}, errWrite
		}
		download.ContentType = "text/csv; charset=utf-8"
		download.Body = buffer.Bytes()
	}
	return download, nil
}
