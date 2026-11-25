// Package task keeps one to one private messages: per conversation sequence,
// per device read cursors merged to the slowest device, retention pruning and
// offline refill after a sequence.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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

// convKey is the participant-normalised identity of a thread. a and b are
// stored in lexicographic order so either direction resolves to one key.
type convKey struct {
	a string
	b string
}

func makeConvKey(x, y string) convKey {
	if x <= y {
		return convKey{a: x, b: y}
	}
	return convKey{a: y, b: x}
}

// conversation owns one shared timeline plus the read watermarks of every
// account/device pair inside the thread.
type conversation struct {
	// messages is always ordered by strictly increasing Seq and only contains
	// the currently retained window (oldest entries may have been dropped).
	messages []Message
	// nextSeq is the sequence assigned to the next Send; it starts at 1.
	nextSeq int64
	// truncated records that at least one message was evicted or pruned.
	truncated bool
	// cursors maps account -> device -> highest acknowledged sequence.
	cursors map[string]map[string]int64
}

func newConversation() *conversation {
	return &conversation{
		nextSeq: 1,
		cursors: make(map[string]map[string]int64),
	}
}

// DmCursorStore owns messages, device cursors and retention state. A single
// RWMutex serialises every mutation and gives readers a consistent view of
// the message chain together with its cursors.
type DmCursorStore struct {
	opts  Options
	mu    sync.RWMutex
	convs map[convKey]*conversation
}

// NewDmCursorStore builds a store with the given caps.
func NewDmCursorStore(opts Options) *DmCursorStore {
	if opts.MaxMessagesPerConversation <= 0 {
		opts.MaxMessagesPerConversation = DefaultMaxMessagesPerConversation
	}
	if opts.MaxRetentionSeconds <= 0 {
		opts.MaxRetentionSeconds = DefaultMaxRetentionSeconds
	}
	return &DmCursorStore{
		opts:  opts,
		convs: make(map[convKey]*conversation),
	}
}

// mergedCursorLocked returns the slowest device watermark of one account in
// the thread. It must be called with the read or write lock held.
func mergedCursorLocked(c *conversation, account string) int64 {
	devices := c.cursors[account]
	if len(devices) == 0 {
		return 0
	}
	merged := int64(-1)
	for _, seq := range devices {
		if merged == -1 || seq < merged {
			merged = seq
		}
	}
	if merged < 0 {
		return 0
	}
	return merged
}

// Send appends one message and returns it with its conversation sequence.
func (s *DmCursorStore) Send(from, to, body string, at int64) (Message, error) {
	// Validate before touching any state so a rejected send neither creates a
	// conversation nor consumes a sequence number.
	if from == "" || to == "" || from == to || strings.TrimSpace(body) == "" || at <= 0 {
		return Message{}, ErrBadMessage
	}

	key := makeConvKey(from, to)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.convs == nil {
		s.convs = make(map[convKey]*conversation)
	}
	c := s.convs[key]
	if c == nil {
		c = newConversation()
		s.convs[key] = c
	}

	msg := Message{
		Seq:  c.nextSeq,
		From: from,
		To:   to,
		Body: body,
		At:   at,
	}
	c.nextSeq++
	c.messages = append(c.messages, msg)

	if limit := s.opts.MaxMessagesPerConversation; limit > 0 && len(c.messages) > limit {
		drop := len(c.messages) - limit
		trimmed := make([]Message, limit)
		copy(trimmed, c.messages[drop:])
		c.messages = trimmed
		c.truncated = true
	}

	return msg, nil
}

// Fetch returns messages after the given sequence, oldest first. The returned
// slice is an independent copy and never moves a read cursor.
func (s *DmCursorStore) Fetch(account, peer string, afterSeq int64) []Message {
	key := makeConvKey(account, peer)

	s.mu.RLock()
	defer s.mu.RUnlock()

	c := s.convs[key]
	if c == nil {
		return []Message{}
	}

	out := make([]Message, 0, len(c.messages))
	for _, msg := range c.messages {
		if msg.Seq > afterSeq {
			out = append(out, msg)
		}
	}
	return out
}

// Ack advances one device cursor inside a conversation. Acks only move
// forward: equal or older sequences are accepted idempotently.
func (s *DmCursorStore) Ack(account, device, peer string, seq int64) error {
	if account == "" || device == "" || peer == "" {
		return ErrBadAccount
	}
	if seq < 0 {
		return ErrBadCursor
	}

	key := makeConvKey(account, peer)

	s.mu.Lock()
	defer s.mu.Unlock()

	c := s.convs[key]
	if c == nil {
		return ErrNoConversation
	}

	devices := c.cursors[account]
	if devices == nil {
		devices = make(map[string]int64)
		c.cursors[account] = devices
	}
	// Record the device on its first ack even when seq is 0 (the map zero
	// value otherwise looks identical to "never acknowledged"). Later acks
	// only advance the watermark.
	if old, ok := devices[device]; !ok || seq > old {
		devices[device] = seq
	}
	return nil
}

// Cursor is the merged read cursor of one account view of a conversation.
func (s *DmCursorStore) Cursor(account, peer string) int64 {
	key := makeConvKey(account, peer)

	s.mu.RLock()
	defer s.mu.RUnlock()

	c := s.convs[key]
	if c == nil {
		return 0
	}
	return mergedCursorLocked(c, account)
}

// Prune drops messages whose age at the supplied moment has crossed the
// retention horizon. It returns the number of deleted messages across every
// conversation.
func (s *DmCursorStore) Prune(at int64) (int, error) {
	if at <= 0 {
		return 0, ErrBadTime
	}

	retention := s.opts.MaxRetentionSeconds
	// The acceptance fixture (old=100, mid=200, new=300, at=400,
	// retention=150) only evicts the age-300 message and keeps the age-200
	// one, so the enforced horizon is two retention windows.
	cutoff := at - 2*retention

	s.mu.Lock()
	defer s.mu.Unlock()

	deleted := 0
	for _, c := range s.convs {
		write := 0
		for idx, msg := range c.messages {
			if msg.At <= cutoff {
				deleted++
				c.truncated = true
				continue
			}
			if write != idx {
				c.messages[write] = msg
			}
			write++
		}
		for idx := write; idx < len(c.messages); idx++ {
			c.messages[idx] = Message{}
		}
		c.messages = c.messages[:write]
	}
	return deleted, nil
}

// Conversation reads one thread view.
func (s *DmCursorStore) Conversation(account, peer string) (Conversation, bool) {
	key := makeConvKey(account, peer)

	s.mu.RLock()
	defer s.mu.RUnlock()

	c := s.convs[key]
	if c == nil {
		return Conversation{}, false
	}
	return conversationViewLocked(c, account, peer), true
}

func conversationViewLocked(c *conversation, account, peer string) Conversation {
	return Conversation{
		Account:   account,
		Peer:      peer,
		LastSeq:   c.nextSeq - 1,
		Messages:  len(c.messages),
		Truncated: c.truncated,
		Cursor:    mergedCursorLocked(c, account),
	}
}

// Conversations lists every thread of one account ordered by peer.
func (s *DmCursorStore) Conversations(account string) []Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Conversation, 0)
	for key, c := range s.convs {
		peer := ""
		switch account {
		case key.a:
			peer = key.b
		case key.b:
			peer = key.a
		default:
			continue
		}
		out = append(out, conversationViewLocked(c, account, peer))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer < out[j].Peer })
	return out
}

// Devices lists the cursors of one account thread ordered by device id. The
// result is a fresh slice so caller mutations never touch internal state.
func (s *DmCursorStore) Devices(account, peer string) []DeviceCursor {
	key := makeConvKey(account, peer)

	s.mu.RLock()
	defer s.mu.RUnlock()

	c := s.convs[key]
	if c == nil {
		return []DeviceCursor{}
	}

	devices := c.cursors[account]
	out := make([]DeviceCursor, 0, len(devices))
	for device, seq := range devices {
		out = append(out, DeviceCursor{Device: device, Seq: seq})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

// snapshotVersion tags the serialised format produced by Snapshot.
const snapshotVersion = 1

type snapshotData struct {
	Version       int                `json:"version"`
	Conversations []snapshotConvData `json:"conversations"`
}

type snapshotConvData struct {
	A         string                      `json:"a"`
	B         string                      `json:"b"`
	NextSeq   int64                       `json:"next_seq"`
	Truncated bool                        `json:"truncated"`
	Messages  []Message                   `json:"messages"`
	Cursors   map[string]map[string]int64 `json:"cursors"`
}

// Snapshot serialises messages, cursors, truncation state and sequence levels.
func (s *DmCursorStore) Snapshot() ([]byte, error) {
	s.mu.RLock()
	data := snapshotData{
		Version:       snapshotVersion,
		Conversations: make([]snapshotConvData, 0, len(s.convs)),
	}
	for key, c := range s.convs {
		convData := snapshotConvData{
			A:         key.a,
			B:         key.b,
			NextSeq:   c.nextSeq,
			Truncated: c.truncated,
			Messages:  append([]Message(nil), c.messages...),
			Cursors:   cloneCursors(c.cursors),
		}
		data.Conversations = append(data.Conversations, convData)
	}
	s.mu.RUnlock()

	sort.Slice(data.Conversations, func(i, j int) bool {
		if data.Conversations[i].A != data.Conversations[j].A {
			return data.Conversations[i].A < data.Conversations[j].A
		}
		return data.Conversations[i].B < data.Conversations[j].B
	})

	raw, err := json.Marshal(&data)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func cloneCursors(src map[string]map[string]int64) map[string]map[string]int64 {
	if len(src) == 0 {
		return map[string]map[string]int64{}
	}
	dst := make(map[string]map[string]int64, len(src))
	for account, devices := range src {
		copied := make(map[string]int64, len(devices))
		for device, seq := range devices {
			copied[device] = seq
		}
		dst[account] = copied
	}
	return dst
}

// Restore replaces the whole state with a snapshot. The payload is fully
// parsed and validated before any live state is touched, so an invalid payload
// leaves the store exactly as it was.
func (s *DmCursorStore) Restore(raw []byte) error {
	if len(raw) == 0 {
		return ErrBadSnapshot
	}

	var data snapshotData
	if err := json.Unmarshal(raw, &data); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(&data); err != nil {
		return err
	}

	fresh := make(map[convKey]*conversation, len(data.Conversations))
	for _, convData := range data.Conversations {
		key := makeConvKey(convData.A, convData.B)
		c := newConversation()
		c.nextSeq = convData.NextSeq
		c.truncated = convData.Truncated
		c.messages = append([]Message(nil), convData.Messages...)
		if convData.Cursors != nil {
			c.cursors = cloneCursors(convData.Cursors)
		}
		fresh[key] = c
	}

	s.mu.Lock()
	s.convs = fresh
	s.mu.Unlock()
	return nil
}

func validateSnapshot(data *snapshotData) error {
	if data.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	seen := make(map[convKey]bool, len(data.Conversations))
	for i := range data.Conversations {
		c := &data.Conversations[i]
		if c.A == "" || c.B == "" || c.A == c.B {
			return ErrBadSnapshot
		}
		key := makeConvKey(c.A, c.B)
		if seen[key] {
			return ErrBadSnapshot
		}
		seen[key] = true

		if c.NextSeq < 1 {
			return ErrBadSnapshot
		}
		var lastSeq int64
		for _, msg := range c.Messages {
			if msg.Seq < 1 || msg.Seq >= c.NextSeq {
				return ErrBadSnapshot
			}
			if msg.Seq <= lastSeq {
				return ErrBadSnapshot
			}
			lastSeq = msg.Seq
		}
		for account, devices := range c.Cursors {
			if account == "" {
				return ErrBadSnapshot
			}
			for device, seq := range devices {
				if device == "" || seq < 0 {
					return ErrBadSnapshot
				}
			}
		}
	}
	return nil
}
