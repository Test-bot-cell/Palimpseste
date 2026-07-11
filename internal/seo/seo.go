// Package seo injects deterministic, build-time SEO into a materialized page:
// document language, charset/viewport, title, description, canonical link,
// Open Graph tags and JSON-LD (WebSite + Organization). Everything here is a
// pure function of site.json and the page — no runtime, no per-request logic.
package seo

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"palimpseste/internal/render"
	"palimpseste/internal/site"
)

// Apply upserts the SEO nodes for page p into doc's head. It is idempotent:
// existing title/meta/canonical nodes are updated in place rather than
// duplicated, so a theme template may pre-declare them.
func Apply(doc *html.Node, s *site.Site, p site.Page) error {
	if htmlEl := render.ElementByAtom(doc, atom.Html); htmlEl != nil && s.Lang != "" {
		render.SetAttr(htmlEl, "lang", s.Lang)
	}
	head := render.ElementByAtom(doc, atom.Head)
	if head == nil {
		return fmt.Errorf("materialized page %q has no <head>", p.ID)
	}

	title := firstNonEmpty(p.Title, s.Name)
	desc := p.Description
	canonical := s.CanonicalURL(p.Route)

	upsertCharset(head)
	upsertMetaName(head, "viewport", "width=device-width, initial-scale=1")
	upsertTitle(head, title)
	if desc != "" {
		upsertMetaName(head, "description", desc)
	}
	upsertCanonical(head, canonical)

	upsertMetaProperty(head, "og:type", "website")
	upsertMetaProperty(head, "og:site_name", s.Name)
	upsertMetaProperty(head, "og:title", title)
	if desc != "" {
		upsertMetaProperty(head, "og:description", desc)
	}
	upsertMetaProperty(head, "og:url", canonical)
	if img := absURL(s, firstNonEmpty(p.OgImage, s.Organization.Logo)); img != "" {
		upsertMetaProperty(head, "og:image", img)
	}

	for _, payload := range jsonLD(s) {
		head.AppendChild(ldScript(payload))
	}
	return nil
}

// --- JSON-LD -----------------------------------------------------------------

type ldWebSite struct {
	Context string `json:"@context"`
	Type    string `json:"@type"`
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
}

type ldOrganization struct {
	Context string `json:"@context"`
	Type    string `json:"@type"`
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Logo    string `json:"logo,omitempty"`
}

// jsonLD returns the ld+json payloads for the site: always a WebSite, plus an
// Organization when one is declared. json.Marshal emits struct fields in
// declaration order, keeping the bytes stable across builds.
func jsonLD(s *site.Site) [][]byte {
	var out [][]byte
	web, _ := json.Marshal(ldWebSite{
		Context: "https://schema.org",
		Type:    "WebSite",
		Name:    s.Name,
		URL:     firstNonEmpty(s.BaseURL, ""),
	})
	out = append(out, web)

	if org := s.Organization; strings.TrimSpace(org.Name) != "" {
		o, _ := json.Marshal(ldOrganization{
			Context: "https://schema.org",
			Type:    "Organization",
			Name:    org.Name,
			URL:     firstNonEmpty(org.URL, s.BaseURL),
			Logo:    absURL(s, org.Logo),
		})
		out = append(out, o)
	}
	return out
}

func ldScript(payload []byte) *html.Node {
	return render.Element("script",
		[]render.Attr{{Key: "type", Val: "application/ld+json"}},
		render.Raw(string(payload)),
	)
}

// --- head upserts ------------------------------------------------------------

func upsertTitle(head *html.Node, title string) {
	if t := childElement(head, atom.Title, nil); t != nil {
		render.ReplaceChildren(t, []*html.Node{render.Text(title)})
		return
	}
	head.AppendChild(render.Element("title", nil, render.Text(title)))
}

func upsertCharset(head *html.Node) {
	for c := head.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Meta {
			if _, ok := render.GetAttr(c, "charset"); ok {
				render.SetAttr(c, "charset", "utf-8")
				return
			}
		}
	}
	head.AppendChild(render.Element("meta", []render.Attr{{Key: "charset", Val: "utf-8"}}))
}

func upsertMetaName(head *html.Node, name, content string) {
	if m := childElement(head, atom.Meta, matchAttr("name", name)); m != nil {
		render.SetAttr(m, "content", content)
		return
	}
	head.AppendChild(render.Element("meta", []render.Attr{
		{Key: "name", Val: name}, {Key: "content", Val: content},
	}))
}

func upsertMetaProperty(head *html.Node, property, content string) {
	if m := childElement(head, atom.Meta, matchAttr("property", property)); m != nil {
		render.SetAttr(m, "content", content)
		return
	}
	head.AppendChild(render.Element("meta", []render.Attr{
		{Key: "property", Val: property}, {Key: "content", Val: content},
	}))
}

func upsertCanonical(head *html.Node, href string) {
	if l := childElement(head, atom.Link, matchAttr("rel", "canonical")); l != nil {
		render.SetAttr(l, "href", href)
		return
	}
	head.AppendChild(render.Element("link", []render.Attr{
		{Key: "rel", Val: "canonical"}, {Key: "href", Val: href},
	}))
}

// childElement returns head's first direct child element of the given atom that
// also satisfies match (match may be nil to accept any).
func childElement(head *html.Node, a atom.Atom, match func(*html.Node) bool) *html.Node {
	for c := head.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == a && (match == nil || match(c)) {
			return c
		}
	}
	return nil
}

func matchAttr(key, val string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		got, ok := render.GetAttr(n, key)
		return ok && got == val
	}
}

// --- helpers -----------------------------------------------------------------

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// absURL resolves a possibly root-relative asset ref against the site base URL.
// Absolute (scheme-qualified) refs and empty refs pass through unchanged.
func absURL(s *site.Site, ref string) string {
	if ref == "" || strings.Contains(ref, "://") {
		return ref
	}
	if s.BaseURL == "" {
		return ref
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	return s.BaseURL + ref
}
