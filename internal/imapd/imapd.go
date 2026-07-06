// Package imapd is a minimal IMAP server backed by internal/store.Store. Any
// username/password is accepted (dev tool, no real auth); the login username
// selects which account's mail is visible, with two mailboxes per account:
// "INBOX" (mail received by that address) and "Sent" (mail sent from that
// address, e.g. via the web UI's compose/reply or an APPEND). Mail normally
// arrives via SMTP, but clients may also APPEND directly into either
// mailbox (e.g. a desktop client filing a Sent copy after submitting over
// SMTP separately); mailbox management (create/delete/rename) and COPY
// remain unsupported.
package imapd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-imap/server"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"

	"imapsmtpserver/internal/mailparse"
	"imapsmtpserver/internal/store"
)

var errReadOnly = errors.New("mailbox is read-only: mail arrives only via SMTP")

type imapBackend struct {
	store *store.Store
}

func (b *imapBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	return &imapUser{account: store.NormalizeAddress(username), store: b.store}, nil
}

type imapUser struct {
	account string
	store   *store.Store
}

func (u *imapUser) Username() string { return u.account }

func (u *imapUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	return []backend.Mailbox{
		&mailbox{store: u.store, account: u.account, name: "INBOX", folder: folderInbox},
		&mailbox{store: u.store, account: u.account, name: "Sent", folder: folderSent},
	}, nil
}

func (u *imapUser) GetMailbox(name string) (backend.Mailbox, error) {
	switch name {
	case "INBOX":
		return &mailbox{store: u.store, account: u.account, name: "INBOX", folder: folderInbox}, nil
	case "Sent":
		return &mailbox{store: u.store, account: u.account, name: "Sent", folder: folderSent}, nil
	default:
		return nil, backend.ErrNoSuchMailbox
	}
}

func (u *imapUser) CreateMailbox(name string) error          { return errReadOnly }
func (u *imapUser) DeleteMailbox(name string) error          { return errReadOnly }
func (u *imapUser) RenameMailbox(existing, new string) error { return errReadOnly }
func (u *imapUser) Logout() error                            { return nil }

type folder int

const (
	folderInbox folder = iota
	folderSent
)

type mailbox struct {
	store   *store.Store
	account string
	name    string
	folder  folder
}

// messages returns this mailbox's messages: the account's received mail for
// INBOX, or its sent mail for Sent.
func (mbox *mailbox) messages() []*store.Message {
	if mbox.folder == folderSent {
		return mbox.store.Sent(mbox.account)
	}
	return mbox.store.Inbox(mbox.account)
}

func (mbox *mailbox) Name() string { return mbox.name }

func (mbox *mailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Delimiter: "/", Name: mbox.name}, nil
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
	messages := mbox.messages()

	status := imap.NewMailboxStatus(mbox.name, items)
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

	messages := mbox.messages()
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
	messages := mbox.messages()

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

// CreateMessage implements APPEND: the appended bytes are parsed exactly
// like incoming SMTP mail, then filed so it's guaranteed to actually show up
// in the mailbox it was appended to - INBOX appends ensure the account is a
// recipient, Sent appends force the account as the sender - since store.Inbox
// / store.Sent are just filters over From/To, not separate physical folders.
//
// Mail clients conventionally submit a message over SMTP and then separately
// APPEND the identical bytes into their IMAP "Sent" mailbox to keep a local
// copy - which on a real server populates a folder that SMTP submission
// never touches. Here, Sent is derived from the same store SMTP already
// writes to, so that append would otherwise create a visible duplicate of
// the message just sent. Since the client preserves the Message-Id between
// the two, a matching existing message is treated as "already filed" rather
// than stored again.
func (mbox *mailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}

	msg, err := mailparse.Parse(raw, "", nil)
	if err != nil {
		return err
	}

	if existing := mbox.store.FindByMessageID(msg.MessageID); existing != nil {
		for _, f := range flags {
			if f == imap.SeenFlag {
				mbox.store.SetSeen(existing.ID, true)
			}
		}
		return nil
	}

	if !date.IsZero() {
		msg.Date = date
	}
	for _, f := range flags {
		if f == imap.SeenFlag {
			msg.Seen = true
		}
	}

	if mbox.folder == folderSent {
		msg.From = "<" + mbox.account + ">"
	} else if !containsAddress(msg.To, mbox.account) {
		msg.To = append(msg.To, "<"+mbox.account+">")
	}

	mbox.store.Add(msg)
	return nil
}

func containsAddress(addrs []string, addr string) bool {
	addr = store.NormalizeAddress(addr)
	for _, a := range addrs {
		if store.NormalizeAddress(a) == addr {
			return true
		}
	}
	return false
}

func (mbox *mailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	messages := mbox.messages()

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
