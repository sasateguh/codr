package xref

import (
	"reflect"
	"strings"
	"testing"
)

var words = Keywords("close,closes,closed,fix,fixes,fixed,resolve,resolves,resolved,tutup,menutup,baiki,membaiki")

func TestClosing(t *testing.T) {
	cases := []struct {
		in   string
		want []Ref
	}{
		{"Fixes #12", []Ref{{Number: 12}}},
		{"fix: retry\n\nCloses: #3, and resolves aiman/codr#9.", []Ref{{Number: 3}, {Owner: "aiman", Repo: "codr", Number: 9}}},
		{"FIXED #4 and fixed #4 again", []Ref{{Number: 4}}},
		{"Menutup #5. Membaiki #6", []Ref{{Number: 5}, {Number: 6}}},
		{"see #7", nil},                  // a mention, not a keyword
		{"prefixes #8", nil},             // a keyword inside a word
		{"fixes#9", nil},                 // no space
		{"fixes #0", nil},                // not a number we use
		{"`fixes #10`", nil},             // inline code
		{"```\nfixes #11\n```\nok", nil}, // fenced code
		{"closes https://x/y#12", nil},   // a URL fragment
		{"fixes #13x", nil},              // not a whole reference
		{"tutup #14 selepas semakan", []Ref{{Number: 14}}},
	}
	for _, c := range cases {
		if got := Closing(c.in, words); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Closing(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	if got := Closing("fixes #1", nil); got != nil {
		t.Fatalf("no keywords configured must close nothing: %v", got)
	}
	if got := Closing("selesai #2", Keywords(" selesai , ")); len(got) != 1 {
		t.Fatalf("keywords come from the setting: %v", got)
	}
}

func TestLinkCross(t *testing.T) {
	seen := map[string]int{}
	vis := func(owner, name string) bool { seen[owner+"/"+name]++; return owner == "aiman" }
	in := "Lihat aiman/codr#4 dan rahsia/projek#2.\n(aiman/codr#5)\n```\naiman/codr#6\n```\n`aiman/codr#7`"
	out := LinkCross(in, vis)
	for _, want := range []string{"[aiman/codr#4](/aiman/codr/isu/4)", "([aiman/codr#5](/aiman/codr/isu/5))", "rahsia/projek#2.", "\naiman/codr#6\n", "`aiman/codr#7`"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in %q", want, out)
		}
	}
	if strings.Contains(out, "/rahsia/projek/isu") {
		t.Fatal("a repository the reader cannot see was linked")
	}
	if seen["aiman/codr"] != 1 {
		t.Fatalf("visibility asked %d times for one repository in one text", seen["aiman/codr"])
	}
}
