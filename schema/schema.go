// Package schema holds the JSON Schema of the capture format (ADR 0003),
// generated from internal/report's structs by tools/genschema. A new
// schema_version adds a capture-vN.json; older files are frozen.
package schema

import _ "embed" // for JSON

//go:generate go run ../tools/genschema -dir .

// URL is the schema's $id, and what captures name in their "$schema" key so
// editors can validate them. It's on main because a v1 file only ever grows.
const URL = "https://raw.githubusercontent.com/jiegui2025/hwspec/main/schema/capture-v1.json"

// JSON is the schema for the current schema_version.
//
//go:embed capture-v1.json
var JSON []byte
