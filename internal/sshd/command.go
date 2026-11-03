package sshd

import (
	"errors"
	"regexp"
	"strings"

	"codr.my/codr/internal/gitcmd"
	"codr.my/codr/internal/names"
)

// Command is a parsed, validated git request: exactly one service and one repo.
type Command struct {
	Service gitcmd.Service
	Owner   string
	Repo    string
}

// ErrBadCommand is returned for anything that is not one of the two git verbs
// followed by exactly one repository path.
var ErrBadCommand = errors.New("sshd: command not allowed")

var gitProtocolRe = regexp.MustCompile(`^version=[0-9]+$`)

// ParseCommand accepts the shapes git clients send:
//
//	git-upload-pack 'owner/repo.git'    git-receive-pack "/owner/repo.git"
//	git upload-pack owner/repo.git
//
// Anything else — options, extra words, shell metacharacters — is rejected.
func ParseCommand(line string) (Command, error) {
	if strings.ContainsAny(line, ";&|$`<>\n\r\\") {
		return Command{}, ErrBadCommand
	}
	words := strings.Fields(line)
	if len(words) == 3 && words[0] == "git" {
		words = []string{"git-" + words[1], words[2]}
	}
	if len(words) != 2 {
		return Command{}, ErrBadCommand
	}
	svc, ok := gitcmd.Parse(words[0])
	if !ok {
		return Command{}, ErrBadCommand
	}
	path := words[1]
	if n := len(path); n >= 2 && (path[0] == '\'' || path[0] == '"') && path[n-1] == path[0] {
		path = path[1 : n-1]
	}
	if strings.ContainsAny(path, "'\"") || strings.HasPrefix(path, "-") {
		return Command{}, ErrBadCommand
	}
	owner, repo, err := names.SplitRef(path)
	if err != nil {
		return Command{}, ErrBadCommand
	}
	return Command{Service: svc, Owner: owner, Repo: repo}, nil
}

// ValidGitProtocol reports whether a GIT_PROTOCOL env value may be passed through.
func ValidGitProtocol(v string) bool { return gitProtocolRe.MatchString(v) }

// LFSAuth is a parsed `git-lfs-authenticate <repo> <upload|download>` (D82): the one request besides the two git
// verbs, and it runs nothing: the server answers it with JSON.
type LFSAuth struct {
	Owner, Repo, Operation string
}

// ParseLFSAuth accepts exactly `git-lfs-authenticate PATH upload|download`, PATH optionally quoted.
func ParseLFSAuth(line string) (LFSAuth, error) {
	if strings.ContainsAny(line, ";&|$`<>\n\r\\") {
		return LFSAuth{}, ErrBadCommand
	}
	words := strings.Fields(line)
	if len(words) != 3 || words[0] != "git-lfs-authenticate" || (words[2] != "upload" && words[2] != "download") {
		return LFSAuth{}, ErrBadCommand
	}
	path := words[1]
	if n := len(path); n >= 2 && (path[0] == '\'' || path[0] == '"') && path[n-1] == path[0] {
		path = path[1 : n-1]
	}
	if strings.ContainsAny(path, "'\"") || strings.HasPrefix(path, "-") {
		return LFSAuth{}, ErrBadCommand
	}
	owner, repo, err := names.SplitRef(path)
	if err != nil {
		return LFSAuth{}, ErrBadCommand
	}
	return LFSAuth{Owner: owner, Repo: repo, Operation: words[2]}, nil
}
