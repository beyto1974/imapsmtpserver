package store

import (
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"
)

// Attachment is a single file attached to a message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Message is a received email, kept both parsed (for the web UI) and raw
// (for IMAP clients that want the original RFC822 bytes).
type Message struct {
	ID          string
	MessageID   string // RFC822 Message-Id header, without angle brackets; empty if absent
	InReplyTo   string // RFC822 Message-Id this message is a reply to, if any
	From        string
	To          []string
	Subject     string
	Date        time.Time
	Text        string
	HTML        string
	Attachments []Attachment
	Raw         []byte
	Size        int
	Seen        bool
}

// NormalizeAddress extracts and lowercases the bare email address from a
// possibly "Name <addr>"-formatted string, so messages can be matched to
// accounts regardless of how the address was formatted.
func NormalizeAddress(raw string) string {
	if a, err := mail.ParseAddress(raw); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// Store is a simple in-memory mailbox. It is intentionally not persisted to
// disk - this is a dev tool, restart clears everything.
type Store struct {
	mu       sync.RWMutex
	messages []*Message
	uidNext  uint32
	uids     map[string]uint32 // message ID -> IMAP UID

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

func New() *Store {
	return &Store{
		uidNext: 1,
		uids:    make(map[string]uint32),
		subs:    make(map[chan struct{}]struct{}),
	}
}

// Subscribe registers for change notifications: every Add, Delete, Clear or
// SetSeen sends (a non-blocking, coalescing) signal on the returned channel.
// Call cancel when done listening to release the subscription.
func (s *Store) Subscribe() (ch <-chan struct{}, cancel func()) {
	c := make(chan struct{}, 1)
	s.subMu.Lock()
	s.subs[c] = struct{}{}
	s.subMu.Unlock()

	return c, func() {
		s.subMu.Lock()
		delete(s.subs, c)
		s.subMu.Unlock()
	}
}

func (s *Store) notify() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for c := range s.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (s *Store) Add(m *Message) {
	s.mu.Lock()
	s.messages = append(s.messages, m)
	s.uids[m.ID] = s.uidNext
	s.uidNext++
	s.mu.Unlock()
	s.notify()
}

func (s *Store) List() []*Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Message, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *Store) Get(id string) *Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.messages {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (s *Store) UID(id string) uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.uids[id]
}

// UIDNext returns the UID that will be assigned to the next added message.
func (s *Store) UIDNext() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.uidNext
}

// SetSeen marks a message as read/unread.
func (s *Store) SetSeen(id string, seen bool) {
	s.mu.Lock()
	changed := false
	for _, m := range s.messages {
		if m.ID == id {
			changed = m.Seen != seen
			m.Seen = seen
			break
		}
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	deleted := false
	for i, m := range s.messages {
		if m.ID == id {
			s.messages = append(s.messages[:i], s.messages[i+1:]...)
			delete(s.uids, id)
			deleted = true
			break
		}
	}
	s.mu.Unlock()
	if deleted {
		s.notify()
	}
	return deleted
}

func (s *Store) Clear() {
	s.mu.Lock()
	s.messages = nil
	s.uids = make(map[string]uint32)
	s.mu.Unlock()
	s.notify()
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// Accounts returns the distinct set of normalized addresses seen across all
// messages' From/To fields, sorted. There is no explicit account
// registration - any address that has sent or received mail is an account.
func (s *Store) Accounts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	set := make(map[string]struct{})
	for _, m := range s.messages {
		if from := NormalizeAddress(m.From); from != "" {
			set[from] = struct{}{}
		}
		for _, to := range m.To {
			if addr := NormalizeAddress(to); addr != "" {
				set[addr] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Inbox returns messages addressed to addr, in the order they were received.
func (s *Store) Inbox(addr string) []*Message {
	addr = NormalizeAddress(addr)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*Message
	for _, m := range s.messages {
		for _, to := range m.To {
			if NormalizeAddress(to) == addr {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// Sent returns messages sent from addr, in the order they were received.
func (s *Store) Sent(addr string) []*Message {
	addr = NormalizeAddress(addr)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*Message
	for _, m := range s.messages {
		if NormalizeAddress(m.From) == addr {
			out = append(out, m)
		}
	}
	return out
}
