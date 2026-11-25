// Package task keeps one to one private messages: per conversation sequence,
// per device read cursors merged to the slowest device, retention pruning and
// offline refill after a sequence.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Defaults used when Options leaves a field unset.
const (
	DefaultMaxMessagesPerConversation = 200
	DefaultMaxRetentionSeconds        = 604800
)

// Errors returned by the store.
var (
	ErrBadMessage     = errors.New("invalid direct message")
	ErrBadAccount     = errors.New("account, device and peer are required")
	ErrBadCursor      = errors.New("cursor sequence must not be negative")
	ErrNoConversation = errors.New("conversation does not exist")
	ErrBadTime        = errors.New("timestamp must be positive")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Options bounds the store.
type Options struct {
	MaxMessagesPerConversation int
	MaxRetentionSeconds        int64
}

// Message is one direct message inside a conversation.
type Message struct {
	Seq  int64  `json:"seq"`
	From string `json:"from"`
	To   string `json:"to"`
	Body string `json:"body"`
	At   int64  `json:"at"`
}

// Conversation is the per account view of one peer thread.
type Conversation struct {
	Account   string `json:"account"`
	Peer      string `json:"peer"`
	LastSeq   int64  `json:"last_seq"`
	Messages  int    `json:"messages"`
	Truncated bool   `json:"truncated"`
	Cursor    int64  `json:"cursor"`
}

// DeviceCursor is one device read watermark.
type DeviceCursor struct {
	Device string `json:"device"`
	Seq    int64  `json:"seq"`
}

// DmCursorStore owns messages, device cursors and retention state.
type DmCursorStore struct {
	opts Options
}

// NewDmCursorStore builds a store with the given caps.
func NewDmCursorStore(opts Options) *DmCursorStore {
	return &DmCursorStore{opts: opts}
}

// Send appends one message and returns it with its conversation sequence.
func (s *DmCursorStore) Send(from, to, body string, at int64) (Message, error) {
	return Message{}, errNotImplemented
}

// Fetch returns messages after the given sequence, oldest first.
func (s *DmCursorStore) Fetch(account, peer string, afterSeq int64) []Message {
	return nil
}

// Ack advances one device cursor inside a conversation.
func (s *DmCursorStore) Ack(account, device, peer string, seq int64) error {
	return errNotImplemented
}

// Cursor is the merged read cursor of one conversation.
func (s *DmCursorStore) Cursor(account, peer string) int64 {
	return 0
}

// Prune drops messages older than the retention window.
func (s *DmCursorStore) Prune(at int64) (int, error) {
	return 0, errNotImplemented
}

// Conversation reads one thread view.
func (s *DmCursorStore) Conversation(account, peer string) (Conversation, bool) {
	return Conversation{}, false
}

// Conversations lists every thread of one account ordered by peer.
func (s *DmCursorStore) Conversations(account string) []Conversation {
	return nil
}

// Devices lists the cursors of one thread ordered by device id.
func (s *DmCursorStore) Devices(account, peer string) []DeviceCursor {
	return nil
}

// Snapshot serialises messages, cursors and truncation state.
func (s *DmCursorStore) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *DmCursorStore) Restore(data []byte) error {
	return errNotImplemented
}
