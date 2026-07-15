package manager

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"path/filepath"
	"strings"

	"cpa-account-config-manager/internal/cpaapi"
)

func importUploadsFromRequest(req cpaapi.ManagementRequest, limits importLimits) ([]importUpload, bool, error) {
	limits = normalizeImportLimits(limits)
	if len(req.Body) > limits.MaxRequestBytes {
		return nil, false, fmt.Errorf("import request exceeds the %s limit", formatByteLimit(int64(limits.MaxRequestBytes)))
	}
	rawContentType := headerValue(req.Headers, "Content-Type")
	mediaType, parameters, errMediaType := mime.ParseMediaType(rawContentType)
	if errMediaType == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		boundary := strings.TrimSpace(parameters["boundary"])
		if boundary == "" {
			return nil, true, fmt.Errorf("multipart boundary is required")
		}
		reader := multipart.NewReader(bytes.NewReader(req.Body), boundary)
		uploads := make([]importUpload, 0)
		for {
			part, errPart := reader.NextPart()
			if errPart == io.EOF {
				break
			}
			if errPart != nil {
				return nil, true, fmt.Errorf("invalid multipart import: %w", errPart)
			}
			filename := strings.TrimSpace(part.FileName())
			if filename == "" {
				_ = part.Close()
				continue
			}
			if len(uploads) >= limits.MaxUploadFiles {
				_ = part.Close()
				return nil, true, fmt.Errorf("import contains more than %d uploaded files", limits.MaxUploadFiles)
			}
			filename = filepath.Base(filename)
			if filename == "" || filename == "." || len(filename) > 240 || strings.ContainsRune(filename, '\x00') {
				_ = part.Close()
				return nil, true, fmt.Errorf("multipart import filename is invalid")
			}
			data, errRead := io.ReadAll(io.LimitReader(part, int64(limits.MaxRequestBytes)+1))
			errClose := part.Close()
			if errRead != nil {
				return nil, true, fmt.Errorf("read multipart import file: %w", errRead)
			}
			if errClose != nil {
				return nil, true, fmt.Errorf("close multipart import file: %w", errClose)
			}
			if len(data) > limits.MaxRequestBytes {
				return nil, true, fmt.Errorf("multipart import file exceeds the %s limit", formatByteLimit(int64(limits.MaxRequestBytes)))
			}
			uploads = append(uploads, importUpload{
				Name:        filename,
				ContentType: part.Header.Get("Content-Type"),
				Data:        data,
			})
		}
		if len(uploads) == 0 {
			return nil, true, fmt.Errorf("multipart import contains no files")
		}
		return uploads, true, nil
	}
	if errMediaType != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawContentType)), "multipart/") {
		return nil, true, fmt.Errorf("invalid multipart content type")
	}
	filename := strings.TrimSpace(headerValue(req.Headers, "X-CPA-Import-Filename"))
	if decoded, errDecode := url.QueryUnescape(filename); errDecode == nil {
		filename = decoded
	}
	if filename == "" {
		filename = firstQuery(req.Query, "filename")
	}
	if len(filename) > 240 || strings.ContainsRune(filename, '\x00') {
		return nil, false, fmt.Errorf("import filename is invalid")
	}
	return []importUpload{{Name: filename, ContentType: rawContentType, Data: req.Body}}, false, nil
}
