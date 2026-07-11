package main

// palimpseste publish is the command-line face of the §13 deployment act: it
// runs the site's declared publish method with credentials from the
// environment (PALIMPSESTE_GIT_TOKEN), never the repository. Scriptable from CI
// exactly like build.

import (
	"flag"
	"fmt"
	"os"

	"palimpseste/internal/publish"
	"palimpseste/internal/site"
)

func cmdPublish(args []string) int {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	siteDir := fs.String("site", ".", "site directory (site.json, content/, themes/)")
	_ = fs.Parse(args)

	s, err := site.Load(*siteDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	if !publish.Configured(s.Publish) {
		fmt.Fprintln(os.Stderr, "palimpseste: aucune méthode de publication déclarée dans site.json (§13)")
		return 1
	}
	res, err := publish.Run(*siteDir, s.Publish)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	fmt.Printf("publié via %s → %s/%s (%s)\n", res.Method, res.Remote, res.Branch, res.Detail)
	return 0
}
