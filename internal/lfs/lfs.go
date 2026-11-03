// Package lfs stores Git LFS objects (D82): content-addressed files per repository under data/lfs, written only after
// their sha256 and size are checked. Objects never change once written, so a backup can hard-link them and a fork can
// share them without copying bytes.
package lfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Errors an upload maps to answers.
var (
	ErrMismatch = errors.New("lfs: content does not match its oid or size")
	ErrTooLarge = errors.New("lfs: object larger than the limit")
)

var oidRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidOID reports whether s is a sha256 object id in lower-case hex.
func ValidOID(s string) bool { return oidRE.MatchString(s) }

// Store is the object store; Root is data/lfs.
type Store struct{ Root string }

func (s Store) repoDir(repoID int64) string {
	return filepath.Join(s.Root, strconv.FormatInt(repoID, 10))
}

// Path is where an object of a repository lives. oid must be valid (callers check ValidOID).
func (s Store) Path(repoID int64, oid string) string {
	return filepath.Join(s.repoDir(repoID), oid[0:2], oid[2:4], oid)
}

// Stat returns the size of a stored object.
func (s Store) Stat(repoID int64, oid string) (int64, bool) {
	if !ValidOID(oid) {
		return 0, false
	}
	fi, err := os.Stat(s.Path(repoID, oid))
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// Open opens a stored object for reading.
func (s Store) Open(repoID int64, oid string) (*os.File, error) {
	if !ValidOID(oid) {
		return nil, fs.ErrNotExist
	}
	return os.Open(s.Path(repoID, oid)) // #nosec G304 -- path built from a numeric id and a validated hex oid
}

// Put writes an object from r, at most limit bytes (0 = no limit). The bytes go to a temporary file first; only when
// their sha256 equals oid (and the size equals size, when size >= 0) is the file moved into place, so a half upload or
// a lie about the content never becomes an object.
func (s Store) Put(repoID int64, oid string, size, limit int64, r io.Reader) error {
	if !ValidOID(oid) {
		return ErrMismatch
	}
	tmpDir := filepath.Join(s.Root, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(tmpDir, "up-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	src := r
	if limit > 0 {
		src = io.LimitReader(r, limit+1)
	}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if limit > 0 && n > limit {
		return ErrTooLarge
	}
	if hex.EncodeToString(h.Sum(nil)) != oid || (size >= 0 && n != size) {
		return ErrMismatch
	}
	dst := s.Path(repoID, oid)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}

// Usage counts a repository's objects and bytes.
func (s Store) Usage(repoID int64) (count int, bytes int64, err error) {
	err = s.walkRepo(repoID, func(_ string, size int64) error {
		count++
		bytes += size
		return nil
	})
	return
}

func (s Store) walkRepo(repoID int64, fn func(oid string, size int64) error) error {
	root := s.repoDir(repoID)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && ValidOID(d.Name()) {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return fn(d.Name(), fi.Size())
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Repos lists the repository ids that have an object folder (backup).
func (s Store) Repos() ([]int64, error) {
	ents, err := os.ReadDir(s.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, e := range ents {
		if id, err := strconv.ParseInt(e.Name(), 10, 64); err == nil && e.IsDir() {
			out = append(out, id)
		}
	}
	return out, nil
}

// Delete removes every object of a repository (the repository was deleted).
func (s Store) Delete(repoID int64) error { return os.RemoveAll(s.repoDir(repoID)) }

// LinkAll makes every object of one repository available in another (a fork, a merge from a fork, a backup): a hard
// link when both are on one filesystem, a copy otherwise. Objects the target has already are left alone.
func (s Store) LinkAll(from int64, to Store, toID int64) (int, error) {
	n := 0
	err := s.walkRepo(from, func(oid string, _ int64) error {
		if _, ok := to.Stat(toID, oid); ok {
			return nil
		}
		dst := to.Path(toID, oid)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.Link(s.Path(from, oid), dst); err != nil {
			if err := copyFile(s.Path(from, oid), dst); err != nil {
				return err
			}
		}
		n++
		return nil
	})
	return n, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- inside the object store
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- inside the object store
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Verify checks every object of a repository against its name (restore drill). It returns the oids that do not match.
func (s Store) Verify(repoID int64) (bad []string, err error) {
	err = s.walkRepo(repoID, func(oid string, _ int64) error {
		f, err := s.Open(repoID, oid)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != oid {
			bad = append(bad, oid)
		}
		return nil
	})
	return
}

// Pointer is a parsed LFS pointer file.
type Pointer struct {
	OID  string
	Size int64
}

const pointerMax = 1024

// ParsePointer recognises a Git LFS pointer file (spec v1): a version line, then sorted key/value lines including
// "oid sha256:<hex>" and "size <n>". Anything else is ordinary content.
func ParsePointer(b []byte) (Pointer, bool) {
	var p Pointer
	if len(b) > pointerMax || !bytes.HasPrefix(b, []byte("version https://git-lfs.github.com/spec/v1\n")) {
		return p, false
	}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n")[1:] {
		k, v, ok := strings.Cut(line, " ")
		if !ok {
			return p, false
		}
		switch k {
		case "oid":
			hexID, ok := strings.CutPrefix(v, "sha256:")
			if !ok || !ValidOID(hexID) {
				return p, false
			}
			p.OID = hexID
		case "size":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return p, false
			}
			p.Size = n
		}
	}
	return p, p.OID != ""
}
