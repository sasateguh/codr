package web

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"codr.my/codr/docs"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/version"
)

// The design write-up on the landing host (D90): /design in the root language, /<other>/design in the other one. The
// text is docs/design.md and docs/reka-bentuk.md (embedded), rendered with the same sanitising Markdown renderer as READMEs.

type designData struct {
	landingData
	Body template.HTML
}

func designPath(lang, root string) string {
	if lang == root {
		return "/design"
	}
	return "/" + lang[:2] + "/design"
}

func (s *Server) designPage(w http.ResponseWriter, r *http.Request, lang string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	root := s.landingRoot(ctx)
	base := s.scheme() + "://" + s.LandingHosts[0]
	d := designData{landingData: landingData{ForgeURL: s.forgeURL(ctx), Index: s.landingIndex(ctx), Home: landingPath(lang, root),
		MSPath: designPath("ms-MY", root), ENPath: designPath("en-US", root)}}
	d.MSURL, d.ENURL, d.RootURL = base+d.MSPath, base+d.ENPath, base+designPath(root, root)
	d.Canonical, d.Alternate = d.MSURL, d.ENURL
	other := "en-US"
	if lang == "en-US" {
		d.Canonical, d.Alternate, other = d.ENURL, d.MSURL, "ms-MY"
	}
	src, err := docs.FS.ReadFile(docs.Design(lang))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Body, _, err = renderMarkdown(src, nil); err != nil {
		s.fail(w, r, err)
		return
	}
	name := "Codr"
	if n, err := s.Store.Setting(ctx, "site.name"); err == nil && n != "" {
		name = n
	}
	static := "/static/" + s.UI.staticHash
	p := page{Lang: lang, SiteName: name, Static: static, Version: version.String(), Path: r.URL.Path,
		Title: i18n.T(lang, "landing.design.title") + " · " + name, Now: time.Now(), OGImage: base + static + "/og.png", OtherLang: other, Data: d}
	if !d.Index {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	s.render(w, r, http.StatusOK, "design.html", p)
}
