// Package hooks is the git hook door of Codr (D9): git runs `codr hook pre-receive`
// (a three-line script written by Codr), the hook forwards the ref updates over a Unix
// socket in data/, and the serve process decides. Protected branches and the push size
// limit live here, once, for every door.
package hooks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/i18n"
)

// Ref is one line of pre-/post-receive stdin: old new refname.
type Ref struct {
	Old  string `json:"old"`
	New  string `json:"new"`
	Name string `json:"name"`
}

// Request is what the hook sends.
type Request struct {
	Hook   string            `json:"hook"` // pre-receive | post-receive
	RepoID int64             `json:"repo_id"`
	UserID int64             `json:"user_id"`
	Door   string            `json:"door"` // ssh | https
	Lang   string            `json:"lang"`
	Refs   []Ref             `json:"refs"`
	Env    map[string]string `json:"env"` // quarantine/object dirs so serve can read the incoming objects
}

// Response is the verdict. Message is already localised.
type Response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// envKeys are the git variables forwarded so merge-base can see quarantined objects.
var envKeys = []string{"GIT_QUARANTINE_PATH", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"}

// Deleted reports whether the update deletes the ref (new id all zeros).
func (r Ref) Deleted() bool { return strings.Trim(r.New, "0") == "" }

// Created reports whether the ref is new (old id all zeros).
func (r Ref) Created() bool { return strings.Trim(r.Old, "0") == "" }

const maxLine = 4096

// ParseRefs reads "old new name" lines.
func ParseRefs(r io.Reader) ([]Ref, error) {
	var out []Ref
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, maxLine), maxLine)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			return nil, fmt.Errorf("hooks: bad ref line %q", sc.Text())
		}
		out = append(out, Ref{Old: f[0], New: f[1], Name: f[2]})
	}
	return out, sc.Err()
}

// Call sends one request over the socket and returns the verdict.
func Call(socket string, req Request, timeout time.Duration) (Response, error) {
	c, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return Response{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(io.LimitReader(c, 64*1024)).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// RunHook is the body of `codr hook <name>`: it reads the environment git and Codr set,
// forwards stdin, and prints the verdict. Without CODR_SOCKET (a push made on the host
// directly to the bare repo, D13) it allows silently — that operator already owns the disk.
// pre-receive fails CLOSED when serve cannot be reached; post-receive never blocks.
func RunHook(name string, stdin io.Reader, stderr io.Writer, getenv func(string) string) int {
	if name != "pre-receive" && name != "post-receive" {
		return 2
	}
	socket := getenv("CODR_SOCKET")
	if socket == "" {
		return 0
	}
	lang := i18n.Normalize(getenv("CODR_LANG"), "")
	refs, err := ParseRefs(stdin)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T(lang, "hook.internal"))
		fmt.Fprintln(stderr, i18n.T(lang, "hook.internal_cause", err.Error()))
		return 1
	}
	repoID, _ := strconv.ParseInt(getenv("CODR_REPO_ID"), 10, 64)
	userID, _ := strconv.ParseInt(getenv("CODR_USER_ID"), 10, 64)
	req := Request{Hook: name, RepoID: repoID, UserID: userID, Door: getenv("CODR_DOOR"), Lang: lang, Refs: refs, Env: map[string]string{}}
	for _, k := range envKeys {
		if v := getenv(k); v != "" {
			req.Env[k] = v
		}
	}
	resp, err := Call(socket, req, 30*time.Second)
	if err != nil {
		if name == "post-receive" {
			return 0
		}
		// the second line is what an operator needs: "connection refused" = serve is not on this socket
		fmt.Fprintln(stderr, i18n.T(lang, "hook.internal"))
		fmt.Fprintln(stderr, i18n.T(lang, "hook.internal_cause", err.Error()))
		return 1
	}
	if !resp.OK {
		fmt.Fprintln(stderr, resp.Message)
		return 1
	}
	return 0
}

// WriteScripts (re)writes the hook scripts that git runs, pointing at the given binary.
// serve calls it on every start so an upgraded binary is picked up.
func WriteScripts(dir, binary string) error {
	if !filepath.IsAbs(binary) {
		return errors.New("hooks: binary path must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, h := range []string{"pre-receive", "post-receive"} {
		body := "#!/bin/sh\n# written by Codr, do not edit; logic lives in `codr serve` (D9)\nexec " + shellQuote(binary) + " hook " + h + "\n"
		if err := os.WriteFile(filepath.Join(dir, h), []byte(body), 0o700); err != nil { // #nosec G306 -- hook must be executable
			return err
		}
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RunIfHook lets a test binary stand in for `amcd`: when invoked as `<bin> hook <name>` it
// runs the hook and exits. Tests call it from TestMain before m.Run().
func RunIfHook(args []string) {
	if len(args) >= 3 && args[1] == "hook" {
		os.Exit(RunHook(args[2], os.Stdin, os.Stderr, os.Getenv))
	}
}
