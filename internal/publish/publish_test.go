package publish

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"palimpseste/internal/site"
)

func sig() *object.Signature {
	return &object.Signature{Name: "T", Email: "t@e", When: time.Unix(1_700_000_000, 0)}
}

// §13: publish is git-push. Set up a bare remote and a working repo, then push
// HEAD onto the declared branch.
func TestGitPushDeploysBranch(t *testing.T) {
	remoteDir := t.TempDir()
	if _, err := git.PlainInit(remoteDir, true); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	repo, err := git.PlainInit(workDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add("index.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{Author: sig()}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remoteDir}}); err != nil {
		t.Fatal(err)
	}

	cfg := site.Publish{Method: "git-push", Remote: "origin", Branch: "pages"}
	res, err := Run(workDir, cfg)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Detail != "poussé" {
		t.Errorf("first push detail = %q, want poussé", res.Detail)
	}

	// The remote now has the pages branch pointing at our commit.
	remote, _ := git.PlainOpen(remoteDir)
	ref, err := remote.Reference(plumbing.NewBranchReferenceName("pages"), true)
	if err != nil {
		t.Fatalf("pages branch missing on remote: %v", err)
	}
	head, _ := repo.Head()
	if ref.Hash() != head.Hash() {
		t.Errorf("remote pages = %s, local HEAD = %s", ref.Hash(), head.Hash())
	}

	// A second publish with nothing new is a success, not an error.
	res2, err := Run(workDir, cfg)
	if err != nil {
		t.Fatalf("idempotent publish errored: %v", err)
	}
	if res2.Detail != "déjà à jour" {
		t.Errorf("second push detail = %q, want déjà à jour", res2.Detail)
	}
}

func TestRunRejectsUnconfigured(t *testing.T) {
	if _, err := Run(t.TempDir(), site.Publish{}); err == nil {
		t.Error("publish with no method should error")
	}
	if _, err := Run(t.TempDir(), site.Publish{Method: "rsync"}); err == nil {
		t.Error("unsupported method should error")
	}
	if Configured(site.Publish{Method: "git-push", Remote: "origin"}) {
		t.Error("git-push without a branch should not be Configured")
	}
	if !Configured(site.Publish{Method: "git-push", Remote: "origin", Branch: "pages"}) {
		t.Error("complete git-push config should be Configured")
	}
}
