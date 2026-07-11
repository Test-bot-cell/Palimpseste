package svg

import (
	"encoding/xml"
	"strings"
	"testing"
)

// §10.2/§14: every execution vector dies. The sanitised output must never carry
// a script, handler, foreignObject, external reference or XXE construct.
func TestSanitizeKillsExecutionVectors(t *testing.T) {
	cases := []struct{ name, in string }{
		{"script", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script>alert(1)</script><path d="M0 0"/></svg>`},
		{"onclick", `<svg viewBox="0 0 1 1"><path d="M0 0" onclick="steal()"/></svg>`},
		{"onload", `<svg viewBox="0 0 1 1" onload="alert(1)"><rect/></svg>`},
		{"foreignObject", `<svg viewBox="0 0 1 1"><foreignObject><iframe src="https://evil"></iframe></foreignObject></svg>`},
		{"xxe doctype", `<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg viewBox="0 0 1 1"><desc>&x;</desc></svg>`},
		{"external image", `<svg viewBox="0 0 1 1"><image href="https://evil/x.png"/></svg>`},
		{"js in fill", `<svg viewBox="0 0 1 1"><rect fill="url(javascript:alert(1))"/></svg>`},
		{"external use", `<svg viewBox="0 0 1 1"><use href="https://evil/x.svg#y"/></svg>`},
		{"animate smil", `<svg viewBox="0 0 1 1"><rect><animate attributeName="x" from="0" to="javascript:1"/></rect></svg>`},
		{"style tag", `<svg viewBox="0 0 1 1"><style>@import url(evil)</style><rect/></svg>`},
	}
	forbidden := []string{"script", "onclick", "onload", "foreignObject", "iframe",
		"javascript:", "ENTITY", "file:///", "<image", "evil", "<animate", "<style", "@import"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := Sanitize([]byte(c.in), ProfileImg)
			if err != nil {
				t.Fatalf("sanitize errored: %v", err)
			}
			low := strings.ToLower(out)
			for _, bad := range forbidden {
				if strings.Contains(low, strings.ToLower(bad)) {
					t.Errorf("vector %q survived:\n%s", bad, out)
				}
			}
		})
	}
}

// Geometry and paint survive; the output re-parses (canonical form).
func TestSanitizeKeepsGeometry(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><g fill="currentColor"><path d="M4 4h16v16H4z"/><circle cx="12" cy="12" r="5"/></g></svg>`
	out, err := Sanitize([]byte(in), ProfileImg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`d="M4 4h16v16H4z"`, `fill="currentColor"`, `cx="12"`, `viewBox="0 0 24 24"`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	// Re-sanitising is a no-op: the canonical form is a fixed point.
	again, _ := Sanitize([]byte(out), ProfileImg)
	if again != out {
		t.Errorf("not idempotent:\n1: %s\n2: %s", out, again)
	}
}

// §10.2: a missing viewBox is synthesised from width/height so CSS can size it.
func TestViewBoxInjected(t *testing.T) {
	out, err := Sanitize([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="48" height="32"><rect/></svg>`), ProfileImg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `viewBox="0 0 48 32"`) {
		t.Errorf("viewBox not injected:\n%s", out)
	}
}

// The inline profile drops ids so an embedded SVG cannot collide with the host.
func TestInlineProfileDropsIds(t *testing.T) {
	out, err := Sanitize([]byte(`<svg viewBox="0 0 1 1"><rect id="a" clip-path="url(#c)"/></svg>`), ProfileInline)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "id=") || strings.Contains(out, "clip-path") {
		t.Errorf("inline profile kept id/reference:\n%s", out)
	}
}

func TestLooksLikeSVG(t *testing.T) {
	yes := [][]byte{
		[]byte(`<svg xmlns="...">`),
		[]byte(`<?xml version="1.0"?>` + strings.Repeat(" ", 20) + `<svg>`),
		[]byte(`  <SVG >`),
	}
	no := [][]byte{
		{0xFF, 0xD8, 0xFF}, // jpeg
		[]byte("<html><body>"),
		{},
	}
	for _, b := range yes {
		if !LooksLikeSVG(b) {
			t.Errorf("false negative: %q", b)
		}
	}
	for _, b := range no {
		if LooksLikeSVG(b) {
			t.Errorf("false positive: %q", b)
		}
	}
}

func TestFaviconsDerived(t *testing.T) {
	clean, _ := Sanitize([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M2 2h20v20H2z" fill="currentColor"/></svg>`), ProfileImg)
	files, err := Favicons([]byte(clean), colorBlack{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range files {
		names[f.Path] = true
		if len(f.Bytes) == 0 {
			t.Errorf("%s is empty", f.Path)
		}
	}
	for _, want := range []string{"favicon.svg", "favicon-32.png", "favicon-180.png", "favicon-512.png"} {
		if !names[want] {
			t.Errorf("favicon set missing %s", want)
		}
	}
	// The raster favicon binds currentColor to a concrete colour (the SVG
	// favicon keeps it themeable).
	for _, f := range files {
		if f.Path == "favicon.svg" && !strings.Contains(string(f.Bytes), "currentColor") {
			t.Error("svg favicon lost its themeable currentColor")
		}
	}
}

type colorBlack struct{}

func (colorBlack) RGBA() (r, g, b, a uint32) { return 0, 0, 0, 0xffff }

// FuzzSanitize is the load-bearing guarantee (§10.2 "la sanitisation porte la
// garantie seule"): whatever bytes go in, the output never carries a script
// vector and always re-parses to itself.
func FuzzSanitize(f *testing.F) {
	for _, s := range []string{
		`<svg viewBox="0 0 1 1"><path d="M0 0"/></svg>`,
		`<svg onload="x"><script>1</script></svg>`,
		`<?xml version="1.0"?><!DOCTYPE svg><svg><use href="//evil"/></svg>`,
		`<svg><rect fill="url(javascript:1)"/></svg>`,
		`not xml at all`,
		`<svg><foreignObject><b>x</b></foreignObject></svg>`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := Sanitize([]byte(in), ProfileImg)
		if err != nil {
			return // rejected input is fine; only accepted output must be inert
		}
		// Structural, not substring: the security property is that no dangerous
		// ELEMENT and no attribute VALUE carrying a script vector survives. Text
		// content is inert (a <desc> may legitimately mention "javascript:") and
		// is therefore not a vector — checking the raw string for that word would
		// be a false positive, not a real leak.
		assertInertSVG(t, in, out)

		again, err := Sanitize([]byte(out), ProfileImg)
		if err != nil {
			t.Fatalf("canonical output failed to re-sanitise: %v\n%s", err, out)
		}
		if again != out {
			t.Fatalf("not idempotent:\n1: %s\n2: %s", out, again)
		}
	})
}

// assertInertSVG parses sanitized output and fails if any element outside the
// whitelist survives, any on* handler survives, or any attribute value carries
// an executable scheme.
func assertInertSVG(t *testing.T, in, out string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(out))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(se.Name.Local)
		if !elementWhitelist[name] {
			t.Fatalf("forbidden element <%s> survived for input %q:\n%s", name, in, out)
		}
		for _, a := range se.Attr {
			key := strings.ToLower(a.Name.Local)
			if strings.HasPrefix(key, "on") {
				t.Fatalf("event handler %q survived for input %q:\n%s", a.Name.Local, in, out)
			}
			val := strings.ToLower(strings.TrimSpace(a.Value))
			if strings.Contains(val, "javascript:") || strings.Contains(val, "data:") {
				t.Fatalf("attribute %q=%q carries a vector for input %q:\n%s", a.Name.Local, a.Value, in, out)
			}
		}
	}
}
