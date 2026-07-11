// Package theme loads and models a Palimpseste theme: theme.json (slot
// declarations, data schemas, explicit templates, ordered stylesheet list,
// typed design tokens — the full §5.1 manifest) plus the template and style
// files on disk. A theme is pure data — HTML templates with data-slot markers
// and vanilla CSS — never executable code.
package theme

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"palimpseste/internal/blocks"
)

// SlotType enumerates the editing contracts a slot can carry (§5.1): each type
// drives a dedicated micro-editor and a dedicated server-side micro-contract.
type SlotType string

const (
	SlotPlain    SlotType = "plain"    // single line of bare text (titles)
	SlotRichtext SlotType = "richtext" // semantic prose, full §4 vocabulary
	SlotNav      SlotType = "nav"      // structured navigation
	SlotStack    SlotType = "stack"    // an ordered pile of blocks, no free prose
	SlotImage    SlotType = "image"    // a single media reference
	SlotData     SlotType = "data"     // a table bound to data/
)

var validSlotTypes = map[SlotType]bool{
	SlotPlain: true, SlotRichtext: true, SlotNav: true,
	SlotStack: true, SlotImage: true, SlotData: true,
}

// Slot is a theme's declaration of an editable region (§5.1).
type Slot struct {
	Type      SlotType `json:"type"`
	Blocks    []string `json:"blocks,omitempty"`    // richtext/stack: allowed block names
	Source    string   `json:"source,omitempty"`    // data slots: data/ table name
	Inline    bool     `json:"inline,omitempty"`    // image slots: inline the SVG (§10.2)
	MaxLength int      `json:"maxLength,omitempty"` // plain slots: advisory length cap
	Formats   []string `json:"formats,omitempty"`   // image slots: raster and/or vector
}

// Token is one typed design token (§5.1 "tokens", §6): the editor generates a
// control from Type and — when Snap names a scale family — magnetises the
// choices to that family's values. Which family exists is the theme's business:
// nothing here is tied to any particular CSS framework.
type Token struct {
	Type string `json:"type"`           // color | radius | font | length
	Snap string `json:"snap,omitempty"` // scale family the editor snaps to
}

var validTokenTypes = map[string]bool{
	"color": true, "radius": true, "font": true, "length": true,
}

// DataTable is a table schema declaration (§3.3, §5.1 "data"): the truth stays
// flat files under data/; the theme declares shape so writes can be validated
// server-side ("le bluemonday des données"). The engine lands at M3; the
// declaration — needed by the §5.3 migration report — is part of the full
// manifest now.
type DataTable struct {
	Format  string            `json:"format"` // csv | json
	Columns map[string]string `json:"columns"`
}

var validColumnTypes = map[string]bool{
	"string": true, "number": true, "bool": true, "date": true, "media": true,
}

// Theme is the parsed theme.json plus the resolved theme directory.
type Theme struct {
	Name      string               `json:"name"`
	Version   string               `json:"version"`
	Slots     map[string]Slot      `json:"slots"`
	Data      map[string]DataTable `json:"data,omitempty"`
	Templates map[string]string    `json:"templates,omitempty"` // explicit name → path; convention fallback
	Styles    []string             `json:"styles"`              // ordered, relative to theme dir
	Tokens    map[string]Token     `json:"tokens,omitempty"`
	// Migrate declares slot renames relative to the previous theme (§5.3):
	// old slot name → new slot name. theme apply moves the backing fragments
	// accordingly, as one dedicated commit, before switching.
	Migrate map[string]string `json:"migrate,omitempty"`

	dir string
}

// Load reads and validates a theme from <siteDir>/themes/<name>.
func Load(siteDir, name string) (*Theme, error) {
	dir := filepath.Join(siteDir, "themes", name)
	path := filepath.Join(dir, "theme.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read theme.json for %q: %w", name, err)
	}
	var t Theme
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("parse theme.json for %q: %w", name, err)
	}
	t.dir = dir
	if err := t.validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

func (t *Theme) validate() error {
	if t.Name == "" {
		return fmt.Errorf("theme.json: name is required")
	}
	for name, s := range t.Slots {
		if s.Type == "" {
			return fmt.Errorf("theme %q: slot %q has no type", t.Name, name)
		}
		if !validSlotTypes[s.Type] {
			return fmt.Errorf("theme %q: slot %q has unknown type %q", t.Name, name, s.Type)
		}
		for _, b := range s.Blocks {
			if _, ok := blocks.Lookup(b); !ok {
				return fmt.Errorf("theme %q: slot %q allows unknown block %q (not in the §4.1 catalogue)", t.Name, name, b)
			}
		}
		for _, f := range s.Formats {
			if f != "raster" && f != "vector" {
				return fmt.Errorf("theme %q: slot %q has unknown format %q", t.Name, name, f)
			}
		}
	}
	for name, d := range t.Data {
		if d.Format != "csv" && d.Format != "json" {
			return fmt.Errorf("theme %q: data table %q has unknown format %q", t.Name, name, d.Format)
		}
		for col, typ := range d.Columns {
			if !validColumnTypes[typ] {
				return fmt.Errorf("theme %q: data table %q column %q has unknown type %q", t.Name, name, col, typ)
			}
		}
	}
	for name, tok := range t.Tokens {
		if !strings.HasPrefix(name, "--") {
			return fmt.Errorf("theme %q: token %q must be a CSS custom property (--…)", t.Name, name)
		}
		if !validTokenTypes[tok.Type] {
			return fmt.Errorf("theme %q: token %q has unknown type %q", t.Name, name, tok.Type)
		}
	}
	for name, rel := range t.Templates {
		if filepath.IsAbs(rel) || !within(t.dir, filepath.Join(t.dir, filepath.FromSlash(rel))) {
			return fmt.Errorf("theme %q: template %q path %q escapes the theme", t.Name, name, rel)
		}
	}
	return nil
}

// Dir is the theme's root directory.
func (t *Theme) Dir() string { return t.dir }

// TemplatesDir is where page templates live by convention.
func (t *Theme) TemplatesDir() string { return filepath.Join(t.dir, "templates") }

// StylesDir is where stylesheets live by convention.
func (t *Theme) StylesDir() string { return filepath.Join(t.dir, "styles") }

// Template reads a named template: through the manifest's explicit
// "templates" map when declared (§5.1), by the templates/<name>.html
// convention otherwise.
func (t *Theme) Template(name string) (string, error) {
	path := filepath.Join(t.TemplatesDir(), name+".html")
	if rel, ok := t.Templates[name]; ok {
		path = filepath.Join(t.dir, filepath.FromSlash(rel))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read template %q: %w", name, err)
	}
	return string(raw), nil
}

// HasTemplate reports whether the theme can serve the named template — the
// §5.3 check needs the answer without reading the file body.
func (t *Theme) HasTemplate(name string) bool {
	_, err := t.Template(name)
	return err == nil
}

// SortedSlotNames returns declared slot names sorted, for deterministic checks.
func (t *Theme) SortedSlotNames() []string {
	names := make([]string, 0, len(t.Slots))
	for n := range t.Slots {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// within guards a joined path against escaping root — same discipline as the
// content loader (§14).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
