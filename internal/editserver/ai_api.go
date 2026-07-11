package editserver

// POST /api/ai/suggest (§8, §12): the assistant's only endpoint. It returns
// proposals and NEVER writes — no fragment, no meta, nothing. Whatever the
// operator accepts travels back through the ordinary write path (PUT
// /api/fragments or /api/pages/{}/meta), where the §4 sanitiser is the
// authority. So even a prompt-injected model cannot store anything the contract
// forbids. When no provider is configured the endpoint reports 404: the feature
// does not exist (§12).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/html"

	"palimpseste/internal/ai"
	"palimpseste/internal/materialize"
	"palimpseste/internal/render"
	"palimpseste/internal/sanitize"
)

var (
	errBadMedia    = errBad("image introuvable ou hors media/")
	errNoContext   = errBad("aucun contenu de page à résumer")
	errUnknownKind = errBad("type de suggestion inconnu")
)

type errBad string

func (e errBad) Error() string { return string(e) }

type suggestRequest struct {
	Kind string `json:"kind"` // alt | description | title
	Page string `json:"page"` // page id (description/title context)
	Slot string `json:"slot"` // fragment slot (unused for now; reserved)
	Src  string `json:"src"`  // media src for alt (image kind)
	Text string `json:"text"` // optional caller-supplied context
}

func (s *Server) handleAISuggest(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	if s.ai == nil {
		http.Error(w, "assistant IA non configuré", http.StatusNotFound)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var req suggestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	aiReq, err := s.buildAIRequest(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	proposals, err := s.ai.Suggest(ctx, aiReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// The response carries proposals only — the client renders them; a human
	// gesture is what later stores one, through the sanitiser.
	writeJSON(w, http.StatusOK, map[string]any{"kind": req.Kind, "proposals": proposals})
}

// buildAIRequest assembles the model input from the site's own content — clean
// fragments for text kinds, the stored image bytes for alt — so the assistant
// grounds on what is really there.
func (s *Server) buildAIRequest(req suggestRequest) (ai.Request, error) {
	switch ai.Kind(req.Kind) {
	case ai.KindAlt:
		src := req.Src
		canon, ok := sanitize.CanonicalMediaSrc(src)
		if !ok {
			return ai.Request{}, errBadMedia
		}
		data, err := os.ReadFile(filepath.Join(s.opts.SiteDir, filepath.FromSlash(canon)))
		if err != nil {
			return ai.Request{}, errBadMedia
		}
		return ai.Request{Kind: ai.KindAlt, ImageData: data, ImageMIME: mimeOf(canon)}, nil
	case ai.KindDescription, ai.KindTitle:
		text := strings.TrimSpace(req.Text)
		if text == "" {
			text = s.pageProse(req.Page)
		}
		if text == "" {
			return ai.Request{}, errNoContext
		}
		return ai.Request{Kind: ai.Kind(req.Kind), Text: text}, nil
	default:
		return ai.Request{}, errUnknownKind
	}
}

// pageProse renders a page and extracts its visible text — the ideal LLM input
// (§12: "input LLM idéal : HTML propre"). Never the raw fragments with markup;
// the plain prose.
func (s *Server) pageProse(pageID string) string {
	snap := s.current()
	p, ok := snap.byID[pageID]
	if !ok {
		return ""
	}
	doc, _, err := materialize.Page(snap.theme, snap.ldr, p, materialize.Options{
		Tables:   liveTables(s.opts.SiteDir, snap.theme),
		Variants: liveVariants(s.opts.SiteDir),
	})
	if err != nil {
		return ""
	}
	body := render.Body(doc)
	if body == nil {
		return ""
	}
	return strings.Join(strings.Fields(nodeVisibleText(body)), " ")
}

func nodeVisibleText(n *html.Node) string {
	var b strings.Builder
	render.Walk(n, func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
	})
	return b.String()
}

func mimeOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/webp"
	}
}
