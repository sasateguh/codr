package web

import (
	"bufio"
	"io"
	"net/http"
	"net/textproto"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/gitcmd"
)

// backend runs `git http-backend` as a CGI program with the request body streamed to
// its stdin. net/http/cgi is not used because it refuses chunked bodies, and git sends
// pushes larger than http.postBuffer chunked; http-backend itself reads stdin to EOF
// when CONTENT_LENGTH is absent. The repo is addressed by id (D27): PATH_INFO is built
// here, never from the URL.
func (s *Server) backend(w http.ResponseWriter, r *http.Request, repoID int64, wiki bool, remoteUser string, push gitcmd.Push) {
	rest := r.URL.Path[strings.LastIndex(r.URL.Path, ".git/")+len(".git"):]
	dir := strconv.FormatInt(repoID, 10) + ".git"
	if wiki {
		dir = strconv.FormatInt(repoID, 10) + ".wiki.git" // D83: the wiki beside the code, read-only over git
	}
	pathInfo := "/" + dir + rest
	env := append(s.Git.Env("", push),
		"GIT_PROJECT_ROOT="+s.Disk.Root, "GIT_HTTP_EXPORT_ALL=1",
		"GATEWAY_INTERFACE=CGI/1.1", "SERVER_PROTOCOL=HTTP/1.1", "REQUEST_METHOD="+r.Method,
		"PATH_INFO="+pathInfo, "QUERY_STRING="+r.URL.RawQuery, "REMOTE_ADDR="+s.clientIP(r))
	if remoteUser != "" {
		env = append(env, "REMOTE_USER="+remoteUser)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		env = append(env, "CONTENT_TYPE="+ct)
	}
	if r.ContentLength > 0 {
		env = append(env, "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
	}
	if ce := r.Header.Get("Content-Encoding"); ce != "" {
		env = append(env, "HTTP_CONTENT_ENCODING="+ce)
	}
	if gp := r.Header.Get("Git-Protocol"); gp != "" && validGitProtocol(gp) {
		env = append(env, "HTTP_GIT_PROTOCOL="+gp)
	}
	args := append(s.Git.ConfigArgs(push), "http-backend")
	cmd := exec.CommandContext(r.Context(), s.Git.Binary, args...) // #nosec G204 -- fixed verb, config keys fixed, path from id
	cmd.Env = env
	cmd.Dir = s.Disk.Root
	cmd.Stdin = r.Body
	errb := &boundedBuffer{max: 4096}
	cmd.Stderr = errb
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.Log.Error("http-backend pipe", "err", err)
		s.plain(w, http.StatusInternalServerError, push.Lang, "http.internal")
		return
	}
	if err := cmd.Start(); err != nil {
		s.Log.Error("http-backend start", "err", err)
		s.plain(w, http.StatusInternalServerError, push.Lang, "http.internal")
		return
	}
	br := bufio.NewReader(stdout)
	hdr, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		_ = cmd.Wait()
		// git prints the real reason on stderr (e.g. "git: 'http-backend' is not a git command"
		// when the git-daemon package is missing); EOF alone tells the operator nothing.
		s.Log.Error("http-backend headers", "err", err, "stderr", strings.TrimSpace(errb.String()))
		s.plain(w, http.StatusInternalServerError, push.Lang, "http.internal")
		return
	}
	status := http.StatusOK
	for k, vs := range hdr {
		if k == "Status" {
			if n, err := strconv.Atoi(strings.SplitN(vs[0], " ", 2)[0]); err == nil {
				status = n
			}
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, br); err != nil {
		s.Log.Debug("http-backend copy", "err", err)
	}
	if err := cmd.Wait(); err != nil {
		s.Log.Warn("http-backend exit", "path", pathInfo, "err", err, "stderr", strings.TrimSpace(errb.String()))
	}
}

// boundedBuffer keeps the first max bytes written (enough for git's error line).
type boundedBuffer struct {
	b   strings.Builder
	max int
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.b.Write(p)
	}
	return len(p), nil
}

func (w *boundedBuffer) String() string { return w.b.String() }

func validGitProtocol(v string) bool {
	for _, part := range strings.Split(v, ":") {
		if !strings.HasPrefix(part, "version=") || len(part) != len("version=")+1 || part[len(part)-1] < '0' || part[len(part)-1] > '9' {
			return false
		}
	}
	return true
}
