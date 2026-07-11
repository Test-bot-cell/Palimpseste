package main

// The theme subcommands: the §5.3 compatibility check and the guarded switch,
// scriptable from CI exactly like build --check — a blocking finding exits
// non-zero, so "apply on green" automates safely.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"palimpseste/internal/history"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
	"palimpseste/internal/themecheck"
)

func cmdTheme(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "palimpseste theme: expected list, check or apply")
		return 2
	}
	sub, rest := args[0], args[1:]

	fs := flag.NewFlagSet("theme "+sub, flag.ExitOnError)
	siteDir := fs.String("site", ".", "site directory (site.json, content/, themes/)")
	_ = fs.Parse(rest)

	switch sub {
	case "list":
		return themeList(*siteDir)
	case "check":
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: palimpseste theme check [-site dir] <name>")
			return 2
		}
		return themeCheck(*siteDir, fs.Arg(0))
	case "apply":
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: palimpseste theme apply [-site dir] <name>")
			return 2
		}
		return themeApply(*siteDir, fs.Arg(0))
	default:
		fmt.Fprintf(os.Stderr, "palimpseste theme: unknown subcommand %q\n", sub)
		return 2
	}
}

func themeList(siteDir string) int {
	s, err := site.Load(siteDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	entries, err := os.ReadDir(filepath.Join(siteDir, "themes"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		t, err := theme.Load(siteDir, name)
		switch {
		case err != nil:
			fmt.Printf("  %-16s (manifeste invalide : %v)\n", name, err)
		case t.Name == s.Theme:
			fmt.Printf("* %-16s %s (actif)\n", t.Name, t.Version)
		default:
			fmt.Printf("  %-16s %s\n", t.Name, t.Version)
		}
	}
	return 0
}

func printReport(rep themecheck.Report) {
	if len(rep.Findings) == 0 {
		fmt.Printf("%s → %s : compatibilité totale, aucune remarque.\n", rep.Current, rep.Candidate)
		return
	}
	fmt.Printf("%s → %s :\n", rep.Current, rep.Candidate)
	for _, f := range rep.Findings {
		fmt.Printf("  [%s] %s: %s\n", f.Severity, f.Rule, f.Detail)
	}
}

func themeCheck(siteDir, name string) int {
	rep, err := themecheck.Check(siteDir, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	printReport(rep)
	if rep.Blocking() {
		return 1
	}
	return 0
}

func themeApply(siteDir, name string) int {
	rep, moved, err := themecheck.Apply(siteDir, name)
	printReport(rep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}

	// §13: the migration renames are their own commit, the switch its own.
	rec, err := history.Open(siteDir)
	if err == nil {
		if len(moved) > 0 {
			var paths []string
			for _, m := range moved {
				paths = append(paths, m.From, m.To)
			}
			if err := rec.CommitPaths(paths, "theme(migrate)"); err != nil {
				fmt.Fprintf(os.Stderr, "palimpseste: commit migrate: %v\n", err)
			}
		}
		if err := rec.CommitPaths([]string{filepath.Join(siteDir, "site.json")},
			fmt.Sprintf("theme(%s)", name)); err != nil {
			fmt.Fprintf(os.Stderr, "palimpseste: commit: %v\n", err)
		}
	}

	fmt.Printf("thème actif : %s (%d fragment(s) migré(s)) — relancer build/edit pour matérialiser.\n", name, len(moved))
	return 0
}
