// Package locale embeds the translation files. Every user-facing string of AMCD
// (CLI output, SSH error lines, web templates later) lives here as a key, never in code.
package locale

import "embed"

// FS holds <lang>.json files such as ms-MY.json and en-US.json.
//
//go:embed *.json
var FS embed.FS
