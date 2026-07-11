// Package render wraps golang.org/x/net/html with the small set of DOM
// helpers Palimpseste needs: parsing documents and fragments, walking the
// tree, reading/writing attributes, and rendering back to canonical HTML.
//
// Rendering through x/net/html is intentionally canonicalizing: the same
// input always renders to the same bytes, which is what makes materialization
// deterministic and diffable.
package render

import (
	"bytes"
	"io"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ParseDocument parses a full HTML document (adds html/head/body if absent).
func ParseDocument(s string) (*html.Node, error) {
	return html.Parse(strings.NewReader(s))
}

// ParseFragment parses an HTML fragment in the context of ctx, returning the
// detached top-level nodes. ctx determines the parser insertion mode (e.g. a
// <ul> context parses bare <li> correctly).
func ParseFragment(s string, ctx *html.Node) ([]*html.Node, error) {
	return html.ParseFragment(strings.NewReader(s), ctx)
}

// Render serializes a node to HTML.
func Render(n *html.Node) (string, error) {
	var b bytes.Buffer
	if err := html.Render(&b, n); err != nil {
		return "", err
	}
	return b.String(), nil
}

// RenderTo serializes a node to w.
func RenderTo(w io.Writer, n *html.Node) error {
	return html.Render(w, n)
}

// Attr is an ordered key/value pair for building elements deterministically.
type Attr struct{ Key, Val string }

// Element builds a detached element node with the given tag and ordered attrs.
func Element(tag string, attrs []Attr, children ...*html.Node) *html.Node {
	n := &html.Node{
		Type:     html.ElementNode,
		Data:     tag,
		DataAtom: atom.Lookup([]byte(tag)),
	}
	for _, a := range attrs {
		n.Attr = append(n.Attr, html.Attribute{Key: a.Key, Val: a.Val})
	}
	for _, c := range children {
		Detach(c)
		n.AppendChild(c)
	}
	return n
}

// Text builds a detached text node.
func Text(s string) *html.Node {
	return &html.Node{Type: html.TextNode, Data: s}
}

// Raw builds a node that serializes verbatim (no escaping). Use only for
// trusted, already-serialized content such as JSON-LD payloads.
func Raw(s string) *html.Node {
	return &html.Node{Type: html.RawNode, Data: s}
}

// GetAttr returns the value of the named attribute and whether it was present.
func GetAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// SetAttr inserts or updates an attribute, preserving position on update.
func SetAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

// RemoveAttr deletes an attribute if present.
func RemoveAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			out = append(out, a)
		}
	}
	n.Attr = out
}

// SortedAttrKeys returns attribute keys sorted; used by callers that need a
// deterministic ordering when the source ordering is not meaningful.
func SortedAttrKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Walk visits n and its descendants depth-first, calling fn on each node.
func Walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		Walk(c, fn)
	}
}

// FindFirst returns the first node (depth-first) satisfying pred, or nil.
func FindFirst(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := FindFirst(c, pred); got != nil {
			return got
		}
	}
	return nil
}

// FindAll returns all nodes (depth-first) satisfying pred.
func FindAll(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	Walk(n, func(x *html.Node) {
		if pred(x) {
			out = append(out, x)
		}
	})
	return out
}

// ElementByAtom returns the first element with the given atom (e.g. atom.Head).
func ElementByAtom(n *html.Node, a atom.Atom) *html.Node {
	return FindFirst(n, func(x *html.Node) bool {
		return x.Type == html.ElementNode && x.DataAtom == a
	})
}

// Detach removes n from its current parent and unlinks its siblings.
func Detach(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
		return
	}
	n.PrevSibling = nil
	n.NextSibling = nil
}

// ReplaceChildren removes all existing children of n and appends kids.
func ReplaceChildren(n *html.Node, kids []*html.Node) {
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	for _, k := range kids {
		Detach(k)
		n.AppendChild(k)
	}
}

// RenderChildren serializes the children of n — its innerHTML.
func RenderChildren(n *html.Node) (string, error) {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s, err := Render(c)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// Head returns the document's <head> element, or nil.
func Head(doc *html.Node) *html.Node { return ElementByAtom(doc, atom.Head) }

// Body returns the document's <body> element, or nil.
func Body(doc *html.Node) *html.Node { return ElementByAtom(doc, atom.Body) }

// EnsureDoctype guarantees a standards-mode <!doctype html> leads the document.
func EnsureDoctype(doc *html.Node) {
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.DoctypeNode {
			return
		}
	}
	dt := &html.Node{Type: html.DoctypeNode, Data: "html"}
	if doc.FirstChild != nil {
		doc.InsertBefore(dt, doc.FirstChild)
	} else {
		doc.AppendChild(dt)
	}
}

// AppendStylesheet appends <link rel="stylesheet" href="..."> to <head>. It is
// a no-op when the document has no head.
func AppendStylesheet(doc *html.Node, href string) {
	head := Head(doc)
	if head == nil {
		return
	}
	head.AppendChild(Element("link", []Attr{
		{Key: "rel", Val: "stylesheet"}, {Key: "href", Val: href},
	}))
}
