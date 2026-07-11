package editserver

// POST /api/media (§8, §10.1): the upload half of the media pipeline. The
// handler does only the cheap gates — authorization, size cap, magic bytes —
// then enqueues and answers 202 immediately: encoding never blocks the editor.
// Progress, completion and failure stream to every connected browser as typed
// `media` events; the finished original is committed as media(<base>) and the
// published tree refreshed so the new files are servable at once.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"

	"palimpseste/internal/media"
)

func (s *Server) handleMediaUpload(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeWrite(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, media.MaxUploadBytes+(1<<20))
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "multipart field \"file\" required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, media.MaxUploadBytes+1))
	if err != nil {
		http.Error(w, "unreadable upload", http.StatusBadRequest)
		return
	}
	if int64(len(raw)) > media.MaxUploadBytes {
		http.Error(w, fmt.Sprintf("upload exceeds %d MiB", media.MaxUploadBytes>>20), http.StatusRequestEntityTooLarge)
		return
	}
	if err := media.ValidUpload(raw); err != nil {
		// The magic-byte gate (§10.1/§14): refused before any queue slot is spent.
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	}

	id, err := randomToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !s.mediaQ.Enqueue(media.Job{ID: id, Filename: header.Filename, Bytes: raw}) {
		http.Error(w, "media queue saturated, retry shortly", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

// onMediaEvent relays queue progress to the SSE stream and, on completion,
// commits the new original (the derived tree is a regenerable cache, §3) and
// republishes so the files are immediately servable.
func (s *Server) onMediaEvent(ev media.Event) {
	payload, _ := json.Marshal(ev)
	s.hub.broadcast(event{Name: "media", Data: string(payload)})
	if ev.Result != nil {
		abs := filepath.Join(s.opts.SiteDir, filepath.FromSlash(ev.Result.Original))
		base := filepath.Base(ev.Result.Original)
		if err := s.hist.Commit(abs, fmt.Sprintf("media(%s)", base)); err != nil {
			log.Printf("palimpseste edit: commit skipped: %v", err)
			s.hub.broadcast(event{Name: "error", Data: "commit: " + err.Error()})
		}
		s.rebuild()
	}
}
