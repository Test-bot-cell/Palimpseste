// Package publish is the explicit deployment act (§13): publishing is decoupled
// from saving — a save is a local commit, publishing pushes. V1 speaks
// git-push, driven by go-git, with credentials read from the environment and
// never from the repository (§13 "Secrets … jamais dans le dépôt").
package publish

import (
	"fmt"
	"os"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"

	"palimpseste/internal/site"
)

// Result summarises a publish.
type Result struct {
	Method string `json:"method"`
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Ref    string `json:"ref"`    // the local ref pushed
	Detail string `json:"detail"` // human-readable outcome
}

// Run executes the site's declared publish method. It never writes content —
// it only pushes what git already holds — so a publish with nothing new to
// send is a success with an "already up to date" detail.
func Run(siteDir string, cfg site.Publish) (*Result, error) {
	switch cfg.Method {
	case "git-push":
		return gitPush(siteDir, cfg)
	case "":
		return nil, fmt.Errorf("no publish method declared in site.json (§13)")
	default:
		return nil, fmt.Errorf("unsupported publish method %q", cfg.Method)
	}
}

func gitPush(siteDir string, cfg site.Publish) (*Result, error) {
	if cfg.Remote == "" || cfg.Branch == "" {
		return nil, fmt.Errorf("git-push needs both remote and branch in site.json")
	}
	repo, err := git.PlainOpenWithOptions(siteDir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD (commit something first): %w", err)
	}

	// The push refspec maps the current HEAD onto the target branch, so
	// `git-push` to a Pages branch works whatever branch the operator edits on.
	refspec := config.RefSpec(fmt.Sprintf("+%s:refs/heads/%s", head.Name().String(), cfg.Branch))
	opts := &git.PushOptions{
		RemoteName: cfg.Remote,
		RefSpecs:   []config.RefSpec{refspec},
		Auth:       credentials(),
	}
	err = repo.Push(opts)
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return nil, fmt.Errorf("push to %s/%s: %w", cfg.Remote, cfg.Branch, err)
	}

	detail := "poussé"
	if err == git.NoErrAlreadyUpToDate {
		detail = "déjà à jour"
	}
	return &Result{
		Method: cfg.Method, Remote: cfg.Remote, Branch: cfg.Branch,
		Ref: head.Name().String(), Detail: detail,
	}, nil
}

// credentials reads push auth from the environment (§13): a token
// (PALIMPSESTE_GIT_TOKEN, optionally with PALIMPSESTE_GIT_USER) for HTTPS
// remotes. Absent credentials mean an unauthenticated push — fine for a public
// remote or an SSH agent the underlying transport handles. Never read from the
// repository.
func credentials() transport.AuthMethod {
	token := os.Getenv("PALIMPSESTE_GIT_TOKEN")
	if token == "" {
		return nil
	}
	user := os.Getenv("PALIMPSESTE_GIT_USER")
	if user == "" {
		user = "palimpseste" // GitHub/GitLab ignore the username when a token is the password
	}
	return &http.BasicAuth{Username: user, Password: token}
}

// Configured reports whether the site declares a usable publish method — the
// editor uses it to enable or hide the publish control.
func Configured(cfg site.Publish) bool {
	return cfg.Method == "git-push" && cfg.Remote != "" && cfg.Branch != ""
}
