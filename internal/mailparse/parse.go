// Package mailparse turns raw RFC822 bytes into the fields the web UI and
// store need (subject, from/to, plain text, html, attachments).
package mailparse

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/google/uuid"

	"imapsmtpserver/internal/store"
)

// Parse reads raw RFC822 message bytes and builds a store.Message.
// from/to are the SMTP envelope addresses, used as a fallback when the
// headers don't parse cleanly.
func Parse(raw []byte, envelopeFrom string, envelopeTo []string) (*store.Message, error) {
	m := &store.Message{
		ID:   uuid.NewString(),
		From: envelopeFrom,
		To:   envelopeTo,
		Date: time.Now(),
		Raw:  raw,
		Size: len(raw),
	}

	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		// Not a well-formed MIME message: keep the raw bytes, best effort.
		return m, nil
	}
	defer reader.Close()

	if subject, err := reader.Header.Subject(); err == nil {
		m.Subject = subject
	}
	if from, err := reader.Header.AddressList("From"); err == nil && len(from) > 0 {
		m.From = from[0].String()
	}
	if to, err := reader.Header.AddressList("To"); err == nil && len(to) > 0 {
		addrs := make([]string, len(to))
		for i, a := range to {
			addrs[i] = a.String()
		}
		m.To = addrs
	}
	if date, err := reader.Header.Date(); err == nil && !date.IsZero() {
		m.Date = date
	}

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			contentType, _, _ := h.ContentType()
			body, _ := io.ReadAll(part.Body)
			switch contentType {
			case "text/html":
				m.HTML += string(body)
			default:
				m.Text += string(body)
			}
		case *mail.AttachmentHeader:
			filename, _ := h.Filename()
			contentType, _, _ := h.ContentType()
			body, err := io.ReadAll(part.Body)
			if err != nil {
				continue
			}
			if filename == "" {
				filename = fmt.Sprintf("attachment-%d", len(m.Attachments)+1)
			}
			m.Attachments = append(m.Attachments, store.Attachment{
				Filename:    filename,
				ContentType: contentType,
				Data:        body,
			})
		}
	}

	return m, nil
}
