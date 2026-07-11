package sanitize

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"palimpseste/internal/blocks"
	"palimpseste/internal/render"
)

// contractElements is the exact element vocabulary the contract permits (§4),
// including the §4.1 block containers and the embed iframe. The fuzzer parses
// every sanitized output and asserts nothing outside this set ever survives —
// a structural check, not a substring scan, so text that merely mentions
// "<script>" can never trip it. Container elements and iframes carry extra
// structural obligations, asserted below.
var contractElements = map[string]bool{
	"h2": true, "h3": true, "h4": true, "p": true, "blockquote": true, "hr": true,
	"ul": true, "ol": true, "li": true,
	"strong": true, "em": true, "code": true, "br": true,
	"figure": true, "figcaption": true, "img": true, "pre": true,
	"a": true,
	// §4.1 block containers — only valid when carrying a catalogued data-block,
	// which assertContractTree enforces per node.
	"div": true, "section": true, "aside": true, "iframe": true,
}

// containerElements must justify their presence with a valid data-block; a bare
// one is a sanitizer bug (figure is ordinary vocabulary and exempt).
var containerElements = map[string]bool{
	"div": true, "section": true, "aside": true,
}

// contractAttrs is the surviving attribute set (§4): href/src/alt/title,
// data-block and its declared parameters (validated structurally below), and
// the fixed iframe attributes the embed block stamps. class, style, id,
// unknown data-*, and every on* handler must be absent.
var contractAttrs = map[string]bool{
	"href": true, "src": true, "alt": true, "title": true,
	"sandbox": true, "loading": true,
}

var contractSchemes = map[string]bool{
	"http": true, "https": true, "mailto": true, "tel": true,
}

// FuzzFragmentRoundTrip drives the sanitiser with arbitrary input and asserts
// the three properties the round-trip depends on: it never panics, its output is
// idempotent (re-sanitising is a no-op, so a stored fragment reads back
// unchanged), and that output only ever contains the contract vocabulary —
// blocks, parameters, media discipline and embeds included.
func FuzzFragmentRoundTrip(f *testing.F) {
	seeds := []string{
		"",
		"   \n\t ",
		"<p>Hello <strong>world</strong></p>",
		`<h2>Title</h2><p>See <a href="/about" title="About">about</a>.</p>`,
		"<ul><li>a<ul><li>b</li></ul></li></ul>",
		`<p class="x" style="color:red" id="y" data-role="z">hi</p>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<img src="data:text/html,<script>1</script>" alt="">`,
		`<script>alert(1)</script><p>ok</p>`,
		`<h1>nope</h1><div><span>bare</span></div>`,
		`<table><tr><td>cell</td></tr></table>`,
		`<b style="font-weight:normal" id="docs-internal-guid-1"><span>pasted</span></b>`,
		`<p>a &amp; b &lt; c &#233;</p>`,
		// §4.1 vocabulary: valid blocks, invalid params, hostile spellings.
		`<figure data-block="gallery"><img src="media/a.webp" alt="a"></figure>`,
		`<div data-block="table" data-source="equipe"></div>`,
		`<aside data-block="toc" data-depth="3"></aside>`,
		`<div data-block="columns" data-count="99"><p>x</p></div>`,
		`<div data-block="widget3000" data-evil="1"><p>x</p></div>`,
		`<div data-block="embed"><iframe src="https://www.youtube-nocookie.com/embed/x" sandbox="allow-top-navigation"></iframe></div>`,
		`<iframe src="https://evil.example/x"></iframe>`,
		`<img src="../../etc/passwd" alt=""><img src="//evil.example/x" alt="">`,
		`<div>ligne un</div><div>ligne deux</div>`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := Fragment(in)

		if again := Fragment(out); again != out {
			t.Fatalf("not idempotent:\n in: %q\n 1st: %q\n 2nd: %q", in, out, again)
		}

		assertContractTree(t, out)
	})
}

// assertContractTree parses sanitized output as a body fragment and walks the
// tree, failing if any element, attribute, URL scheme, block parameter, media
// src or iframe placement falls outside the contract.
func assertContractTree(t *testing.T, out string) {
	t.Helper()
	nodes, err := render.ParseFragment(out, render.Element("body", nil))
	if err != nil {
		t.Fatalf("sanitized output does not reparse: %v\n%q", err, out)
	}
	for _, n := range nodes {
		render.Walk(n, func(x *html.Node) {
			if x.Type != html.ElementNode {
				return
			}
			if !contractElements[x.Data] {
				t.Fatalf("forbidden element <%s> survived in %q", x.Data, out)
			}

			blockName, hasBlock := "", false
			for _, a := range x.Attr {
				if a.Key == "data-block" {
					blockName, hasBlock = a.Val, true
				}
			}

			// A layout container without a catalogued block must never survive.
			if containerElements[x.Data] && (!hasBlock || !blocks.AllowedOn(blockName, x.Data)) {
				t.Fatalf("bare container <%s> (block %q) survived in %q", x.Data, blockName, out)
			}
			if hasBlock && !blocks.AllowedOn(blockName, x.Data) {
				t.Fatalf("unknown/misplaced block %q on <%s> survived in %q", blockName, x.Data, out)
			}
			if hasBlock && blocks.Computed(blockName) && x.FirstChild != nil {
				t.Fatalf("computed block %q stored with children in %q", blockName, out)
			}

			for _, a := range x.Attr {
				key := strings.ToLower(a.Key)
				switch {
				case key == "data-block":
					// validated above
				case strings.HasPrefix(key, "data-"):
					if !hasBlock || !blocks.ValidParam(blockName, strings.TrimPrefix(key, "data-"), a.Val) {
						t.Fatalf("undeclared/invalid block parameter %q=%q on <%s> in %q", a.Key, a.Val, x.Data, out)
					}
				case !contractAttrs[key]:
					t.Fatalf("forbidden attribute %q on <%s> survived in %q", a.Key, x.Data, out)
				case key == "href" || key == "src":
					assertSafeURL(t, a.Val, out)
				}
			}

			switch x.Data {
			case "img":
				src, alt := "", false
				for _, a := range x.Attr {
					if a.Key == "src" {
						src = a.Val
					}
					if a.Key == "alt" {
						alt = true
					}
				}
				if canon, ok := CanonicalMediaSrc(src); !ok || canon != src {
					t.Fatalf("img src %q escapes the media/ discipline in %q", src, out)
				}
				if !alt {
					t.Fatalf("img without alt survived in %q", out)
				}
			case "iframe":
				src, sandbox := "", ""
				for _, a := range x.Attr {
					if a.Key == "src" {
						src = a.Val
					}
					if a.Key == "sandbox" {
						sandbox = a.Val
					}
				}
				if !blocks.ValidEmbedSrc(src) {
					t.Fatalf("iframe src %q off the embed whitelist in %q", src, out)
				}
				if sandbox != embedSandbox {
					t.Fatalf("iframe sandbox %q differs from the fixed policy in %q", sandbox, out)
				}
			}
		})
	}
}

func assertSafeURL(t *testing.T, raw, out string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		return // an unparseable URL cannot carry an executable scheme
	}
	if u.Scheme == "" {
		return // relative URL: allowed
	}
	if !contractSchemes[strings.ToLower(u.Scheme)] {
		t.Fatalf("unsafe URL scheme %q survived in %q", u.Scheme, out)
	}
}
