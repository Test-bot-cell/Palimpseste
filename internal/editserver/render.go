package editserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"

	"palimpseste/internal/blocks"
	"palimpseste/internal/content"
	"palimpseste/internal/data"
	"palimpseste/internal/materialize"
	"palimpseste/internal/render"
	"palimpseste/internal/seo"
	"palimpseste/internal/site"
	"palimpseste/internal/svg"
	"palimpseste/internal/theme"
)

// Overlay resource routes. These live under /_pal/ so they never collide with a
// site's own routes, and are only ever served by the edit server.
const (
	overlayJSPath = "/_pal/app.js"
	themeCSSPath  = "/_pal/theme.css"
)

// overlayConfig is the JSON the browser overlay reads from #_pal-config before
// booting: which page it is editing, the CSRF token to authorize writes, the
// page list for the switcher, the theme's declared slots so each region gets
// the micro-editor its type calls for (§5.1), the block catalogue schema so
// stack config panels are generated — never duplicated — from the single
// source of truth (§9), and the page's current meta for the SEO panel (§11).
// The UI never offers what the server would refuse.
type overlayConfig struct {
	Page    string                        `json:"page"`
	CSRF    string                        `json:"csrf"`
	Pages   []pageEntry                   `json:"pages"`
	Slots   map[string]slotDecl           `json:"slots"`
	Blocks  map[string]blocks.BlockSchema `json:"blocks"`
	Meta    pageMeta                      `json:"meta"`
	Publish bool                          `json:"publish"` // site declares a publish method (§13)
	AI      bool                          `json:"ai"`      // an AI provider is configured (§12)
}

// slotDecl is the slot subset the overlay needs.
type slotDecl struct {
	Type   string   `json:"type"`
	Blocks []string `json:"blocks,omitempty"`
}

// pageMeta feeds the SEO panel with the page's current values.
type pageMeta struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	OgImage     string `json:"ogImage,omitempty"`
}

// liveTables resolves data/ tables for the edit view: read fresh per render so
// the editor always shows the current grid, validated against the same schema
// gate as the build (§3.3).
func liveTables(siteDir string, t *theme.Theme) func(string) ([]string, [][]string, bool) {
	return func(name string) ([]string, [][]string, bool) {
		schema, declared := t.Data[name]
		if !declared {
			return nil, nil, false
		}
		tab, found, err := data.Load(siteDir, name)
		if err != nil || !found || data.Validate(tab, schema) != nil {
			return nil, nil, false
		}
		return tab.Header, tab.Rows, true
	}
}

// liveVariants probes media/derived for the responsive renditions of an
// original (§10.1) — same shape as the build's resolver, read fresh per render.
func liveVariants(siteDir string) func(string) []materialize.MediaVariant {
	return func(src string) []materialize.MediaVariant {
		const orig = "media/originals/"
		if !strings.HasPrefix(src, orig) {
			return nil
		}
		base := strings.TrimSuffix(path.Base(src), path.Ext(src))
		var out []materialize.MediaVariant
		for _, w := range []int{480, 800, 1200} {
			rel := fmt.Sprintf("media/derived/%s-%d.webp", base, w)
			if _, err := os.Stat(filepath.Join(siteDir, filepath.FromSlash(rel))); err == nil {
				out = append(out, materialize.MediaVariant{Path: rel, Width: w})
			}
		}
		return out
	}
}

// pageEntry is one option in the overlay's page switcher.
type pageEntry struct {
	ID    string `json:"id"`
	Route string `json:"route"`
	Title string `json:"title"`
}

// renderEditPage materializes a page exactly as production would — SEO, theme
// stylesheet — but keeps the data-slot markers so the overlay can turn each
// region into an editor, then injects the overlay itself. hasCSS toggles the
// theme stylesheet link (served live at themeCSSPath, not content-addressed).
func renderEditPage(t *theme.Theme, ldr *content.Loader, s *site.Site, p site.Page, siteDir string, hasCSS bool, cfg overlayConfig) (string, error) {
	doc, _, err := materialize.Page(t, ldr, p, materialize.Options{
		KeepSlotMarkers: true,
		Tables:          liveTables(siteDir, t),
		Variants:        liveVariants(siteDir),
		InlineSVG:       svg.InlineResolver(siteDir, t),
	})
	if err != nil {
		return "", fmt.Errorf("materialize page %q: %w", p.ID, err)
	}
	if err := seo.Apply(doc, s, p); err != nil {
		return "", err
	}
	if hasCSS {
		render.AppendStylesheet(doc, themeCSSPath)
	}
	if err := injectOverlay(doc, cfg); err != nil {
		return "", err
	}
	render.EnsureDoctype(doc)
	return render.Render(doc)
}

// injectOverlay wires the editor into a materialized document: the config blob
// and the module script at the end of the body, so the overlay boots after the
// page's own content is in the DOM. No stylesheet touches the page (§9 "Shadow
// DOM intégral"): the overlay chrome styles itself inside its shadow root, and
// editing affordances are inline styles on template-owned nodes — theme CSS and
// editor CSS share no cascade in either direction.
func injectOverlay(doc *html.Node, cfg overlayConfig) error {
	body := render.Body(doc)
	if body == nil {
		return fmt.Errorf("edit page is missing <body>")
	}

	// json.Marshal escapes <, > and & (< …), so nothing in a page title or
	// route can break out of this <script> element.
	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode overlay config: %w", err)
	}
	body.AppendChild(render.Element("script",
		[]render.Attr{
			{Key: "id", Val: "_pal-config"},
			{Key: "type", Val: "application/json"},
		},
		render.Raw(string(payload)),
	))
	body.AppendChild(render.Element("script", []render.Attr{
		{Key: "type", Val: "module"},
		{Key: "src", Val: overlayJSPath},
	}))
	return nil
}
