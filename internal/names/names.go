// Package names is the single place that decides what a user or repository name may be.
// Disk paths are never built from these strings (repos live under their numeric id, D27),
// but they still appear in URLs, SSH commands and the DB, so the rules are strict.
package names

import (
	"errors"
	"regexp"
	"strings"
)

// Limits — mirrored in locale messages via %d.
const (
	MaxUser = 39
	MaxRepo = 100
)

var (
	// ErrUser, ErrRepo and ErrReserved are matched by callers to pick a locale key.
	ErrUser     = errors.New("invalid user name")
	ErrRepo     = errors.New("invalid repo name")
	ErrReserved = errors.New("reserved name")

	userRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,37}[a-z0-9])?$`)
	repoRe = regexp.MustCompile(`^[a-z0-9_-][a-z0-9._-]{0,99}$`)

	// reserved are top-level URL segments the web UI will own; a user named "admin"
	// would otherwise shadow /admin. Kept here so the rule exists before the routes do.
	reserved = map[string]bool{
		"admin": true, "api": true, "login": true, "logout": true, "register": true, "settings": true, "akaun": true,
		"static": true, "healthz": true, "baharu": true, "lang": true, "explore": true, "new": true, "org": true, "orgs": true,
		"user": true, "users": true, "repo": true, "repos": true, "git": true, "ssh": true, "amcd": true, "codr": true,
		"root": true, "system": true, "ci": true, "help": true, "about": true, "search": true, "notifications": true, "pemberitahuan": true,
	}
)

// User validates a user name. Names are case-sensitive on input but only lowercase is accepted.
func User(name string) error {
	if len(name) < 2 || len(name) > MaxUser || !userRe.MatchString(name) || strings.Contains(name, "..") {
		return ErrUser
	}
	if reserved[name] {
		return ErrReserved
	}
	return nil
}

// Repo validates a repository name. A trailing ".git" is not part of the name.
func Repo(name string) error {
	if name == "" || len(name) > MaxRepo || !repoRe.MatchString(name) || strings.Contains(name, "..") ||
		strings.HasSuffix(name, ".git") || name == "." {
		return ErrRepo
	}
	return nil
}

// SplitRef parses "owner/name" (optionally with a leading "/" or trailing ".git")
// and validates both parts.
func SplitRef(ref string) (owner, name string, err error) {
	ref = strings.TrimPrefix(ref, "/")
	ref = strings.TrimSuffix(ref, ".git")
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return "", "", ErrRepo
	}
	if err := User(parts[0]); err != nil {
		return "", "", err
	}
	if err := Repo(parts[1]); err != nil {
		return "", "", err
	}
	return parts[0], parts[1], nil
}
