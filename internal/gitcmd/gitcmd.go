// Package gitcmd runs the two git transport services (D2: exec the git binary, never
// parse packs ourselves). The verb set is closed; arguments are never user strings.
package gitcmd

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Service is one of the two allowed git server verbs.
type Service string

// Allowed services.
const (
	UploadPack  Service = "upload-pack"  // clone / fetch (read)
	ReceivePack Service = "receive-pack" // push (write)
)

// Parse maps the command word a client sends to a Service.
func Parse(word string) (Service, bool) {
	switch word {
	case "git-upload-pack", "upload-pack":
		return UploadPack, true
	case "git-receive-pack", "receive-pack":
		return ReceivePack, true
	}
	return "", false
}

// Runner executes services with a timeout. HooksDir is forced as core.hooksPath on every
// run (hooks are written by Codr only) and HookSocket is where those hooks talk
// to the serve process (D9).
type Runner struct {
	Binary     string
	Timeout    time.Duration
	HooksDir   string
	HookSocket string
}

// Push carries what the hooks need to know about one receive-pack, and the hard limits
// read from settings by the caller (code reads; code does not hold numbers).
type Push struct {
	RepoID, UserID int64
	Lang, Door     string
	// MaxInputBytes is passed as receive.maxInputSize: git aborts the pack early. The
	// localised limit (repo.push_max_mb) is enforced by the pre-receive hook; this is the
	// coarse bound that keeps a hostile pack from filling the disk first (0 = none).
	MaxInputBytes int64
	// Fsck enables receive.fsckObjects.
	Fsck bool
}

// ConfigArgs returns the `-c key=value` arguments that precede the verb.
func (r Runner) ConfigArgs(p Push) []string {
	// refs/codr/ holds Codr's own refs (pull request heads fetched from forks, D75): never advertised to a
	// fetch (so their objects cannot be asked for) and never accepted from a push (so nobody can forge them).
	args := []string{"-c", "core.hooksPath=" + r.HooksDir, "-c", "uploadpack.hideRefs=refs/codr", "-c", "receive.hideRefs=refs/codr"}
	if p.MaxInputBytes > 0 {
		args = append(args, "-c", "receive.maxInputSize="+strconv.FormatInt(p.MaxInputBytes, 10))
	}
	if p.Fsck {
		args = append(args, "-c", "receive.fsckObjects=true")
	}
	return args
}

// Env is the minimal environment for git plus what the hooks read. GIT_PROTOCOL is the
// only client-controlled value and the caller validates it.
func (r Runner) Env(gitProtocol string, p Push) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1",
		"CODR_SOCKET=" + r.HookSocket, "CODR_REPO_ID=" + strconv.FormatInt(p.RepoID, 10), "CODR_USER_ID=" + strconv.FormatInt(p.UserID, 10),
		"CODR_LANG=" + p.Lang, "CODR_DOOR=" + p.Door}
	if gitProtocol != "" {
		env = append(env, "GIT_PROTOCOL="+gitProtocol)
	}
	return env
}

// Run executes `git <service> <repoPath>` wired to the given streams and returns the
// process exit code.
func (r Runner) Run(ctx context.Context, svc Service, repoPath, gitProtocol string, p Push, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if svc != UploadPack && svc != ReceivePack {
		return 1, errors.New("gitcmd: service not allowed")
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	args := append(r.ConfigArgs(p), string(svc), repoPath)
	cmd := exec.CommandContext(ctx, r.Binary, args...) // #nosec G204 -- verb from closed set, path from repo id, config keys fixed
	cmd.Env = r.Env(gitProtocol, p)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), nil
	default:
		return 1, err
	}
}
