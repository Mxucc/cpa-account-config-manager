package main

import (
	"encoding/json"
	"fmt"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func decodeEnvelopeResult(raw []byte) (json.RawMessage, error) {
	var response envelope
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host envelope: %w", errUnmarshal)
	}
	if !response.OK {
		if response.Error != nil {
			return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
		}
		return nil, fmt.Errorf("host callback failed")
	}
	return append(json.RawMessage(nil), response.Result...), nil
}
