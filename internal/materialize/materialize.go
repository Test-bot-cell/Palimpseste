// Package materialize turns a theme template plus a page's content fragments
// into a single HTML document. This is the heart of Palimpseste: publishing is
// f(fragments, theme) -> page, run once per page at build time.
//
// The template is parsed once with x/net/html. Every element carrying a
// data-slot marker is located, its declared fragment is parsed in the element's
// own context (so bare <li> inside a <ul> parses correctly) and injected as the
// element's children. After injection the computed blocks of §4.1 are rendered
// (`toc` now; `table` ships with the data/ layer, §18 M3) and stored media
// references are resolved to page-relative URLs. In production output the
// data-slot marker is stripped — it is an editing affordance, not something the
// public page needs.
package materialize

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"palimpseste/internal/content"
	"palimpseste/internal/render"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// slotAttr marks an editable region in a theme template.
const slotAttr = "data-slot"

// Options tune a single materialization.
type Options struct {
	// KeepSlotMarkers leaves the data-slot attribute in place. The edit server
	// needs it to map DOM regions back to fragments; production strips it.
	KeepSlotMarkers bool
	// Tables resolves a data/ table for the computed `table` block (§4.1). A
	// nil resolver, or an unresolved name, leaves the block container empty —
	// the §4.1 graceful degradation, never a broken page.
	Tables func(name string) (header []string, rows [][]string, ok bool)
	// Variants lists the derived responsive renditions of a stored media path
	// (§10.1); when present, the materializer emits srcset/sizes on the <img>.
	// nil, or an empty answer, leaves the image as a single src.
	Variants func(src string) []MediaVariant
}

// MediaVariant is one derived rendition offered to srcset.
type MediaVariant struct {
	Path  string // media/derived/<base>-<w>.webp
	Width int
}

// Report records which slots the template asked for and whether each was
// backed by a fragment on disk. The linter consumes it without re-parsing.
type Report struct {
	Filled  []string
	Missing []string
}

// SlotFragment is one slot's resolved backing content, in the exact form Page
// would inject it: the slot name, whether a fragment exists on disk, and its
// bytes when it does. Inputs returns these so the build layer can content-
// address a page for memoisation (§7) without rendering it.
type SlotFragment struct {
	Name  string
	Found bool
	Body  string
}

// isSlot reports whether n is a template element marked with data-slot. Page and
// Inputs share it so slot discovery can never drift between the two.
func isSlot(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	_, ok := render.GetAttr(n, slotAttr)
	return ok
}

// Inputs resolves, without rendering, the tuple a page's output depends on: the
// template's bytes and, in template order, each data-slot's backing fragment.
// It parses the template exactly as Page does and reads the same fragments, so
// hashing what Inputs returns yields a key that changes precisely when Page's
// output would (§7's "hashs des fragments, du template"). It performs no
// injection and no serialisation, so a cache hit costs a template parse and a
// few file reads rather than a full materialization.
func Inputs(t *theme.Theme, ldr *content.Loader, p site.Page) (template string, frags []SlotFragment, err error) {
	tmpl, err := t.Template(p.Template)
	if err != nil {
		return "", nil, err
	}
	doc, err := render.ParseDocument(tmpl)
	if err != nil {
		return "", nil, fmt.Errorf("parse template %q: %w", p.Template, err)
	}
	slots := render.FindAll(doc, isSlot)
	frags = make([]SlotFragment, 0, len(slots))
	for _, el := range slots {
		name, _ := render.GetAttr(el, slotAttr)
		body, found, err := ldr.Fragment(p.ID, name, p.Slots)
		if err != nil {
			return "", nil, err
		}
		frags = append(frags, SlotFragment{Name: name, Found: found, Body: body})
	}
	return tmpl, frags, nil
}

// Page materializes one page: it reads the page's template from the theme,
// injects each slot's fragment, and returns the resulting document node plus a
// slot report. It does not touch the document head (SEO) or assets (CSS); the
// build orchestrates those on the returned node.
func Page(t *theme.Theme, ldr *content.Loader, p site.Page, opts Options) (*html.Node, Report, error) {
	tmpl, err := t.Template(p.Template)
	if err != nil {
		return nil, Report{}, err
	}
	doc, err := render.ParseDocument(tmpl)
	if err != nil {
		return nil, Report{}, fmt.Errorf("parse template %q: %w", p.Template, err)
	}

	// Collect slot elements from the template *before* injecting, so fragment
	// content is never rescanned for nested markers (fragments are content,
	// not templates).
	slots := render.FindAll(doc, isSlot)

	var rep Report
	for _, el := range slots {
		name, _ := render.GetAttr(el, slotAttr)
		frag, found, err := ldr.Fragment(p.ID, name, p.Slots)
		if err != nil {
			return nil, Report{}, err
		}
		if found {
			nodes, err := render.ParseFragment(frag, el)
			if err != nil {
				return nil, Report{}, fmt.Errorf("parse fragment for slot %q: %w", name, err)
			}
			render.ReplaceChildren(el, nodes)
			rep.Filled = append(rep.Filled, name)
		} else {
			rep.Missing = append(rep.Missing, name)
		}
		if !opts.KeepSlotMarkers {
			render.RemoveAttr(el, slotAttr)
		}
	}

	renderComputedBlocks(doc)
	renderTableBlocks(doc, opts.Tables)
	resolveMediaURLs(doc, p.Route, opts.Variants)

	return doc, rep, nil
}

// TablesReferenced returns the data/ table names the resolved fragments use in
// `table` blocks, sorted and deduplicated — the slot/table → page edges of the
// §7 dependency graph, which the build folds into each page's memoisation key.
func TablesReferenced(frags []SlotFragment) []string {
	seen := map[string]bool{}
	for _, f := range frags {
		if !f.Found || !strings.Contains(f.Body, "data-block") {
			continue
		}
		host := render.Element("body", nil)
		nodes, err := render.ParseFragment(f.Body, host)
		if err != nil {
			continue
		}
		render.ReplaceChildren(host, nodes)
		for _, n := range render.FindAll(host, func(n *html.Node) bool {
			if n.Type != html.ElementNode {
				return false
			}
			name, ok := render.GetAttr(n, "data-block")
			return ok && name == "table"
		}) {
			if src, ok := render.GetAttr(n, "data-source"); ok && src != "" {
				seen[src] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// renderTableBlocks renders every `table` computed block (§4.1) from its data/
// source: header row then body rows, every cell as text nodes — the systematic
// escaping §14 demands lives in the serializer, no string concatenation ever
// touches cell content.
func renderTableBlocks(doc *html.Node, resolve func(string) ([]string, [][]string, bool)) {
	for _, n := range render.FindAll(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		name, ok := render.GetAttr(n, "data-block")
		return ok && name == "table"
	}) {
		render.ReplaceChildren(n, nil)
		if resolve == nil {
			continue
		}
		src, _ := render.GetAttr(n, "data-source")
		header, rows, ok := resolve(src)
		if !ok || len(header) == 0 {
			continue
		}

		thead := render.Element("thead", nil)
		hr := render.Element("tr", nil)
		for _, h := range header {
			hr.AppendChild(render.Element("th", nil, render.Text(h)))
		}
		thead.AppendChild(hr)

		tbody := render.Element("tbody", nil)
		for _, row := range rows {
			tr := render.Element("tr", nil)
			for _, cell := range row {
				tr.AppendChild(render.Element("td", nil, render.Text(cell)))
			}
			tbody.AppendChild(tr)
		}
		n.AppendChild(render.Element("table", nil, thead, tbody))
	}
}

// --- computed blocks (§4.1, §7) -------------------------------------------------

// renderComputedBlocks runs the build-time half of the block catalogue over a
// materialized document. Built-ins: `toc` renders here; `table` renders in
// renderTableBlocks against the data/ layer.
func renderComputedBlocks(doc *html.Node) {
	tocs := render.FindAll(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		name, ok := render.GetAttr(n, "data-block")
		return ok && name == "toc"
	})
	if len(tocs) == 0 {
		return
	}

	headings := collectHeadings(doc)
	for _, toc := range tocs {
		depth := 4
		if v, ok := render.GetAttr(toc, "data-depth"); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 2 && n <= 4 {
				depth = n
			}
		}
		render.ReplaceChildren(toc, nil)
		if list := tocList(headings, depth); list != nil {
			toc.AppendChild(list)
		}
	}
}

// heading is one anchor target the toc points at.
type heading struct {
	level int
	id    string
	text  string
}

// collectHeadings gathers the document's h2..h4 in order, guaranteeing each an
// id. Ids are deterministic slugs of the heading text (unique via a numeric
// suffix); a heading that already carries an id — a template author's choice —
// keeps it. Assigning ids only on toc pages keeps every other page's output
// byte-identical to a build without this pass.
func collectHeadings(doc *html.Node) []heading {
	var out []heading
	seen := map[string]int{}
	body := render.Body(doc)
	if body == nil {
		return nil
	}
	for _, n := range render.FindAll(body, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		return n.Data == "h2" || n.Data == "h3" || n.Data == "h4"
	}) {
		text := strings.TrimSpace(nodeText(n))
		if text == "" {
			continue
		}
		id, has := render.GetAttr(n, "id")
		if !has || id == "" {
			id = slug(text)
			seen[id]++
			if seen[id] > 1 {
				id = fmt.Sprintf("%s-%d", id, seen[id])
			}
			render.SetAttr(n, "id", id)
		}
		out = append(out, heading{level: int(n.Data[1] - '0'), id: id, text: text})
	}
	return out
}

// tocList renders headings up to maxLevel as nested <ul> lists, nesting by the
// relative level jumps actually present (a document may skip h3).
func tocList(hs []heading, maxLevel int) *html.Node {
	var filtered []heading
	for _, h := range hs {
		if h.level <= maxLevel {
			filtered = append(filtered, h)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	root := render.Element("ul", nil)
	stack := []*html.Node{root}        // open <ul>s, innermost last
	levels := []int{filtered[0].level} // heading level owning each <ul>
	var lastLi *html.Node

	for _, h := range filtered {
		for len(levels) > 1 && h.level < levels[len(levels)-1] {
			stack = stack[:len(stack)-1]
			levels = levels[:len(levels)-1]
		}
		if h.level > levels[len(levels)-1] && lastLi != nil {
			sub := render.Element("ul", nil)
			lastLi.AppendChild(sub)
			stack = append(stack, sub)
			levels = append(levels, h.level)
		}
		li := render.Element("li", nil,
			render.Element("a", []render.Attr{{Key: "href", Val: "#" + h.id}}, render.Text(h.text)))
		stack[len(stack)-1].AppendChild(li)
		lastLi = li
	}
	return root
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	render.Walk(n, func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
	})
	return b.String()
}

// slug derives a deterministic anchor id from heading text: lowercase letters
// and digits, everything else collapsing to single dashes. Unicode letters are
// kept (they are valid in ids and URLs), so French headings slug naturally.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// --- media resolution (§4) -------------------------------------------------------

// resolveMediaURLs rewrites the canonical stored media form ("media/<path>",
// §4) into the URL that resolves from this page's published location. Output
// pages live at <route>/index.html, so a page one level deep needs "../" to
// reach the site-root media/ tree. Pure function of the route: deterministic,
// host-agnostic (no leading slash, so sub-path hosting keeps working).
func resolveMediaURLs(doc *html.Node, route string, variants func(string) []MediaVariant) {
	prefix := strings.Repeat("../", routeDepth(route))
	for _, n := range render.FindAll(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "img"
	}) {
		src, ok := render.GetAttr(n, "src")
		if !ok || !strings.HasPrefix(src, "media/") {
			continue
		}
		// Responsive renditions first (§10.1): srcset built from the canonical
		// stored path, then every URL — src included — resolved to page depth.
		if variants != nil {
			if vs := variants(src); len(vs) > 0 {
				var parts []string
				for _, v := range vs {
					parts = append(parts, fmt.Sprintf("%s%s %dw", prefix, v.Path, v.Width))
				}
				render.SetAttr(n, "srcset", strings.Join(parts, ", "))
				render.SetAttr(n, "sizes", "100vw")
			}
		}
		if prefix != "" {
			render.SetAttr(n, "src", prefix+src)
		}
	}
}

func routeDepth(route string) int {
	clean := strings.Trim(route, "/")
	if clean == "" {
		return 0
	}
	return strings.Count(clean, "/") + 1
}
