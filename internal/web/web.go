// Package web serves the REST API and the static web UI used to browse
// messages caught by the SMTP server.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/google/uuid"

	"imapsmtpserver/internal/store"
)

//go:embed static
var staticFS embed.FS

type handler struct {
	store    *store.Store
	smtpAddr string
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

// listAccounts returns every address that has sent or received mail so far.
// Accounts aren't configured up front - any address seen in a message's
// From/To becomes one.
func (h *handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.store.Accounts())
}

// listAccountMessages returns an account's mail, either received (folder=inbox,
// the default) or sent (folder=sent).
func (h *handler) listAccountMessages(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("address")

	var messages []*store.Message
	if r.URL.Query().Get("folder") == "sent" {
		messages = h.store.Sent(account)
	} else {
		messages = h.store.Inbox(account)
	}

	out := make([]messageSummary, len(messages))
	for i, m := range messages {
		out[i] = summarize(m)
	}
	writeJSON(w, out)
}

type sendRequest struct {
	From      string `json:"from"`
	To        string `json:"to"` // comma-separated recipient addresses
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	InReplyTo string `json:"inReplyTo"` // store ID of the message being replied to, optional
}

func splitAddresses(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// buildRawMessage renders a plain-text RFC822 message for submission over SMTP.
func buildRawMessage(from string, to []string, subject, text, inReplyTo string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-Id: <%s@imapsmtpserver>\r\n", uuid.NewString())
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", inReplyTo)
		fmt.Fprintf(&b, "References: <%s>\r\n", inReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	b.WriteString(normalized)
	return []byte(b.String())
}

// sendMessage composes a message from a JSON sendRequest and submits it to
// the server's own SMTP port, exactly as an external sender would - so it
// arrives in the recipient account's inbox (and shows up in the sender's
// Sent view) via the normal SMTP -> mailparse -> store path.
func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	from := store.NormalizeAddress(req.From)
	to := splitAddresses(req.To)
	for i, addr := range to {
		to[i] = store.NormalizeAddress(addr)
	}
	if from == "" || len(to) == 0 {
		http.Error(w, "from and to are required", http.StatusBadRequest)
		return
	}

	var inReplyTo string
	if req.InReplyTo != "" {
		if orig := h.store.Get(req.InReplyTo); orig != nil {
			inReplyTo = orig.MessageID
		}
	}

	raw := buildRawMessage(from, to, req.Subject, req.Text, inReplyTo)
	if err := smtp.SendMail(h.smtpAddr, nil, from, to, raw); err != nil {
		http.Error(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// events streams a Server-Sent Events feed: an "update" event is pushed
// whenever the store changes (message added/deleted/cleared/seen-toggled),
// so the frontend can refetch the list instead of polling for it.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	changes, cancel := h.store.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	io.WriteString(w, "event: update\ndata: {}\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			io.WriteString(w, "event: update\ndata: {}\n\n")
			flusher.Flush()
		case <-keepalive.C:
			io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// New builds an HTTP server bound to addr (e.g. "127.0.0.1:8025") serving
// the JSON API under /api/ and the static frontend at /. smtpAddr is the
// address of this process's own SMTP server (e.g. "127.0.0.1:1025"), used to
// submit mail composed through the web UI's send/reply feature.
func New(addr string, st *store.Store, smtpAddr string) *http.Server {
	h := &handler{store: st, smtpAddr: smtpAddr}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/messages", h.listMessages)
	mux.HandleFunc("GET /api/messages/{id}", h.getMessage)
	mux.HandleFunc("GET /api/messages/{id}/raw", h.getRaw)
	mux.HandleFunc("GET /api/messages/{id}/attachments/{filename}", h.getAttachment)
	mux.HandleFunc("DELETE /api/messages/{id}", h.deleteMessage)
	mux.HandleFunc("DELETE /api/messages", h.deleteAll)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/accounts", h.listAccounts)
	mux.HandleFunc("GET /api/accounts/{address}/messages", h.listAccountMessages)
	mux.HandleFunc("POST /api/send", h.sendMessage)

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServerFS(static))

	return &http.Server{Addr: addr, Handler: mux}
}
