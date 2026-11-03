package gitread

import (
	"bytes"
	"context"
	"strconv"
	"strings"
)

// PathSize is one blob of a tree listing.
type PathSize struct {
	Path string
	Size int64
}

// TreeSizes lists every blob under ref with its size in ONE `git ls-tree -r -l` (D67), stopping after max
// entries; capped reports whether the listing was cut. Submodules and symlinks are skipped.
func (r Repo) TreeSizes(ctx context.Context, ref string, max int) (files []PathSize, capped bool, err error) {
	if !ValidRef(ref) {
		return nil, false, ErrBadRef
	}
	out, err := r.run(ctx, nil, "ls-tree", "-r", "-l", "-z", "--end-of-options", ref)
	if err != nil {
		return nil, false, err
	}
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, name, ok := strings.Cut(string(rec), "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 || f[1] != "blob" || f[0] == "120000" {
			continue
		}
		if len(files) >= max {
			return files, true, nil
		}
		n, _ := strconv.ParseInt(f[3], 10, 64)
		files = append(files, PathSize{Path: name, Size: n})
	}
	return files, false, nil
}
