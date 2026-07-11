package editserver

// The M2 API surface (§8): theme inspection and switching, live token editing,
// per-page SEO meta, and the on-demand lint report. Every mutation goes through
// the same wall as fragment writes — CSRF token + Origin check — and lands as
// the structured commit §13 prescribes (theme(tokens), theme(<name>),
// theme(migrate), meta(<page>)).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"palimpseste/internal/lint"
	"palimpseste/internal/materialize"
	"palimpseste/internal/sanitize"
	"palimpseste/internal/seo"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
	"palimpseste/internal/themecheck"
)

// maxJSONBody caps the structured API bodies (tokens, meta, apply): small
// documents by construction.
const maxJSONBody = 256 << 10 // 256 KiB

// --- GET /api/theme -------------------------------------------------------------

// themeInfo is the manifest subset the token panel needs: declared tokens with
// their current values, and whether live editing is possible at all.
type themeInfo struct {
	Name     string                `json:"name"`
	Version  string                `json:"version"`
	Tokens   map[string]tokenInfo  `json:"tokens"`
	Editable bool                  `json:"editable"` // theme declares a tokens.css
	Slots    map[string]theme.Slot `json:"slots"`
}

type tokenInfo struct {
	Type  string `json:"type"`
	Snap  string `json:"snap,omitempty"`
	Value string `json:"value,omitempty"`
}

func (s *Server) handleThemeGet(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	values, err := snap.theme.ReadTokenValues()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, editable := snap.theme.TokensFile()
	info := themeInfo{
		Name:     snap.theme.Name,
		Version:  snap.theme.Version,
		Tokens:   map[string]tokenInfo{},
		Editable: editable,
		Slots:    snap.theme.Slots,
	}
	for name, tok := range snap.theme.Tokens {
		info.Tokens[name] = tokenInfo{Type: tok.Type, Snap: tok.Snap, Value: values[name]}
	}
	writeJSON(w, http.StatusOK, info)
}

// --- GET /api/themes ------------------------------------------------------------

type themeEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Active  bool   `json:"active"`
	Error   string `json:"error,omitempty"` // manifest problem, shown as-is
}

func (s *Server) handleThemesList(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	entries, err := os.ReadDir(filepath.Join(s.opts.SiteDir, "themes"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []themeEntry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		t, err := theme.Load(s.opts.SiteDir, e.Name())
		if err != nil {
			out = append(out, themeEntry{Name: e.Name(), Error: err.Error()})
			continue
		}
		out = append(out, themeEntry{Name: t.Name, Version: t.Version, Active: t.Name == snap.theme.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// --- PUT /api/theme/tokens (§6) ---------------------------------------------------

func (s *Server) handleTokensPut(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	snap := s.current()

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var values map[string]string
	if err := json.NewDecoder(r.Body).Decode(&values); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	for k, v := range values {
		if err := validTokenValue(v); err != nil {
			http.Error(w, fmt.Sprintf("token %q: %v", k, err), http.StatusBadRequest)
			return
		}
	}

	path, err := snap.theme.WriteTokenValues(values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.record(path, "theme(tokens)")

	// tokens.css feeds the live bundle: refresh the snapshot now (the watcher
	// would get there too, but the response must already reflect the change),
	// then republish.
	if err := s.reload(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.rebuild()
	writeJSON(w, http.StatusOK, map[string]string{"tokens": filepath.Base(path)})
}

// validTokenValue admits one CSS declaration value: it must not be able to
// escape the declaration it will be serialised into. Type-level refinement is
// the panel's UX; containment is the server's contract.
func validTokenValue(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("empty value")
	}
	if len(v) > 256 {
		return fmt.Errorf("value too long")
	}
	if strings.ContainsAny(v, "{};<>") || strings.Contains(v, "/*") || strings.Contains(v, "*/") {
		return fmt.Errorf("value carries CSS structure characters")
	}
	return nil
}

// --- POST /api/theme/apply (§5.3) --------------------------------------------------

func (s *Server) handleThemeApply(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var req struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !pageIDRE.MatchString(req.Theme) {
		http.Error(w, "body must be {\"theme\": \"<name>\"}", http.StatusBadRequest)
		return
	}

	rep, moved, err := themecheck.Apply(s.opts.SiteDir, req.Theme)
	if rep.Blocking() {
		writeJSON(w, http.StatusConflict, rep)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// §5.3/§13: the migration renames are their own commit, the switch its own.
	if len(moved) > 0 {
		var paths []string
		for _, m := range moved {
			paths = append(paths, m.From, m.To)
		}
		if err := s.hist.CommitPaths(paths, "theme(migrate)"); err != nil {
			log.Printf("palimpseste edit: commit skipped: %v", err)
			s.hub.broadcast(event{Name: "error", Data: "commit: " + err.Error()})
		}
	}
	if err := s.hist.CommitPaths([]string{filepath.Join(s.opts.SiteDir, "site.json")},
		fmt.Sprintf("theme(%s)", req.Theme)); err != nil {
		log.Printf("palimpseste edit: commit skipped: %v", err)
		s.hub.broadcast(event{Name: "error", Data: "commit: " + err.Error()})
	}

	// site.json changed under our feet by design; the snapshot must follow
	// before anyone renders, then the published tree.
	if err := s.reload(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.rebuild()
	s.hub.broadcast(event{Name: "reload", Data: "1"})
	writeJSON(w, http.StatusOK, rep)
}

// --- GET /api/theme/check?theme=<name> — dry-run compatibility report -------------

func (s *Server) handleThemeCheck(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("theme")
	if !pageIDRE.MatchString(name) {
		http.Error(w, "query parameter theme=<name> required", http.StatusBadRequest)
		return
	}
	rep, err := themecheck.Check(s.opts.SiteDir, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// --- PUT /api/pages/{page}/meta (§9/§11 SEO panel) ---------------------------------

func (s *Server) handleMetaPut(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	pageID := r.PathValue("page")
	if !pageIDRE.MatchString(pageID) {
		http.Error(w, "invalid page identifier", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var req struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		OgImage     string `json:"ogImage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Meta fields are bare single-line text (§11: plain micro-contract), and
	// the og:image points into media/ or at an https URL — same discipline as
	// the content contract.
	req.Title = sanitize.Plain(req.Title)
	req.Description = sanitize.Plain(req.Description)
	if req.OgImage != "" {
		if canon, ok := sanitize.CanonicalMediaSrc(req.OgImage); ok {
			req.OgImage = canon
		} else if !strings.HasPrefix(req.OgImage, "https://") {
			http.Error(w, "ogImage must be a media/ path or an https URL", http.StatusBadRequest)
			return
		}
	}
	if req.Title == "" {
		http.Error(w, "title must not be empty", http.StatusBadRequest)
		return
	}

	st, err := site.Load(s.opts.SiteDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found := false
	for i := range st.Pages {
		if st.Pages[i].ID == pageID {
			st.Pages[i].Title = req.Title
			st.Pages[i].Description = req.Description
			st.Pages[i].OgImage = req.OgImage
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "unknown page", http.StatusNotFound)
		return
	}
	if err := site.Save(s.opts.SiteDir, st); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.record(filepath.Join(s.opts.SiteDir, "site.json"), fmt.Sprintf("meta(%s)", pageID))

	if err := s.reload(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.rebuild()
	writeJSON(w, http.StatusOK, req)
}

// --- GET /api/check (§11) -----------------------------------------------------------

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	start := time.Now()
	routes := lint.RouteSet(snap.site)

	var issues []lint.Issue
	for _, p := range snap.site.SortedPages() {
		doc, rep, err := materialize.Page(snap.theme, snap.ldr, p, materialize.Options{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := seo.Apply(doc, snap.site, p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		issues = append(issues, lint.CheckPage(doc, snap.site, p, rep, routes)...)
	}
	lint.Sort(issues)

	type checkIssue struct {
		Severity string `json:"severity"`
		Rule     string `json:"rule"`
		Page     string `json:"page"`
		Detail   string `json:"detail"`
	}
	out := struct {
		Issues []checkIssue `json:"issues"`
		Ms     int64        `json:"ms"`
	}{Issues: []checkIssue{}, Ms: time.Since(start).Milliseconds()}
	for _, is := range issues {
		out.Issues = append(out.Issues, checkIssue{
			Severity: is.Severity.String(),
			Rule:     is.Rule,
			Page:     is.Page,
			Detail:   is.Message,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
