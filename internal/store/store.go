package store

import (
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
