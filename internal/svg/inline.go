package svg

// Shared plumbing for the logo mechanism (§10.2), used by both the build and
// the edit server so the inline behaviour is defined once.

import (
	"os"
	"path/filepath"
	"strings"

	"palimpseste/internal/theme"
)

// InlineResolver returns a materialize.Options.InlineSVG-shaped function: for a
// slot the theme declares as image+inline, whose fragment references an SVG
// under media/, it reads that SVG, sanitises it to the inline profile, and
// returns it embedded (fill="currentColor" preserved so it inherits the theme).
// Every other slot is declined, leaving its fragment untouched.
func InlineResolver(siteDir string, t *theme.Theme) func(slot, src string) (string, bool) {
	inline := map[string]bool{}
	for name, sl := range t.Slots {
		if sl.Type == theme.SlotImage && sl.Inline {
			inline[name] = true
		}
	}
	if len(inline) == 0 {
		return nil
	}
	return func(slot, src string) (string, bool) {
		if !inline[slot] || !strings.HasPrefix(src, "media/") || !strings.HasSuffix(src, ".svg") {
			return "", false
		}
		raw, err := os.ReadFile(filepath.Join(siteDir, filepath.FromSlash(src)))
		if err != nil {
			return "", false
		}
		clean, err := Sanitize(raw, ProfileInline)
		if err != nil {
			return "", false
		}
		return clean, true
	}
}

// LogoSource returns the media path of the site's inline logo, if any: the
// first inline image slot with a fragment that references an SVG. The build
// uses it to know which SVG to derive favicons from (§10.2).
func LogoSource(siteDir string, t *theme.Theme, readFragment func(slot string) (string, bool)) (string, bool) {
	for name, sl := range t.Slots {
		if sl.Type != theme.SlotImage || !sl.Inline {
			continue
		}
		frag, ok := readFragment(name)
		if !ok {
			continue
		}
		if src := firstSVGSrc(frag); src != "" {
			return src, true
		}
	}
	return "", false
}

// firstSVGSrc extracts the first media/*.svg src from a fragment, cheaply.
func firstSVGSrc(frag string) string {
	for {
		i := strings.Index(frag, `src="`)
		if i < 0 {
			return ""
		}
		frag = frag[i+5:]
		j := strings.IndexByte(frag, '"')
		if j < 0 {
			return ""
		}
		src := frag[:j]
		if strings.HasPrefix(src, "media/") && strings.HasSuffix(src, ".svg") {
			return src
		}
		frag = frag[j+1:]
	}
}
