package history

import (
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func initRepo(t *testing.T, dir, userName, userEmail string) *git.Repository {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.User.Name = userName
	cfg.User.Email = userEmail
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return repo
}

func headCommit(t *testing.T, repo *git.Repository) *object.Commit {
	t.Helper()
	ref, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// §13: chaque sauvegarde = un commit, auteur = utilisateur (the repo's own
// configured identity), message structuré.
func TestCommitUsesRepoUserIdentity(t *testing.T) {
	dir := t.TempDir()
	repo := initRepo(t, dir, "Raphaël Test", "raph@example.org")

	rec, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Enabled() {
		t.Fatal("recorder should be enabled inside a repo")
	}

	frag := filepath.Join(dir, "content", "home", "main.html")
	if err := os.MkdirAll(filepath.Dir(frag), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frag, []byte("<p>v1</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rec.Commit(frag, "edit(home/main)"); err != nil {
		t.Fatal(err)
	}

	c := headCommit(t, repo)
	if c.Message != "edit(home/main)" {
		t.Errorf("message = %q", c.Message)
	}
	if c.Author.Name != "Raphaël Test" || c.Author.Email != "raph@example.org" {
		t.Errorf("author = %s <%s>, want the repo's configured user (§13)", c.Author.Name, c.Author.Email)
	}
}

// A site outside any repository edits fine (no-op), and a repository created
// while the editor runs is picked up at the very next save — no restart needed.
func TestDisabledRecorderReprobesOnCommit(t *testing.T) {
	dir := t.TempDir()
	rec, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Enabled() {
		t.Fatal("no repo yet: recorder should be disabled")
	}

	frag := filepath.Join(dir, "note.html")
	if err := os.WriteFile(frag, []byte("<p>a</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rec.Commit(frag, "edit(x/y)"); err != nil {
		t.Fatalf("no-op commit errored: %v", err)
	}

	repo := initRepo(t, dir, "Late Init", "late@example.org")
	if err := rec.Commit(frag, "edit(x/y)"); err != nil {
		t.Fatalf("commit after late git init: %v", err)
	}
	c := headCommit(t, repo)
	if c.Message != "edit(x/y)" || c.Author.Name != "Late Init" {
		t.Errorf("late-init commit = %q by %s", c.Message, c.Author.Name)
	}
}

// Re-saving identical content never manufactures an empty commit.
func TestUnchangedFileSkipsEmptyCommit(t *testing.T) {
	dir := t.TempDir()
	repo := initRepo(t, dir, "U", "u@example.org")
	rec, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	frag := filepath.Join(dir, "main.html")
	if err := os.WriteFile(frag, []byte("<p>same</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rec.Commit(frag, "edit(a/b)"); err != nil {
		t.Fatal(err)
	}
	if err := rec.Commit(frag, "edit(a/b)"); err != nil {
		t.Fatal(err)
	}

	iter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	_ = iter.ForEach(func(*object.Commit) error { count++; return nil })
	if count != 1 {
		t.Errorf("commit count = %d, want 1 (no empty commits)", count)
	}
}
