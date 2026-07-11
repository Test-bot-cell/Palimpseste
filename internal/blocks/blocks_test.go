package blocks

import (
	"strings"
	"testing"
)

func TestCatalogueShape(t *testing.T) {
	// The V1 catalogue is exactly §4.1: four static blocks, two computed.
	for _, name := range []string{"gallery", "columns", "cta", "embed"} {
		b, ok := Lookup(name)
		if !ok || b.Computed {
			t.Errorf("static block %q: ok=%v computed=%v", name, ok, b.Computed)
		}
	}
	for _, name := range []string{"table", "toc"} {
		b, ok := Lookup(name)
		if !ok || !b.Computed {
			t.Errorf("computed block %q: ok=%v computed=%v", name, ok, b.Computed)
		}
	}
	if _, ok := Lookup("recent"); ok {
		t.Error("recent is V2 (§4.1) and must not be in the V1 catalogue")
	}
}

func TestAllowedOn(t *testing.T) {
	cases := []struct {
		block, tag string
		want       bool
	}{
		{"gallery", "figure", true},
		{"gallery", "aside", false},
		{"table", "div", true},
		{"table", "aside", false},
		{"toc", "aside", true},
		{"nope", "div", false},
	}
	for _, c := range cases {
		if got := AllowedOn(c.block, c.tag); got != c.want {
			t.Errorf("AllowedOn(%q,%q) = %v, want %v", c.block, c.tag, got, c.want)
		}
	}
}

func TestValidParamSchemas(t *testing.T) {
	cases := []struct {
		block, param, value string
		want                bool
	}{
		// Int with bounds.
		{"columns", "count", "2", true},
		{"columns", "count", "4", true},
		{"columns", "count", "1", false},
		{"columns", "count", "5", false},
		{"columns", "count", "abc", false},
		{"toc", "depth", "3", true},
		{"toc", "depth", "9", false},
		// Enum.
		{"cta", "variant", "primary", true},
		{"cta", "variant", "subtle", true},
		{"cta", "variant", "flashy", false},
		// Name: strict identifier, traversal unspellable.
		{"table", "source", "equipe", true},
		{"table", "source", "tarifs_2026", true},
		{"table", "source", "../secrets", false},
		{"table", "source", "Equipe", false},
		{"table", "source", "", false},
		// Undeclared params are refused whatever the value.
		{"gallery", "count", "2", false},
		{"table", "evil", "x", false},
		{"unknown", "source", "equipe", false},
	}
	for _, c := range cases {
		if got := ValidParam(c.block, c.param, c.value); got != c.want {
			t.Errorf("ValidParam(%q,%q,%q) = %v, want %v", c.block, c.param, c.value, got, c.want)
		}
	}
}

func TestValidEmbedSrc(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"https://www.youtube-nocookie.com/embed/abc", true},
		{"https://player.vimeo.com/video/1", true},
		{"http://www.youtube.com/embed/abc", false}, // https only
		{"https://evil.example/x", false},
		{"https://user:pw@www.youtube.com/embed/x", false}, // no credentials
		{"//www.youtube.com/embed/x", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ValidEmbedSrc(c.src); got != c.want {
			t.Errorf("ValidEmbedSrc(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestUnionsCoverCatalogue(t *testing.T) {
	els := strings.Join(ContainerElements(), ",")
	for _, want := range []string{"div", "figure", "section", "aside"} {
		if !strings.Contains(els, want) {
			t.Errorf("ContainerElements() = %s, missing %s", els, want)
		}
	}
	attrs := strings.Join(ParamAttrs(), ",")
	for _, want := range []string{"data-count", "data-variant", "data-source", "data-depth"} {
		if !strings.Contains(attrs, want) {
			t.Errorf("ParamAttrs() = %s, missing %s", attrs, want)
		}
	}
}
