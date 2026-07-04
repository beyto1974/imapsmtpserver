// Package imapd is a minimal read-only IMAP server backed by
// internal/store.Store. Any username/password is accepted (dev tool, no real
// auth); there is a single mailbox, "INBOX", populated only by mail received
// over SMTP.
package imapd

import (
	"bufio"
	"bytes"
	"errors"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-imap/server"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"

	"imapsmtpserver/internal/store"
)

var errReadOnly = errors.New("mailbox is read-only: mail arrives only via SMTP")

type imapBackend struct {
	store *store.Store
}

func (b *imapBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	return &imapUser{username: username, store: b.store}, nil
}

type imapUser struct {
	username string
	store    *store.Store
}

func (u *imapUser) Username() string { return u.username }

func (u *imapUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	return []backend.Mailbox{&mailbox{store: u.store}}, nil
}

func (u *imapUser) GetMailbox(name string) (backend.Mailbox, error) {
	if name != "INBOX" {
		return nil, backend.ErrNoSuchMailbox
	}
	return &mailbox{store: u.store}, nil
}

func (u *imapUser) CreateMailbox(name string) error          { return errReadOnly }
func (u *imapUser) DeleteMailbox(name string) error          { return errReadOnly }
func (u *imapUser) RenameMailbox(existing, new string) error { return errReadOnly }
func (u *imapUser) Logout() error                            { return nil }

type mailbox struct {
	store *store.Store
}

func (mbox *mailbox) Name() string { return "INBOX" }

func (mbox *mailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Delimiter: "/", Name: "INBOX"}, nil
}

func (mbox *mailbox) unseenSeqNum(messages []*store.Message) uint32 {
	for i, m := range messages {
		if !m.Seen {
			return uint32(i + 1)
		}
	}
	return 0
}

func (mbox *mailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	messages := mbox.store.List()

	status := imap.NewMailboxStatus("INBOX", items)
	status.Flags = []string{imap.SeenFlag}
	status.PermanentFlags = []string{imap.SeenFlag}
	status.UnseenSeqNum = mbox.unseenSeqNum(messages)

	for _, item := range items {
		switch item {
		case imap.StatusMessages:
			status.Messages = uint32(len(messages))
		case imap.StatusUidNext:
			status.UidNext = mbox.store.UIDNext()
		case imap.StatusUidValidity:
			status.UidValidity = 1
		case imap.StatusRecent:
			status.Recent = 0
		case imap.StatusUnseen:
			status.Unseen = 0
		}
	}

	return status, nil
}

func (mbox *mailbox) SetSubscribed(subscribed bool) error { return nil }

func (mbox *mailbox) Check() error { return nil }

func headerAndBody(raw []byte) (textproto.Header, *bufio.Reader, error) {
	body := bufio.NewReader(bytes.NewReader(raw))
	hdr, err := textproto.ReadHeader(body)
	return hdr, body, err
}

func fetchMessage(msg *store.Message, uid uint32, seqNum uint32, items []imap.FetchItem) (*imap.Message, error) {
	fetched := imap.NewMessage(seqNum, items)
	for _, item := range items {
		switch item {
		case imap.FetchEnvelope:
			hdr, _, err := headerAndBody(msg.Raw)
			if err != nil {
				break
			}
			fetched.Envelope, _ = backendutil.FetchEnvelope(hdr)
		case imap.FetchBody, imap.FetchBodyStructure:
			hdr, body, err := headerAndBody(msg.Raw)
			if err != nil {
				break
			}
			fetched.BodyStructure, _ = backendutil.FetchBodyStructure(hdr, body, item == imap.FetchBodyStructure)
		case imap.FetchFlags:
			if msg.Seen {
				fetched.Flags = []string{imap.SeenFlag}
			}
		case imap.FetchInternalDate:
			fetched.InternalDate = msg.Date
		case imap.FetchRFC822Size:
			fetched.Size = uint32(msg.Size)
		case imap.FetchUid:
			fetched.Uid = uid
		default:
			section, err := imap.ParseBodySectionName(item)
			if err != nil {
				break
			}
			hdr, body, err := headerAndBody(msg.Raw)
			if err != nil {
				break
			}
			l, _ := backendutil.FetchBodySection(hdr, body, section)
			fetched.Body[section] = l
		}
	}
	return fetched, nil
}

func (mbox *mailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	messages := mbox.store.List()
	for i, msg := range messages {
		seqNum := uint32(i + 1)
		msgUID := mbox.store.UID(msg.ID)

		id := seqNum
		if uid {
			id = msgUID
		}
		if !seqset.Contains(id) {
			continue
		}

		fetched, err := fetchMessage(msg, msgUID, seqNum, items)
		if err != nil {
			continue
		}
		ch <- fetched
	}

	return nil
}

func (mbox *mailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	messages := mbox.store.List()

	var ids []uint32
	for i, msg := range messages {
		seqNum := uint32(i + 1)
		msgUID := mbox.store.UID(msg.ID)

		var flags []string
		if msg.Seen {
			flags = []string{imap.SeenFlag}
		}

		entity, err := message.Read(bytes.NewReader(msg.Raw))
		if err != nil {
			continue
		}
		ok, err := backendutil.Match(entity, seqNum, msgUID, msg.Date, flags, criteria)
		if err != nil || !ok {
			continue
		}

		if uid {
			ids = append(ids, msgUID)
		} else {
			ids = append(ids, seqNum)
		}
	}

	return ids, nil
}

func (mbox *mailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	return errReadOnly
}

func (mbox *mailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	messages := mbox.store.List()

	seen := false
	for _, f := range flags {
		if f == imap.SeenFlag {
			seen = true
		}
	}
	if !seen {
		// Only \Seen is tracked; other flags are silently accepted and dropped.
		return nil
	}

	for i, msg := range messages {
		seqNum := uint32(i + 1)
		msgUID := mbox.store.UID(msg.ID)

		id := seqNum
		if uid {
			id = msgUID
		}
		if !seqset.Contains(id) {
			continue
		}

		current := []string{}
		if msg.Seen {
			current = []string{imap.SeenFlag}
		}
		updated := backendutil.UpdateFlags(current, op, flags)

		newSeen := false
		for _, f := range updated {
			if f == imap.SeenFlag {
				newSeen = true
			}
		}
		mbox.store.SetSeen(msg.ID, newSeen)
	}

	return nil
}

func (mbox *mailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	return errReadOnly
}

func (mbox *mailbox) Expunge() error { return nil }

// New builds an IMAP server bound to addr (e.g. "127.0.0.1:1143").
func New(addr string, st *store.Store) *server.Server {
	srv := server.New(&imapBackend{store: st})
	srv.Addr = addr
	srv.AllowInsecureAuth = true
	return srv
}
