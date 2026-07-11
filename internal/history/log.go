package history

// The history view and revert (§13): "l'UI d'historique n'est qu'une vue sur
// git log." These read the commit history touching a given file and restore a
// file's bytes from a past commit — the undo that git already gives, surfaced.

import (
	"fmt"
	"io"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Revision is one commit that touched a file.
type Revision struct {
	Hash    string `json:"hash"`
	Message string `json:"message"`
	Author  string `json:"author"`
	When    string `json:"when"` // RFC3339
}

// Log returns the commits that changed absPath, newest first, capped at limit.
// A disabled Recorder (no repo) yields an empty list, not an error — history is
// a bonus, exactly like commit-on-save.
func (r *Recorder) Log(absPath string, limit int) ([]Revision, error) {
	if !r.Enabled() {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	wt, err := r.repo.Worktree()
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(wt.Filesystem.Root(), absPath)
	if err != nil {
		return nil, err
	}
	relSlash := filepath.ToSlash(rel)

	iter, err := r.repo.Log(&git.LogOptions{FileName: &relSlash, Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []Revision
	err = iter.ForEach(func(c *object.Commit) error {
		out = append(out, Revision{
			Hash:    c.Hash.String(),
			Message: c.Message,
			Author:  c.Author.Name,
			When:    c.Author.When.UTC().Format("2006-01-02T15:04:05Z"),
		})
		if limit > 0 && len(out) >= limit {
			return storeStop
		}
		return nil
	})
	if err != nil && err != storeStop {
		return nil, err
	}
	return out, nil
}

// storeStop short-circuits ForEach once the limit is reached.
var storeStop = fmt.Errorf("stop")

// FileAt returns the bytes absPath had at commit hash — the content a revert
// would restore. The caller re-sanitises and re-writes through the normal path,
// so a revert is an ordinary edit, not a privileged rewrite.
func (r *Recorder) FileAt(absPath, hash string) ([]byte, error) {
	if !r.Enabled() {
		return nil, fmt.Errorf("no repository")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	h := plumbing.NewHash(hash)
	if h.IsZero() {
		return nil, fmt.Errorf("invalid commit hash %q", hash)
	}
	commit, err := r.repo.CommitObject(h)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", hash, err)
	}
	wt, err := r.repo.Worktree()
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(wt.Filesystem.Root(), absPath)
	if err != nil {
		return nil, err
	}
	f, err := commit.File(filepath.ToSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("file at %s: %w", hash, err)
	}
	reader, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
