// Package docs carries the design write-up (D90) so the landing page can serve it: docs/design.md (English) and
// docs/reka-bentuk.md (Malay). The files are the source; the page renders them, nothing is copied into code.
package docs

import "embed"

// FS holds the Markdown documents.
//
//go:embed design.md reka-bentuk.md
var FS embed.FS

// Design is the write-up's file name for a language.
func Design(lang string) string {
	if lang == "ms-MY" {
		return "reka-bentuk.md"
	}
	return "design.md"
}
