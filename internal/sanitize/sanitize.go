// Package sanitize enforces Palimpseste's content contract: the whitelist of
// semantic HTML a fragment may contain (§4), including the named-block
// containers of §4.1 and the media discipline (`src` relative into media/,
// `alt` required).
//
// It is two guarantees in one pass. First, security: every write path — a human
// editing in the overlay, HTML pasted from Word, a future AI suggestion — is
// funnelled through Fragment, which strips scripts, event handlers and unsafe
// URL schemes so a fragment can never carry stored XSS into a published page.
// Second, portability: theme-specific hooks (class, style, id, unknown data-*)
// are dropped, so a fragment stays semantic prose that survives a theme swap.
//
// The pipeline is three ordered passes:
//
//  1. normalize — aggressive paste normalisation (§4 "collage … normalisé
//     agressivement"): presentational bold/italic (<b>, <i>, styled spans)
//     become the contract's <strong>/<em> BEFORE the whitelist would unwrap
//     them and lose the semantics. Word/Docs wrapper quirks are recognised.
//  2. bluemonday — the authoritative whitelist. Deny by default.
//  3. canonicalize — structural contract enforcement on the parsed tree:
//     data-block containers validated against the catalogue (params typed,
//     bounded, enumerated; computed blocks stored empty), stray layout
//     containers folded into paragraphs or unwrapped, img src confined to
//     media/, alt guaranteed present, embed iframes pinned to the domain
//     whitelist with a forced sandbox — then one deterministic re-serialisation
//     through x/net/html, the same encoder the materializer uses. The stored
//     bytes are exactly what the build reads back.
package sanitize

import (
	"net/url"
	"path"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"palimpseste/internal/blocks"
	"palimpseste/internal/render"
)

// policy is built once; bluemonday policies are safe for concurrent use.
var policy = buildPolicy()

// embedSandbox is the fixed sandbox value stamped on every embed iframe: enough
// for the whitelisted players to run, nothing more. Fixed (not author-supplied)
// so a fragment can never widen its own sandbox.
const embedSandbox = "allow-scripts allow-same-origin"

// SlotPolicy narrows the contract to what a theme's slot declaration admits
// (§5.1): a stack slot is a pile of blocks with no free prose, and a slot may
// restrict which catalogue blocks it accepts. The zero value is the full §4
// vocabulary.
type SlotPolicy struct {
	// Stack keeps only block containers at the top level (§5.1 "pile de
	// blocs, sans prose libre").
	Stack bool
	// AllowedBlocks, when non-empty, is the slot's declared block list: any
	// other block loses its identity and degrades (§4.1) exactly like an
	// unknown one.
	AllowedBlocks []string
}

// Fragment sanitises untrusted fragment HTML to the content contract and
// returns it canonicalised: normalised, whitelisted, structurally enforced and
// re-rendered through x/net/html, trimmed of surrounding whitespace. The output
// is inert, theme-agnostic, and byte-for-byte what the materializer reads back.
func Fragment(raw string) string {
	return FragmentForSlot(raw, SlotPolicy{})
}

// FragmentForSlot is Fragment under a slot's own micro-contract.
func FragmentForSlot(raw string, pol SlotPolicy) string {
	pre, err := normalize(raw)
	if err != nil {
		// x/net/html does not fail on any byte stream in practice; degrade to
		// the raw input — bluemonday remains the security authority either way.
		pre = raw
	}
	safe := policy.Sanitize(pre)
	canon, err := canonicalize(safe, pol)
	if err != nil {
		return strings.TrimSpace(safe)
	}
	return canon
}

// Plain sanitises a plain slot (§5.1: "texte nu, une ligne"): every tag is
// stripped, whitespace collapses to single spaces, and the result is one line
// of HTML-escaped text — the strictest micro-contract in the system.
func Plain(raw string) string {
	text := bluemonday.StrictPolicy().Sanitize(raw)
	return strings.Join(strings.Fields(text), " ")
}

// --- pass 1: paste normalisation ----------------------------------------------

// normalize rewrites presentational emphasis into the contract's semantic
// elements before the whitelist runs, because bluemonday unwraps disallowed
// tags — by then the author's bold/italic intent would already be lost.
//
//   - <b> -> <strong> and <i> -> <em>, EXCEPT the wrappers word processors abuse
//     as containers: Google Docs wraps every copied selection in
//     <b style="font-weight:normal" id="docs-internal-guid-…">, which is
//     explicitly not bold. Those are left for the whitelist to unwrap.
//   - <span style="font-weight:bold|600+"> -> <strong>,
//     <span style="font-style:italic"> -> <em> — how Google Docs actually marks
//     emphasis. Other spans are left for the whitelist to unwrap.
func normalize(raw string) (string, error) {
	host := render.Element("body", nil)
	nodes, err := render.ParseFragment(raw, host)
	if err != nil {
		return "", err
	}
	render.ReplaceChildren(host, nodes)

	for _, n := range render.FindAll(host, func(n *html.Node) bool { return n.Type == html.ElementNode }) {
		style, _ := render.GetAttr(n, "style")
		id, _ := render.GetAttr(n, "id")
		switch n.Data {
		case "b":
			if !isNeutralWrapper(style, id) {
				rename(n, "strong")
			}
		case "i":
			if !isNeutralWrapper(style, id) {
				rename(n, "em")
			}
		case "span":
			switch {
			case styleIsBold(style):
				rename(n, "strong")
			case styleIsItalic(style):
				rename(n, "em")
			}
		}
	}
	return render.RenderChildren(host)
}

// isNeutralWrapper recognises the word-processor container idiom: an emphasis
// tag that its own inline style declares non-emphatic, or a Google Docs
// selection wrapper.
func isNeutralWrapper(style, id string) bool {
	s := strings.ToLower(style)
	return strings.Contains(s, "font-weight:normal") ||
		strings.Contains(s, "font-weight: normal") ||
		strings.Contains(s, "font-style:normal") ||
		strings.Contains(s, "font-style: normal") ||
		strings.HasPrefix(id, "docs-internal-guid")
}

func styleIsBold(style string) bool {
	s := strings.ToLower(style)
	if strings.Contains(s, "font-weight:bold") || strings.Contains(s, "font-weight: bold") {
		return true
	}
	for _, w := range []string{"600", "700", "800", "900"} {
		if strings.Contains(s, "font-weight:"+w) || strings.Contains(s, "font-weight: "+w) {
			return true
		}
	}
	return false
}

func styleIsItalic(style string) bool {
	s := strings.ToLower(style)
	return strings.Contains(s, "font-style:italic") || strings.Contains(s, "font-style: italic")
}

// rename changes an element's tag in place, keeping children and position.
func rename(n *html.Node, tag string) {
	n.Data = tag
	n.DataAtom = atom.Lookup([]byte(tag))
}

// --- pass 2: the whitelist -----------------------------------------------------

// buildPolicy declares the content contract's vocabulary (§4). It is a
// deliberately small whitelist — prose, not layout — so a fragment stays
// portable across themes. Everything unnamed is dropped by bluemonday's default
// deny: script/style/object/form and every on* handler for safety, and
// class/style/id/unknown data-* plus presentational tags for portability.
// Disallowed *elements* are unwrapped, keeping their text.
//
// Named-block containers (§4.1) and the embed iframe are admitted here in
// broad strokes; canonicalize then applies the parts a static whitelist cannot
// express — catalogue lookups, parameter schemas, the media/ prefix, the embed
// domain whitelist.
func buildPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// Structure: headings start at h2 — h1 is the template's, never a fragment's.
	p.AllowElements("h2", "h3", "h4", "p", "blockquote", "hr")

	// Lists.
	p.AllowElements("ul", "ol", "li")

	// Inline semantics — the minimal set the contract names.
	p.AllowElements("strong", "em", "code", "br")

	// Media and code blocks.
	p.AllowElements("figure", "figcaption", "img", "pre")

	// Named-block containers (§4.1). The catalogue is the source of truth for
	// which element may carry which block and which parameters are declared;
	// canonicalize enforces it. Elements admitted here that end up carrying no
	// valid data-block are folded away by canonicalize, so plain layout divs
	// from a paste can never survive.
	containers := blocks.ContainerElements()
	p.AllowElements(containers...)
	p.AllowAttrs("data-block").OnElements(append(containers, "figure")...)
	if params := blocks.ParamAttrs(); len(params) > 0 {
		p.AllowAttrs(params...).OnElements(append(containers, "figure")...)
	}

	// The embed block's iframe (§4.1 "iframe sandboxée, liste blanche de
	// domaines"). canonicalize removes any iframe outside a valid embed block
	// or off the whitelist, and stamps the sandbox.
	p.AllowElements("iframe")
	p.AllowAttrs("src", "title", "sandbox", "allowfullscreen", "loading").OnElements("iframe")

	// Links: safe schemes and relative URLs only; javascript:/data: are rejected
	// because they are absent from the scheme whitelist.
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto", "tel")
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)

	// Images: src (canonicalize confines it to media/) and the required alt.
	p.AllowAttrs("src", "alt").OnElements("img")

	// title is the only other attribute the contract lets survive, and only
	// where it carries meaning.
	p.AllowAttrs("title").OnElements("a", "img")

	return p
}

// --- pass 3: structural enforcement + canonical serialisation ------------------

// canonicalize re-parses safe HTML as a body fragment, applies the structural
// rules the flat whitelist cannot express, and renders it back — the one
// deterministic serialisation used everywhere in Palimpseste.
func canonicalize(safe string, pol SlotPolicy) (string, error) {
	host := render.Element("body", nil)
	nodes, err := render.ParseFragment(safe, host)
	if err != nil {
		return "", err
	}
	render.ReplaceChildren(host, nodes)

	enforceBlocks(host, pol)
	enforceEmbeds(host)
	enforceImages(host)
	if pol.Stack {
		enforceStack(host)
	}

	inner, err := render.RenderChildren(host)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(inner), nil
}

// enforceStack applies the stack micro-contract (§5.1): the fragment is an
// ordered pile of block containers and nothing else — free prose between
// blocks is dropped, not wrapped, because a stack has no place to put it.
func enforceStack(host *html.Node) {
	c := host.FirstChild
	for c != nil {
		next := c.NextSibling
		keep := false
		if c.Type == html.ElementNode {
			if name, ok := render.GetAttr(c, "data-block"); ok && blocks.AllowedOn(name, c.Data) {
				keep = true
			}
		}
		if !keep {
			host.RemoveChild(c)
		}
		c = next
	}
}

// enforceBlocks validates every data-block container against the catalogue —
// and the slot's own allowed list when one is declared — and folds away the
// layout containers that carry none. Nodes are visited children before parents
// (reverse pre-order) so unwrapping never skips a nested case.
func enforceBlocks(host *html.Node, pol SlotPolicy) {
	slotAllows := func(name string) bool {
		if len(pol.AllowedBlocks) == 0 {
			return true
		}
		for _, b := range pol.AllowedBlocks {
			if b == name {
				return true
			}
		}
		return false
	}

	els := render.FindAll(host, func(n *html.Node) bool { return n.Type == html.ElementNode })
	for i := len(els) - 1; i >= 0; i-- {
		n := els[i]
		name, hasBlock := render.GetAttr(n, "data-block")

		if hasBlock && blocks.AllowedOn(name, n.Data) && slotAllows(name) {
			filterBlockParams(n, name)
			if blocks.Computed(name) {
				// Computed blocks are rendered at build time; their children are
				// build output, never source. Canonical stored form: empty.
				render.ReplaceChildren(n, nil)
			}
			continue
		}

		// No block (or an unknown/misplaced one): strip every data-* so nothing
		// undeclared survives (§4), then normalise the container itself.
		stripDataAttrs(n)
		switch n.Data {
		case "div", "section", "aside":
			// Graceful degradation (§4.1): the content stays valid semantic
			// HTML. A container of pure phrasing content was a paragraph in the
			// author's mind (contenteditable's default separator); anything
			// else unwraps in place.
			if isPhrasingOnly(n) {
				if n.FirstChild == nil || textIsBlank(n) {
					render.Detach(n)
				} else {
					rename(n, "p")
				}
			} else {
				unwrap(n)
			}
		}
	}
}

// filterBlockParams keeps data-block plus the parameters the catalogue declares
// (and whose values validate); every other data-* is dropped.
func filterBlockParams(n *html.Node, block string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.HasPrefix(a.Key, "data-") || a.Key == "data-block" {
			out = append(out, a)
			continue
		}
		if blocks.ValidParam(block, strings.TrimPrefix(a.Key, "data-"), a.Val) {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func stripDataAttrs(n *html.Node) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.HasPrefix(a.Key, "data-") {
			out = append(out, a)
		}
	}
	n.Attr = out
}

// phrasingTags is the inline subset of the vocabulary: a container holding only
// these (and text) reads as one paragraph.
var phrasingTags = map[string]bool{
	"a": true, "strong": true, "em": true, "code": true, "br": true, "img": true,
}

// isPhrasingOnly inspects the whole subtree, not just direct children: the
// HTML parser will happily build a block element under an inline one
// (strong > p), and renaming such a container to <p> would nest p-in-p — a
// shape the next parse splits apart, breaking idempotence.
func isPhrasingOnly(n *html.Node) bool {
	ok := true
	for c := n.FirstChild; c != nil && ok; c = c.NextSibling {
		render.Walk(c, func(x *html.Node) {
			switch x.Type {
			case html.TextNode:
			case html.ElementNode:
				if !phrasingTags[x.Data] {
					ok = false
				}
			default:
				ok = false
			}
		})
	}
	return ok
}

func textIsBlank(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			return false
		}
		if c.Type == html.TextNode && strings.TrimSpace(c.Data) != "" {
			return false
		}
	}
	return true
}

// unwrap replaces n with its children, in place.
func unwrap(n *html.Node) {
	parent := n.Parent
	if parent == nil {
		return
	}
	for n.FirstChild != nil {
		c := n.FirstChild
		n.RemoveChild(c)
		parent.InsertBefore(c, n)
	}
	parent.RemoveChild(n)
}

// enforceEmbeds removes every iframe that is not the child of a valid embed
// block with a whitelisted https src, and stamps the fixed sandbox on the
// survivors so a fragment can never widen its own privileges.
func enforceEmbeds(host *html.Node) {
	iframes := render.FindAll(host, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "iframe"
	})
	for _, n := range iframes {
		src, _ := render.GetAttr(n, "src")
		if !insideEmbedBlock(n) || !blocks.ValidEmbedSrc(src) {
			render.Detach(n)
			continue
		}
		// An embed iframe is a leaf: its element content is the browser's
		// fallback, never authored data. Clearing it keeps the canonical form
		// stable — a stray text child (a lone quote from a sloppy paste) would
		// otherwise re-escape on each pass and break idempotence.
		render.ReplaceChildren(n, nil)

		title, hasTitle := render.GetAttr(n, "title")
		attrs := []render.Attr{{Key: "src", Val: src}}
		if hasTitle && strings.TrimSpace(title) != "" {
			attrs = append(attrs, render.Attr{Key: "title", Val: title})
		}
		attrs = append(attrs,
			render.Attr{Key: "sandbox", Val: embedSandbox},
			render.Attr{Key: "loading", Val: "lazy"},
		)
		n.Attr = n.Attr[:0]
		for _, a := range attrs {
			render.SetAttr(n, a.Key, a.Val)
		}
	}
}

func insideEmbedBlock(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if name, ok := render.GetAttr(p, "data-block"); ok {
			return name == "embed" && blocks.AllowedOn("embed", p.Data)
		}
	}
	return false
}

// enforceImages applies the §4 media row: `src` relative into media/ (anything
// else removes the image — schemes were already vetted by the whitelist, this
// is the location discipline) and `alt` required (guaranteed present; an empty
// alt is explicit "decorative" and the §11 lint's business to flag).
func enforceImages(host *html.Node) {
	imgs := render.FindAll(host, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "img"
	})
	for _, n := range imgs {
		src, _ := render.GetAttr(n, "src")
		canon, ok := CanonicalMediaSrc(src)
		if !ok {
			render.Detach(n)
			continue
		}
		render.SetAttr(n, "src", canon)
		if _, has := render.GetAttr(n, "alt"); !has {
			render.SetAttr(n, "alt", "")
		}
	}
}

// CanonicalMediaSrc reduces an image src to its canonical stored form:
// "media/<clean path>". Accepted inputs are site-relative spellings of a path
// under media/ ("media/x", "./media/x", "/media/x"); everything else — absolute
// URLs, protocol-relative, traversal, paths outside media/ — is refused. The
// materializer re-derives the correct page-relative URL from this form at
// build time, so the stored fragment stays portable (§3.2).
func CanonicalMediaSrc(src string) (string, bool) {
	s := strings.TrimSpace(src)
	if s == "" || strings.HasPrefix(s, "//") {
		return "", false
	}
	if u, err := url.Parse(s); err != nil || u.Scheme != "" || u.Host != "" {
		return "", false
	}
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimPrefix(s, "./")
	clean := path.Clean(s)
	if clean == "media" || !strings.HasPrefix(clean, "media/") || strings.Contains(clean, "..") {
		return "", false
	}
	return clean, true
}
