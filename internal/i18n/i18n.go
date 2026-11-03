// Package i18n resolves message keys to text in the requested language.
// Translations live in locale/*.json (embedded); code never carries user-facing text.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"codr.my/codr/locale"
)

// Fallback is the language used when a key is missing in the requested one.
const Fallback = "en-US"

var (
	once     sync.Once
	catalogs map[string]map[string]string
	loadErr  error
)

func load() {
	catalogs = map[string]map[string]string{}
	entries, err := fs.ReadDir(locale.FS, ".")
	if err != nil {
		loadErr = err
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := locale.FS.ReadFile(name)
		if err != nil {
			loadErr = err
			return
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			loadErr = fmt.Errorf("locale %s: %w", name, err)
			return
		}
		catalogs[strings.TrimSuffix(name, ".json")] = m
	}
}

// Languages lists the available language tags, sorted.
func Languages() []string {
	once.Do(load)
	out := make([]string, 0, len(catalogs))
	for k := range catalogs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a catalog exists for lang.
func Has(lang string) bool {
	once.Do(load)
	_, ok := catalogs[lang]
	return ok
}

// Normalize maps loose inputs ("ms", "MS-my", "en") to a known tag, or returns def.
func Normalize(lang, def string) string {
	once.Do(load)
	l := strings.ToLower(strings.TrimSpace(lang))
	for tag := range catalogs {
		lt := strings.ToLower(tag)
		if l == lt || l == strings.SplitN(lt, "-", 2)[0] {
			return tag
		}
	}
	return def
}

// T formats the message for key in lang. A missing key falls back to Fallback,
// then to the key itself so a gap is visible rather than silent.
func T(lang, key string, args ...any) string {
	once.Do(load)
	if loadErr != nil {
		return key + ": " + loadErr.Error()
	}
	msg, ok := catalogs[lang][key]
	if !ok {
		msg, ok = catalogs[Fallback][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// N is T for a counted message: when n == 1 and a "<key>.one" message exists it is used
// (English "1 comment" vs "2 comments"); Malay has no plural form and simply omits ".one".
func N(lang, key string, n int, args ...any) string {
	once.Do(load)
	if n == 1 && loadErr == nil {
		one, ok := catalogs[lang][key+".one"]
		if !ok {
			one, ok = catalogs[Fallback][key+".one"]
		}
		if ok {
			// a ".one" message usually spells the number out ("1 comment on %s"): pass n only when it has a verb for it,
			// i.e. one more verb than the other arguments. Counting "%" alone gave "1 contribution on %!s(int=1)".
			if verbs(one) > len(args) {
				return T(lang, key+".one", append([]any{n}, args...)...)
			}
			return T(lang, key+".one", args...)
		}
	}
	return T(lang, key, append([]any{n}, args...)...)
}

// verbs counts the formatting verbs of a message ("%%" is a literal percent, not a verb).
func verbs(msg string) int {
	n := 0
	for i := 0; i < len(msg)-1; i++ {
		if msg[i] != '%' {
			continue
		}
		if msg[i+1] == '%' {
			i++
			continue
		}
		n++
	}
	return n
}

// Keys returns every key of one catalog (for the parity test).
func Keys(lang string) []string {
	once.Do(load)
	out := make([]string, 0, len(catalogs[lang]))
	for k := range catalogs[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Err reports a load failure, if any (checked by `amcd doctor` and tests).
func Err() error {
	once.Do(load)
	return loadErr
}
