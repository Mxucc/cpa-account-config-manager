package web

import _ "embed"

//go:embed dist/index.html
var indexHTML []byte

func IndexHTML() []byte {
	return append([]byte(nil), indexHTML...)
}
