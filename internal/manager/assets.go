package manager

import (
	_ "embed"
	"encoding/base64"
)

//go:embed cpama.svg
var pluginLogoSVG []byte

var pluginLogoDataURI = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(pluginLogoSVG)
