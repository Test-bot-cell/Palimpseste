// Package history records each editor save as a git commit, satisfying the
// versioning contract (§13): every write to a fragment is a discrete, diffable
// revision with a conventional message — edit(page/slot), data(page), theme(id)
// — authored by the user (their merged git config identity), exactly as if they
// had committed by hand.
//
// It is a thin, defensive layer over go-git. Two properties matter. First, it
// is optional: if the site directory is not inside a git working tree the
// Recorder is disabled and every Commit is a no-op, so `palimpseste edit` still
// runs on an un-versioned folder — but a disabled Recorder re-probes on every
// Commit, so a repository initialised while the editor runs is picked up at the
// very next save. Second, publication is out of scope here — this package only
// ever writes local commits; pushing to a remote is a distinct, authenticated
// step deferred to M3.
package history

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Fallback authorship, used only when the surrounding repository declares no
// user.name/user.email anywhere in its merged config. §13 wants the user as
// author; an anonymous machine identity is the honest degraded form.
const (
	fallbackName  = "Palimpseste"
	fallbackEmail = "edit@palimpseste.local"
)

// Recorder commits fragment writes to the git repository enclosing a site. The
// zero value is not useful; obtain one from Open. A single mutex serialises
// commits because go-git worktree operations are not safe for concurrent use.
type Recorder struct {
	mu      sync.Mutex
	siteDir string
	repo    *git.Repository
	name    string
	email   string
}

// Open returns a Recorder bound to the git working tree enclosing siteDir, or a
// disabled one — nil error — when no repository encloses it (versioning is a
// bonus, never a prerequisite for editing). DetectDotGit walks parents, so a
// site nested inside a larger repository is versioned against that repository.
func Open(siteDir string) (*Recorder, error) {
	r := &Recorder{siteDir: siteDir}
	if err := r.probe(); err != nil {
		return nil, err
	}
	return r, nil
}

// probe (re-)attempts to bind the enclosing repository and resolve the author
// identity. Caller holds mu (or owns r exclusively, as Open does).
func (r *Recorder) probe() error {
	repo, err := git.PlainOpenWithOptions(r.siteDir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			r.repo = nil
			return nil
		}
		return fmt.Errorf("open git repository for %q: %w", r.siteDir, err)
	}
	r.repo = repo
	r.name, r.email = authorIdentity(repo)
	return nil
}

// authorIdentity resolves the commit author per §13 ("auteur = utilisateur"):
// the repository's merged config — local, then global, then system — exactly
// the identity `git commit` would sign with. Missing pieces fall back to the
// editor's own identity so a commit is never blocked on configuration.
func authorIdentity(repo *git.Repository) (name, email string) {
	name, email = fallbackName, fallbackEmail
	for _, scope := range []config.Scope{config.SystemScope, config.GlobalScope} {
		if cfg, err := config.LoadConfig(scope); err == nil {
			if cfg.User.Name != "" {
				name = cfg.User.Name
			}
			if cfg.User.Email != "" {
				email = cfg.User.Email
			}
		}
	}
	if cfg, err := repo.Config(); err == nil {
		if cfg.User.Name != "" {
			name = cfg.User.Name
		}
		if cfg.User.Email != "" {
			email = cfg.User.Email
		}
	}
	return name, email
}

// Enabled reports whether commits will actually be recorded.
func (r *Recorder) Enabled() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.repo != nil
}

// Commit stages the single file at absPath and records it with message. It is a
// no-op — returning nil — when no repository encloses the site or when absPath
// already matches HEAD, so re-saving unchanged prose never manufactures an
// empty commit. A disabled Recorder first re-probes, so `git init` run while
// the editor is up takes effect on the next save.
func (r *Recorder) Commit(absPath, message string) error {
	return r.CommitPaths([]string{absPath}, message)
}

// CommitPaths stages exactly the given files — additions, edits and deletions
// alike (a theme migration renames fragments, which is one deletion plus one
// addition, committed together as §5.3's dedicated commit) — and records them
// with message. Paths that match HEAD are skipped; if nothing remains, no
// commit is made.
//
// go-git commits the whole staging area, so only these paths are added; any
// unrelated changes the operator left staged before opening the editor are
// their own to manage, exactly as with git on the command line.
func (r *Recorder) CommitPaths(absPaths []string, message string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.repo == nil {
		if err := r.probe(); err != nil || r.repo == nil {
			return err
		}
	}

	wt, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	root := wt.Filesystem.Root()

	st, err := wt.Status()
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	staged := 0
	for _, absPath := range absPaths {
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("locate %q under worktree %q: %w", absPath, root, err)
		}
		// go-git's status index is keyed by slash-separated paths; Add takes
		// the OS-native form. On the editor's target platforms these coincide,
		// but keep both explicit so the pre-check and the stage always refer
		// to one file.
		relSlash := filepath.ToSlash(rel)

		// go-git's status map only carries files that differ from HEAD; an
		// absent entry IS the unmodified case (Status.File would fabricate an
		// "untracked" placeholder for it — the trap that used to turn every
		// unchanged re-save into a doomed empty-commit attempt).
		fs, changed := st[relSlash]
		if !changed || (fs.Worktree == git.Unmodified && fs.Staging == git.Unmodified) {
			continue
		}
		if _, err := wt.Add(rel); err != nil {
			return fmt.Errorf("stage %q: %w", relSlash, err)
		}
		staged++
	}
	if staged == 0 {
		return nil // nothing changed; skip the empty commit
	}

	_, err = wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  r.name,
			Email: r.email,
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("commit %q: %w", message, err)
	}
	return nil
}
