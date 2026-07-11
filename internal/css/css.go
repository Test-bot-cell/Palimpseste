// Package css bundles a theme's ordered stylesheet list into one minified,
// content-addressed file using esbuild in-process (no Node toolchain).
//
// The output name embeds a hash of the bytes (style.<hash>.css) so it is both
// immutable-cacheable and part of the build's attestation: identical input CSS
// always yields the same filename, which is what makes deploys diffable.
package css

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"palimpseste/internal/theme"
)

// Bundle is the result of compiling a theme's styles. A theme with no styles
// yields a zero Bundle (empty Filename, nil Contents).
type Bundle struct {
	Filename string
	Contents []byte
}

// Empty reports whether the bundle carries no CSS.
func (b Bundle) Empty() bool { return len(b.Contents) == 0 }

// assetExternals keeps url() references to fonts and images untouched so
// bundling never fails on an unresolved asset. Media pipelines (M3) own these.
var assetExternals = []string{
	"*.woff", "*.woff2", "*.ttf", "*.otf", "*.eot",
	"*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg", "*.webp", "*.avif",
}

// BuildTheme concatenates the theme's stylesheets in declared order (via
// synthetic @import entry), bundles and minifies them, and returns the result.
func BuildTheme(t *theme.Theme) (Bundle, error) {
	if len(t.Styles) == 0 {
		return Bundle{}, nil
	}

	var entry strings.Builder
	for _, s := range t.Styles {
		fmt.Fprintf(&entry, "@import %q;\n", "./"+filepath.ToSlash(s))
	}

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry.String(),
			ResolveDir: t.Dir(),
			Sourcefile: "palimpseste-entry.css",
			Loader:     api.LoaderCSS,
		},
		Bundle:            true,
		MinifyWhitespace:  true,
		MinifySyntax:      true,
		MinifyIdentifiers: false,
		LegalComments:     api.LegalCommentsNone,
		Write:             false,
		External:          assetExternals,
		Target:            api.ESNext,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{})
		return Bundle{}, fmt.Errorf("css bundle failed:\n%s", strings.Join(msgs, "\n"))
	}
	if len(result.OutputFiles) == 0 {
		return Bundle{}, nil
	}

	contents := result.OutputFiles[0].Contents
	sum := sha256.Sum256(contents)
	name := fmt.Sprintf("style.%s.css", hex.EncodeToString(sum[:])[:16])
	return Bundle{Filename: name, Contents: contents}, nil
}
