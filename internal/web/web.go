// Package web serves the REST API and the static web UI used to browse
// messages caught by the SMTP server.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	"imapsmtpserver/internal/store"
)

//go:embed static
var staticFS embed.FS

type handler struct {
	store *store.Store
}

type messageSummary struct {
	ID             string   `json:"id"`
	From           string   `json:"from"`
	To             []string `json:"to"`
	Subject        string   `json:"subject"`
	Date           string   `json:"date"`
	Size           int      `json:"size"`
	HasAttachments bool     `json:"hasAttachments"`
	Seen           bool     `json:"seen"`
}

type attachmentInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
}

type messageDetail struct {
	ID          string           `json:"id"`
	From        string           `json:"from"`
	To          []string         `json:"to"`
	Subject     string           `json:"subject"`
	Date        string           `json:"date"`
	Size        int              `json:"size"`
	Text        string           `json:"text"`
	HTML        string           `json:"html"`
	Attachments []attachmentInfo `json:"attachments"`
}

func summarize(m *store.Message) messageSummary {
	return messageSummary{
		ID:             m.ID,
		From:           m.From,
		To:             m.To,
		Subject:        m.Subject,
		Date:           m.Date.Format("2006-01-02T15:04:05Z07:00"),
		Size:           m.Size,
		HasAttachments: len(m.Attachments) > 0,
		Seen:           m.Seen,
	}
}

func detail(m *store.Message) messageDetail {
	attachments := make([]attachmentInfo, len(m.Attachments))
	for i, a := range m.Attachments {
		attachments[i] = attachmentInfo{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        len(a.Data),
		}
	}
	return messageDetail{
		ID:          m.ID,
		From:        m.From,
		To:          m.To,
		Subject:     m.Subject,
		Date:        m.Date.Format("2006-01-02T15:04:05Z07:00"),
		Size:        m.Size,
		Text:        m.Text,
		HTML:        m.HTML,
		Attachments: attachments,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (h *handler) listMessages(w http.ResponseWriter, r *http.Request) {
	messages := h.store.List()
	out := make([]messageSummary, len(messages))
	for i, m := range messages {
		out[i] = summarize(m)
	}
	writeJSON(w, out)
}

func (h *handler) getMessage(w http.ResponseWriter, r *http.Request) {
	m := h.store.Get(r.PathValue("id"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	h.store.SetSeen(m.ID, true)
	writeJSON(w, detail(m))
}

func (h *handler) getRaw(w http.ResponseWriter, r *http.Request) {
	m := h.store.Get(r.PathValue("id"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(m.Raw)
}

func (h *handler) getAttachment(w http.ResponseWriter, r *http.Request) {
	m := h.store.Get(r.PathValue("id"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	filename := r.PathValue("filename")
	for _, a := range m.Attachments {
		if a.Filename == filename {
			w.Header().Set("Content-Type", a.ContentType)
			w.Header().Set("Content-Disposition", "attachment; filename=\""+a.Filename+"\"")
			w.Write(a.Data)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if !h.store.Delete(r.PathValue("id")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteAll(w http.ResponseWriter, r *http.Request) {
	h.store.Clear()
	w.WriteHeader(http.StatusNoContent)
}

// New builds an HTTP server bound to addr (e.g. "127.0.0.1:8025") serving
// the JSON API under /api/ and the static frontend at /.
func New(addr string, st *store.Store) *http.Server {
	h := &handler{store: st}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/messages", h.listMessages)
	mux.HandleFunc("GET /api/messages/{id}", h.getMessage)
	mux.HandleFunc("GET /api/messages/{id}/raw", h.getRaw)
	mux.HandleFunc("GET /api/messages/{id}/attachments/{filename}", h.getAttachment)
	mux.HandleFunc("DELETE /api/messages/{id}", h.deleteMessage)
	mux.HandleFunc("DELETE /api/messages", h.deleteAll)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServerFS(static))

	return &http.Server{Addr: addr, Handler: mux}
}
