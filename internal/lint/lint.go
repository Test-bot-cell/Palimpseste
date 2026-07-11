// Package lint runs Palimpseste's build-time content checks (the `--check`
// pass). Every check is a pure function of the materialized document plus the
// site model, so linting adds no runtime dependency and stays deterministic.
//
// Checks are advisory by default (Warn); only outright missing structure
// (e.g. a page with no title) is an Error. The build reports issues but a
// caller decides whether warnings should fail CI.
package lint

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"palimpseste/internal/materialize"
	"palimpseste/internal/render"
	"palimpseste/internal/site"
)

// Severity ranks an issue.
type Severity int

const (
	Warn Severity = iota
	Error
)

func (s Severity) String() string {
	if s == Error {
		return "error"
	}
	return "warn"
}

// Issue is a single finding on a page.
type Issue struct {
	Page     string
	Severity Severity
	Rule     string
	Message  string
}

func (i Issue) String() string {
	return fmt.Sprintf("[%s] %s (%s): %s", i.Severity, i.Page, i.Rule, i.Message)
}

// Length bounds for the metadata checks; conventional SEO guidance.
const (
	titleMax = 60
	descMin  = 50
	descMax  = 160
)

// CheckPage runs every check against a materialized page. routes is the set of
// valid internal routes (from site.json) used to flag broken internal links.
func CheckPage(doc *html.Node, s *site.Site, p site.Page, rep materialize.Report, routes map[string]bool) []Issue {
	var issues []Issue
	add := func(sev Severity, rule, msg string) {
		issues = append(issues, Issue{Page: p.ID, Severity: sev, Rule: rule, Message: msg})
	}

	// Missing slots: template asked for a region with no fragment on disk.
	for _, name := range rep.Missing {
		add(Warn, "missing-slot", fmt.Sprintf("slot %q has no fragment", name))
	}

	// Title.
	switch title := strings.TrimSpace(p.Title); {
	case title == "":
		add(Error, "title", "page has no title")
	case len(title) > titleMax:
		add(Warn, "title", fmt.Sprintf("title is %d chars (> %d)", len(title), titleMax))
	}

	// Description.
	switch d := strings.TrimSpace(p.Description); {
	case d == "":
		add(Warn, "description", "page has no description")
	case len(d) < descMin:
		add(Warn, "description", fmt.Sprintf("description is %d chars (< %d)", len(d), descMin))
	case len(d) > descMax:
		add(Warn, "description", fmt.Sprintf("description is %d chars (> %d)", len(d), descMax))
	}

	// Image alt text.
	for _, img := range render.FindAll(doc, isElement(atom.Img)) {
		alt, ok := render.GetAttr(img, "alt")
		if !ok || strings.TrimSpace(alt) == "" {
			src, _ := render.GetAttr(img, "src")
			add(Warn, "img-alt", fmt.Sprintf("<img src=%q> has empty alt", src))
		}
	}

	// Heading hierarchy: first heading should be h1, and levels must not jump
	// by more than one on the way down.
	prev := 0
	for _, h := range render.FindAll(doc, isHeading) {
		lvl := headingLevel(h)
		switch {
		case prev == 0 && lvl != 1:
			add(Warn, "heading", fmt.Sprintf("first heading is h%d, expected h1", lvl))
		case lvl > prev+1:
			add(Warn, "heading", fmt.Sprintf("heading jumps from h%d to h%d", prev, lvl))
		}
		prev = lvl
	}

	// Broken internal links.
	for _, a := range render.FindAll(doc, isElement(atom.A)) {
		href, ok := render.GetAttr(a, "href")
		if !ok || !isInternal(href) {
			continue
		}
		if !routeKnown(href, routes) {
			add(Warn, "broken-link", fmt.Sprintf("internal link %q matches no route", href))
		}
	}

	return issues
}

// RouteSet builds the lookup of valid internal routes from a site.
func RouteSet(s *site.Site) map[string]bool {
	set := make(map[string]bool, len(s.Pages))
	for _, p := range s.Pages {
		set[p.Route] = true
	}
	return set
}

// Sort orders issues deterministically for stable reporting.
func Sort(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Page != issues[j].Page {
			return issues[i].Page < issues[j].Page
		}
		if issues[i].Rule != issues[j].Rule {
			return issues[i].Rule < issues[j].Rule
		}
		return issues[i].Message < issues[j].Message
	})
}

// --- predicates --------------------------------------------------------------

func isElement(a atom.Atom) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Type == html.ElementNode && n.DataAtom == a }
}

func isHeading(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}

func headingLevel(n *html.Node) int {
	switch n.DataAtom {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	case atom.H6:
		return 6
	}
	return 0
}

// isInternal reports whether href points within this site (root-relative and
// not an asset, anchor, or external/scheme link).
func isInternal(href string) bool {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return false
	}
	if strings.Contains(href, "://") {
		return false
	}
	for _, scheme := range []string{"mailto:", "tel:", "data:", "javascript:"} {
		if strings.HasPrefix(href, scheme) {
			return false
		}
	}
	return strings.HasPrefix(href, "/")
}

// routeKnown checks an internal href (minus query/fragment) against the route
// set. Asset links under /assets/ are always accepted.
func routeKnown(href string, routes map[string]bool) bool {
	path := href
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if strings.HasPrefix(path, "/assets/") {
		return true
	}
	if routes[path] {
		return true
	}
	// tolerate trailing-slash variants of a known route
	trimmed := strings.TrimRight(path, "/")
	return trimmed != "" && routes[trimmed]
}
