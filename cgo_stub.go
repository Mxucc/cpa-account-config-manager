//go:build !cgo

package main

import (
	"encoding/json"
	"fmt"
)

func callHost(method string, payload any) (json.RawMessage, error) {
	_ = payload
	return nil, fmt.Errorf("host callback %s requires CGO", method)
}
