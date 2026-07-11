package build

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"palimpseste/internal/materialize"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// cacheFormatVersion is folded into every page key. Bump it whenever a change to
// materialization, SEO or serialisation alters a page's output for unchanged
// inputs — otherwise an on-disk entry written by the old code would be served
// verbatim by the new code (a stale page). It pairs with Options.Version, the
// binary identity recorded in site.lock (§7): either one changing invalidates
// every entry. The byte-exact golden test builds with the cache off, so any such
// output change surfaces there and is the cue to bump this constant.
const cacheFormatVersion = "2"  // bumped: HTML minify pass (§7) changes page bytes

// renderCache memoises fully rendered page bytes keyed by the content hash of a
// page's input tuple (§7: "mémoïsation par content-addressing"). It is an
// optimisation only — a hit is byte-for-byte what a fresh materialization would
// produce, because pageKey covers every input the pure page function reads, so
// enabling the cache can only change build speed, never build output.
//
// Each entry is a single file named by its hex key holding the rendered bytes.
// The cache is best-effort: any I/O error degrades to a miss (or a skipped
// store), so a broken or read-only cache dir slows a build but never fails it.
type renderCache struct {
	dir string
}

// openCache returns a cache rooted at dir, or nil (a disabled cache, always a
// miss) when dir is empty. A nil *renderCache is a valid receiver.
func openCache(dir string) *renderCache {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	return &renderCache{dir: dir}
}

// get returns the cached bytes for key and whether they were found. It is safe
// for concurrent use.
func (c *renderCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(c.dir, key))
	if err != nil {
		return nil, false
	}
	return b, true
}

// put stores b under key, writing atomically so a concurrent get never observes
// a half-written entry. Failures are swallowed: the cache must never break a
// build.
func (c *renderCache) put(key string, b []byte) {
	if c == nil {
		return
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(c.dir, ".pal-cache-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless no-op once the rename succeeds
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, filepath.Join(c.dir, key))
}

// pageKey computes a page's content-address: a sha256 over every input that can
// change its rendered bytes (§7's "tuple d'entrées"). Being over-inclusive is
// safe — a redundant rebuild — but missing an input is not, so the key folds in:
//
//   - the cache-format and binary versions (code identity);
//   - the stylesheet href, which threads the global CSS hash into every page
//     and so realises the "_global:* → toutes" dependency edge;
//   - the theme identity and design tokens;
//   - the template bytes;
//   - the whole site record (a superset of the fields seo.Apply reads, so no
//     SEO input can be silently omitted) and the whole page record (route,
//     title, description, slot overrides);
//   - every resolved fragment, name and body, in template order.
//
// Components are length-framed before hashing so no two distinct tuples can
// serialise to the same byte stream.
func pageKey(version, styleHref, template string, t *theme.Theme, s *site.Site, p site.Page, frags []materialize.SlotFragment, tableMaterial []string) string {
	h := sha256.New()
	writeChunk := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	writeStr := func(s string) { writeChunk([]byte(s)) }

	writeStr(cacheFormatVersion)
	writeStr(version)
	writeStr(styleHref)
	writeStr(t.Name)
	writeStr(t.Version)
	writeStr(template)

	// json.Marshal sorts map keys, so tokens/slots hash deterministically.
	tokens, _ := json.Marshal(t.Tokens)
	writeChunk(tokens)
	siteJSON, _ := json.Marshal(s)
	writeChunk(siteJSON)
	pageJSON, _ := json.Marshal(p)
	writeChunk(pageJSON)

	for _, f := range frags {
		writeStr(f.Name)
		if f.Found {
			writeStr("1")
			writeStr(f.Body)
		} else {
			writeStr("0")
		}
	}
	// §7's table → page edges: the referenced tables' current bytes, so a data
	// edit invalidates exactly the pages that render it. The theme's declared
	// schemas ride along so a schema change re-renders too.
	for _, chunk := range tableMaterial {
		writeStr(chunk)
	}
	dataJSON, _ := json.Marshal(t.Data)
	writeChunk(dataJSON)
	return hex.EncodeToString(h.Sum(nil))
}
