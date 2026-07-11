package editserver

// History and revert (§8, §13): a view over git log and a restore. The history
// UI is nothing but a view on the commits touching a fragment; a revert reads
// the fragment's bytes at a past commit and re-writes them through the ORDINARY
// path — sanitise, store, commit, rebuild — so a revert is an ordinary edit,
// never a privileged rewrite. Both are disabled gracefully when the site is not
// under git.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"palimpseste/internal/history"
	"palimpseste/internal/sanitize"
	"palimpseste/internal/theme"
)

// hashRE pins a commit hash argument to hex (§14): validated before any git use.
var hashRE = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// GET /api/history/{page}/{slot} — the revisions of one fragment, newest first.
func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	p, slot, ok := s.fragmentTarget(w, r, snap)
	if !ok {
		return
	}
	// Resolve the fragment's path without writing it (a read-only locate).
	path, err := snap.ldr.FragmentPath(p.ID, slot, p.Slots)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	revs, err := s.hist.Log(path, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if revs == nil {
		revs = []history.Revision{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   s.hist.Enabled(),
		"revisions": revs,
	})
}

// POST /api/revert/{page}/{slot} with {"hash": "..."} — restore the fragment to
// that commit, through the ordinary write path.
func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	snap := s.current()
	p, slot, ok := s.fragmentTarget(w, r, snap)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var req struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !hashRE.MatchString(req.Hash) {
		http.Error(w, "body must be {\"hash\": \"<commit>\"}", http.StatusBadRequest)
		return
	}
	if !s.hist.Enabled() {
		http.Error(w, "site is not under git; no history to revert to", http.StatusBadRequest)
		return
	}

	path, err := snap.ldr.FragmentPath(p.ID, slot, p.Slots)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	past, err := s.hist.FileAt(path, req.Hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Re-sanitise the restored bytes under the slot's own contract — a revert is
	// an ordinary edit, held to today's rules, not a trusted rewrite of history.
	decl := snap.theme.Slots[slot]
	var safe string
	switch decl.Type {
	case theme.SlotPlain:
		safe = sanitize.Plain(string(past))
	case theme.SlotStack:
		safe = sanitize.FragmentForSlot(string(past), sanitize.SlotPolicy{Stack: true, AllowedBlocks: decl.Blocks})
	default:
		safe = sanitize.FragmentForSlot(string(past), sanitize.SlotPolicy{AllowedBlocks: decl.Blocks})
	}
	written, err := snap.ldr.WriteFragment(p.ID, slot, p.Slots, safe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.record(written, fmt.Sprintf("revert(%s/%s @%s)", p.ID, slot, req.Hash[:7]))
	s.rebuild()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(safe))
}
