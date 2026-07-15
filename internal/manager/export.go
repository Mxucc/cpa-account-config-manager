package manager

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

const (
	ResultExportFormatJSON  = "json"
	ResultExportFormatCSV   = "csv"
	ResultExportFormatJSONL = "jsonl"
)

type exportDownload struct {
	Filename    string
	ContentType string
	Body        []byte
	Credential  bool
	Exported    int
	Skipped     int
}

type resultExportRow struct {
	JobID         string   `json:"job_id,omitempty"`
	ParentJobID   string   `json:"parent_job_id,omitempty"`
	JobState      string   `json:"job_state"`
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	Label         string   `json:"label,omitempty"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
	AppliedFields []string `json:"applied_fields,omitempty"`
	Retryable     bool     `json:"retryable"`
}

func resultExportFormatFromValues(values map[string][]string) (string, error) {
	format := strings.ToLower(firstQuery(values, "format"))
	if format == "" {
		return ResultExportFormatJSON, nil
	}
	if format == "ndjson" {
		format = ResultExportFormatJSONL
	}
	switch format {
	case ResultExportFormatJSON, ResultExportFormatCSV, ResultExportFormatJSONL:
		return format, nil
	default:
		return "", fmt.Errorf("format must be json, csv, or jsonl")
	}
}

func renderResultExport(format string, snapshot JobSnapshot) (exportDownload, error) {
	switch format {
	case ResultExportFormatJSON:
		return renderJSONDownload("cpa-account-config-results.json", snapshot)
	case ResultExportFormatCSV:
		body, errCSV := renderResultCSV(snapshot)
		return exportDownload{Filename: "cpa-account-config-results.csv", ContentType: "text/csv; charset=utf-8", Body: body}, errCSV
	case ResultExportFormatJSONL:
		body, errJSONL := renderResultJSONL(snapshot)
		return exportDownload{Filename: "cpa-account-config-results.jsonl", ContentType: "application/x-ndjson; charset=utf-8", Body: body}, errJSONL
	default:
		return exportDownload{}, fmt.Errorf("unsupported result export format")
	}
}

func renderJSONDownload(filename string, value any) (exportDownload, error) {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return exportDownload{}, fmt.Errorf("encode JSON export: %w", errMarshal)
	}
	return exportDownload{Filename: filename, ContentType: "application/json; charset=utf-8", Body: body}, nil
}

func renderResultCSV(snapshot JobSnapshot) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	writeSafeCSVRow(writer, []string{"job_id", "parent_job_id", "job_state", "id", "name", "provider", "label", "status", "error", "applied_fields", "retryable"})
	for _, row := range resultExportRows(snapshot) {
		writeSafeCSVRow(writer, []string{
			row.JobID, row.ParentJobID, row.JobState, row.ID, row.Name, row.Provider, row.Label, row.Status, row.Error,
			strings.Join(row.AppliedFields, ";"), strconv.FormatBool(row.Retryable),
		})
	}
	writer.Flush()
	if errWrite := writer.Error(); errWrite != nil {
		return nil, fmt.Errorf("encode result CSV export: %w", errWrite)
	}
	return buffer.Bytes(), nil
}

func renderResultJSONL(snapshot JobSnapshot) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, row := range resultExportRows(snapshot) {
		if errEncode := encoder.Encode(row); errEncode != nil {
			return nil, fmt.Errorf("encode result JSON Lines export: %w", errEncode)
		}
	}
	return buffer.Bytes(), nil
}

func resultExportRows(snapshot JobSnapshot) []resultExportRow {
	rows := make([]resultExportRow, 0, len(snapshot.Results))
	for _, result := range snapshot.Results {
		rows = append(rows, resultExportRow{
			JobID:         snapshot.ID,
			ParentJobID:   snapshot.ParentJobID,
			JobState:      snapshot.State,
			ID:            result.ID,
			Name:          result.Name,
			Provider:      result.Provider,
			Label:         result.Label,
			Status:        result.Status,
			Error:         result.Error,
			AppliedFields: append([]string(nil), result.AppliedFields...),
			Retryable:     result.Retryable,
		})
	}
	return rows
}

func writeSafeCSVRow(writer *csv.Writer, values []string) {
	row := make([]string, len(values))
	for index, value := range values {
		row[index] = neutralizeCSVFormula(value)
	}
	_ = writer.Write(row)
}

func neutralizeCSVFormula(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

func exportDownloadResponse(download exportDownload) cpaapi.ManagementResponse {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": download.Filename})
	headers := http.Header{
		"Content-Type":                  []string{download.ContentType},
		"Content-Disposition":           []string{disposition},
		"X-Content-Type-Options":        []string{"nosniff"},
		"Access-Control-Expose-Headers": []string{"Content-Disposition, X-Exported-Accounts, X-Skipped-Accounts"},
	}
	if download.Credential {
		headers.Set("Cache-Control", "no-store, private, max-age=0")
		headers.Set("Pragma", "no-cache")
		headers.Set("Expires", "0")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Exported-Accounts", strconv.Itoa(download.Exported))
		headers.Set("X-Skipped-Accounts", strconv.Itoa(download.Skipped))
	}
	return cpaapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    headers,
		Body:       download.Body,
	}
}
