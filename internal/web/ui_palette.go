package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The command palette (D68): Ctrl/Cmd-K in codr.js asks this endpoint for repositories the viewer may read.
// Same query, same permission check as the home list (ReposVisible); never returns more than paletteMax rows.

const paletteMax = 12

type paletteRepo struct {
	Path    string `json:"p"`
	Private bool   `json:"k,omitempty"`
	Mirror  bool   `json:"m,omitempty"`
}

func (s *Server) uiPalette(w http.ResponseWriter, r *http.Request) {
	u := s.viewer(r)
	var uid int64
	if u != nil {
		uid = u.ID
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 100 {
		q = q[:100]
	}
	repos, _, err := s.Store.ReposVisible(r.Context(), uid, "", q, "recent", paletteMax, 0)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	out := make([]paletteRepo, 0, len(repos))
	for _, rp := range repos {
		out = append(out, paletteRepo{Path: rp.Owner + "/" + rp.Name, Private: rp.Private, Mirror: rp.Mirror})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}
