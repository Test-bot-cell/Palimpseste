// Package svg is the SVG contract (§10.2). An SVG is the most palimpsestic
// format there is — text, diffable, committable, themeable — but it is also an
// XML *document*: <script>, on* handlers, <foreignObject>, external references,
// SMIL. Direct navigation to …/media/logo.svg would execute its scripts in the
// site's origin, and a static host's HTTP headers are not ours to set — there
// is no CSP fallback. The sanitisation carries the guarantee alone.
//
// The strategy (§10.2): a home-grown whitelist over the encoding/xml token
// stream — Go's parser ignores external entities, so XXE dies at the door —
// then re-serialisation from the cleaned tree, never the original bytes. That
// kills polyglots (SVG has no magic bytes), yields a canonical, diffable form,
// and lets us inject a viewBox where one is missing. Two profiles: `img`
// (served via <img>, no execution context, standard whitelist) and `inline`,
// stricter, for assets the materializer embeds into the HTML.
package svg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// Profile selects how strict the whitelist is.
type Profile int

const (
	// ProfileImg is for SVGs served through <img>: no script execution context
	// applies, but the file is still re-serialised canonically.
	ProfileImg Profile = iota
	// ProfileInline is for SVGs the materializer embeds directly into HTML: the
	// same element whitelist, but the caller (materialize) is expected to reduce
	// them to currentColor. This profile forbids id/reference constructs that
	// could collide with the host document's own ids.
	ProfileInline
)

// elementWhitelist is the geometry-and-paint vocabulary an SVG may keep. It is
// deliberately closed: shapes, paths, grouping, gradients, defs and a
// locally-restricted <use>. Everything else — script, foreignObject, image,
// animation, metadata — is dropped whole.
var elementWhitelist = map[string]bool{
	"svg": true, "g": true, "defs": true, "symbol": true, "use": true,
	"path": true, "rect": true, "circle": true, "ellipse": true,
	"line": true, "polyline": true, "polygon": true,
	"title": true, "desc": true,
	"lineargradient": true, "radialgradient": true, "stop": true,
	"clippath": true, "mask": true, "pattern": true,
	"tspan": true, "text": true,
}

// attrWhitelist is the attribute vocabulary. Presentational geometry and paint
// only; every on* handler, every href/xlink:href except the locally-checked
// <use>, style, and class are absent.
var attrWhitelist = map[string]bool{
	"d": true, "points": true, "x": true, "y": true, "x1": true, "y1": true,
	"x2": true, "y2": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"width": true, "height": true, "viewbox": true, "transform": true,
	"fill": true, "stroke": true, "stroke-width": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-dasharray": true, "stroke-opacity": true,
	"fill-opacity": true, "fill-rule": true, "opacity": true, "clip-rule": true,
	"gradientunits": true, "gradienttransform": true, "offset": true,
	"stop-color": true, "stop-opacity": true, "spreadmethod": true,
	"clip-path": true, "mask": true, "patternunits": true,
	"preserveaspectratio": true, "id": true, "text-anchor": true,
	"font-size": true, "font-family": true, "font-weight": true,
}

// LooksLikeSVG reports whether bytes plausibly open an SVG document — the cheap
// pre-check the upload handler uses to route to the sanitiser (§10.2: SVG has
// no magic bytes, so this scans the head for the root/prolog).
func LooksLikeSVG(b []byte) bool {
	head := b
	if len(head) > 512 {
		head = head[:512]
	}
	low := strings.ToLower(string(head))
	return strings.Contains(low, "<svg") ||
		(strings.Contains(low, "<?xml") && strings.Contains(strings.ToLower(string(b)), "<svg"))
}

// Sanitize cleans raw SVG bytes to the contract and returns the canonical
// re-serialised form. viewBox is guaranteed present on the root (§10.2): a
// missing one is synthesised from width/height so themes can size the SVG in
// CSS. The output always parses, is inert, and is byte-stable for identical
// input.
func Sanitize(raw []byte, profile Profile) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity // known entities only; external entities ignored

	var out strings.Builder
	enc := xml.NewEncoder(&out)

	// skipDepth > 0 means we are inside a dropped element and discard everything
	// until it closes.
	skipDepth := 0
	rootSeen := false
	var rootHadViewBox, rootHadWH bool
	var rootW, rootH string

	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return "", fmt.Errorf("parse svg: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if skipDepth > 0 || !elementWhitelist[name] {
				skipDepth++
				continue
			}
			clean := xml.StartElement{Name: xml.Name{Local: name}}
			isRoot := name == "svg" && !rootSeen
			for _, a := range t.Attr {
				key := strings.ToLower(a.Name.Local)
				// <use> may only reference a local fragment (#…), never an
				// external document (§10.2).
				if (key == "href") && name == "use" {
					if strings.HasPrefix(a.Value, "#") && profile == ProfileImg {
						clean.Attr = append(clean.Attr, xml.Attr{Name: xml.Name{Local: "href"}, Value: a.Value})
					}
					continue
				}
				if !attrWhitelist[key] {
					continue
				}
				// The inline profile forbids ids and id-references: an embedded
				// SVG must not collide with or hijack the host document's ids.
				if profile == ProfileInline && (key == "id" || key == "clip-path" || key == "mask") {
					continue
				}
				if !safeValue(a.Value) {
					continue
				}
				canonKey := key
				if key == "viewbox" {
					canonKey = "viewBox"
					if isRoot {
						rootHadViewBox = true
					}
				}
				clean.Attr = append(clean.Attr, xml.Attr{Name: xml.Name{Local: canonKey}, Value: a.Value})
				if isRoot && key == "width" {
					rootW, rootHadWH = a.Value, true
				}
				if isRoot && key == "height" {
					rootH = a.Value
				}
			}
			if isRoot {
				clean.Attr = append([]xml.Attr{{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"}}, clean.Attr...)
				if !rootHadViewBox {
					if vb := viewBoxFromWH(rootW, rootH, rootHadWH); vb != "" {
						clean.Attr = append(clean.Attr, xml.Attr{Name: xml.Name{Local: "viewBox"}, Value: vb})
					}
				}
				rootSeen = true
			}
			if err := enc.EncodeToken(clean); err != nil {
				return "", err
			}

		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			name := strings.ToLower(t.Name.Local)
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}}); err != nil {
				return "", err
			}

		case xml.CharData:
			if skipDepth > 0 {
				continue
			}
			if err := enc.EncodeToken(xml.CharData(t)); err != nil {
				return "", err
			}

		// Comments, processing instructions and directives (incl. DOCTYPE, the
		// XXE vector) are dropped entirely — they carry no geometry.
		default:
		}
	}
	if err := enc.Flush(); err != nil {
		return "", err
	}
	if !rootSeen {
		return "", fmt.Errorf("no <svg> root element found")
	}
	return out.String(), nil
}

// safeValue rejects attribute values carrying a script vector even on a
// whitelisted attribute: url(javascript:…) and data: URIs, chiefly in paint
// and clip references.
func safeValue(v string) bool {
	low := strings.ToLower(strings.TrimSpace(v))
	if strings.Contains(low, "javascript:") || strings.Contains(low, "data:") {
		return false
	}
	// A url() reference must target a local fragment.
	if i := strings.Index(low, "url("); i >= 0 {
		rest := low[i+4:]
		rest = strings.TrimLeft(rest, " '\"")
		if !strings.HasPrefix(rest, "#") {
			return false
		}
	}
	return true
}

// viewBoxFromWH derives a viewBox from numeric width/height, so a themeable
// SVG that shipped only pixel dimensions still scales in CSS.
func viewBoxFromWH(w, h string, had bool) string {
	if !had {
		return ""
	}
	wn, hn := trimUnit(w), trimUnit(h)
	if wn == "" || hn == "" {
		return ""
	}
	return "0 0 " + wn + " " + hn
}

func trimUnit(s string) string {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.') {
		end++
	}
	return s[:end]
}
