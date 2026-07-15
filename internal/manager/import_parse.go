package manager

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultImportMaxRequestBytes  = 12 << 20
	defaultImportMaxUploadFiles   = 64
	defaultImportMaxArchiveFiles  = 256
	defaultImportMaxEntryBytes    = 2 << 20
	defaultImportMaxExpandedBytes = 32 << 20
	defaultImportMaxJSONDepth     = 32
	defaultImportMaxJSONNodes     = 50_000
	defaultImportMaxAccounts      = 10_000
	defaultImportMaxRatio         = 200
)

var (
	errImportNotCandidate      = errors.New("not an import candidate")
	errImportJSONDocumentLimit = errors.New("JSON document count exceeds the node limit")
)

type importLimits struct {
	MaxRequestBytes  int
	MaxUploadFiles   int
	MaxArchiveFiles  int
	MaxEntryBytes    int64
	MaxExpandedBytes int64
	MaxJSONDepth     int
	MaxJSONNodes     int
	MaxAccounts      int
	MaxRatio         uint64
}

type importUpload struct {
	Name        string
	ContentType string
	Data        []byte
}

type importCandidate struct {
	SourceName       string
	SourcePath       string
	Email            string
	AccountID        string
	Name             string
	SyntheticIDToken bool
	Warnings         []string
	AuthJSON         json.RawMessage
	fingerprint      string
}

type importSkipped struct {
	SourceName string `json:"source_name"`
	SourcePath string `json:"source_path,omitempty"`
	Reason     string `json:"reason"`
}

type importParseResult struct {
	SourceFiles int
	Candidates  []importCandidate
	Skipped     []importSkipped
}

type importCollector struct {
	limits       importLimits
	now          time.Time
	nodes        int
	candidates   []importCandidate
	skipped      []importSkipped
	fingerprints map[string]struct{}
}

func defaultImportLimits() importLimits {
	return importLimits{
		MaxRequestBytes:  defaultImportMaxRequestBytes,
		MaxUploadFiles:   defaultImportMaxUploadFiles,
		MaxArchiveFiles:  defaultImportMaxArchiveFiles,
		MaxEntryBytes:    defaultImportMaxEntryBytes,
		MaxExpandedBytes: defaultImportMaxExpandedBytes,
		MaxJSONDepth:     defaultImportMaxJSONDepth,
		MaxJSONNodes:     defaultImportMaxJSONNodes,
		MaxAccounts:      defaultImportMaxAccounts,
		MaxRatio:         defaultImportMaxRatio,
	}
}

func parseImportUpload(upload importUpload, limits importLimits, now time.Time) (importParseResult, error) {
	return parseImportUploadsInternal([]importUpload{upload}, limits, now, true)
}

func parseImportUploads(uploads []importUpload, limits importLimits, now time.Time) (importParseResult, error) {
	return parseImportUploadsInternal(uploads, limits, now, false)
}

func parseImportUploadsInternal(uploads []importUpload, limits importLimits, now time.Time, strictSingle bool) (importParseResult, error) {
	limits = normalizeImportLimits(limits)
	if len(uploads) == 0 {
		return importParseResult{}, fmt.Errorf("at least one import file is required")
	}
	if len(uploads) > limits.MaxUploadFiles {
		return importParseResult{}, fmt.Errorf("import contains more than %d uploaded files", limits.MaxUploadFiles)
	}
	collector := &importCollector{
		limits:       limits,
		now:          now.UTC(),
		fingerprints: make(map[string]struct{}),
	}
	result := importParseResult{}
	var requestBytes int
	var archiveEntries int
	var expandedBytes int64
	for uploadIndex, upload := range uploads {
		name := strings.TrimSpace(upload.Name)
		if name == "" {
			name = fmt.Sprintf("import-%02d.json", uploadIndex+1)
		}
		name = filepath.Base(name)
		if len(upload.Data) == 0 {
			if strictSingle {
				return importParseResult{}, fmt.Errorf("import file is empty")
			}
			collector.skipped = append(collector.skipped, importSkipped{SourceName: name, Reason: "uploaded file is empty"})
			continue
		}
		requestBytes += len(upload.Data)
		if requestBytes > limits.MaxRequestBytes {
			return importParseResult{}, fmt.Errorf("import files exceed the %s request limit", formatByteLimit(int64(limits.MaxRequestBytes)))
		}

		if importUploadIsZIP(upload) {
			zipLimits := limits
			zipLimits.MaxArchiveFiles = limits.MaxArchiveFiles - archiveEntries
			zipLimits.MaxExpandedBytes = limits.MaxExpandedBytes - expandedBytes
			if zipLimits.MaxArchiveFiles <= 0 || zipLimits.MaxExpandedBytes <= 0 {
				return importParseResult{}, fmt.Errorf("ZIP archive limits were exceeded")
			}
			zipResult, errZIP := readImportZIP(upload.Data, zipLimits)
			if errZIP != nil {
				return importParseResult{}, fmt.Errorf("inspect %s: %w", name, errZIP)
			}
			archiveEntries += zipResult.Entries
			expandedBytes += zipResult.ExpandedBytes
			for _, skipped := range zipResult.Skipped {
				if len(uploads) > 1 {
					skipped.SourceName = name + "!/" + skipped.SourceName
				}
				collector.skipped = append(collector.skipped, skipped)
			}
			for _, source := range zipResult.Sources {
				sourceName := source.Name
				if len(uploads) > 1 {
					sourceName = name + "!/" + source.Name
				}
				documents, errDecode := decodeImportJSONDocuments(source.Data, limits.MaxJSONNodes-collector.nodes)
				if errDecode != nil {
					if errors.Is(errDecode, errImportJSONDocumentLimit) {
						return importParseResult{}, fmt.Errorf("inspect %s: %w", sourceName, errDecode)
					}
					collector.skipped = append(collector.skipped, importSkipped{
						SourceName: sourceName,
						Reason:     "entry does not contain valid JSON",
					})
					continue
				}
				result.SourceFiles++
				for documentIndex, value := range documents {
					if errVisit := collector.visit(value, sourceName, importDocumentPath(len(documents), documentIndex), 0); errVisit != nil {
						return importParseResult{}, fmt.Errorf("inspect %s: %w", sourceName, errVisit)
					}
				}
			}
			continue
		}

		documents, errDecode := decodeImportJSONDocuments(upload.Data, limits.MaxJSONNodes-collector.nodes)
		if errDecode != nil {
			if strictSingle || errors.Is(errDecode, errImportJSONDocumentLimit) {
				return importParseResult{}, fmt.Errorf("invalid JSON in %s: %w", name, errDecode)
			}
			collector.skipped = append(collector.skipped, importSkipped{SourceName: name, Reason: "uploaded file does not contain valid JSON"})
			continue
		}
		result.SourceFiles++
		for documentIndex, value := range documents {
			if errVisit := collector.visit(value, name, importDocumentPath(len(documents), documentIndex), 0); errVisit != nil {
				return importParseResult{}, fmt.Errorf("inspect %s: %w", name, errVisit)
			}
		}
	}
	result.Candidates = collector.candidates
	result.Skipped = collector.skipped
	return result, nil
}

func normalizeImportLimits(limits importLimits) importLimits {
	defaults := defaultImportLimits()
	if limits.MaxRequestBytes <= 0 {
		limits.MaxRequestBytes = defaults.MaxRequestBytes
	}
	if limits.MaxUploadFiles <= 0 {
		limits.MaxUploadFiles = defaults.MaxUploadFiles
	}
	if limits.MaxArchiveFiles <= 0 {
		limits.MaxArchiveFiles = defaults.MaxArchiveFiles
	}
	if limits.MaxEntryBytes <= 0 {
		limits.MaxEntryBytes = defaults.MaxEntryBytes
	}
	if limits.MaxExpandedBytes <= 0 {
		limits.MaxExpandedBytes = defaults.MaxExpandedBytes
	}
	if limits.MaxJSONDepth <= 0 {
		limits.MaxJSONDepth = defaults.MaxJSONDepth
	}
	if limits.MaxJSONNodes <= 0 {
		limits.MaxJSONNodes = defaults.MaxJSONNodes
	}
	if limits.MaxAccounts <= 0 {
		limits.MaxAccounts = defaults.MaxAccounts
	}
	if limits.MaxRatio == 0 {
		limits.MaxRatio = defaults.MaxRatio
	}
	return limits
}

type importJSONSource struct {
	Name string
	Data []byte
}

type importZIPReadResult struct {
	Sources       []importJSONSource
	Skipped       []importSkipped
	Entries       int
	ExpandedBytes int64
}

func readImportZIP(raw []byte, limits importLimits) (importZIPReadResult, error) {
	reader, errOpen := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if errOpen != nil {
		return importZIPReadResult{}, fmt.Errorf("invalid ZIP archive: %w", errOpen)
	}
	if len(reader.File) > limits.MaxArchiveFiles {
		return importZIPReadResult{}, fmt.Errorf("ZIP archives contain more than %d entries in total", defaultImportMaxArchiveFiles)
	}

	sources := make([]importJSONSource, 0, len(reader.File))
	skipped := make([]importSkipped, 0)
	var expanded int64
	for _, entry := range reader.File {
		if entry == nil {
			continue
		}
		if errName := validateImportZIPPath(entry.Name); errName != nil {
			return importZIPReadResult{}, errName
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			return importZIPReadResult{}, fmt.Errorf("ZIP entry %q is a symbolic link", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Flags&0x1 != 0 {
			return importZIPReadResult{}, fmt.Errorf("ZIP entry %q is encrypted", entry.Name)
		}
		if entry.Method != zip.Store && entry.Method != zip.Deflate {
			return importZIPReadResult{}, fmt.Errorf("ZIP entry %q uses an unsupported compression method", entry.Name)
		}
		if !isImportJSONSourceName(entry.Name) {
			skipped = append(skipped, importSkipped{SourceName: entry.Name, Reason: "entry is not a JSON or text JSON file"})
			continue
		}
		if entry.UncompressedSize64 > uint64(limits.MaxEntryBytes) {
			return importZIPReadResult{}, fmt.Errorf("ZIP expanded size exceeds the %s per-entry limit", formatByteLimit(limits.MaxEntryBytes))
		}
		if entry.UncompressedSize64 > 0 && entry.CompressedSize64 > 0 && entry.UncompressedSize64/entry.CompressedSize64 > limits.MaxRatio {
			return importZIPReadResult{}, fmt.Errorf("ZIP entry %q exceeds the compression-ratio limit", entry.Name)
		}
		if expanded+int64(entry.UncompressedSize64) > limits.MaxExpandedBytes {
			return importZIPReadResult{}, fmt.Errorf("ZIP expanded size exceeds the %s aggregate limit", formatByteLimit(defaultImportMaxExpandedBytes))
		}

		handle, errEntry := entry.Open()
		if errEntry != nil {
			return importZIPReadResult{}, fmt.Errorf("open ZIP entry %q: %w", entry.Name, errEntry)
		}
		data, errRead := io.ReadAll(io.LimitReader(handle, limits.MaxEntryBytes+1))
		errClose := handle.Close()
		if errRead != nil {
			return importZIPReadResult{}, fmt.Errorf("read ZIP entry %q: %w", entry.Name, errRead)
		}
		if errClose != nil {
			return importZIPReadResult{}, fmt.Errorf("close ZIP entry %q: %w", entry.Name, errClose)
		}
		if int64(len(data)) > limits.MaxEntryBytes || expanded+int64(len(data)) > limits.MaxExpandedBytes {
			return importZIPReadResult{}, fmt.Errorf("ZIP expanded size exceeds the configured limit")
		}
		expanded += int64(len(data))
		sources = append(sources, importJSONSource{Name: entry.Name, Data: data})
	}
	return importZIPReadResult{Sources: sources, Skipped: skipped, Entries: len(reader.File), ExpandedBytes: expanded}, nil
}

func validateImportZIPPath(name string) error {
	if name == "" || len(name) > 240 || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("ZIP entry has an unsafe path")
	}
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.IsAbs(name) {
		return fmt.Errorf("ZIP entry %q has an unsafe path", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != strings.TrimSuffix(name, "/") {
		return fmt.Errorf("ZIP entry %q has an unsafe path", name)
	}
	return nil
}

func importUploadIsZIP(upload importUpload) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(upload.ContentType, ";")[0]))
	if contentType == "application/zip" || contentType == "application/x-zip-compressed" {
		return true
	}
	if strings.EqualFold(filepath.Ext(upload.Name), ".zip") {
		return true
	}
	return len(upload.Data) >= 4 && bytes.Equal(upload.Data[:4], []byte{'P', 'K', 0x03, 0x04})
}

func isImportJSONSourceName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".jsonl", ".ndjson", ".txt":
		return true
	default:
		return false
	}
}

func decodeImportJSONDocuments(raw []byte, maxDocuments int) ([]any, error) {
	if maxDocuments <= 0 {
		return nil, fmt.Errorf("%w: JSON structure exceeds the configured node limit", errImportJSONDocumentLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	documents := make([]any, 0, 1)
	for {
		var value any
		errDecode := decoder.Decode(&value)
		if errors.Is(errDecode, io.EOF) {
			break
		}
		if errDecode != nil {
			return nil, errDecode
		}
		if len(documents) >= maxDocuments {
			return nil, fmt.Errorf("%w: JSON structure exceeds the configured node limit", errImportJSONDocumentLimit)
		}
		documents = append(documents, value)
	}
	if len(documents) == 0 {
		return nil, fmt.Errorf("file must contain at least one JSON value")
	}
	return documents, nil
}

func importDocumentPath(total, index int) string {
	if total <= 1 {
		return "$"
	}
	return fmt.Sprintf("$document[%d]", index)
}

func (c *importCollector) visit(value any, sourceName, sourcePath string, depth int) error {
	if depth > c.limits.MaxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", c.limits.MaxJSONDepth)
	}
	c.nodes++
	if c.nodes > c.limits.MaxJSONNodes {
		return fmt.Errorf("JSON structure exceeds %d nodes", c.limits.MaxJSONNodes)
	}

	switch typed := value.(type) {
	case map[string]any:
		candidate, errConvert := convertImportRecord(typed, sourceName, sourcePath, c.now)
		if errConvert == nil {
			if _, duplicate := c.fingerprints[candidate.fingerprint]; duplicate {
				c.skipped = append(c.skipped, importSkipped{SourceName: sourceName, SourcePath: sourcePath, Reason: "duplicate credential record"})
				return nil
			}
			if len(c.candidates) >= c.limits.MaxAccounts {
				return fmt.Errorf("import contains more than %d accounts", c.limits.MaxAccounts)
			}
			c.fingerprints[candidate.fingerprint] = struct{}{}
			c.candidates = append(c.candidates, candidate)
			return nil
		}
		if !errors.Is(errConvert, errImportNotCandidate) {
			c.skipped = append(c.skipped, importSkipped{SourceName: sourceName, SourcePath: sourcePath, Reason: errConvert.Error()})
			return nil
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if errVisit := c.visit(typed[key], sourceName, sourcePath+"."+key, depth+1); errVisit != nil {
				return errVisit
			}
		}
	case []any:
		for index, child := range typed {
			if errVisit := c.visit(child, sourceName, fmt.Sprintf("%s[%d]", sourcePath, index), depth+1); errVisit != nil {
				return errVisit
			}
		}
	}
	return nil
}

func convertImportRecord(record map[string]any, sourceName, sourcePath string, now time.Time) (importCandidate, error) {
	accessToken := firstImportString(record,
		[]string{"accessToken"}, []string{"access_token"},
		[]string{"tokens", "accessToken"}, []string{"tokens", "access_token"},
		[]string{"token", "accessToken"}, []string{"token", "access_token"},
		[]string{"credentials", "accessToken"}, []string{"credentials", "access_token"},
		[]string{"credential", "accessToken"}, []string{"credential", "access_token"},
		[]string{"auth", "accessToken"}, []string{"auth", "access_token"},
	)
	if accessToken == "" {
		return importCandidate{}, errImportNotCandidate
	}

	accessPayload := parseImportJWTPayload(accessToken)
	idTokenInput := firstImportString(record,
		[]string{"idToken"}, []string{"id_token"},
		[]string{"tokens", "idToken"}, []string{"tokens", "id_token"},
		[]string{"token", "idToken"}, []string{"token", "id_token"},
		[]string{"credentials", "idToken"}, []string{"credentials", "id_token"},
		[]string{"credential", "idToken"}, []string{"credential", "id_token"},
	)
	idPayload := parseImportJWTPayload(idTokenInput)
	accessAuth := importObjectAt(accessPayload, "https://api.openai.com/auth")
	idAuth := importObjectAt(idPayload, "https://api.openai.com/auth")
	accessProfile := importObjectAt(accessPayload, "https://api.openai.com/profile")

	email := firstImportString(record,
		[]string{"user", "email"}, []string{"email"}, []string{"profile", "email"},
		[]string{"meta", "label"}, []string{"label"}, []string{"credentials", "email"},
		[]string{"credential", "email"}, []string{"providerSpecificData", "email"},
	)
	if email == "" {
		email = firstStringFromMaps([]map[string]any{accessProfile, idPayload, accessPayload}, "email")
	}
	accountID := firstImportString(record,
		[]string{"account", "id"}, []string{"account_id"}, []string{"accountId"},
		[]string{"tokens", "accountId"}, []string{"tokens", "account_id"},
		[]string{"chatgptAccountId"}, []string{"chatgpt_account_id"},
		[]string{"meta", "chatgptAccountId"}, []string{"meta", "chatgpt_account_id"},
		[]string{"tokens", "chatgptAccountId"}, []string{"tokens", "chatgpt_account_id"},
		[]string{"providerSpecificData", "chatgptAccountId"}, []string{"providerSpecificData", "chatgpt_account_id"},
		[]string{"credentials", "chatgpt_account_id"}, []string{"credential", "chatgpt_account_id"},
	)
	if accountID == "" {
		accountID = firstStringFromMaps([]map[string]any{accessAuth, idAuth}, "chatgpt_account_id", "account_id")
	}
	if accountID == "" && strings.EqualFold(firstImportString(record, []string{"provider"}), "codex") {
		accountID = firstImportString(record, []string{"id"})
	}
	displayName := firstImportString(record, []string{"name"}, []string{"label"}, []string{"meta", "label"})
	if email == "" && accountID == "" && displayName == "" {
		return importCandidate{}, fmt.Errorf("record has an access token but no email or account id")
	}

	refreshToken := firstImportString(record,
		[]string{"refreshToken"}, []string{"refresh_token"},
		[]string{"tokens", "refreshToken"}, []string{"tokens", "refresh_token"},
		[]string{"token", "refreshToken"}, []string{"token", "refresh_token"},
		[]string{"credentials", "refreshToken"}, []string{"credentials", "refresh_token"},
		[]string{"credential", "refreshToken"}, []string{"credential", "refresh_token"},
	)
	sessionToken := firstImportString(record,
		[]string{"sessionToken"}, []string{"session_token"},
		[]string{"tokens", "sessionToken"}, []string{"tokens", "session_token"},
		[]string{"token", "sessionToken"}, []string{"token", "session_token"},
		[]string{"credentials", "session_token"}, []string{"credential", "session_token"},
	)
	planType := firstImportString(record,
		[]string{"account", "planType"}, []string{"account", "plan_type"},
		[]string{"planType"}, []string{"plan_type"}, []string{"chatgpt_plan_type"},
		[]string{"providerSpecificData", "chatgptPlanType"}, []string{"providerSpecificData", "chatgpt_plan_type"},
		[]string{"credentials", "plan_type"},
	)
	if planType == "" {
		planType = firstStringFromMaps([]map[string]any{accessAuth, idAuth}, "chatgpt_plan_type", "plan_type")
	}
	userID := firstImportString(record,
		[]string{"user", "id"}, []string{"user_id"}, []string{"chatgptUserId"}, []string{"chatgpt_user_id"},
		[]string{"providerSpecificData", "chatgptUserId"}, []string{"providerSpecificData", "chatgpt_user_id"},
	)
	if userID == "" {
		userID = firstStringFromMaps([]map[string]any{accessAuth, idAuth}, "chatgpt_user_id", "user_id")
	}

	expiresAt := ""
	if refreshToken == "" {
		expiresAt = importJWTExpiry(accessPayload)
		if expiresAt == "" {
			expiresAt = firstImportTimestamp(record,
				[]string{"expires"}, []string{"expiresAt"}, []string{"expired"}, []string{"expires_at"},
			)
		}
	}
	idToken := idTokenInput
	syntheticIDToken := false
	if idToken == "" && accountID != "" {
		idToken = buildSyntheticImportIDToken(email, accountID, planType, userID, expiresAt, now)
		syntheticIDToken = idToken != ""
	}
	name := firstNonEmptyImportString(displayName, email, strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName)), "Imported Codex account")

	document := map[string]any{
		"type":          "codex",
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"last_refresh":  now.UTC().Format(time.RFC3339),
		"name":          name,
	}
	setImportString(document, "account_id", accountID)
	setImportString(document, "chatgpt_account_id", accountID)
	setImportString(document, "email", email)
	setImportString(document, "plan_type", planType)
	setImportString(document, "chatgpt_plan_type", planType)
	setImportString(document, "id_token", idToken)
	setImportString(document, "session_token", sessionToken)
	setImportString(document, "expired", expiresAt)
	if syntheticIDToken {
		document["id_token_synthetic"] = true
	}
	copyImportConfiguration(record, document)

	authJSON, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return importCandidate{}, fmt.Errorf("encode converted auth JSON: %w", errMarshal)
	}
	warnings := make([]string, 0, 3)
	if refreshToken == "" {
		warnings = append(warnings, "refresh token is missing")
	}
	if syntheticIDToken {
		warnings = append(warnings, "ID token was synthesized from account metadata")
	}
	if accountID == "" {
		warnings = append(warnings, "account ID is missing")
	}
	fingerprintSum := sha256.Sum256([]byte(accessToken + "\x00" + accountID))
	return importCandidate{
		SourceName:       sourceName,
		SourcePath:       sourcePath,
		Email:            email,
		AccountID:        accountID,
		Name:             name,
		SyntheticIDToken: syntheticIDToken,
		Warnings:         warnings,
		AuthJSON:         authJSON,
		fingerprint:      base64.RawURLEncoding.EncodeToString(fingerprintSum[:]),
	}, nil
}

func copyImportConfiguration(source, destination map[string]any) {
	if disabled, ok := importBoolAt(source, "disabled"); ok && disabled {
		destination["disabled"] = true
	}
	if priority, ok := importIntegerAt(source, "priority"); ok {
		destination["priority"] = priority
	}
	for _, field := range []string{"note", "prefix", "proxy_url"} {
		setImportString(destination, field, firstImportString(source, []string{field}))
	}
	if websockets, ok := importBoolAt(source, "websockets"); ok {
		destination["websockets"] = websockets
	}
	if headers := importStringMapAt(source, "headers"); len(headers) > 0 {
		destination["headers"] = headers
	}
}

func importValueAt(record map[string]any, keys ...string) any {
	var current any = record
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[key]
		if !ok {
			return nil
		}
	}
	return current
}

func firstImportString(record map[string]any, paths ...[]string) string {
	for _, itemPath := range paths {
		if value := importScalarString(importValueAt(record, itemPath...)); value != "" {
			return value
		}
	}
	return ""
}

func importScalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func firstNonEmptyImportString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstStringFromMaps(objects []map[string]any, keys ...string) string {
	for _, object := range objects {
		for _, key := range keys {
			if value := importScalarString(object[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func importObjectAt(record map[string]any, keys ...string) map[string]any {
	value := importValueAt(record, keys...)
	object, _ := value.(map[string]any)
	return object
}

func parseImportJWTPayload(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 || parts[1] == "" {
		return nil
	}
	decoded, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		decoded, errDecode = base64.URLEncoding.DecodeString(parts[1])
	}
	if errDecode != nil {
		return nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	if errJSON := decoder.Decode(&payload); errJSON != nil {
		return nil
	}
	return payload
}

func importJWTExpiry(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	seconds, ok := importNumber(payload["exp"])
	if !ok || seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func firstImportTimestamp(record map[string]any, paths ...[]string) string {
	for _, itemPath := range paths {
		if value := normalizeImportTimestamp(importValueAt(record, itemPath...)); value != "" {
			return value
		}
	}
	return ""
}

func normalizeImportTimestamp(value any) string {
	if seconds, ok := importNumber(value); ok {
		if seconds > 100_000_000_000 {
			seconds /= 1000
		}
		return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
	}
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if errParse != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func importNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		if integer, errInteger := typed.Int64(); errInteger == nil {
			return integer, true
		}
		floating, errFloat := typed.Float64()
		return int64(floating), errFloat == nil
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case string:
		integer, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return integer, errParse == nil
	default:
		return 0, false
	}
}

func buildSyntheticImportIDToken(email, accountID, planType, userID, expiresAt string, now time.Time) string {
	if strings.TrimSpace(accountID) == "" {
		return ""
	}
	expires := now.Add(90 * 24 * time.Hour).Unix()
	if parsed, errParse := time.Parse(time.RFC3339, expiresAt); errParse == nil {
		expires = parsed.Unix()
	}
	auth := map[string]any{"chatgpt_account_id": accountID}
	setImportString(auth, "chatgpt_plan_type", planType)
	setImportString(auth, "chatgpt_user_id", userID)
	setImportString(auth, "user_id", userID)
	payload := map[string]any{
		"iat":                         now.Unix(),
		"exp":                         expires,
		"https://api.openai.com/auth": auth,
	}
	setImportString(payload, "email", email)
	header := map[string]any{"alg": "none", "typ": "JWT", "cpa_synthetic": true}
	return encodeImportJWTPart(header) + "." + encodeImportJWTPart(payload) + ".synthetic"
}

func encodeImportJWTPart(value any) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func setImportString(destination map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		destination[key] = strings.TrimSpace(value)
	}
}

func importBoolAt(record map[string]any, key string) (bool, bool) {
	value, ok := record[key]
	if !ok {
		return false, false
	}
	boolean, ok := value.(bool)
	return boolean, ok
}

func importIntegerAt(record map[string]any, key string) (int64, bool) {
	return importNumber(record[key])
}

func importStringMapAt(record map[string]any, key string) map[string]string {
	object, ok := record[key].(map[string]any)
	if !ok || len(object) == 0 || len(object) > 64 {
		return nil
	}
	result := make(map[string]string, len(object))
	for name, raw := range object {
		value, ok := raw.(string)
		name = strings.TrimSpace(name)
		if !ok || name == "" || len(name) > 128 || len(value) > 8<<10 {
			continue
		}
		result[name] = value
	}
	return result
}

func formatByteLimit(size int64) string {
	if size%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", size/(1<<20))
	}
	if size%(1<<10) == 0 {
		return fmt.Sprintf("%d KiB", size/(1<<10))
	}
	return fmt.Sprintf("%d bytes", size)
}
