// Package media implements the raster pipeline (§10.1). The order is the
// guarantee: decode → bake the EXIF orientation into pixels → resize → encode.
// Metadata cannot survive re-encoding, so the purge is by construction — and a
// polyglot file dies at the same door, because the stored bytes are always our
// own encoder's output, never the upload's.
//
// Delivery format is WebP (§10.1): lossy for photographs, lossless for
// graphics and captures — decided by the source format (JPEG carries photos,
// PNG carries graphics). Every image gets responsive variants (480/800/1200,
// only ever downscaled) plus one JPEG for the og:image, the pragmatic
// exception for social scrapers. Encoding is pure Go — libwebp compiled to
// WASM, executed by wazero (gen2brain/webp): no CGo, the single binary stays
// single. AVIF remains a premium option, never imposed (§10.1).
package media

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/webp"
	"github.com/rwcarlsen/goexif/exif"
)

// Encoderidentity pins the WASM encoder in the attestation (§7, §10.1): the
// module whose embedded libwebp blob produced every derived image. go.sum
// carries its content hash; site.lock carries this identity.
const EncoderIdentity = "github.com/gen2brain/webp v0.6.4 (libwebp WASM via wazero)"

// MaxUploadBytes caps one upload: generous for photographs, a hard ceiling
// against hostile bodies (§14).
const MaxUploadBytes = 32 << 20 // 32 MiB

// variantWidths are the §10.1 responsive widths.
var variantWidths = []int{480, 800, 1200}

// Variant is one derived rendition.
type Variant struct {
	Path  string `json:"path"`  // media/derived/<base>-<w>.webp
	Width int    `json:"width"` // pixels
}

// Result names everything one ingest produced, in canonical media/ form.
type Result struct {
	Original string    `json:"original"` // media/originals/<base>.<ext>
	Variants []Variant `json:"variants"`
	OgJPEG   string    `json:"ogJpeg"` // media/derived/<base>-og.jpg
}

// sniff identifies an upload by magic bytes (§10.1, §14) — the filename's
// extension is advisory at best, hostile at worst.
func sniff(b []byte) (format string, err error) {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpeg", nil
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png", nil
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "webp", nil
	}
	return "", fmt.Errorf("unsupported upload: not a JPEG, PNG or WebP (magic bytes)")
}

// ValidUpload reports whether b passes the magic-byte gate — the cheap check
// an upload handler runs before spending a queue slot.
func ValidUpload(b []byte) error {
	_, err := sniff(b)
	return err
}

// Ingest runs the full pipeline on one upload and writes its files under
// <siteDir>/media. progress, when non-nil, is called after each stage with a
// short human-readable label — the SSE feed (§10.1).
func Ingest(siteDir, filename string, r io.Reader, progress func(stage string)) (*Result, error) {
	report := func(stage string) {
		if progress != nil {
			progress(stage)
		}
	}

	raw, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(raw)) > MaxUploadBytes {
		return nil, fmt.Errorf("upload exceeds %d MiB", MaxUploadBytes>>20)
	}
	format, err := sniff(raw)
	if err != nil {
		return nil, err
	}

	// Decode, then bake the EXIF orientation into the pixels (JPEG only —
	// that is where cameras put it). From here on the orientation IS the
	// pixel data; no consumer ever needs the tag again.
	img, err := decode(raw, format)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", format, err)
	}
	img = bakeOrientation(img, raw, format)
	report("décodée")

	base := slugBase(filename)
	res := &Result{}

	// The stored original: re-encoded (metadata purged, polyglots dead), same
	// family as the source so nothing is lossy-recompressed twice needlessly.
	origExt := map[string]string{"jpeg": "jpg", "png": "png", "webp": "webp"}[format]
	origRel := filepath.ToSlash(filepath.Join("media", "originals", base+"."+origExt))
	origBytes, err := encodeOriginal(img, format)
	if err != nil {
		return nil, err
	}
	base, origRel, err = writeUnique(siteDir, origRel, base, origBytes)
	if err != nil {
		return nil, err
	}
	res.Original = origRel
	report("original purgé")

	// Responsive WebP variants (§10.1): lossy for photos, lossless for
	// graphics, alpha preserved either way; never upscaled.
	lossless := format == "png"
	for _, w := range variantWidths {
		if img.Bounds().Dx() < w {
			continue
		}
		resized := imaging.Resize(img, w, 0, imaging.Lanczos)
		var buf bytes.Buffer
		if err := webp.Encode(&buf, resized, webp.Options{Quality: 82, Lossless: lossless, Method: 4}); err != nil {
			return nil, fmt.Errorf("encode webp %dw: %w", w, err)
		}
		rel := filepath.ToSlash(filepath.Join("media", "derived", fmt.Sprintf("%s-%d.webp", base, w)))
		if err := writeFile(filepath.Join(siteDir, filepath.FromSlash(rel)), buf.Bytes()); err != nil {
			return nil, err
		}
		res.Variants = append(res.Variants, Variant{Path: rel, Width: w})
		report(fmt.Sprintf("variante %d px", w))
	}

	// The og:image JPEG (§10.1): one pragmatic exception for social scrapers.
	og := img
	if og.Bounds().Dx() > 1200 {
		og = imaging.Resize(og, 1200, 0, imaging.Lanczos)
	}
	var ogBuf bytes.Buffer
	if err := jpeg.Encode(&ogBuf, og, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode og jpeg: %w", err)
	}
	ogRel := filepath.ToSlash(filepath.Join("media", "derived", base+"-og.jpg"))
	if err := writeFile(filepath.Join(siteDir, filepath.FromSlash(ogRel)), ogBuf.Bytes()); err != nil {
		return nil, err
	}
	res.OgJPEG = ogRel
	report("og:image prête")

	return res, nil
}

func decode(raw []byte, format string) (image.Image, error) {
	switch format {
	case "jpeg":
		return jpeg.Decode(bytes.NewReader(raw))
	case "png":
		return png.Decode(bytes.NewReader(raw))
	case "webp":
		return webp.Decode(bytes.NewReader(raw))
	}
	return nil, fmt.Errorf("unreachable format %q", format)
}

// bakeOrientation applies the EXIF orientation tag as a pixel transform. A
// missing or unreadable tag means "already upright" — never an error.
func bakeOrientation(img image.Image, raw []byte, format string) image.Image {
	if format != "jpeg" {
		return img
	}
	x, err := exif.Decode(bytes.NewReader(raw))
	if err != nil {
		return img
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return img
	}
	o, err := tag.Int(0)
	if err != nil {
		return img
	}
	switch o {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	}
	return img
}

func encodeOriginal(img image.Image, format string) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92})
	case "png":
		err = png.Encode(&buf, img)
	case "webp":
		err = webp.Encode(&buf, img, webp.Options{Quality: 90, Method: 4})
	}
	if err != nil {
		return nil, fmt.Errorf("re-encode original: %w", err)
	}
	return buf.Bytes(), nil
}

// SlugBase is exported for callers (the SVG path) that need the same basename
// discipline without running the raster pipeline.
func SlugBase(filename string) string { return slugBase(filename) }

// slugBase reduces an upload's filename to a safe media basename: lowercase
// letters, digits and dashes — the same shape the §14 identifier discipline
// likes, and a URL that never needs escaping.
func slugBase(filename string) string {
	name := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) && r < 128, unicode.IsDigit(r) && r < 128:
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "image"
	}
	return out
}

// writeUnique writes the original, suffixing the basename if a different file
// already owns it — an upload never silently overwrites another's media.
func writeUnique(siteDir, rel, base string, b []byte) (finalBase, finalRel string, err error) {
	abs := filepath.Join(siteDir, filepath.FromSlash(rel))
	if existing, err := os.ReadFile(abs); err == nil {
		if bytes.Equal(existing, b) {
			return base, rel, nil // same bytes: idempotent re-upload
		}
		for i := 2; ; i++ {
			cand := fmt.Sprintf("%s-%d", base, i)
			candRel := strings.Replace(rel, base, cand, 1)
			if _, err := os.Stat(filepath.Join(siteDir, filepath.FromSlash(candRel))); os.IsNotExist(err) {
				base, rel, abs = cand, candRel, filepath.Join(siteDir, filepath.FromSlash(candRel))
				break
			}
		}
	}
	if err := writeFile(abs, b); err != nil {
		return "", "", err
	}
	return base, rel, nil
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pal-media-*.tmp")
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
