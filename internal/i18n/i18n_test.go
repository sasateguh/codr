package i18n

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"codr.my/codr/locale"
)

func TestCatalogsLoadAndMatch(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	langs := Languages()
	if len(langs) < 2 || !Has("ms-MY") || !Has("en-US") {
		t.Fatalf("expected ms-MY and en-US, got %v", langs)
	}
	ms, en := Keys("ms-MY"), Keys("en-US")
	if strings.Join(ms, ",") != strings.Join(en, ",") {
		t.Fatalf("key sets differ:\nms-MY=%v\nen-US=%v", ms, en)
	}
	// Same format verbs in both languages, in the same order.
	verbs := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
	for _, k := range ms {
		a := verbs.FindAllString(T("ms-MY", k), -1)
		b := verbs.FindAllString(T("en-US", k), -1)
		if strings.Join(a, "") != strings.Join(b, "") {
			t.Errorf("%s: verbs differ ms=%v en=%v", k, a, b)
		}
	}
}

func TestFallbackAndFormat(t *testing.T) {
	if got := T("ms-MY", "cli.unknown_command", "x"); !strings.Contains(got, "x") {
		t.Fatalf("format failed: %q", got)
	}
	if got := T("xx-XX", "cli.unknown_command", "y"); got != T("en-US", "cli.unknown_command", "y") {
		t.Fatalf("fallback failed: %q", got)
	}
	if got := T("en-US", "no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key should echo key, got %q", got)
	}
	if Normalize("MS", "en-US") != "ms-MY" || Normalize("en", "ms-MY") != "en-US" || Normalize("zz", "ms-MY") != "ms-MY" {
		t.Fatal("Normalize")
	}
}

// TestNSingular: "<key>.one" is used for exactly one and may omit the %d verb (English "1 comment");
// Malay carries the same key with the number spelled out, so both languages format cleanly.
func TestNSingular(t *testing.T) {
	for _, c := range []struct{ lang, want1, want2 string }{
		{"en-US", "1 comment", "2 comments"},
		{"ms-MY", "1 komen", "2 komen"},
	} {
		if got := N(c.lang, "ui.issue.comments_n", 1); got != c.want1 {
			t.Errorf("%s n=1: %q", c.lang, got)
		}
		if got := N(c.lang, "ui.issue.comments_n", 2); got != c.want2 {
			t.Errorf("%s n=2: %q", c.lang, got)
		}
	}
	if got := N("en-US", "ui.time.months", 1); got != "1 month ago" {
		t.Errorf("months: %q", got)
	}
}

// TestNEveryPlural renders every counted message at 1 and 2 with the arguments its base form asks for: none may show a
// fmt error. "1 contribution on %s" once printed "%!s(int=1)" on every day with exactly one contribution.
func TestNEveryPlural(t *testing.T) {
	if got := N("en-US", "ui.profile.day", 1, "16 Oct"); got != "1 contribution on 16 Oct" {
		t.Errorf("day.one: %q", got)
	}
	for _, lang := range []string{"en-US", "ms-MY"} {
		for _, k := range Keys(lang) {
			base, ok := strings.CutSuffix(k, ".one")
			if !ok {
				continue
			}
			var extra []any
			for i := 1; i < verbs(catalogs[lang][base]); i++ {
				extra = append(extra, "x")
			}
			for _, n := range []int{1, 2} {
				if got := N(lang, base, n, extra...); strings.Contains(got, "%!") {
					t.Errorf("%s %s n=%d: %q", lang, base, n, got)
				}
			}
		}
	}
}

// TestLocaleNoEmDash: no user-facing string carries an em dash, web or terminal (D51, D57).
func TestLocaleNoEmDash(t *testing.T) {
	entries, err := fs.ReadDir(locale.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := locale.FS.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(e.Name(), err)
		}
		for k, v := range m {
			if strings.Contains(v, "—") {
				t.Errorf("%s %s: em dash in user-facing text: %q", e.Name(), k, v)
			}
		}
	}
}
