// Package blocks declares the named-block catalogue (§4.1): the one source of
// truth for which data-block containers the content contract admits and which
// typed data-* parameters each admits.
//
// A block is a conventional semantic structure, never code. Its parameters are
// whitelisted per type and validated at write time — type, bounds, enumerated
// values — so a fragment can only ever carry parameters the catalogue declares.
// The sanitizer consults this catalogue on every save; the materializer
// consults it to know which blocks are computed at build time.
//
// V1 catalogue (§4.1): static blocks gallery, columns, cta, embed; computed
// blocks table and toc. `recent` arrives with the V2 collections. The `table`
// block is accepted by the contract now but rendered with the data/ layer (M3,
// §18); until then it degrades gracefully to its (empty) container.
package blocks

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Kind is the value space of one data-* parameter.
type Kind int

const (
	// Int is a bounded integer: Min <= value <= Max.
	Int Kind = iota
	// Enum is one of a closed set of string values.
	Enum
	// Name is a table/collection identifier: lowercase, digits, - and _.
	Name
	// URL is an https URL whose host must be on the catalogue's embed whitelist.
	URL
)

// Param is the declared schema of one data-* parameter.
type Param struct {
	Kind   Kind
	Min    int      // Int only, inclusive
	Max    int      // Int only, inclusive
	Values []string // Enum only
}

// Block is one catalogue entry.
type Block struct {
	// Computed marks blocks rendered by the build (§4.1 "computés au build").
	// Their children are build output, not source: the canonical stored form is
	// the bare container, so the sanitizer empties them on save.
	Computed bool
	// Elements are the container tags this block may be carried by.
	Elements []string
	// Params maps a parameter name (without the "data-" prefix) to its schema.
	Params map[string]Param
}

// nameRE pins the Name parameter shape; it matches the strict identifier
// discipline of §14 (no dots, so no traversal is even expressible).
var nameRE = regexp.MustCompile(`^[a-z0-9]+(?:[_-][a-z0-9]+)*$`)

// EmbedHosts is the §4.1 domain whitelist for the embed block's iframe. It is
// deliberately short: privacy-preserving video first. Extending it is a
// catalogue change, reviewed like code, never site configuration.
var EmbedHosts = map[string]bool{
	"www.youtube-nocookie.com": true,
	"www.youtube.com":          true,
	"player.vimeo.com":         true,
	"www.dailymotion.com":      true,
}

// catalog is the V1 block catalogue keyed by data-block value.
var catalog = map[string]Block{
	"gallery": {
		Elements: []string{"figure", "div", "section"},
		Params:   map[string]Param{},
	},
	"columns": {
		Elements: []string{"div", "section"},
		Params: map[string]Param{
			"count": {Kind: Int, Min: 2, Max: 4},
		},
	},
	"cta": {
		Elements: []string{"div", "aside", "section"},
		Params: map[string]Param{
			"variant": {Kind: Enum, Values: []string{"primary", "subtle"}},
		},
	},
	"embed": {
		Elements: []string{"div", "figure"},
		Params:   map[string]Param{},
	},
	"table": {
		Computed: true,
		Elements: []string{"div"},
		Params: map[string]Param{
			"source": {Kind: Name},
		},
	},
	"toc": {
		Computed: true,
		Elements: []string{"div", "aside"},
		Params: map[string]Param{
			"depth": {Kind: Int, Min: 2, Max: 4},
		},
	},
}

// Lookup returns the catalogue entry for a data-block value.
func Lookup(name string) (Block, bool) {
	b, ok := catalog[name]
	return b, ok
}

// AllowedOn reports whether block name may be carried by the given element tag.
func AllowedOn(name, tag string) bool {
	b, ok := catalog[name]
	if !ok {
		return false
	}
	for _, el := range b.Elements {
		if el == tag {
			return true
		}
	}
	return false
}

// Computed reports whether name is a build-computed block.
func Computed(name string) bool {
	b, ok := catalog[name]
	return ok && b.Computed
}

// ValidParam validates one data-* parameter value against block name's schema.
// param is the attribute name without its "data-" prefix.
func ValidParam(name, param, value string) bool {
	b, ok := catalog[name]
	if !ok {
		return false
	}
	p, ok := b.Params[param]
	if !ok {
		return false
	}
	switch p.Kind {
	case Int:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		return err == nil && n >= p.Min && n <= p.Max
	case Enum:
		for _, v := range p.Values {
			if v == value {
				return true
			}
		}
		return false
	case Name:
		return nameRE.MatchString(value)
	case URL:
		return ValidEmbedSrc(value)
	}
	return false
}

// ContainerElements returns the sorted-stable union of every element tag a
// catalogue block may be carried by. The sanitizer whitelists exactly these.
func ContainerElements() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range []string{"gallery", "columns", "cta", "embed", "table", "toc"} {
		for _, el := range catalog[name].Elements {
			if !seen[el] {
				seen[el] = true
				out = append(out, el)
			}
		}
	}
	return out
}

// ParamAttrs returns the sorted-stable union of every declared parameter's
// data-* attribute name. The sanitizer whitelists exactly these on containers.
func ParamAttrs() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range []string{"gallery", "columns", "cta", "embed", "table", "toc"} {
		for p := range catalog[name].Params {
			attr := "data-" + p
			if !seen[attr] {
				seen[attr] = true
				out = append(out, attr)
			}
		}
	}
	return out
}

// ValidEmbedSrc reports whether an iframe src is admissible inside an embed
// block: https only, host on the whitelist, no credentials.
func ValidEmbedSrc(src string) bool {
	u, err := url.Parse(strings.TrimSpace(src))
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.User == nil && EmbedHosts[u.Host]
}

// --- serializable schema (for the editor's generated config panels, §9) --------

// ParamSchema is the JSON shape of one parameter, consumed by the overlay to
// generate a block's configuration form ("panneaux de configuration générés
// depuis le schéma des data-*").
type ParamSchema struct {
	Kind   string   `json:"kind"` // int | enum | name | url
	Min    int      `json:"min,omitempty"`
	Max    int      `json:"max,omitempty"`
	Values []string `json:"values,omitempty"`
}

// BlockSchema is the JSON shape of one catalogue entry.
type BlockSchema struct {
	Computed bool                   `json:"computed"`
	Elements []string               `json:"elements"`
	Params   map[string]ParamSchema `json:"params"`
}

// Schema exports the whole catalogue in serializable form: the single source
// of truth the overlay reads instead of duplicating parameter knowledge.
func Schema() map[string]BlockSchema {
	kindName := map[Kind]string{Int: "int", Enum: "enum", Name: "name", URL: "url"}
	out := make(map[string]BlockSchema, len(catalog))
	for name, b := range catalog {
		params := make(map[string]ParamSchema, len(b.Params))
		for p, spec := range b.Params {
			params[p] = ParamSchema{
				Kind:   kindName[spec.Kind],
				Min:    spec.Min,
				Max:    spec.Max,
				Values: append([]string(nil), spec.Values...),
			}
		}
		out[name] = BlockSchema{
			Computed: b.Computed,
			Elements: append([]string(nil), b.Elements...),
			Params:   params,
		}
	}
	return out
}
