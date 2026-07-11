package sanitize

import (
	"strings"
	"testing"
)

// --- security: stored XSS must not survive ----------------------------------

func TestStripsDangerousConstructs(t *testing.T) {
	cases := []struct {
		name, in, mustNotContain string
	}{
		{"script tag", `<p>ok</p><script>alert(1)</script>`, "alert"},
		{"style tag", `<style>body{x:url(javascript:1)}</style><p>ok</p>`, "javascript"},
		{"iframe", `<iframe src="https://evil.test"></iframe><p>ok</p>`, "iframe"},
		{"onclick handler", `<p onclick="steal()">ok</p>`, "onclick"},
		{"onerror on img", `<img src="x" onerror="alert(1)">`, "onerror"},
		{"javascript: href", `<a href="javascript:alert(1)">x</a>`, "javascript"},
		{"data: href", `<a href="data:text/html,<script>1</script>">x</a>`, "data:"},
		{"object", `<object data="x.swf"></object><p>ok</p>`, "object"},
		{"form", `<form action="/x"><input></form><p>ok</p>`, "<form"},
		{"svg onload", `<svg onload="alert(1)"></svg><p>ok</p>`, "onload"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Fragment(c.in)
			if strings.Contains(strings.ToLower(out), c.mustNotContain) {
				t.Errorf("output still contains %q:\n%s", c.mustNotContain, out)
			}
		})
	}
}

func TestDangerousElementsDropButKeepInertText(t *testing.T) {
	// The <a> loses its unsafe href but keeps its text and the surrounding prose.
	out := Fragment(`<p>See <a href="javascript:evil()">this</a> now</p>`)
	for _, want := range []string{"See ", "this", " now"} {
		if !strings.Contains(out, want) {
			t.Errorf("lost inert text %q in %q", want, out)
		}
	}
}

// --- portability: theme hooks are stripped ----------------------------------

func TestStripsThemeHooks(t *testing.T) {
	in := `<p class="lead accent" style="color:red" id="x" data-role="hero">Hi</p>`
	out := Fragment(in)
	if out != `<p>Hi</p>` {
		t.Errorf("theme hooks not fully stripped: %q", out)
	}
}

// --- paste-from-Word --------------------------------------------------------

func TestPasteFromWordIsCleaned(t *testing.T) {
	word := `<!--[if gte mso 9]><xml><o:OfficeDocumentSettings/></xml><![endif]-->` +
		`<p class="MsoNormal" style="margin:0cm;mso-pagination:widow-orphan">` +
		`<span style="font-family:Calibri;mso-fareast-language:EN-US">Bonjour</span>` +
		`<o:p></o:p></p>` +
		`<p class=MsoNormal><span lang=FR>le monde</span></p>`
	out := Fragment(word)

	for _, bad := range []string{"mso", "MsoNormal", "o:p", "<xml", "OfficeDocument", "style="} {
		if strings.Contains(out, bad) {
			t.Errorf("Word cruft %q survived:\n%s", bad, out)
		}
	}
	for _, good := range []string{"Bonjour", "le monde"} {
		if !strings.Contains(out, good) {
			t.Errorf("lost real content %q:\n%s", good, out)
		}
	}
}

// --- canonical round-trip ---------------------------------------------------

func TestCleanSemanticContentRoundTrips(t *testing.T) {
	cases := []string{
		`<h2>Title</h2><p>Hello <strong>world</strong></p>`,
		`<ul><li>one</li><li>two</li></ul>`,
		`<p>See <a href="/about" title="About">about</a>.</p>`,
		`<blockquote><p>Quote</p></blockquote>`,
		`<p>a &amp; b &lt; c</p>`,
	}
	for _, in := range cases {
		if out := Fragment(in); out != in {
			t.Errorf("clean content changed on round-trip:\n in: %q\nout: %q", in, out)
		}
	}
}

func TestIdempotent(t *testing.T) {
	messy := `<p class="Mso" style="x">Hello <b>bold</b> <a href="javascript:1">x</a></p>` +
		`<script>alert(1)</script><div id="y"><span>keep</span></div>`
	first := Fragment(messy)
	second := Fragment(first)
	if first != second {
		t.Errorf("not idempotent:\n first: %q\nsecond: %q", first, second)
	}
}

func TestEmptyAndWhitespace(t *testing.T) {
	if out := Fragment(""); out != "" {
		t.Errorf("empty input -> %q", out)
	}
	if out := Fragment("   \n\t "); out != "" {
		t.Errorf("whitespace input -> %q", out)
	}
}

func TestSafeAttributesSurvive(t *testing.T) {
	out := Fragment(`<img src="media/photo.webp" alt="A photo" title="Portrait">`)
	for _, want := range []string{`src="media/photo.webp"`, `alt="A photo"`, `title="Portrait"`} {
		if !strings.Contains(out, want) {
			t.Errorf("safe attribute missing (%s) in %q", want, out)
		}
	}
}

// The contract's surviving attribute set is exactly href/src/alt/title.
// Presentational hints outside it — even harmless ones — are dropped, so a
// fragment carries no rendering intent a theme would have to honour.
func TestNonContractAttributesStripped(t *testing.T) {
	out := Fragment(`<img src="media/p.webp" alt="x" width="640" height="480" loading="lazy">`)
	for _, gone := range []string{"width", "height", "loading"} {
		if strings.Contains(out, gone) {
			t.Errorf("non-contract attribute %q survived in %q", gone, out)
		}
	}
}

// --- vocabulary: only §4 elements survive; the rest unwrap to their text ------

func TestNonContractElementsUnwrapped(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"h1 belongs to the template", `<h1>Title</h1>`, `Title`},
		{"h5 below the vocabulary", `<h5>deep</h5>`, `deep`},
		{"span is a theme hook", `<p><span>hi</span></p>`, `<p>hi</p>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Fragment(c.in); out != c.want {
				t.Errorf("Fragment(%q) = %q, want %q", c.in, out, c.want)
			}
		})
	}
}

// Aggressive normalisation (§4): presentational emphasis becomes the contract's
// semantic elements — the author's intent survives the whitelist instead of
// being silently destroyed — and stray layout containers become paragraphs
// (phrasing content) or unwrap in place (block content).
func TestPresentationalMarkupNormalises(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"b becomes strong", `<p>a <b>b</b> c</p>`, `<p>a <strong>b</strong> c</p>`},
		{"i becomes em", `<p>a <i>b</i> c</p>`, `<p>a <em>b</em> c</p>`},
		{"bold span becomes strong", `<p><span style="font-weight:700;">x</span></p>`, `<p><strong>x</strong></p>`},
		{"italic span becomes em", `<p><span style="font-style:italic;">x</span></p>`, `<p><em>x</em></p>`},
		{"phrasing div becomes p", `<div>ligne un</div><div>ligne deux</div>`, `<p>ligne un</p><p>ligne deux</p>`},
		{"block-level div unwraps", `<div><p>a</p><p>b</p></div>`, `<p>a</p><p>b</p>`},
		{"empty div disappears", `<p>x</p><div>  </div>`, `<p>x</p>`},
		{"gdocs neutral b-wrapper is not bold", `<b style="font-weight:normal;" id="docs-internal-guid-1"><p>x</p></b>`, `<p>x</p>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Fragment(c.in); out != c.want {
				t.Errorf("Fragment(%q) = %q, want %q", c.in, out, c.want)
			}
		})
	}
}

// The §4 media row: src is confined to media/ — spelled canonically — and alt
// is guaranteed present (empty alt is explicit "decorative"; the §11 lint flags
// it, the contract only requires the attribute).
func TestImageMediaDiscipline(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"canonical form kept", `<img src="media/a.webp" alt="x">`, `<img src="media/a.webp" alt="x"/>`},
		{"root-relative normalised", `<img src="/media/a.webp" alt="x">`, `<img src="media/a.webp" alt="x"/>`},
		{"dot-slash normalised", `<img src="./media/a.webp" alt="x">`, `<img src="media/a.webp" alt="x"/>`},
		{"missing alt injected", `<img src="media/a.webp">`, `<img src="media/a.webp" alt=""/>`},
		{"external https dropped", `<img src="https://evil.example/x.png" alt="x">`, ``},
		{"protocol-relative dropped", `<img src="//evil.example/x.png" alt="x">`, ``},
		{"traversal dropped", `<img src="../../etc/passwd" alt="">`, ``},
		{"traversal inside media dropped", `<img src="media/../site.json" alt="">`, ``},
		{"outside media dropped", `<img src="notmedia/x.png" alt="x">`, ``},
		{"figure keeps its caption when img dies", `<figure><img src="/etc/x" alt=""><figcaption>cap</figcaption></figure>`, `<figure><figcaption>cap</figcaption></figure>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Fragment(c.in); out != c.want {
				t.Errorf("Fragment(%q) = %q, want %q", c.in, out, c.want)
			}
		})
	}
}

// --- named blocks (§4.1) ------------------------------------------------------

func TestBlockContainersSurviveWithDeclaredParams(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"gallery on figure", `<figure data-block="gallery"><img src="media/a.webp" alt="a"/></figure>`, `<figure data-block="gallery"><img src="media/a.webp" alt="a"/></figure>`},
		{"table with valid source, stored empty", `<div data-block="table" data-source="equipe">stale render</div>`, `<div data-block="table" data-source="equipe"></div>`},
		{"toc with valid depth, stored empty", `<aside data-block="toc" data-depth="3"><ul><li>old</li></ul></aside>`, `<aside data-block="toc" data-depth="3"></aside>`},
		{"columns with bounded count", `<div data-block="columns" data-count="3"><p>a</p></div>`, `<div data-block="columns" data-count="3"><p>a</p></div>`},
		{"cta with enum variant", `<aside data-block="cta" data-variant="primary"><p>go</p></aside>`, `<aside data-block="cta" data-variant="primary"><p>go</p></aside>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Fragment(c.in); out != c.want {
				t.Errorf("Fragment(%q) = %q, want %q", c.in, out, c.want)
			}
		})
	}
}

func TestBlockParamSchemaEnforced(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"out-of-bounds int dropped", `<div data-block="columns" data-count="9"><p>a</p></div>`, `<div data-block="columns"><p>a</p></div>`},
		{"bad enum dropped", `<aside data-block="cta" data-variant="flashy"><p>x</p></aside>`, `<aside data-block="cta"><p>x</p></aside>`},
		{"undeclared param dropped", `<div data-block="table" data-source="equipe" data-evil="x"></div>`, `<div data-block="table" data-source="equipe"></div>`},
		{"traversal-shaped source dropped", `<div data-block="table" data-source="../secrets"></div>`, `<div data-block="table"></div>`},
		{"unknown block degrades to prose", `<div data-block="widget3000"><p>content</p></div>`, `<p>content</p>`},
		{"block on wrong element degrades", `<aside data-block="gallery"><p>x</p></aside>`, `<p>x</p>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Fragment(c.in); out != c.want {
				t.Errorf("Fragment(%q) = %q, want %q", c.in, out, c.want)
			}
		})
	}
}

func TestEmbedIframeWhitelisted(t *testing.T) {
	in := `<div data-block="embed"><iframe src="https://www.youtube-nocookie.com/embed/abc" title="clip" width="560"></iframe></div>`
	out := Fragment(in)
	for _, want := range []string{
		`data-block="embed"`,
		`src="https://www.youtube-nocookie.com/embed/abc"`,
		`title="clip"`,
		`sandbox="allow-scripts allow-same-origin"`,
		`loading="lazy"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("embed output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "width") {
		t.Errorf("undeclared iframe attribute survived:\n%s", out)
	}

	for name, hostile := range map[string]string{
		"off-whitelist host":    `<div data-block="embed"><iframe src="https://evil.example/x"></iframe></div>`,
		"http not https":        `<div data-block="embed"><iframe src="http://www.youtube.com/embed/x"></iframe></div>`,
		"iframe outside embed":  `<iframe src="https://www.youtube.com/embed/x"></iframe>`,
		"iframe in other block": `<div data-block="columns" data-count="2"><iframe src="https://www.youtube.com/embed/x"></iframe></div>`,
	} {
		t.Run(name, func(t *testing.T) {
			if out := Fragment(hostile); strings.Contains(out, "iframe") {
				t.Errorf("hostile iframe survived: %q", out)
			}
		})
	}
}

// A fragment author cannot widen their own sandbox: whatever sandbox value the
// input carries, the canonical form carries the catalogue's fixed one.
func TestEmbedSandboxCannotBeWidened(t *testing.T) {
	in := `<div data-block="embed"><iframe src="https://player.vimeo.com/video/1" sandbox="allow-top-navigation allow-popups"></iframe></div>`
	out := Fragment(in)
	if !strings.Contains(out, `sandbox="allow-scripts allow-same-origin"`) {
		t.Errorf("fixed sandbox missing: %q", out)
	}
	for _, bad := range []string{"allow-top-navigation", "allow-popups"} {
		if strings.Contains(out, bad) {
			t.Errorf("widened sandbox capability %q survived: %q", bad, out)
		}
	}
}

// --- plain slots (§5.1) ---------------------------------------------------------

func TestPlainIsBareSingleLineText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"tags stripped", `<h1>Big <em>title</em></h1>`, "Big title"},
		{"newlines collapse", "un\n\ndeux\ttrois", "un deux trois"},
		{"script dies", `<script>alert(1)</script>titre`, "titre"},
		{"entities stay escaped", "A &amp; B", "A &amp; B"},
		{"empty stays empty", "  \n ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := Plain(c.in); out != c.want {
				t.Errorf("Plain(%q) = %q, want %q", c.in, out, c.want)
			}
		})
	}
}

// Raw <table> markup is not fragment vocabulary — tables are a computed block
// (§4.1) rendered from data/, not hand-authored. The tags unwrap; the cell text
// is kept so nothing is silently lost.
func TestRawTableUnwrapsToText(t *testing.T) {
	out := Fragment(`<table><thead><tr><th>Name</th></tr></thead><tbody><tr><td>Ada</td></tr></tbody></table>`)
	for _, tag := range []string{"<table", "<thead", "<tr", "<th", "<td"} {
		if strings.Contains(out, tag) {
			t.Errorf("table markup %q survived: %q", tag, out)
		}
	}
	for _, text := range []string{"Name", "Ada"} {
		if !strings.Contains(out, text) {
			t.Errorf("lost cell text %q: %q", text, out)
		}
	}
}

// --- §18 golden corpus: whitespace, nesting, real-world paste ----------------

func TestDeepNestingRoundTrips(t *testing.T) {
	// Legitimately nested lists are all-vocabulary and must survive untouched.
	in := `<ul><li>a<ul><li>b<ul><li>c</li></ul></li></ul></li></ul>`
	if out := Fragment(in); out != in {
		t.Errorf("nested lists changed on round-trip:\n in: %q\nout: %q", in, out)
	}
}

func TestSurroundingWhitespaceTrimmed(t *testing.T) {
	if out := Fragment("  \n\t <p>hi</p> \n  "); out != "<p>hi</p>" {
		t.Errorf("surrounding whitespace not trimmed: %q", out)
	}
}

// Significant internal whitespace is sacred: preformatted code must round-trip
// byte-for-byte, and ordinary inter-word spacing must survive the unwrapping of
// non-contract inline wrappers.
func TestSignificantWhitespacePreserved(t *testing.T) {
	pre := "<pre><code>if x:\n    return  [1,\t2]\n\n# fin</code></pre>"
	if out := Fragment(pre); out != pre {
		t.Errorf("pre/code whitespace changed:\n in: %q\nout: %q", pre, out)
	}
	spaced := `<p>un <span>deux</span> trois</p>`
	if out := Fragment(spaced); out != `<p>un deux trois</p>` {
		t.Errorf("inter-word spacing lost around unwrapped span: %q", out)
	}
}

func TestPasteFromGoogleDocsIsCleaned(t *testing.T) {
	// Google Docs wraps a selection in a guid-tagged <b> and styles every span.
	docs := `<b style="font-weight:normal;" id="docs-internal-guid-abc123">` +
		`<p dir="ltr" style="line-height:1.38;margin-top:0pt;">` +
		`<span style="font-size:11pt;font-family:Arial;color:#000000;">Hello </span>` +
		`<span style="font-weight:700;">world</span></p></b>`
	out := Fragment(docs)

	for _, bad := range []string{"docs-internal-guid", "font-family", "font-weight", "style=", "id=", "<span", "<b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("Google Docs cruft %q survived:\n%s", bad, out)
		}
	}
	for _, good := range []string{"Hello", "world"} {
		if !strings.Contains(out, good) {
			t.Errorf("lost real content %q:\n%s", good, out)
		}
	}
}

func TestPasteFromLibreOfficeIsCleaned(t *testing.T) {
	// LibreOffice tags paragraphs class="western" and wraps runs in <font>.
	lo := `<p class="western" style="margin-bottom:0cm;">` +
		`<font face="Liberation Serif, serif"><font size="3">Bonjour </font></font>` +
		`<font color="#ff0000">le monde</font></p>`
	out := Fragment(lo)

	for _, bad := range []string{"western", "<font", "face=", "class=", "style="} {
		if strings.Contains(out, bad) {
			t.Errorf("LibreOffice cruft %q survived:\n%s", bad, out)
		}
	}
	for _, good := range []string{"Bonjour", "le monde"} {
		if !strings.Contains(out, good) {
			t.Errorf("lost real content %q:\n%s", good, out)
		}
	}
}

// --- stack slots (§5.1) ---------------------------------------------------------

// A stack slot is a pile of blocks with no free prose: prose between blocks is
// dropped, and a slot's declared block list narrows the catalogue.
func TestStackSlotKeepsBlocksDropsProse(t *testing.T) {
	in := `<p>prose libre</p>` +
		`<aside data-block="cta" data-variant="primary"><p>appel</p></aside>` +
		`texte nu` +
		`<div data-block="columns" data-count="2"><p>a</p></div>`
	out := FragmentForSlot(in, SlotPolicy{Stack: true})
	want := `<aside data-block="cta" data-variant="primary"><p>appel</p></aside>` +
		`<div data-block="columns" data-count="2"><p>a</p></div>`
	if out != want {
		t.Errorf("stack sanitisation:\n got %q\nwant %q", out, want)
	}
}

func TestStackSlotRestrictsToAllowedBlocks(t *testing.T) {
	in := `<aside data-block="cta"><p>ok</p></aside><div data-block="columns" data-count="2"><p>no</p></div>`
	// Only cta is allowed on this slot; columns degrades (its prose survives?
	// no — in a stack, a degraded block's content is not a block, so it is
	// dropped by the stack rule).
	out := FragmentForSlot(in, SlotPolicy{Stack: true, AllowedBlocks: []string{"cta"}})
	if !strings.Contains(out, `data-block="cta"`) {
		t.Errorf("allowed block lost: %q", out)
	}
	if strings.Contains(out, "columns") {
		t.Errorf("disallowed block survived in stack: %q", out)
	}
}

// A richtext slot with an allowed-block list degrades the disallowed block to
// its prose (not a stack: content is kept), never keeps its block identity.
func TestRichtextSlotBlockListDegrades(t *testing.T) {
	in := `<p>intro</p><aside data-block="cta"><p>garde</p></aside>`
	out := FragmentForSlot(in, SlotPolicy{AllowedBlocks: []string{"gallery"}})
	if strings.Contains(out, "data-block") {
		t.Errorf("disallowed block kept its identity: %q", out)
	}
	if !strings.Contains(out, "garde") || !strings.Contains(out, "intro") {
		t.Errorf("content lost on degradation: %q", out)
	}
}
