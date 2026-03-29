package messages

import "sync"

const MaxMessages = 10

// Store holds a ring-buffer of recent messages.
type Store struct {
	mu       sync.Mutex
	messages []string
}

// NewStore creates a new message store.
func NewStore() *Store {
	return &Store{
		messages: make([]string, 0, MaxMessages),
	}
}

// Add adds a message to the store, dropping the oldest if full.
func (s *Store) Add(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) >= MaxMessages {
		s.messages = s.messages[1:]
	}
	s.messages = append(s.messages, msg)
}

// All returns a copy of all current messages.
func (s *Store) All() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, len(s.messages))
	copy(result, s.messages)
	return result
}
