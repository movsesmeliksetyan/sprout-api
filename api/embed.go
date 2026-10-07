// Package api holds the OpenAPI description of the Sprout API.
package api

import _ "embed" // for the go:embed directive below

// Spec is the OpenAPI document, served to clients outside production.
//
//go:embed openapi.yaml
var Spec []byte
