package svg

// Favicon derivation (§10.2): from one sanitized logo SVG the binary derives
// the whole set — the native SVG (best, scalable), PNG fallbacks for
// compatibility, and an Apple touch icon. Rasterisation is pure Go
// (oksvg/rasterx), so the single binary stays single.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// FaviconFile is one derived icon: its output path (relative to the site root)
// and bytes.
type FaviconFile struct {
	Path  string
	Bytes []byte
}

// faviconSizes are the PNG compatibility renditions plus the Apple touch icon.
var faviconSizes = []struct {
	name string
	size int
}{
	{"favicon-16.png", 16},
	{"favicon-32.png", 32},
	{"favicon-180.png", 180}, // apple-touch-icon
	{"favicon-192.png", 192},
	{"favicon-512.png", 512},
}

// Favicons derives the icon set from sanitized logo SVG bytes. The SVG must
// already be through Sanitize; a fill="currentColor" is resolved to a concrete
// colour for the raster renditions (browsers cannot inherit into a favicon),
// while the vector favicon keeps its themeable form.
func Favicons(sanitizedSVG []byte, rasterColor color.Color) ([]FaviconFile, error) {
	files := []FaviconFile{
		{Path: "favicon.svg", Bytes: sanitizedSVG},
	}

	// currentColor is meaningless in a standalone raster: bind it to a concrete
	// colour so the PNGs are visible.
	concrete := bindCurrentColor(string(sanitizedSVG), rasterColor)
	icon, err := oksvg.ReadIconStream(strings.NewReader(concrete))
	if err != nil {
		return nil, fmt.Errorf("parse svg for rasterisation: %w", err)
	}

	for _, f := range faviconSizes {
		png, err := rasterize(icon, f.size)
		if err != nil {
			return nil, fmt.Errorf("rasterise %s: %w", f.name, err)
		}
		files = append(files, FaviconFile{Path: f.name, Bytes: png})
	}
	return files, nil
}

func rasterize(icon *oksvg.SvgIcon, size int) ([]byte, error) {
	icon.SetTarget(0, 0, float64(size), float64(size))
	rgba := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, rgba, rgba.Bounds())
	raster := rasterx.NewDasher(size, size, scanner)
	icon.Draw(raster, 1.0)

	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bindCurrentColor replaces fill/stroke="currentColor" with a concrete hex for
// standalone rasterisation.
func bindCurrentColor(svg string, c color.Color) string {
	r, g, b, _ := c.RGBA()
	hex := fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
	svg = strings.ReplaceAll(svg, `"currentColor"`, `"`+hex+`"`)
	svg = strings.ReplaceAll(svg, `'currentColor'`, `'`+hex+`'`)
	return svg
}

// FaviconLinks returns the <head> link/meta markup pointing at the derived set,
// for the materializer to inject (§10.2). Paths are site-root-relative; the
// caller prefixes to page depth like any other asset.
func FaviconLinks(prefix string) string {
	return strings.Join([]string{
		`<link rel="icon" href="` + prefix + `favicon.svg" type="image/svg+xml">`,
		`<link rel="icon" href="` + prefix + `favicon-32.png" sizes="32x32" type="image/png">`,
		`<link rel="apple-touch-icon" href="` + prefix + `favicon-180.png">`,
	}, "")
}
