package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/store"
)

// Git LFS (D82): the Batch API and the basic transfer, at the URL git-lfs derives from a clone URL
// (https://host/owner/repo.git/info/lfs). Credentials are the same as for git over HTTPS (a token, never a password),
// or a short token from `git-lfs-authenticate` over SSH. Reading needs read access; uploading needs write access to a
// repository that is neither a mirror nor archived. Objects are checked against their sha256 before they exist.

const lfsMedia = "application/vnd.git-lfs+json"

func (s *Server) registerLFS(mux *http.ServeMux) {
	mux.HandleFunc("POST /{owner}/{repo}/info/lfs/objects/batch", s.lfsBatch)
	mux.HandleFunc("PUT /{owner}/{repo}/info/lfs/objects/{oid}", s.lfsUpload)
	mux.HandleFunc("GET /{owner}/{repo}/info/lfs/objects/{oid}", s.lfsDownload)
	mux.HandleFunc("POST /{owner}/{repo}/info/lfs/locks/verify", s.lfsLocksVerify)
	mux.HandleFunc("GET /{owner}/{repo}/info/lfs/locks", s.lfsLocks)
}

func lfsJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", lfsMedia)
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func lfsFail(w http.ResponseWriter, code int, msg string) {
	lfsJSON(w, code, map[string]string{"message": msg, "documentation_url": "https://github.com/git-lfs/git-lfs/blob/main/docs/api"})
}

// lfsOpen authenticates and authorises one LFS request. need is the access the operation requires.
func (s *Server) lfsOpen(w http.ResponseWriter, r *http.Request, need store.Access) (store.Repo, bool) {
	if s.LFS == nil {
		lfsFail(w, http.StatusNotFound, "Git LFS is not enabled on this server")
		return store.Repo{}, false
	}
	ctx, ip := r.Context(), s.clientIP(r)
	if s.Limiter != nil && s.Limiter.Blocked(ip) {
		lfsFail(w, http.StatusTooManyRequests, "too many failed sign-ins from this address")
		return store.Repo{}, false
	}
	repo, err := s.Store.RepoByPath(ctx, r.PathValue("owner"), strings.TrimSuffix(r.PathValue("repo"), ".git"))
	notFound := func() (store.Repo, bool) {
		lfsFail(w, http.StatusNotFound, "Repository not found")
		return store.Repo{}, false
	}
	if err != nil {
		return notFound()
	}
	var uid int64
	capWrite := true // what the credential allows at most, before the person's access
	h := r.Header.Get("Authorization")
	if scheme, rest, _ := strings.Cut(h, " "); strings.EqualFold(scheme, "bearer") && lfs.IsAuth(strings.TrimSpace(rest)) {
		var key []byte
		if s.Mirror != nil {
			key = s.Mirror.Key
		}
		u, rid, write, ok := lfs.VerifyAuth(key, strings.TrimSpace(rest), time.Now())
		if !ok || rid != repo.ID {
			if s.Limiter != nil {
				s.Limiter.Fail(ip)
			}
			lfsFail(w, http.StatusUnauthorized, "The LFS credential is not valid for this repository; run the command again")
			return store.Repo{}, false
		}
		uid, capWrite = u, write
	} else {
		tok, u, err := s.authenticate(ctx, r)
		if err != nil {
			if s.Limiter != nil {
				s.Limiter.Fail(ip)
			}
			w.Header().Set("LFS-Authenticate", `Basic realm="Codr"`)
			lfsFail(w, http.StatusUnauthorized, "Use an access token as the password")
			return store.Repo{}, false
		}
		if u != nil {
			uid = u.ID
			capWrite = tok.Allows(store.AccessWrite)
		}
	}
	a, err := s.Store.AccessFor(ctx, uid, repo)
	if err != nil {
		lfsFail(w, http.StatusInternalServerError, "internal error")
		return store.Repo{}, false
	}
	if a > store.AccessRead && !capWrite {
		a = store.AccessRead
	}
	switch {
	case uid == 0 && (a < store.AccessRead || need >= store.AccessWrite):
		// anonymous and not enough: ask for credentials (a 403 makes git-lfs give up instead of asking)
		w.Header().Set("LFS-Authenticate", `Basic realm="Codr"`)
		lfsFail(w, http.StatusUnauthorized, "Credentials needed")
		return store.Repo{}, false
	case a < store.AccessRead:
		return notFound()
	case need >= store.AccessWrite && a < store.AccessWrite:
		lfsFail(w, http.StatusForbidden, "You may not push to this repository")
		return store.Repo{}, false
	case need >= store.AccessWrite && (repo.Mirror || repo.Archived):
		lfsFail(w, http.StatusForbidden, "This repository is read-only")
		return store.Repo{}, false
	}
	return repo, true
}

type lfsObject struct {
	OID     string               `json:"oid"`
	Size    int64                `json:"size"`
	Actions map[string]lfsAction `json:"actions,omitempty"`
	Err     *lfsObjErr           `json:"error,omitempty"`
}

type lfsObjErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lfsAction struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

func (s *Server) lfsLimits(ctx context.Context) (maxObject, quota int64) {
	return s.settingInt(ctx, "lfs.max_object_mb", 2048) << 20, s.settingInt(ctx, "lfs.repo_quota_mb", 0) << 20
}

func (s *Server) lfsBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operation string   `json:"operation"`
		Transfers []string `json:"transfers"`
		Objects   []struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"objects"`
		HashAlgo string `json:"hash_algo"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Operation != "download" && req.Operation != "upload") {
		lfsFail(w, http.StatusUnprocessableEntity, "Bad batch request")
		return
	}
	need := store.AccessRead
	if req.Operation == "upload" {
		need = store.AccessWrite
	}
	repo, ok := s.lfsOpen(w, r, need)
	if !ok {
		return
	}
	ctx := r.Context()
	if req.HashAlgo != "" && req.HashAlgo != "sha256" {
		lfsFail(w, http.StatusConflict, "Only sha256 is supported")
		return
	}
	if n := s.settingInt(ctx, "lfs.batch_max", 1000); int64(len(req.Objects)) > n {
		lfsFail(w, http.StatusRequestEntityTooLarge, "Too many objects in one batch")
		return
	}
	maxObject, quota := s.lfsLimits(ctx)
	var used int64
	if quota > 0 && need == store.AccessWrite {
		_, used, _ = s.LFS.Usage(repo.ID)
	}
	href := strings.TrimRight(s.UI.HTTPHost, "/") + "/" + repo.Owner + "/" + repo.Name + ".git/info/lfs/objects/"
	var hdr map[string]string
	if a := r.Header.Get("Authorization"); a != "" {
		hdr = map[string]string{"Authorization": a} // the client sends the same credential with the transfer
	}
	expires := int(s.settingInt(ctx, "lfs.ssh_token_minutes", 60) * 60)
	out := make([]lfsObject, 0, len(req.Objects))
	for _, o := range req.Objects {
		obj := lfsObject{OID: o.OID, Size: o.Size}
		have, exists := s.LFS.Stat(repo.ID, o.OID)
		switch {
		case !lfs.ValidOID(o.OID) || o.Size < 0:
			obj.Err = &lfsObjErr{422, "Invalid object"}
		case req.Operation == "download" && !exists:
			obj.Err = &lfsObjErr{404, "Object does not exist"}
		case req.Operation == "download":
			obj.Size = have
			obj.Actions = map[string]lfsAction{"download": {Href: href + o.OID, Header: hdr, ExpiresIn: expires}}
		case exists && have == o.Size:
			// already stored: nothing to send
		case maxObject > 0 && o.Size > maxObject:
			obj.Err = &lfsObjErr{422, "Object is larger than this server accepts (lfs.max_object_mb)"}
		case quota > 0 && used+o.Size > quota:
			obj.Err = &lfsObjErr{507, "Repository LFS quota exceeded (lfs.repo_quota_mb)"}
		default:
			used += o.Size
			obj.Actions = map[string]lfsAction{"upload": {Href: href + o.OID, Header: hdr, ExpiresIn: expires}}
		}
		out = append(out, obj)
	}
	lfsJSON(w, http.StatusOK, map[string]any{"transfer": "basic", "objects": out, "hash_algo": "sha256"})
}

func (s *Server) lfsUpload(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.lfsOpen(w, r, store.AccessWrite)
	if !ok {
		return
	}
	oid := r.PathValue("oid")
	if !lfs.ValidOID(oid) {
		lfsFail(w, http.StatusUnprocessableEntity, "Invalid object id")
		return
	}
	maxObject, _ := s.lfsLimits(r.Context())
	size := int64(-1)
	if r.ContentLength >= 0 {
		size = r.ContentLength
	}
	err := s.LFS.Put(repo.ID, oid, size, maxObject, r.Body)
	switch {
	case errors.Is(err, lfs.ErrTooLarge):
		lfsFail(w, http.StatusRequestEntityTooLarge, "Object is larger than this server accepts")
	case errors.Is(err, lfs.ErrMismatch):
		lfsFail(w, http.StatusUnprocessableEntity, "Content does not match the object id")
	case err != nil:
		s.Log.Error("lfs upload", "repo", repo.ID, "err", err)
		lfsFail(w, http.StatusInternalServerError, "internal error")
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) lfsDownload(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.lfsOpen(w, r, store.AccessRead)
	if !ok {
		return
	}
	s.serveLFSObject(w, r, repo.ID, r.PathValue("oid"), "")
}

// serveLFSObject streams one object (LFS download, and the raw view of a pointer file).
func (s *Server) serveLFSObject(w http.ResponseWriter, r *http.Request, repoID int64, oid, name string) {
	f, err := s.LFS.Open(repoID, oid)
	if err != nil {
		lfsFail(w, http.StatusNotFound, "Object does not exist")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		lfsFail(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	if name != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(name, "\"", "")+"\"")
	}
	_, _ = io.Copy(w, f)
}

// Locks are not implemented; answering empty keeps `git push` from warning on every push.
func (s *Server) lfsLocksVerify(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lfsOpen(w, r, store.AccessWrite); ok {
		lfsJSON(w, http.StatusOK, map[string]any{"ours": []any{}, "theirs": []any{}})
	}
}

func (s *Server) lfsLocks(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lfsOpen(w, r, store.AccessRead); ok {
		lfsJSON(w, http.StatusOK, map[string]any{"locks": []any{}})
	}
}

// LFSAuthenticate answers `git-lfs-authenticate owner/repo upload|download` over SSH: where the LFS server is and a
// short token for it (D82). Access is checked here and again on every use of the token.
func (s *Server) LFSAuthenticate(ctx context.Context, uid int64, owner, name, op string) ([]byte, error) {
	if s.LFS == nil || s.Mirror == nil || s.Mirror.Key == nil {
		return nil, errors.New("lfs: not enabled")
	}
	repo, err := s.Store.RepoByPath(ctx, owner, strings.TrimSuffix(name, ".git"))
	if err != nil {
		return nil, store.ErrNotFound
	}
	a, err := s.Store.AccessFor(ctx, uid, repo)
	if err != nil {
		return nil, err
	}
	write := op == "upload"
	if a < store.AccessRead || (write && (a < store.AccessWrite || repo.Mirror || repo.Archived)) {
		return nil, store.ErrNotFound
	}
	ttl := time.Duration(s.settingInt(ctx, "lfs.ssh_token_minutes", 60)) * time.Minute
	tok := lfs.SignAuth(s.Mirror.Key, uid, repo.ID, write, time.Now().Add(ttl))
	return json.Marshal(map[string]any{"href": strings.TrimRight(s.UI.HTTPHost, "/") + "/" + repo.Owner + "/" + repo.Name + ".git/info/lfs",
		"header": map[string]string{"Authorization": "Bearer " + tok}, "expires_in": int(ttl / time.Second)})
}
