package render

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestRenderRoundTripCanonical(t *testing.T) {
	src := `<!doctype html><html><head><title>x</title></head><body><p>hi</p></body></html>`
	doc, err := ParseDocument(src)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc2, _ := ParseDocument(first)
	second, _ := Render(doc2)
	if first != second {
		t.Errorf("render not idempotent:\n%s\n---\n%s", first, second)
	}
}

func TestAttrOps(t *testing.T) {
	n := Element("div", []Attr{{Key: "id", Val: "a"}})
	if v, ok := GetAttr(n, "id"); !ok || v != "a" {
		t.Fatalf("GetAttr = %q,%v", v, ok)
	}
	SetAttr(n, "id", "b")
	if v, _ := GetAttr(n, "id"); v != "b" {
		t.Errorf("SetAttr update failed: %q", v)
	}
	SetAttr(n, "class", "c")
	if v, _ := GetAttr(n, "class"); v != "c" {
		t.Errorf("SetAttr insert failed: %q", v)
	}
	RemoveAttr(n, "id")
	if _, ok := GetAttr(n, "id"); ok {
		t.Error("RemoveAttr did not remove id")
	}
}

func TestParseFragmentContext(t *testing.T) {
	ul := Element("ul", nil)
	nodes, err := ParseFragment(`<li>a</li><li>b</li>`, ul)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, n := range nodes {
		if n.DataAtom == atom.Li {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 <li>, got %d", count)
	}
}

func TestReplaceChildren(t *testing.T) {
	div := Element("div", nil, Text("old"))
	ReplaceChildren(div, []*html.Node{Text("new")})
	out, _ := Render(div)
	if !strings.Contains(out, "new") || strings.Contains(out, "old") {
		t.Errorf("ReplaceChildren result = %q", out)
	}
}
