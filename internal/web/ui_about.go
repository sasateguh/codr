package web

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/lexers"

	"codr.my/codr/internal/gitread"
)

// The "About" panel of a repository home (D67): what the code is made of, measured, never typed in.

type langShare struct {
	Name     string
	Pct      string // one decimal, for text
	X, W     int    // position and width on a 1000-unit bar
	Hue      string // .h0–.h7 tile class
	Other    bool   // the bucket for everything past the top languages
	Bytes    int64
	tenthPct int
}

type aboutData struct {
	Langs   []langShare
	Files   int
	Bytes   int64
	Capped  bool
	Licence string // first line of LICENSE/COPYING, "" = none found
}

const maxLangs = 6

func splitSetting(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// about measures ref: language shares by bytes (one ls-tree, one lexer lookup per extension), file count,
// size and the licence's title line.
func (s *Server) about(ctx context.Context, g gitread.Repo, ref string, root []gitread.Entry) *aboutData {
	max, _ := s.Store.SettingInt(ctx, "ui.lang_max_files")
	ignoreV, _ := s.Store.Setting(ctx, "ui.lang_ignore")
	skipV, _ := s.Store.Setting(ctx, "ui.lang_skip_paths")
	files, capped, err := g.TreeSizes(ctx, ref, int(max))
	if err != nil {
		return nil
	}
	ignore := map[string]bool{}
	for _, n := range splitSetting(ignoreV) {
		ignore[strings.ToLower(n)] = true
	}
	skip := splitSetting(skipV)
	d := &aboutData{Files: len(files), Capped: capped}
	byLang := map[string]int64{}
	cache := map[string]string{}
	var total int64
	for _, f := range files {
		d.Bytes += f.Size
		bad := false
		for _, sp := range skip {
			if strings.Contains(f.Path, sp) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		base := path.Base(f.Path)
		key := strings.ToLower(path.Ext(base))
		if key == "" {
			key = base
		}
		name, ok := cache[key]
		if !ok {
			if l := lexers.Match(base); l != nil {
				name = l.Config().Name
			}
			cache[key] = name
		}
		if name == "" || ignore[strings.ToLower(name)] {
			continue
		}
		byLang[name] += f.Size
		total += f.Size
	}
	if total > 0 {
		for n, b := range byLang {
			d.Langs = append(d.Langs, langShare{Name: n, Bytes: b})
		}
		sort.Slice(d.Langs, func(i, j int) bool {
			if d.Langs[i].Bytes != d.Langs[j].Bytes {
				return d.Langs[i].Bytes > d.Langs[j].Bytes
			}
			return d.Langs[i].Name < d.Langs[j].Name
		})
		if len(d.Langs) > maxLangs {
			var rest int64
			for _, l := range d.Langs[maxLangs-1:] {
				rest += l.Bytes
			}
			d.Langs = append(d.Langs[:maxLangs-1], langShare{Other: true, Bytes: rest})
		}
		x := 0
		for i := range d.Langs {
			l := &d.Langs[i]
			l.tenthPct = int((l.Bytes*1000 + total/2) / total)
			l.Pct = strconv.Itoa(l.tenthPct/10) + "." + strconv.Itoa(l.tenthPct%10)
			l.W = l.tenthPct
			if i == len(d.Langs)-1 {
				l.W = 1000 - x // rounding never leaves a gap or overflows the bar
			}
			l.X = x
			x += l.W
			l.Hue = "h" + strconv.Itoa(i%8)
		}
	}
	for _, e := range root {
		n := strings.ToUpper(e.Name)
		if e.IsDir() || !(strings.HasPrefix(n, "LICENSE") || strings.HasPrefix(n, "LICENCE") || strings.HasPrefix(n, "COPYING")) {
			continue
		}
		if b, err := g.Blob(ctx, ref, e.Name, 4096); err == nil && !b.Binary {
			for _, line := range strings.Split(string(b.Content), "\n") {
				if line = strings.TrimSpace(strings.Trim(line, "= *#")); line != "" {
					if len(line) > 80 {
						line = line[:80]
					}
					d.Licence = line
					break
				}
			}
		}
		break
	}
	return d
}
