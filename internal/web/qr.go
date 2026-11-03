package web

import (
	"html/template"
	"strconv"
	"strings"

	"rsc.io/qr"
)

// qrSVG draws a QR code as inline SVG: one path of unit squares on a 4-module quiet zone. The colours come from CSS
// (.qr), fixed dark-on-light in both themes because phone cameras read that most reliably. The SVG is built from the
// encoder's bitmap and numbers only, so it is safe to mark as HTML.
func qrSVG(text string) template.HTML {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	const quiet = 4
	n := c.Size + 2*quiet
	var d strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				d.WriteString("M" + strconv.Itoa(x+quiet) + " " + strconv.Itoa(y+quiet) + "h1v1h-1z")
			}
		}
	}
	size := strconv.Itoa(n)
	return template.HTML(`<svg class="qr" viewBox="0 0 ` + size + ` ` + size + `" width="200" height="200" role="img" aria-hidden="true" shape-rendering="crispEdges">` + // #nosec G203 -- numbers and fixed markup only
		`<rect class="qr-kertas" width="` + size + `" height="` + size + `"/><path class="qr-dakwat" d="` + d.String() + `"/></svg>`)
}
