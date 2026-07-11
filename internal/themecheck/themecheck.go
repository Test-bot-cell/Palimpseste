// Package themecheck implements the §5.3 compatibility validation that runs
// before any theme apply: it compares what the site actually holds — pages,
// fragments on disk, blocks in use, the current theme's declarations — against
// a candidate theme, and reports every consequence of switching. The rules,
// verbatim from the doc: required slot absent → blocking error; offered slot
// without content → warning (the page renders with an empty fragment);
// undeclared block → warning (the §4.1 graceful degradation); diverging data
// schema → migration report; renames → the optional "migrate" table, applied
// as a dedicated commit by the caller.
//
// Check never mutates anything. Apply performs exactly two mutations — the
// migrate renames and the site.json theme switch — and returns what it did so
// the caller (edit server or CLI) can commit each as §13 prescribes.
package themecheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"palimpseste/internal/content"
	"palimpseste/internal/render"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// Severity of one finding: blocking errors forbid the apply, warnings inform.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Finding is one §5.3 consequence of switching themes.
type Finding struct {
	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"`
	Detail   string   `json:"detail"`
}

// Report is the machine-readable outcome of a compatibility check.
type Report struct {
	Site      string    `json:"site"`
	Current   string    `json:"current"`
	Candidate string    `json:"candidate"`
	Findings  []Finding `json:"findings"`
}

// Blocking reports whether any finding forbids the apply.
func (r Report) Blocking() bool {
	for _, f := range r.Findings {
		if f.Severity == Error {
			return true
		}
	}
	return false
}

func (r *Report) add(sev Severity, rule, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{Severity: sev, Rule: rule, Detail: fmt.Sprintf(format, args...)})
}

// Check validates switching siteDir's site from its current theme to the named
// candidate. The candidate is loaded (and its manifest validated) as part of
// the check; a candidate that does not even load is itself the blocking error.
func Check(siteDir, candidateName string) (Report, error) {
	s, err := site.Load(siteDir)
	if err != nil {
		return Report{}, err
	}
	current, err := theme.Load(siteDir, s.Theme)
	if err != nil {
		return Report{}, fmt.Errorf("load current theme: %w", err)
	}
	rep := Report{Site: s.Name, Current: current.Name, Candidate: candidateName}

	candidate, err := theme.Load(siteDir, candidateName)
	if err != nil {
		rep.add(Error, "candidate-invalid", "%v", err)
		return rep, nil
	}
	rep.Candidate = candidate.Name

	ldr := content.NewLoader(siteDir)

	// Which slots does each theme's template set actually reference, per page?
	curSlots, err := templateSlots(current, s)
	if err != nil {
		return rep, err
	}
	candSlots, err := templateSlots(candidate, s)
	if err != nil {
		rep.add(Error, "template-missing", "%v", err)
		return rep, nil
	}

	// Renames the candidate declares are honoured before comparing: a slot the
	// migrate table maps is not "absent", it is moved.
	renamed := func(slot string) string {
		if to, ok := candidate.Migrate[slot]; ok {
			return to
		}
		return slot
	}

	for _, p := range s.SortedPages() {
		if !candidate.HasTemplate(p.Template) {
			rep.add(Error, "template-missing", "page %q needs template %q, absent from theme %q", p.ID, p.Template, candidate.Name)
			continue
		}

		cur := curSlots[p.Template]
		cand := toSet(candSlots[p.Template])

		// Required slot absent (blocking): the site holds content for a slot
		// the current theme displays, and the candidate's template loses it —
		// applying would silently drop the operator's prose.
		for _, slot := range cur {
			_, found, err := ldr.Fragment(p.ID, slot, p.Slots)
			if err != nil {
				return rep, err
			}
			if found && !cand[renamed(slot)] {
				rep.add(Error, "slot-required-absent", "page %q: slot %q carries content but theme %q offers no such slot", p.ID, slot, candidate.Name)
			}
		}

		// Offered slot without content (warning + empty fragment at render).
		for _, slot := range candSlots[p.Template] {
			_, found, err := ldr.Fragment(p.ID, slot, p.Slots)
			if err != nil {
				return rep, err
			}
			if !found {
				rep.add(Warning, "slot-empty", "page %q: slot %q offered by theme %q has no fragment yet (will render empty)", p.ID, slot, candidate.Name)
			}
		}

		// Blocks in use but undeclared by the candidate slot (warning: §4.1
		// degradation — valid semantic HTML, just unstyled/unrendered).
		for _, slot := range cur {
			frag, found, err := ldr.Fragment(p.ID, slot, p.Slots)
			if err != nil || !found {
				continue
			}
			target := renamed(slot)
			if !cand[target] {
				continue // already reported as slot-required-absent (or no content)
			}
			decl := toSet(candidate.Slots[target].Blocks)
			for _, b := range fragmentBlocks(frag) {
				if len(decl) > 0 && !decl[b] {
					rep.add(Warning, "block-undeclared", "page %q: slot %q uses block %q which theme %q does not declare for it (graceful degradation)", p.ID, slot, b, candidate.Name)
				}
			}
		}
	}

	// Slot type changes: same name, different editing contract.
	for name, cur := range current.Slots {
		if cand, ok := candidate.Slots[renamed(name)]; ok && cand.Type != cur.Type {
			rep.add(Warning, "slot-type-changed", "slot %q changes type %q → %q; existing content may not fit the new contract", name, cur.Type, cand.Type)
		}
	}

	// Data schema divergence → migration report (§5.3). The data/ engine lands
	// at M3; the schemas are contract now, so the report is owed now.
	dataMigration(&rep, current, candidate)

	sortFindings(rep.Findings)
	return rep, nil
}

// dataMigration compares declared data schemas table by table.
func dataMigration(rep *Report, current, candidate *theme.Theme) {
	names := map[string]bool{}
	for n := range current.Data {
		names[n] = true
	}
	for n := range candidate.Data {
		names[n] = true
	}
	for _, n := range sortedKeys(names) {
		cur, inCur := current.Data[n]
		cand, inCand := candidate.Data[n]
		switch {
		case inCur && !inCand:
			rep.add(Warning, "data-table-dropped", "table %q declared by %q is absent from %q (data kept on disk, no longer rendered)", n, current.Name, candidate.Name)
		case !inCur && inCand:
			rep.add(Warning, "data-table-added", "table %q is new in %q (format %s); create data/%s to feed it", n, candidate.Name, cand.Format, n)
		default:
			if cur.Format != cand.Format {
				rep.add(Warning, "data-format-changed", "table %q changes format %q → %q; migration needed", n, cur.Format, cand.Format)
			}
			cols := map[string]bool{}
			for c := range cur.Columns {
				cols[c] = true
			}
			for c := range cand.Columns {
				cols[c] = true
			}
			for _, c := range sortedKeys(cols) {
				ct, inC := cur.Columns[c]
				kt, inK := cand.Columns[c]
				switch {
				case inC && !inK:
					rep.add(Warning, "data-column-dropped", "table %q: column %q (%s) dropped in %q", n, c, ct, candidate.Name)
				case !inC && inK:
					rep.add(Warning, "data-column-added", "table %q: column %q (%s) added in %q", n, c, kt, candidate.Name)
				case ct != kt:
					rep.add(Warning, "data-column-type-changed", "table %q: column %q changes type %q → %q", n, c, ct, kt)
				}
			}
		}
	}
}

// AppliedMigration describes one fragment moved by a migrate rename.
type AppliedMigration struct {
	From, To string // absolute file paths
}

// Apply switches the site to the candidate theme. It re-runs Check, refuses on
// any blocking finding, applies the candidate's migrate renames (global
// fragments and every page's local fragments), then rewrites site.json. It
// returns the report, the performed renames and the new site so the caller can
// commit each mutation as §13 prescribes: one dedicated commit for the
// migration, one for the switch.
func Apply(siteDir, candidateName string) (Report, []AppliedMigration, error) {
	rep, err := Check(siteDir, candidateName)
	if err != nil {
		return rep, nil, err
	}
	if rep.Blocking() {
		return rep, nil, fmt.Errorf("theme %q is not compatible: %d blocking finding(s)", candidateName, countBlocking(rep))
	}
	candidate, err := theme.Load(siteDir, candidateName)
	if err != nil {
		return rep, nil, err
	}
	s, err := site.Load(siteDir)
	if err != nil {
		return rep, nil, err
	}

	var moved []AppliedMigration
	contentRoot := filepath.Join(siteDir, "content")
	for _, from := range sortedKeys(toSet(keys(candidate.Migrate))) {
		to := candidate.Migrate[from]
		for _, dir := range fragmentDirs(s, contentRoot) {
			src := filepath.Join(dir, from+".html")
			dst := filepath.Join(dir, to+".html")
			if !within(contentRoot, src) || !within(contentRoot, dst) {
				return rep, moved, fmt.Errorf("migrate %q → %q escapes content/", from, to)
			}
			if _, err := os.Stat(src); err != nil {
				continue
			}
			if err := os.Rename(src, dst); err != nil {
				return rep, moved, fmt.Errorf("migrate %q → %q: %w", from, to, err)
			}
			moved = append(moved, AppliedMigration{From: src, To: dst})
		}
	}

	s.Theme = candidateName
	if err := site.Save(siteDir, s); err != nil {
		return rep, moved, err
	}
	return rep, moved, nil
}

// fragmentDirs lists where slot fragments live: _global plus one directory per
// page id.
func fragmentDirs(s *site.Site, contentRoot string) []string {
	dirs := []string{filepath.Join(contentRoot, "_global")}
	for _, p := range s.SortedPages() {
		dirs = append(dirs, filepath.Join(contentRoot, p.ID))
	}
	return dirs
}

// templateSlots parses every template the site's pages use under t and returns
// the data-slot names each references, in template order.
func templateSlots(t *theme.Theme, s *site.Site) (map[string][]string, error) {
	out := map[string][]string{}
	for _, p := range s.SortedPages() {
		if _, done := out[p.Template]; done {
			continue
		}
		tmpl, err := t.Template(p.Template)
		if err != nil {
			return nil, fmt.Errorf("theme %q: %w", t.Name, err)
		}
		doc, err := render.ParseDocument(tmpl)
		if err != nil {
			return nil, fmt.Errorf("theme %q template %q: %w", t.Name, p.Template, err)
		}
		var slots []string
		for _, el := range render.FindAll(doc, func(n *html.Node) bool {
			if n.Type != html.ElementNode {
				return false
			}
			_, ok := render.GetAttr(n, "data-slot")
			return ok
		}) {
			name, _ := render.GetAttr(el, "data-slot")
			slots = append(slots, name)
		}
		out[p.Template] = slots
	}
	return out, nil
}

// fragmentBlocks lists the data-block names a stored fragment uses.
func fragmentBlocks(frag string) []string {
	host := render.Element("body", nil)
	nodes, err := render.ParseFragment(frag, host)
	if err != nil {
		return nil
	}
	render.ReplaceChildren(host, nodes)
	var out []string
	for _, n := range render.FindAll(host, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		_, ok := render.GetAttr(n, "data-block")
		return ok
	}) {
		name, _ := render.GetAttr(n, "data-block")
		out = append(out, name)
	}
	return out
}

// --- small helpers -------------------------------------------------------------

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func countBlocking(r Report) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == Error {
			n++
		}
	}
	return n
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity == Error
		}
		if fs[i].Rule != fs[j].Rule {
			return fs[i].Rule < fs[j].Rule
		}
		return fs[i].Detail < fs[j].Detail
	})
}

// within mirrors the loader's confinement guard (§14).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
