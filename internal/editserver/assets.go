package editserver

import (
	"fmt"
	"strings"
	"sync"

	"embed"

	"github.com/evanw/esbuild/pkg/api"
)

// overlayFS holds the in-place editor overlay compiled into the binary, so
// `palimpseste edit` needs nothing on disk beyond the site itself, and nothing
// of it is ever emitted by a production build. The source of truth is vanilla
// TypeScript (§9, §17); it is transpiled in-process by esbuild — the library
// already linked for the CSS pass — once per process, at startup. No Node, no
// bundler, no build step for the maintainer: edit the .ts, rebuild the binary.
//
//go:embed assets/app.ts
var overlayFS embed.FS

var (
	overlayOnce sync.Once
	overlayCode []byte
	overlayErr  error
)

// overlayJS returns the overlay module as browser-ready JavaScript. The
// transpilation is deterministic (same source, same esbuild version, same
// output) and its failure is a startup error, never a silent broken editor.
func overlayJS() ([]byte, error) {
	overlayOnce.Do(func() {
		src, err := overlayFS.ReadFile("assets/app.ts")
		if err != nil {
			overlayErr = err
			return
		}
		res := api.Transform(string(src), api.TransformOptions{
			Loader:     api.LoaderTS,
			Target:     api.ES2020,
			Format:     api.FormatESModule,
			Sourcefile: "app.ts",
		})
		if len(res.Errors) > 0 {
			msgs := api.FormatMessages(res.Errors, api.FormatMessagesOptions{})
			overlayErr = fmt.Errorf("transpile app.ts:\n%s", strings.Join(msgs, "\n"))
			return
		}
		overlayCode = res.Code
	})
	return overlayCode, overlayErr
}
