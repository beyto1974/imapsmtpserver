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
}

func New() *Store {
	return &Store{
		uidNext: 1,
		uids:    make(map[string]uint32),
	}
}

func (s *Store) Add(m *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
	s.uids[m.ID] = s.uidNext
	s.uidNext++
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
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m.ID == id {
			m.Seen = seen
			return
		}
	}
}

func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.messages {
		if m.ID == id {
			s.messages = append(s.messages[:i], s.messages[i+1:]...)
			delete(s.uids, id)
			return true
		}
	}
	return false
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.uids = make(map[string]uint32)
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}
