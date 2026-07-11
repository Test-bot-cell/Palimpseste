package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gen2brain/webp"
)

// testJPEG builds a wide gradient photo in memory.
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testPNG builds a small graphic with an alpha hole.
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 600, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 600; x++ {
			a := uint8(255)
			if x < 100 {
				a = 0
			}
			img.Set(x, y, color.NRGBA{200, 40, 40, a})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// §10.1 end to end: decode → resize → encode; originals purged and re-encoded,
// WebP variants only downscaled, og-JPEG present.
func TestIngestJPEGPipeline(t *testing.T) {
	dir := t.TempDir()
	var stages []string
	res, err := Ingest(dir, "Photo de Vacances!!.JPG", bytes.NewReader(testJPEG(t, 1600, 900)),
		func(s string) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}

	if res.Original != "media/originals/photo-de-vacances.jpg" {
		t.Errorf("original = %q (slug attendu)", res.Original)
	}
	if len(res.Variants) != 3 {
		t.Fatalf("variants = %+v, want 480/800/1200", res.Variants)
	}
	for _, v := range res.Variants {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(v.Path)))
		if err != nil {
			t.Fatalf("variant %s missing: %v", v.Path, err)
		}
		img, err := webp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("variant %s is not decodable webp: %v", v.Path, err)
		}
		if img.Bounds().Dx() != v.Width {
			t.Errorf("variant %s width = %d, want %d", v.Path, img.Bounds().Dx(), v.Width)
		}
	}
	og, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(res.OgJPEG)))
	if err != nil || len(og) < 3 || og[0] != 0xFF || og[1] != 0xD8 {
		t.Errorf("og:image is not a JPEG: %v", err)
	}
	if len(stages) < 5 {
		t.Errorf("progress stages = %v, want the full feed", stages)
	}
}

// A small image is never upscaled: fewer variants, never wider than the source.
func TestIngestNeverUpscales(t *testing.T) {
	dir := t.TempDir()
	res, err := Ingest(dir, "petite.jpg", bytes.NewReader(testJPEG(t, 600, 400)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Variants) != 1 || res.Variants[0].Width != 480 {
		t.Errorf("variants = %+v, want only 480", res.Variants)
	}
}

// PNG graphics go lossless (§10.1) and keep their alpha.
func TestIngestPNGLosslessAlpha(t *testing.T) {
	dir := t.TempDir()
	res, err := Ingest(dir, "capture.png", bytes.NewReader(testPNG(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Variants) != 1 {
		t.Fatalf("variants = %+v", res.Variants)
	}
	b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(res.Variants[0].Path)))
	img, err := webp.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, a := img.At(10, 10).RGBA()
	if a != 0 {
		t.Errorf("alpha hole lost: a=%d", a)
	}
}

// §10.1/§14: metadata does not survive — the stored original is our encoder's
// output, so an EXIF block (or anything after it) from the upload is gone.
func TestIngestPurgesMetadata(t *testing.T) {
	dir := t.TempDir()
	raw := testJPEG(t, 500, 300)
	// Graft a fake EXIF-ish marker payload into the upload stream.
	tainted := append(append([]byte{}, raw[:2]...), []byte("\xFF\xE1\x00\x10Exif\x00\x00SECRET-GPS")...)
	tainted = append(tainted, raw[2:]...)
	res, err := Ingest(dir, "meta.jpg", bytes.NewReader(tainted), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(res.Original)))
	if bytes.Contains(stored, []byte("SECRET-GPS")) || bytes.Contains(stored, []byte("Exif")) {
		t.Error("metadata survived re-encoding")
	}
}

func TestValidUploadMagicBytes(t *testing.T) {
	if err := ValidUpload(testJPEG(t, 10, 10)); err != nil {
		t.Errorf("jpeg refused: %v", err)
	}
	for name, hostile := range map[string][]byte{
		"html polyglot": []byte("<html><script>alert(1)</script>"),
		"svg":           []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"),
		"empty":         {},
		"renamed elf":   {0x7F, 'E', 'L', 'F', 0, 0, 0, 0, 0, 0, 0, 0},
	} {
		if err := ValidUpload(hostile); err == nil {
			t.Errorf("%s accepted by the magic-byte gate", name)
		}
	}
}

func TestWriteUniqueNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	a, err := Ingest(dir, "logo.jpg", bytes.NewReader(testJPEG(t, 500, 300)), nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(a.Original)))

	// Different content, same name → suffixed, first file untouched.
	b, err := Ingest(dir, "logo.jpg", bytes.NewReader(testJPEG(t, 800, 200)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Original == b.Original {
		t.Errorf("second upload overwrote the first: %s", a.Original)
	}
	if !strings.Contains(b.Original, "logo-2") {
		t.Errorf("expected suffixed base, got %s", b.Original)
	}
	after, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(a.Original)))
	if !bytes.Equal(before, after) {
		t.Error("first upload's bytes changed")
	}

	// Same content, same name → idempotent, same path, no suffix.
	c, err := Ingest(dir, "logo.jpg", bytes.NewReader(testJPEG(t, 500, 300)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Original != a.Original {
		t.Errorf("idempotent re-upload got %s, want %s", c.Original, a.Original)
	}
}

// The queue drains asynchronously and reports completion through the sink.
func TestQueueReportsCompletion(t *testing.T) {
	dir := t.TempDir()
	events := make(chan Event, 32)
	q := NewQueue(dir, func(e Event) { events <- e })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	if !q.Enqueue(Job{ID: "j1", Filename: "a.jpg", Bytes: testJPEG(t, 900, 600)}) {
		t.Fatal("enqueue refused")
	}
	for e := range events {
		if e.ID != "j1" {
			continue
		}
		if e.Err != "" {
			t.Fatalf("ingest failed: %s", e.Err)
		}
		if e.Result != nil {
			if e.Result.Original == "" || len(e.Result.Variants) == 0 {
				t.Errorf("incomplete result: %+v", e.Result)
			}
			return
		}
	}
}
