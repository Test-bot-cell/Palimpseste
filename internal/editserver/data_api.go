package editserver

// The data/ API (§8): the grid micro-editor's read and write halves. Writes
// run the full §3.3 gate — schema validation against the active theme, then
// atomic canonical CSV — and land as the data(<table>) commit §13 prescribes,
// followed by the incremental rebuild so every `table` block refreshes.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"palimpseste/internal/data"
)

// dataPayload is the grid editor's wire shape, both directions.
type dataPayload struct {
	Name   string            `json:"name"`
	Header []string          `json:"header"`
	Rows   [][]string        `json:"rows"`
	Schema map[string]string `json:"schema"` // column → type, from the theme
	Found  bool              `json:"found"`  // false: table declared but no file yet
}

// tableTarget validates the {table} identifier and resolves its schema from
// the active theme — the declaration is the gate (§3.3): undeclared tables do
// not exist for the API.
func (s *Server) tableTarget(w http.ResponseWriter, r *http.Request) (string, map[string]string, bool) {
	name := r.PathValue("table")
	if !data.ValidName(name) {
		http.Error(w, "invalid table identifier", http.StatusBadRequest)
		return "", nil, false
	}
	schema, declared := s.current().theme.Data[name]
	if !declared {
		http.Error(w, "table not declared by the active theme", http.StatusNotFound)
		return "", nil, false
	}
	return name, schema.Columns, true
}

func (s *Server) handleDataGet(w http.ResponseWriter, r *http.Request) {
	name, cols, ok := s.tableTarget(w, r)
	if !ok {
		return
	}
	t, found, err := data.Load(s.opts.SiteDir, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := dataPayload{Name: name, Schema: cols, Found: found}
	if found {
		out.Header, out.Rows = t.Header, t.Rows
	}
	if out.Rows == nil {
		out.Rows = [][]string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDataPut(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	name, _, ok := s.tableTarget(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var in dataPayload
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	t := &data.Table{Name: name, Header: in.Header, Rows: in.Rows}
	path, err := data.Save(s.opts.SiteDir, t, s.current().theme.Data[name])
	if err != nil {
		// The schema gate (§3.3): the grid never stores what the contract refuses.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.record(path, fmt.Sprintf("data(%s)", name))
	s.rebuild()

	out := dataPayload{Name: name, Header: t.Header, Rows: t.Rows, Schema: s.current().theme.Data[name].Columns, Found: true}
	writeJSON(w, http.StatusOK, out)
}
