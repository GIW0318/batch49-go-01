// Package task maintains per stream audit chains, exchanges signed head
// anchors between nodes and reports truncation and forked history.
package task

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"sort"
	"strings"
	"sync"
)

const snapshotVersion = 1

// Verdict status values.
const (
	StatusIntact     = "intact"
	StatusTruncated  = "truncated"
	StatusFork       = "fork"
	StatusUnanchored = "unanchored"
)

// Gap kinds.
const (
	GapTailTruncation = "tail_truncation"
)

// Signer signs an anchor for one stream position. KeyID identifies the key so
// that retired keys can still be recognised.
type Signer interface {
	KeyID() string
	Sign(stream string, seq uint64, digest string) string
}

// Record is one audit entry in a stream chain.
type Record struct {
	Stream  string `json:"stream"`
	Seq     uint64 `json:"seq"`
	Payload string `json:"payload"`
	Digest  string `json:"digest"`
}

// Anchor is a signed claim about the digest at one stream position.
type Anchor struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Digest    string `json:"digest"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// Gap is a missing sequence range.
type Gap struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
	Kind string `json:"kind"`
}

// Fork is one sequence position with conflicting digests.
type Fork struct {
	Seq     uint64   `json:"seq"`
	Digests []string `json:"digests"`
}

// Verdict is the result of comparing the local chain with trusted anchors.
type Verdict struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	Head        uint64 `json:"head"`
	TrustedHead uint64 `json:"trusted_head"`
	Gaps        []Gap  `json:"gaps"`
	Forks       []Fork `json:"forks"`
}

// Limits bounds the guard tables.
type Limits struct {
	MaxStreams int
	MaxRecords int
	MaxAnchors int
}

// Errors returned by the guard.
var (
	ErrBlankStream      = errors.New("stream id is required")
	ErrDuplicateStream  = errors.New("stream already registered")
	ErrUnknownStream    = errors.New("unknown stream")
	ErrNilSigner        = errors.New("signer is required")
	ErrEmptyPayload     = errors.New("audit payload is required")
	ErrNoRecords        = errors.New("stream has no records to anchor")
	ErrAnchorInvalid    = errors.New("anchor fields are incomplete")
	ErrSignatureInvalid = errors.New("anchor signature does not verify")
	ErrUnknownKey       = errors.New("signing key is not trusted")
	ErrKeyRetired       = errors.New("signing key is retired beyond this sequence")
	ErrLimitExceeded    = errors.New("limit exceeded")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// AuditChainTruncationGuard owns stream chains, the trusted key table and the
// anchors exchanged with peer nodes.
type AuditChainTruncationGuard struct {
	mu          sync.RWMutex
	limits      Limits
	signer      Signer
	activeKeyID string
	streams     map[string]*streamState
	anchors     []Anchor
	keys        map[string]*keyState
}

// NewAuditChainTruncationGuard builds a guard with the given caps and the
// initial signing key.
func NewAuditChainTruncationGuard(limits Limits, signer Signer) *AuditChainTruncationGuard {
	g := &AuditChainTruncationGuard{
		limits:  limits,
		signer:  signer,
		streams: make(map[string]*streamState),
		keys:    make(map[string]*keyState),
	}
	if signer != nil {
		id := signer.KeyID()
		g.activeKeyID = id
		g.keys[id] = &keyState{signer: signer}
	}
	return g
}

// RegisterStream adds a stream that will be written by this node.
func (g *AuditChainTruncationGuard) RegisterStream(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if strings.TrimSpace(id) == "" {
		return ErrBlankStream
	}
	if _, ok := g.streams[id]; ok {
		return ErrDuplicateStream
	}
	if g.signer == nil {
		return ErrNilSigner
	}
	if g.limits.MaxStreams > 0 && len(g.streams) >= g.limits.MaxStreams {
		return ErrLimitExceeded
	}

	g.streams[id] = &streamState{}
	return nil
}

// Append writes one audit record and returns the sealed entry.
func (g *AuditChainTruncationGuard) Append(stream, payload string) (Record, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.streams[stream]
	if !ok {
		return Record{}, ErrUnknownStream
	}
	if g.signer == nil {
		return Record{}, ErrNilSigner
	}
	if strings.TrimSpace(payload) == "" {
		return Record{}, ErrEmptyPayload
	}
	if g.limits.MaxRecords > 0 && len(s.records) >= g.limits.MaxRecords {
		return Record{}, ErrLimitExceeded
	}

	seq := uint64(len(s.records) + 1)
	prev := ""
	if len(s.records) > 0 {
		prev = s.records[len(s.records)-1].Digest
	}
	r := Record{
		Stream:  stream,
		Seq:     seq,
		Payload: payload,
		Digest:  chainDigest(stream, seq, prev, payload),
	}
	s.records = append(s.records, r)
	return r, nil
}

// Records returns the local chain of a stream in sequence order.
func (g *AuditChainTruncationGuard) Records(stream string) ([]Record, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	s, ok := g.streams[stream]
	if !ok {
		return nil, ErrUnknownStream
	}
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out, nil
}

// Head returns the newest local record.
func (g *AuditChainTruncationGuard) Head(stream string) (Record, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	s, ok := g.streams[stream]
	if !ok || len(s.records) == 0 {
		return Record{}, false
	}
	return s.records[len(s.records)-1], true
}

// Anchor signs the current head and records the anchor locally.
func (g *AuditChainTruncationGuard) Anchor(stream string) (Anchor, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.streams[stream]
	if !ok {
		return Anchor{}, ErrUnknownStream
	}
	if g.signer == nil {
		return Anchor{}, ErrNilSigner
	}
	if len(s.records) == 0 {
		return Anchor{}, ErrNoRecords
	}

	head := s.records[len(s.records)-1]
	keyID := g.signer.KeyID()
	if strings.TrimSpace(keyID) == "" {
		return Anchor{}, ErrNilSigner
	}
	ks, ok := g.keys[keyID]
	if !ok {
		return Anchor{}, ErrUnknownKey
	}
	if ks.retired && head.Seq > ks.afterSeq {
		return Anchor{}, ErrKeyRetired
	}

	a := Anchor{
		Stream:    stream,
		Seq:       head.Seq,
		Digest:    head.Digest,
		KeyID:     keyID,
		Signature: g.signer.Sign(stream, head.Seq, head.Digest),
	}
	for _, existing := range g.anchors {
		if sameAnchorClaim(existing, a) {
			return existing, nil
		}
	}
	if g.limits.MaxAnchors > 0 && len(g.anchors) >= g.limits.MaxAnchors {
		return Anchor{}, ErrLimitExceeded
	}

	g.anchors = append(g.anchors, a)
	return a, nil
}

// IngestAnchor validates and stores an anchor received from a peer.
func (g *AuditChainTruncationGuard) IngestAnchor(a Anchor) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !anchorComplete(a) {
		return ErrAnchorInvalid
	}
	if _, ok := g.streams[a.Stream]; !ok {
		return ErrUnknownStream
	}
	if g.hasExactAnchor(a) {
		return nil
	}
	ks, ok := g.keys[a.KeyID]
	if !ok {
		return ErrUnknownKey
	}
	if ks.retired && a.Seq > ks.afterSeq {
		return ErrKeyRetired
	}
	if ks.signer == nil {
		return ErrSignatureInvalid
	}
	expected := ks.signer.Sign(a.Stream, a.Seq, a.Digest)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(a.Signature)) != 1 {
		return ErrSignatureInvalid
	}
	if g.limits.MaxAnchors > 0 && len(g.anchors) >= g.limits.MaxAnchors {
		return ErrLimitExceeded
	}

	g.anchors = append(g.anchors, a)
	return nil
}

// Verify compares the local chain with every trusted anchor.
func (g *AuditChainTruncationGuard) Verify(stream string) (Verdict, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	s, ok := g.streams[stream]
	if !ok {
		return Verdict{}, ErrUnknownStream
	}

	head := localHead(s.records)
	trusted := g.trustedAnchorsLocked(stream)
	if len(trusted) == 0 {
		return Verdict{
			Stream:      stream,
			Status:      StatusUnanchored,
			Head:        head,
			TrustedHead: 0,
			Gaps:        []Gap{},
			Forks:       []Fork{},
		}, nil
	}

	trustedHead := highestAnchor(trusted)
	gaps := tailGaps(head, trustedHead)
	forks := findForks(s.records, trusted)
	status := StatusIntact
	if head < trustedHead {
		status = StatusTruncated
	} else if len(forks) > 0 {
		status = StatusFork
	}

	return Verdict{
		Stream:      stream,
		Status:      status,
		Head:        head,
		TrustedHead: trustedHead,
		Gaps:        gaps,
		Forks:       forks,
	}, nil
}

// Gaps returns the missing sequence ranges of a stream.
func (g *AuditChainTruncationGuard) Gaps(stream string) ([]Gap, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	s, ok := g.streams[stream]
	if !ok {
		return nil, ErrUnknownStream
	}
	trusted := g.trustedAnchorsLocked(stream)
	if len(trusted) == 0 {
		return []Gap{}, nil
	}
	head := localHead(s.records)
	trustedHead := highestAnchor(trusted)
	return tailGaps(head, trustedHead), nil
}

// RotateKey makes next the active signer and keeps the previous keys trusted.
func (g *AuditChainTruncationGuard) RotateKey(next Signer) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if next == nil {
		return ErrNilSigner
	}
	keyID := next.KeyID()
	if strings.TrimSpace(keyID) == "" {
		return ErrNilSigner
	}
	if ks, ok := g.keys[keyID]; ok {
		ks.signer = next
	} else {
		g.keys[keyID] = &keyState{signer: next}
	}
	g.signer = next
	g.activeKeyID = keyID
	return nil
}

// RetireKey stops accepting anchors signed by keyID beyond afterSeq.
func (g *AuditChainTruncationGuard) RetireKey(keyID string, afterSeq uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	ks, ok := g.keys[keyID]
	if !ok {
		return ErrUnknownKey
	}
	if !ks.retired {
		ks.retired = true
		ks.afterSeq = afterSeq
	} else if afterSeq > ks.afterSeq {
		ks.afterSeq = afterSeq
	}
	return nil
}

// Snapshot serialises streams, records, anchors and key retirement state.
func (g *AuditChainTruncationGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	state := snapshotState{
		Version:     snapshotVersion,
		ActiveKeyID: g.activeKeyID,
		Streams:     make([]snapshotStream, 0, len(g.streams)),
		Anchors:     append([]Anchor(nil), g.anchors...),
		Keys:        make([]snapshotKey, 0, len(g.keys)),
	}
	streamIDs := make([]string, 0, len(g.streams))
	for id := range g.streams {
		streamIDs = append(streamIDs, id)
	}
	sort.Strings(streamIDs)
	for _, id := range streamIDs {
		s := g.streams[id]
		state.Streams = append(state.Streams, snapshotStream{
			ID:      id,
			Records: append([]Record(nil), s.records...),
		})
	}
	keyIDs := make([]string, 0, len(g.keys))
	for id := range g.keys {
		keyIDs = append(keyIDs, id)
	}
	sort.Strings(keyIDs)
	for _, id := range keyIDs {
		ks := g.keys[id]
		state.Keys = append(state.Keys, snapshotKey{
			KeyID:    id,
			Retired:  ks.retired,
			AfterSeq: ks.afterSeq,
		})
	}
	g.mu.RUnlock()

	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(stateBytes)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		State:    stateBytes,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state from a snapshot.
func (g *AuditChainTruncationGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(envelope.Checksum)) != 1 {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil || state.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	g.mu.RLock()
	available := make(map[string]Signer, len(g.keys))
	for id, ks := range g.keys {
		if ks.signer != nil {
			available[id] = ks.signer
		}
	}
	currentSigner := g.signer
	currentKeyID := g.activeKeyID
	g.mu.RUnlock()

	streams, anchors, keys, err := buildRestoredState(state, g.limits, available)
	if err != nil {
		return ErrBadSnapshot
	}

	activeID := state.ActiveKeyID
	var active Signer
	if ks, ok := keys[activeID]; ok && ks.signer != nil {
		active = ks.signer
	}
	if active == nil && currentSigner != nil {
		if _, ok := keys[currentKeyID]; ok {
			active = currentSigner
			activeID = currentKeyID
		} else {
			cid := currentSigner.KeyID()
			if _, ok := keys[cid]; ok {
				active = currentSigner
				activeID = cid
			}
		}
	}
	if active == nil {
		signable := make([]string, 0, len(keys))
		for id, ks := range keys {
			if ks.signer != nil {
				signable = append(signable, id)
			}
		}
		sort.Strings(signable)
		if len(signable) > 0 {
			activeID = signable[0]
			active = keys[activeID].signer
		}
	}

	g.mu.Lock()
	g.streams = streams
	g.anchors = anchors
	g.keys = keys
	g.signer = active
	g.activeKeyID = activeID
	g.mu.Unlock()
	return nil
}

type streamState struct {
	records []Record
}

type keyState struct {
	// signer is kept in memory so anchors signed by keys rotated out can still
	// be verified during the lifetime of this guard.
	signer   Signer
	retired  bool
	afterSeq uint64
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	State    json.RawMessage `json:"state"`
	Checksum string          `json:"checksum"`
}

type snapshotState struct {
	Version     int              `json:"version"`
	ActiveKeyID string           `json:"active_key_id,omitempty"`
	Streams     []snapshotStream `json:"streams"`
	Anchors     []Anchor         `json:"anchors"`
	Keys        []snapshotKey    `json:"keys"`
}

type snapshotStream struct {
	ID      string   `json:"id"`
	Records []Record `json:"records"`
}

type snapshotKey struct {
	KeyID    string `json:"key_id"`
	Retired  bool   `json:"retired"`
	AfterSeq uint64 `json:"after_seq,omitempty"`
}

func chainDigest(stream string, seq uint64, prev, payload string) string {
	h := sha256.New()
	writeDigestField(h, stream)
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], seq)
	h.Write(seqBytes[:])
	writeDigestField(h, prev)
	writeDigestField(h, payload)
	return hex.EncodeToString(h.Sum(nil))
}

func writeDigestField(h hash.Hash, s string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(s)))
	h.Write(length[:])
	h.Write([]byte(s))
}

func anchorComplete(a Anchor) bool {
	return strings.TrimSpace(a.Stream) != "" &&
		a.Seq > 0 &&
		strings.TrimSpace(a.Digest) != "" &&
		strings.TrimSpace(a.KeyID) != "" &&
		strings.TrimSpace(a.Signature) != ""
}

func sameAnchorClaim(a, b Anchor) bool {
	return a.Stream == b.Stream &&
		a.Seq == b.Seq &&
		a.Digest == b.Digest &&
		a.KeyID == b.KeyID
}

func (g *AuditChainTruncationGuard) hasExactAnchor(a Anchor) bool {
	for _, existing := range g.anchors {
		if existing == a {
			return true
		}
	}
	return false
}

func (g *AuditChainTruncationGuard) trustedAnchorsLocked(stream string) []Anchor {
	trusted := make([]Anchor, 0, len(g.anchors))
	for _, a := range g.anchors {
		if a.Stream != stream {
			continue
		}
		ks, ok := g.keys[a.KeyID]
		if !ok {
			continue
		}
		if ks.retired && a.Seq > ks.afterSeq {
			continue
		}
		trusted = append(trusted, a)
	}
	return trusted
}

func localHead(records []Record) uint64 {
	if len(records) == 0 {
		return 0
	}
	return records[len(records)-1].Seq
}

func highestAnchor(anchors []Anchor) uint64 {
	var highest uint64
	for _, a := range anchors {
		if a.Seq > highest {
			highest = a.Seq
		}
	}
	return highest
}

func tailGaps(head, trustedHead uint64) []Gap {
	if head >= trustedHead {
		return []Gap{}
	}
	return []Gap{{From: head + 1, To: trustedHead, Kind: GapTailTruncation}}
}

func findForks(records []Record, anchors []Anchor) []Fork {
	digests := make(map[uint64]map[string]struct{})
	add := func(seq uint64, digest string) {
		set, ok := digests[seq]
		if !ok {
			set = make(map[string]struct{})
			digests[seq] = set
		}
		set[digest] = struct{}{}
	}
	for _, r := range records {
		add(r.Seq, r.Digest)
	}
	for _, a := range anchors {
		add(a.Seq, a.Digest)
	}

	seqs := make([]uint64, 0, len(digests))
	for seq, set := range digests {
		if len(set) > 1 {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	forks := make([]Fork, 0, len(seqs))
	for _, seq := range seqs {
		values := make([]string, 0, len(digests[seq]))
		for digest := range digests[seq] {
			values = append(values, digest)
		}
		sort.Strings(values)
		forks = append(forks, Fork{Seq: seq, Digests: values})
	}
	return forks
}

func buildRestoredState(state snapshotState, limits Limits, available map[string]Signer) (map[string]*streamState, []Anchor, map[string]*keyState, error) {
	if limits.MaxStreams > 0 && len(state.Streams) > limits.MaxStreams {
		return nil, nil, nil, ErrBadSnapshot
	}
	streams := make(map[string]*streamState, len(state.Streams))
	for _, item := range state.Streams {
		if strings.TrimSpace(item.ID) == "" {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := streams[item.ID]; exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		if limits.MaxRecords > 0 && len(item.Records) > limits.MaxRecords {
			return nil, nil, nil, ErrBadSnapshot
		}
		s := &streamState{records: make([]Record, 0, len(item.Records))}
		prev := ""
		for i, r := range item.Records {
			if r.Stream != item.ID || r.Seq != uint64(i+1) || strings.TrimSpace(r.Payload) == "" {
				return nil, nil, nil, ErrBadSnapshot
			}
			if r.Digest != chainDigest(item.ID, r.Seq, prev, r.Payload) {
				return nil, nil, nil, ErrBadSnapshot
			}
			prev = r.Digest
			s.records = append(s.records, r)
		}
		streams[item.ID] = s
	}

	keys := make(map[string]*keyState, len(state.Keys))
	for _, item := range state.Keys {
		if strings.TrimSpace(item.KeyID) == "" {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := keys[item.KeyID]; exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		keys[item.KeyID] = &keyState{
			signer:   available[item.KeyID],
			retired:  item.Retired,
			afterSeq: item.AfterSeq,
		}
	}
	if state.ActiveKeyID != "" {
		if _, exists := keys[state.ActiveKeyID]; !exists {
			return nil, nil, nil, ErrBadSnapshot
		}
	}

	if limits.MaxAnchors > 0 && len(state.Anchors) > limits.MaxAnchors {
		return nil, nil, nil, ErrBadSnapshot
	}
	anchors := make([]Anchor, 0, len(state.Anchors))
	for _, a := range state.Anchors {
		if !anchorComplete(a) {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := streams[a.Stream]; !exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := keys[a.KeyID]; !exists {
			return nil, nil, nil, ErrBadSnapshot
		}
		anchors = append(anchors, a)
	}
	return streams, anchors, keys, nil
}
