// Package editserver runs the ephemeral, localhost-only editor behind
// `palimpseste edit`. It serves each page materialized exactly as production
// would but with the data-slot markers kept and an editor overlay injected, so
// the operator edits prose in place. Saves flow back through the content
// contract — sanitize, then WriteFragment — and connected browsers live-reload
// over SSE when any watched file changes.
//
// The server never appears in a production build: its assets are embedded, it
// binds only to a loopback address, and every write is gated by an unguessable
// per-session CSRF token plus an Origin check.
package editserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"palimpseste/internal/blocks"
	"palimpseste/internal/build"
	"palimpseste/internal/content"
	"palimpseste/internal/css"
	"palimpseste/internal/history"
	"palimpseste/internal/sanitize"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// DefaultAddr is the loopback address the editor binds to when none is given.
const DefaultAddr = "127.0.0.1:7777"

// maxFragmentBytes caps a single fragment PUT: generous for prose, a hard
// ceiling against a runaway or hostile body.
const maxFragmentBytes = 4 << 20 // 4 MiB

// Identifier discipline (§14): page and slot ids are validated by strict regex
// BEFORE any path work, with the loader's confined resolution as the second,
// independent line of defense. The token shape — alnum runs joined by single
// . _ or - — makes ".." unspellable; slots may carry the "_global:" prefix.
var (
	pageIDRE = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)
	slotIDRE = regexp.MustCompile(`^(?:_global:)?[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)
)

// Options configure an edit server.
type Options struct {
	SiteDir string // directory holding site.json, content/, themes/
	Addr    string // loopback bind address; defaults to DefaultAddr
	Version string // binary version (reserved for future surfacing)
}

// Server is the running editor. Its mutable view of the site lives in an
// immutable snapshot swapped under mu on reload, so request handlers read a
// coherent picture without holding a lock across a render.
type Server struct {
	opts Options
	csrf string
	hub  *hub
	hist *history.Recorder
	mux  *http.ServeMux

	mu  sync.RWMutex
	cur *snapshot
}

// snapshot is the coherent, immutable set a handler needs to serve a request.
// reload builds a fresh one and atomically swaps the pointer; existing readers
// keep using the old one until they release it.
type snapshot struct {
	site    *site.Site
	theme   *theme.Theme
	ldr     *content.Loader
	css     css.Bundle
	byRoute map[string]site.Page
	byID    map[string]site.Page
	pages   []pageEntry
}

// New builds an edit server for a site directory. It loads the site once up
// front (so obvious errors surface before the browser opens) and refuses any
// non-loopback bind address.
func New(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	if err := requireLoopbackAddr(opts.Addr); err != nil {
		return nil, err
	}
	tok, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	s := &Server{opts: opts, csrf: tok, hub: newHub()}
	// Transpile the overlay now so a TypeScript error surfaces at startup, not
	// as a broken editor in the browser.
	if _, err := overlayJS(); err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	// Versioning is best-effort: a site outside a git tree edits fine, just
	// without commit-on-save. The watcher itself is created later, in Serve,
	// since there is nothing to watch until the server is actually running.
	hist, err := history.Open(opts.SiteDir)
	if err != nil {
		return nil, err
	}
	s.hist = hist
	s.routes()
	return s, nil
}

// Addr is the address the server binds to; useful for printing the editor URL.
func (s *Server) Addr() string { return s.opts.Addr }

// reload re-reads site.json, the theme and the CSS bundle, then swaps in a fresh
// snapshot. Fragments themselves are read straight from disk per request, so a
// prose edit needs no reload — only structural inputs do.
func (s *Server) reload() error {
	st, err := site.Load(s.opts.SiteDir)
	if err != nil {
		return err
	}
	t, err := theme.Load(s.opts.SiteDir, st.Theme)
	if err != nil {
		return err
	}
	bundle, err := css.BuildTheme(t)
	if err != nil {
		return err
	}
	byRoute := make(map[string]site.Page, len(st.Pages))
	byID := make(map[string]site.Page, len(st.Pages))
	for _, p := range st.Pages {
		byRoute[p.Route] = p
		byID[p.ID] = p
	}
	pages := make([]pageEntry, 0, len(st.Pages))
	for _, p := range st.SortedPages() {
		pages = append(pages, pageEntry{ID: p.ID, Route: p.Route, Title: p.Title})
	}
	snap := &snapshot{
		site:    st,
		theme:   t,
		ldr:     content.NewLoader(s.opts.SiteDir),
		css:     bundle,
		byRoute: byRoute,
		byID:    byID,
		pages:   pages,
	}
	s.mu.Lock()
	s.cur = snap
	s.mu.Unlock()
	return nil
}

// current returns the live snapshot pointer under a read lock.
func (s *Server) current() *snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// watchRoots are the trees a running editor watches for live-reload (§8): the
// content fragments and the active theme. site.json is deliberately excluded —
// it is structural (it defines pages, routes and slot wiring that reload builds
// its maps from), so a change there warrants restarting the editor rather than a
// hot reload against a shifted page set.
func (s *Server) watchRoots() []string {
	snap := s.current()
	return []string{
		snap.ldr.Root(),
		snap.theme.Dir(),
	}
}

// --- routing -----------------------------------------------------------------

func (s *Server) routes() {
	mux := http.NewServeMux()
	// Overlay assets stay under /_pal/ — an editor-only namespace that never
	// collides with a site route. The editing API lives under /api/ exactly as
	// the design specifies (§8), addressing a fragment by page and slot.
	mux.HandleFunc("GET "+overlayJSPath, s.handleOverlayJS)
	mux.HandleFunc("GET "+themeCSSPath, s.handleThemeCSS)
	mux.HandleFunc("GET /api/pages", s.handlePages)
	mux.HandleFunc("GET /api/fragments/{page}/{slot}", s.handleFragmentGet)
	mux.HandleFunc("PUT /api/fragments/{page}/{slot}", s.handleFragmentPut)
	mux.HandleFunc("PUT /api/pages/{page}/meta", s.handleMetaPut)
	mux.HandleFunc("GET /api/theme", s.handleThemeGet)
	mux.HandleFunc("PUT /api/theme/tokens", s.handleTokensPut)
	mux.HandleFunc("GET /api/theme/check", s.handleThemeCheck)
	mux.HandleFunc("POST /api/theme/apply", s.handleThemeApply)
	mux.HandleFunc("GET /api/themes", s.handleThemesList)
	mux.HandleFunc("GET /api/check", s.handleCheck)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /media/", s.handleMedia)
	mux.HandleFunc("/", s.handlePage)
	s.mux = mux
}

// ServeHTTP adds hardening headers common to every response, then dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The local editor should never be framed by another origin.
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	s.mux.ServeHTTP(w, r)
}

// --- handlers ----------------------------------------------------------------

// handlePage serves any site route as an editable page. Unknown paths 404.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.current()
	route := normalizeRoute(r.URL.Path)
	p, ok := snap.byRoute[route]
	if !ok {
		http.NotFound(w, r)
		return
	}
	slots := make(map[string]slotDecl, len(snap.theme.Slots))
	for name, sl := range snap.theme.Slots {
		slots[name] = slotDecl{Type: string(sl.Type), Blocks: sl.Blocks}
	}
	cfg := overlayConfig{
		Page:   p.ID,
		CSRF:   s.csrf,
		Pages:  snap.pages,
		Slots:  slots,
		Blocks: blocks.Schema(),
		Meta:   pageMeta{Title: p.Title, Description: p.Description, OgImage: p.OgImage},
	}
	out, err := renderEditPage(snap.theme, snap.ldr, snap.site, p, !snap.css.Empty(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	io.WriteString(w, out)
}

// handleOverlayJS serves the overlay module: TypeScript source embedded in the
// binary, transpiled in-process at startup (§9, §17 — no Node, no framework).
func (s *Server) handleOverlayJS(w http.ResponseWriter, r *http.Request) {
	body, err := overlayJS()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// handleMedia serves the site's media/ tree so fragments referencing
// media/<path> (§4) render in the editor exactly as they will in production.
// http.FileServer cleans the path, so resolution stays confined (§14);
// directory listings are refused — media/ is content, not a browsable index.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	root := filepath.Join(s.opts.SiteDir, "media")
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix("/media/", http.FileServer(http.Dir(root))).ServeHTTP(w, r)
}

// handleThemeCSS serves the live theme bundle (rebuilt on reload, uncached so
// edits show immediately). A theme with no styles yields a 404.
func (s *Server) handleThemeCSS(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	if snap.css.Empty() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(snap.css.Contents)
}

// handlePages returns the page list as JSON (the switcher reads the same data
// from the injected config; this endpoint exists for tooling and tests).
func (s *Server) handlePages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.current().pages)
}

// handleFragmentGet is the read half of the round-trip: it returns the fragment
// backing a page's slot as raw HTML. A slot with no fragment yet is not an error
// — it yields an empty body, so the overlay can address any slot uniformly.
func (s *Server) handleFragmentGet(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	p, slot, ok := s.fragmentTarget(w, r, snap)
	if !ok {
		return
	}
	html, _, err := snap.ldr.Fragment(p.ID, slot, p.Slots)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, html)
}

// fragmentTarget resolves and validates the {page}/{slot} pair of a fragment
// request: strict regex first (§14), then existence. It writes the error
// response itself and reports ok=false when the request must not proceed.
func (s *Server) fragmentTarget(w http.ResponseWriter, r *http.Request, snap *snapshot) (site.Page, string, bool) {
	pageID := r.PathValue("page")
	slot := r.PathValue("slot")
	if !pageIDRE.MatchString(pageID) {
		http.Error(w, "invalid page identifier", http.StatusBadRequest)
		return site.Page{}, "", false
	}
	if !slotIDRE.MatchString(slot) {
		http.Error(w, "invalid slot identifier", http.StatusBadRequest)
		return site.Page{}, "", false
	}
	p, ok := snap.byID[pageID]
	if !ok {
		http.Error(w, "unknown page", http.StatusNotFound)
		return site.Page{}, "", false
	}
	return p, slot, true
}

// handleFragmentPut is the write half of the round-trip, the full §8 chain:
// authorize → sanitize → write → commit (§13) → incremental build. The request
// body is the raw edited HTML; the response is the canonical, sanitized HTML
// now on disk, so the overlay reflects precisely what was stored. The slot's
// declared type picks its micro-contract: plain slots hold one line of bare
// text (§5.1), everything else the full §4 vocabulary.
func (s *Server) handleFragmentPut(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	snap := s.current()
	p, slot, ok := s.fragmentTarget(w, r, snap)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFragmentBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}

	// The slot's declared type picks its micro-contract (§5.1): plain is bare
	// single-line text, stack is a pile of blocks without free prose, and any
	// declared block list narrows the catalogue for this slot.
	decl := snap.theme.Slots[slot]
	var safe string
	switch decl.Type {
	case theme.SlotPlain:
		safe = sanitize.Plain(string(raw))
	case theme.SlotStack:
		safe = sanitize.FragmentForSlot(string(raw), sanitize.SlotPolicy{Stack: true, AllowedBlocks: decl.Blocks})
	default:
		safe = sanitize.FragmentForSlot(string(raw), sanitize.SlotPolicy{AllowedBlocks: decl.Blocks})
	}
	path, err := snap.ldr.WriteFragment(p.ID, slot, p.Slots, safe)
	if err != nil {
		// The traversal guard in WriteFragment lands here; keep the site intact.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.record(path, fmt.Sprintf("edit(%s/%s)", p.ID, slot))
	s.rebuild()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, safe)
}

// record commits a just-written fragment, best-effort. A commit failure (a
// locked index, a detached state) is logged and surfaced on the event stream —
// the fragment is already safely on disk, and losing its revision is not worth
// failing the save, but the operator deserves to know it happened.
func (s *Server) record(absPath, message string) {
	if err := s.hist.Commit(absPath, message); err != nil {
		log.Printf("palimpseste edit: commit skipped: %v", err)
		s.hub.broadcast(event{Name: "error", Data: "commit: " + err.Error()})
	}
}

// rebuild closes the §8 save chain: the site is re-materialized incrementally
// (memoised, §7) into the §3 publish layout, and the outcome is streamed to
// every connected editor. A build failure never fails the save — the fragment
// and its commit are already durable — but it is reported, not swallowed.
func (s *Server) rebuild() {
	start := time.Now()
	res, err := build.Run(build.Options{
		SiteDir:  s.opts.SiteDir,
		Version:  s.opts.Version,
		CacheDir: filepath.Join(s.opts.SiteDir, ".palimpseste", "cache"),
	})
	if err != nil {
		log.Printf("palimpseste edit: build failed: %v", err)
		s.hub.broadcast(event{Name: "error", Data: "build: " + err.Error()})
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"pages":  res.Pages,
		"built":  res.Built,
		"cached": res.Cached,
		"ms":     time.Since(start).Milliseconds(),
	})
	s.hub.broadcast(event{Name: "build", Data: string(payload)})
}

// handleEvents is the SSE stream. It primes the connection, forwards reload
// pings from the hub, and sends periodic comments to keep the socket alive.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	io.WriteString(w, ": connected\n\n") // flips EventSource to OPEN promptly
	flusher.Flush()

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, ev.Data)
			flusher.Flush()
		case <-keepAlive.C:
			io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// --- write authorization -----------------------------------------------------

// authorizeWrite gates every mutating request. The per-session CSRF token is the
// primary guard — a cross-site page cannot read it — and the Origin check is a
// second line that also blunts DNS-rebinding attempts.
func (s *Server) authorizeWrite(w http.ResponseWriter, r *http.Request) bool {
	if !constantTimeMatch(r.Header.Get("X-Pal-CSRF"), s.csrf) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return false
	}
	return true
}

// --- lifecycle ---------------------------------------------------------------

// Run binds opts.Addr and serves until ctx is cancelled. Callers that need the
// resolved address (e.g. an ephemeral test port) should build their own
// listener and call Serve instead.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.opts.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve runs the file watcher and HTTP server on ln until ctx is cancelled, then
// shuts down gracefully. The watcher is event-driven (fsnotify), so the process
// is genuinely idle at rest — no polling ticker keeps the CPU warm (§15).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	if w, err := newWatcher(s.watchRoots()...); err != nil {
		// A watcher failure degrades live-reload but must not stop editing:
		// saves still work, the operator just refreshes by hand.
		log.Printf("palimpseste edit: live reload disabled: %v", err)
	} else {
		defer w.Close()
		go w.Watch(watchCtx, s.onFSChange)
	}

	srv := &http.Server{Handler: s}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// onFSChange handles a debounced filesystem change: reload the structural inputs
// and ping every browser. A reload failure (a file saved mid-edit into a
// transiently invalid state) is logged and skipped, keeping the last good
// snapshot live until the next change.
func (s *Server) onFSChange() {
	if err := s.reload(); err != nil {
		log.Printf("palimpseste edit: reload skipped: %v", err)
		s.hub.broadcast(event{Name: "error", Data: "reload: " + err.Error()})
		return
	}
	s.hub.broadcast(event{Name: "reload", Data: "1"})
}

// --- small helpers -----------------------------------------------------------

func normalizeRoute(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	p = strings.TrimSuffix(p, "index.html")
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

func constantTimeMatch(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch host := u.Hostname(); host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
}

// requireLoopbackAddr rejects any bind address that is not on the loopback
// interface, so the editor is never inadvertently exposed to the network.
func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("edit server refuses non-loopback address %q (bind to 127.0.0.1)", addr)
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
