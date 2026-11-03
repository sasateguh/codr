package runner

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Runner receives webhooks and runs jobs. One process; jobs wait in a bounded queue.
type Runner struct {
	Cfg Config
	Log *slog.Logger
	// Exec runs a command (tests replace it); nil = os/exec.
	Exec  Execer
	queue chan push
	wg    sync.WaitGroup
	// http is the API client (tests point it at a fake forge).
	http *http.Client
	// unit is how long one "minute" of a time limit lasts (tests shorten it); 0 = a minute.
	unit time.Duration
}

type push struct {
	Repo   string // owner/name
	Branch string
	SHA    string
	Hook   string // the delivery id, for the log
}

var (
	shaRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
	logRE  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Handler serves POST /hook, GET /ci/log/{id} and GET /healthz.
func (rn *Runner) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hook", rn.hook)
	mux.HandleFunc("GET /ci/log/{id}", rn.logPage)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	return mux
}

// Start begins Concurrency workers; Stop waits for them.
func (rn *Runner) Start(ctx context.Context) {
	rn.queue = make(chan push, rn.Cfg.QueueMax)
	if rn.http == nil {
		rn.http = &http.Client{Timeout: 30 * time.Second}
	}
	if rn.Exec == nil {
		rn.Exec = osExec{}
	}
	for i := 0; i < rn.Cfg.Concurrency; i++ {
		rn.wg.Add(1)
		go func() {
			defer rn.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case p := <-rn.queue:
					rn.run(ctx, p)
				}
			}
		}()
	}
	rn.wg.Add(1)
	go func() { defer rn.wg.Done(); rn.prune(ctx) }()
}

// Wait blocks until the workers stopped (after ctx ended).
func (rn *Runner) Wait() { rn.wg.Wait() }

// verify checks the X-Codr-Signature-256 (or X-Hub-Signature-256) HMAC of the exact body, in constant time.
func verify(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

func (rn *Runner) servesRepo(full string) bool {
	if len(rn.Cfg.Repos) == 0 {
		return true
	}
	for _, g := range rn.Cfg.Repos {
		if ok, _ := path.Match(g, full); ok {
			return true
		}
	}
	return false
}

func (rn *Runner) hook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var p struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	_ = json.Unmarshal(body, &p) // read only to pick the repository's secret; nothing is trusted before the signature
	sig := r.Header.Get("X-Codr-Signature-256")
	if sig == "" {
		sig = r.Header.Get("X-Hub-Signature-256")
	}
	secret := rn.Cfg.SecretFor(p.Repository.FullName)
	if secret == "" || !verify(secret, body, sig) {
		rn.Log.Warn("hook: bad signature", "remote", r.RemoteAddr, "repo", p.Repository.FullName)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	event := r.Header.Get("X-Codr-Event")
	if event != "push" { // ping and every other kind: acknowledged, nothing to run
		w.WriteHeader(http.StatusNoContent)
		return
	}
	branch, isBranch := strings.CutPrefix(p.Ref, "refs/heads/")
	if !isBranch || p.Deleted || !shaRE.MatchString(p.After) || !repoRE.MatchString(p.Repository.FullName) || !rn.servesRepo(p.Repository.FullName) {
		w.WriteHeader(http.StatusNoContent) // a tag, a deletion or a repository this runner does not serve
		return
	}
	select {
	case rn.queue <- push{Repo: p.Repository.FullName, Branch: branch, SHA: p.After, Hook: r.Header.Get("X-Codr-Delivery")}:
		w.WriteHeader(http.StatusAccepted)
	default:
		rn.Log.Warn("hook: queue full", "repo", p.Repository.FullName, "sha", p.After)
		http.Error(w, "queue full", http.StatusServiceUnavailable)
	}
}

func newLogID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// logPage serves one job log. The id is 128 random bits: the address is the permission (it is shown only as the
// status link, which only readers of the repository see).
func (rn *Runner) logPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// a job prints whatever it likes: the log is inert text, never a page that runs or frames anything (D94)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !logRE.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(rn.Cfg.LogDir, id+".log")) // #nosec G304 -- id is 32 hex characters
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, f)
}

// prune removes logs older than log_keep_days, hourly.
func (rn *Runner) prune(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		cut := time.Now().Add(-time.Duration(rn.Cfg.LogKeepDays) * 24 * time.Hour)
		if es, err := os.ReadDir(rn.Cfg.LogDir); err == nil {
			for _, e := range es {
				if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".log") && info.ModTime().Before(cut) {
					_ = os.Remove(filepath.Join(rn.Cfg.LogDir, e.Name()))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
