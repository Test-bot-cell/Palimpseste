// Package site loads and models site.json, the top-level declaration of a
// Palimpseste site: its identity, its pages/routes, and the SEO metadata that
// drives materialization.
package site

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Site is the parsed site.json.
type Site struct {
	Name         string       `json:"name"`
	BaseURL      string       `json:"baseURL"`
	Lang         string       `json:"lang"`
	Theme        string       `json:"theme"`
	Pages        []Page       `json:"pages"`
	Organization Organization `json:"organization,omitempty"`
	Publish      Publish      `json:"publish,omitempty"`
}

// Publish declares how `POST /api/publish` deploys the site (§13). The method
// is committed; the credentials are not — they live in the environment. V1
// speaks git-push (branch under a remote, e.g. a Pages branch).
type Publish struct {
	Method string `json:"method,omitempty"` // "git-push" (V1)
	Remote string `json:"remote,omitempty"` // git remote name, e.g. "origin"
	Branch string `json:"branch,omitempty"` // target branch, e.g. "pages"
}

// Page is a single materialized route.
type Page struct {
	ID          string `json:"id"`
	Route       string `json:"route"`
	Template    string `json:"template"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// OgImage is the page's social-preview image (§9 "choix de l'og:image"),
	// a media/ path or absolute URL; empty falls back to the organization logo.
	// The dedicated JPEG variant arrives with the media pipeline (M3).
	OgImage string `json:"ogImage,omitempty"`
	// Slots maps a slot name used in the template to a fragment path relative
	// to content/ (without the .html suffix). When empty, slots resolve by
	// convention: content/<page id>/<slot name>.html.
	Slots map[string]string `json:"slots,omitempty"`
}

// Organization feeds the Organization JSON-LD block.
type Organization struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
	Logo string `json:"logo,omitempty"`
}

// Load reads and validates site.json from a site directory.
func Load(dir string) (*Site, error) {
	path := filepath.Join(dir, "site.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read site.json: %w", err)
	}
	var s Site
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse site.json: %w", err)
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Site) validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("site.json: name is required")
	}
	if strings.TrimSpace(s.Theme) == "" {
		return fmt.Errorf("site.json: theme is required")
	}
	if s.Lang == "" {
		s.Lang = "en"
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	seenID := map[string]bool{}
	seenRoute := map[string]bool{}
	for i := range s.Pages {
		p := &s.Pages[i]
		if p.ID == "" {
			return fmt.Errorf("site.json: page %d has no id", i)
		}
		if p.Template == "" {
			return fmt.Errorf("site.json: page %q has no template", p.ID)
		}
		if p.Route == "" {
			return fmt.Errorf("site.json: page %q has no route", p.ID)
		}
		if !strings.HasPrefix(p.Route, "/") {
			return fmt.Errorf("site.json: page %q route must start with '/'", p.ID)
		}
		if seenID[p.ID] {
			return fmt.Errorf("site.json: duplicate page id %q", p.ID)
		}
		if seenRoute[p.Route] {
			return fmt.Errorf("site.json: duplicate route %q", p.Route)
		}
		seenID[p.ID] = true
		seenRoute[p.Route] = true
	}
	return nil
}

// Save writes the manifest back to <dir>/site.json — canonical two-space
// indentation, atomic rename — after re-running validation. Only the editor's
// structural mutations (theme apply §5.3, the SEO meta panel §11) call this;
// site.json stays a human-owned file the rest of the time.
func Save(dir string, s *Site) error {
	if err := s.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	path := filepath.Join(dir, "site.json")
	tmp, err := os.CreateTemp(dir, ".pal-site-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// SortedPages returns pages ordered by ID for deterministic iteration.
func (s *Site) SortedPages() []Page {
	out := make([]Page, len(s.Pages))
	copy(out, s.Pages)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// OutputPath maps a route to a filesystem path under an output root, using
// pretty-URL conventions: "/" -> index.html, "/about" -> about/index.html.
func OutputPath(route string) string {
	clean := strings.Trim(route, "/")
	if clean == "" {
		return "index.html"
	}
	return filepath.Join(filepath.FromSlash(clean), "index.html")
}

// CanonicalURL joins the site base URL and a route.
func (s *Site) CanonicalURL(route string) string {
	if s.BaseURL == "" {
		return route
	}
	if route == "/" {
		return s.BaseURL + "/"
	}
	return s.BaseURL + route
}
