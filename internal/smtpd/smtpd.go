// Package smtpd is a minimal SMTP server that accepts any mail from any
// sender (no auth, no relaying) and hands parsed messages to the store.
package smtpd

import (
	"bytes"
	"io"
	"log"

	"github.com/emersion/go-smtp"

	"imapsmtpserver/internal/mailparse"
	"imapsmtpserver/internal/store"
)

type backend struct {
	store *store.Store
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{store: b.store}, nil
}

type session struct {
	store *store.Store
	from  string
	to    []string
}

func (s *session) AuthPlain(username, password string) error {
	return nil // accept anything, this is a dev sink
}

func (s *session) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, opts *smtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	msg, err := mailparse.Parse(bytes.Clone(raw), s.from, s.to)
	if err != nil {
		return err
	}
	s.store.Add(msg)
	log.Printf("smtp: received message %s (%d bytes) from=%s to=%v subject=%q",
		msg.ID, msg.Size, msg.From, msg.To, msg.Subject)
	return nil
}

func (s *session) Reset() {
	s.from = ""
	s.to = nil
}

func (s *session) Logout() error {
	return nil
}

// New builds an SMTP server bound to addr (e.g. "127.0.0.1:1025").
func New(addr string, st *store.Store) *smtp.Server {
	srv := smtp.NewServer(&backend{store: st})
	srv.Addr = addr
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = true
	return srv
}
