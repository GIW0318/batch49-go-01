// Package task runs the ticket flow of a service account key: request, review,
// issue, activate and finally disable.
package task

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Request states.
const (
	RequestPending  = "pending"
	RequestApproved = "approved"
	RequestRejected = "rejected"
	RequestIssued   = "issued"
)

// Key states.
const (
	KeyPendingActivation = "pending_activation"
	KeyActive            = "active"
	KeyDisabled          = "disabled"
	KeyExpired           = "expired"
)

// Errors reported by the issuer.
var (
	ErrBlankID         = errors.New("account, requester or approver id is required")
	ErrBadSpec         = errors.New("key name, scopes and ttl are required")
	ErrTTLTooLong      = errors.New("requested ttl exceeds the hard limit")
	ErrSelfApproval    = errors.New("requester cannot approve the own request")
	ErrRequestNotFound = errors.New("request not found")
	ErrRequestClosed   = errors.New("request is already closed")
	ErrNotApproved     = errors.New("request has not reached the approval quorum")
	ErrAlreadyIssued   = errors.New("request was already issued")
	ErrRequestLimit    = errors.New("pending request limit reached for this account")
	ErrKeyLimit        = errors.New("key limit reached for this account")
	ErrKeyNotFound     = errors.New("key not found")
	ErrTooEarly        = errors.New("key activation delay has not elapsed")
	ErrKeyExpired      = errors.New("key has expired")
	ErrKeyDisabled     = errors.New("key is disabled")
	ErrBadReason       = errors.New("disable reason is required")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// KeySpec is what the requester asks for.
type KeySpec struct {
	Name   string        `json:"name"`
	Scopes []string      `json:"scopes"`
	TTL    time.Duration `json:"ttl"`
}

// Request is one key ticket in the review flow.
type Request struct {
	ID           string   `json:"id"`
	AccountID    string   `json:"account_id"`
	RequesterID  string   `json:"requester_id"`
	Spec         KeySpec  `json:"spec"`
	Approvals    []string `json:"approvals"`
	Required     int      `json:"required"`
	State        string   `json:"state"`
	RejectReason string   `json:"reject_reason"`
	Seq          int64    `json:"seq"`
}

// IssuedKey is the one response that carries the plaintext secret.
type IssuedKey struct {
	KeyID      string    `json:"key_id"`
	RequestID  string    `json:"request_id"`
	AccountID  string    `json:"account_id"`
	Plaintext  string    `json:"plaintext"`
	Prefix     string    `json:"prefix"`
	Scopes     []string  `json:"scopes"`
	State      string    `json:"state"`
	IssuedAt   time.Time `json:"issued_at"`
	ActivateAt time.Time `json:"activate_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Seq        int64     `json:"seq"`
}

// KeyMeta is what the issuer exposes after issuance: never the plaintext.
type KeyMeta struct {
	KeyID      string    `json:"key_id"`
	AccountID  string    `json:"account_id"`
	Prefix     string    `json:"prefix"`
	Scopes     []string  `json:"scopes"`
	State      string    `json:"state"`
	ActivateAt time.Time `json:"activate_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Seq        int64     `json:"seq"`
}

// Options bounds the issuer and supplies the clock.
type Options struct {
	RequiredApprovals  int
	MaxTTL             time.Duration
	ActivationDelay    time.Duration
	MaxPendingRequests int
	MaxKeysPerAccount  int
	Now                func() time.Time
}

// keyRecord is the persisted key state. It deliberately has no plaintext field:
// the secret is returned exactly once and only its prefix is retained.
type keyRecord struct {
	KeyID         string    `json:"key_id"`
	RequestID     string    `json:"request_id"`
	AccountID     string    `json:"account_id"`
	Prefix        string    `json:"prefix"`
	Scopes        []string  `json:"scopes"`
	State         string    `json:"state"`
	IssuedAt      time.Time `json:"issued_at"`
	ActivateAt    time.Time `json:"activate_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Seq           int64     `json:"seq"`
	DisableReason string    `json:"disable_reason,omitempty"`
}

// ServiceAccountKeyIssuer owns the request table, the key table and the
// monotonic counters. A single mutex serialises every logical operation so
// check-then-act transitions and snapshots are atomic.
type ServiceAccountKeyIssuer struct {
	opts Options

	mu       sync.Mutex
	requests map[string]*Request
	keys     map[string]*keyRecord
	reqSeq   int64
	keySeq   int64
}

// NewServiceAccountKeyIssuer builds an issuer with the given options.
func NewServiceAccountKeyIssuer(opts Options) *ServiceAccountKeyIssuer {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RequiredApprovals <= 0 {
		opts.RequiredApprovals = 1
	}
	return &ServiceAccountKeyIssuer{
		opts:     opts,
		requests: make(map[string]*Request),
		keys:     make(map[string]*keyRecord),
	}
}

func (i *ServiceAccountKeyIssuer) now() time.Time {
	return i.opts.Now()
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// cloneRequest returns a value copy whose slices do not alias stored state.
func cloneRequest(req *Request) Request {
	cp := *req
	cp.Approvals = cloneStrings(req.Approvals)
	cp.Spec.Scopes = cloneStrings(req.Spec.Scopes)
	return cp
}

func toKeyMeta(rec *keyRecord) KeyMeta {
	return KeyMeta{
		KeyID:      rec.KeyID,
		AccountID:  rec.AccountID,
		Prefix:     rec.Prefix,
		Scopes:     cloneStrings(rec.Scopes),
		State:      rec.State,
		ActivateAt: rec.ActivateAt,
		ExpiresAt:  rec.ExpiresAt,
		Seq:        rec.Seq,
	}
}

// expireIfDueLocked performs the lazy automatic expiry. Manual disable is
// sticky and is never rewritten by the clock. Caller must hold i.mu.
func (i *ServiceAccountKeyIssuer) expireIfDueLocked(rec *keyRecord) {
	if rec.State != KeyPendingActivation && rec.State != KeyActive {
		return
	}
	if !i.now().Before(rec.ExpiresAt) {
		rec.State = KeyExpired
	}
}

// Request opens a key ticket for an account.
func (i *ServiceAccountKeyIssuer) Request(accountID, requesterID string, spec KeySpec) (Request, error) {
	accountID = strings.TrimSpace(accountID)
	requesterID = strings.TrimSpace(requesterID)
	if accountID == "" || requesterID == "" {
		return Request{}, ErrBlankID
	}
	if isBlank(spec.Name) {
		return Request{}, ErrBadSpec
	}
	if len(spec.Scopes) == 0 {
		return Request{}, ErrBadSpec
	}
	if spec.TTL <= 0 {
		return Request{}, ErrBadSpec
	}
	if spec.TTL > i.opts.MaxTTL {
		return Request{}, ErrTTLTooLong
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	pending := 0
	for _, req := range i.requests {
		if req.AccountID == accountID && req.State == RequestPending {
			pending++
		}
	}
	if i.opts.MaxPendingRequests > 0 && pending >= i.opts.MaxPendingRequests {
		return Request{}, ErrRequestLimit
	}

	i.reqSeq++
	req := &Request{
		ID:          fmt.Sprintf("req-%d", i.reqSeq),
		AccountID:   accountID,
		RequesterID: requesterID,
		Spec: KeySpec{
			Name:   strings.TrimSpace(spec.Name),
			Scopes: cloneStrings(spec.Scopes),
			TTL:    spec.TTL,
		},
		Approvals: []string{},
		Required:  i.opts.RequiredApprovals,
		State:     RequestPending,
		Seq:       i.reqSeq,
	}
	i.requests[req.ID] = req
	return cloneRequest(req), nil
}

// Approve records one reviewer vote and closes the quorum when reached.
func (i *ServiceAccountKeyIssuer) Approve(requestID, approverID string) (Request, error) {
	requestID = strings.TrimSpace(requestID)
	approverID = strings.TrimSpace(approverID)
	if requestID == "" || approverID == "" {
		return Request{}, ErrBlankID
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	req, ok := i.requests[requestID]
	if !ok {
		return Request{}, ErrRequestNotFound
	}

	// An identity veto never records a vote, regardless of ticket state.
	if approverID == req.RequesterID {
		return cloneRequest(req), ErrSelfApproval
	}
	if req.State != RequestPending {
		return cloneRequest(req), ErrRequestClosed
	}

	// A repeated vote from the same reviewer is deduplicated: double clicking
	// must never be able to pad the quorum.
	for _, existing := range req.Approvals {
		if existing == approverID {
			return cloneRequest(req), nil
		}
	}

	req.Approvals = append(req.Approvals, approverID)
	required := req.Required
	if required <= 0 {
		required = 1
	}
	if len(req.Approvals) >= required {
		req.State = RequestApproved
	}
	return cloneRequest(req), nil
}

// Reject terminates a ticket without issuing anything.
func (i *ServiceAccountKeyIssuer) Reject(requestID, approverID, reason string) (Request, error) {
	requestID = strings.TrimSpace(requestID)
	approverID = strings.TrimSpace(approverID)
	if requestID == "" || approverID == "" {
		return Request{}, ErrBlankID
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	req, ok := i.requests[requestID]
	if !ok {
		return Request{}, ErrRequestNotFound
	}
	if req.State != RequestPending {
		return cloneRequest(req), ErrRequestClosed
	}

	req.State = RequestRejected
	req.RejectReason = strings.TrimSpace(reason)
	return cloneRequest(req), nil
}

// Issue turns an approved ticket into a key and returns the plaintext once.
func (i *ServiceAccountKeyIssuer) Issue(requestID string) (IssuedKey, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return IssuedKey{}, ErrBlankID
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	req, ok := i.requests[requestID]
	if !ok {
		return IssuedKey{}, ErrRequestNotFound
	}

	// Already-issued is checked first so racing duplicates converge on the
	// specific ErrAlreadyIssued outcome.
	if req.State == RequestIssued {
		return IssuedKey{}, ErrAlreadyIssued
	}
	if req.State == RequestRejected {
		return IssuedKey{}, ErrRequestClosed
	}
	if req.State != RequestApproved {
		return IssuedKey{}, ErrNotApproved
	}

	if i.opts.MaxKeysPerAccount > 0 {
		keyCount := 0
		for _, rec := range i.keys {
			if rec.AccountID == req.AccountID {
				keyCount++
			}
		}
		if keyCount >= i.opts.MaxKeysPerAccount {
			return IssuedKey{}, ErrKeyLimit
		}
	}

	plaintext, err := generatePlaintext()
	if err != nil {
		return IssuedKey{}, err
	}

	issuedAt := i.now()
	i.keySeq++
	rec := &keyRecord{
		KeyID:      fmt.Sprintf("key-%d", i.keySeq),
		RequestID:  req.ID,
		AccountID:  req.AccountID,
		Prefix:     plaintext[:8],
		Scopes:     cloneStrings(req.Spec.Scopes),
		State:      KeyPendingActivation,
		IssuedAt:   issuedAt,
		ActivateAt: issuedAt.Add(i.opts.ActivationDelay),
		ExpiresAt:  issuedAt.Add(req.Spec.TTL),
		Seq:        i.keySeq,
	}
	i.keys[rec.KeyID] = rec
	req.State = RequestIssued

	return IssuedKey{
		KeyID:      rec.KeyID,
		RequestID:  rec.RequestID,
		AccountID:  rec.AccountID,
		Plaintext:  plaintext,
		Prefix:     rec.Prefix,
		Scopes:     cloneStrings(rec.Scopes),
		State:      rec.State,
		IssuedAt:   rec.IssuedAt,
		ActivateAt: rec.ActivateAt,
		ExpiresAt:  rec.ExpiresAt,
		Seq:        rec.Seq,
	}, nil
}

func generatePlaintext() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	// 32 random bytes become 64 hex characters, comfortably above the floor.
	return hex.EncodeToString(raw), nil
}

// Activate enables a key once its activation delay has elapsed.
func (i *ServiceAccountKeyIssuer) Activate(keyID string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	rec, ok := i.keys[keyID]
	if !ok {
		return ErrKeyNotFound
	}
	if rec.State == KeyDisabled {
		return ErrKeyDisabled
	}

	i.expireIfDueLocked(rec)
	if rec.State == KeyExpired {
		return ErrKeyExpired
	}

	if rec.State == KeyPendingActivation && i.now().Before(rec.ActivateAt) {
		return ErrTooEarly
	}

	rec.State = KeyActive
	return nil
}

// Disable turns a key off with an operator reason.
func (i *ServiceAccountKeyIssuer) Disable(keyID, reason string) error {
	if isBlank(reason) {
		return ErrBadReason
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	rec, ok := i.keys[keyID]
	if !ok {
		return ErrKeyNotFound
	}
	rec.State = KeyDisabled
	rec.DisableReason = strings.TrimSpace(reason)
	return nil
}

// List returns the key metadata of one account, oldest first.
func (i *ServiceAccountKeyIssuer) List(accountID string) []KeyMeta {
	accountID = strings.TrimSpace(accountID)

	i.mu.Lock()
	defer i.mu.Unlock()

	metas := make([]KeyMeta, 0)
	for _, rec := range i.keys {
		if rec.AccountID != accountID {
			continue
		}
		i.expireIfDueLocked(rec)
		metas = append(metas, toKeyMeta(rec))
	}
	sort.Slice(metas, func(a, b int) bool {
		return metas[a].Seq < metas[b].Seq
	})
	return metas
}

// Get reads one request record.
func (i *ServiceAccountKeyIssuer) Get(requestID string) (Request, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	req, ok := i.requests[requestID]
	if !ok {
		return Request{}, false
	}
	return cloneRequest(req), true
}

// LookupKey reads one key metadata record.
func (i *ServiceAccountKeyIssuer) LookupKey(keyID string) (KeyMeta, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()

	rec, ok := i.keys[keyID]
	if !ok {
		return KeyMeta{}, false
	}
	i.expireIfDueLocked(rec)
	return toKeyMeta(rec), true
}

const snapshotVersion = 1

// snapshotEnvelope seals the payload so a torn or tampered file is detected.
type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

// snapshotPayload carries requests, key metadata, schedules and counters. It
// references keyRecord, which has no plaintext field by construction.
type snapshotPayload struct {
	ReqSeq   int64       `json:"req_seq"`
	KeySeq   int64       `json:"key_seq"`
	Requests []Request   `json:"requests"`
	Keys     []keyRecord `json:"keys"`
}

// Snapshot serialises requests, key metadata, schedules and the counters.
func (i *ServiceAccountKeyIssuer) Snapshot() ([]byte, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	payload := snapshotPayload{
		ReqSeq:   i.reqSeq,
		KeySeq:   i.keySeq,
		Requests: make([]Request, 0, len(i.requests)),
		Keys:     make([]keyRecord, 0, len(i.keys)),
	}
	for _, req := range i.requests {
		payload.Requests = append(payload.Requests, cloneRequest(req))
	}
	sort.Slice(payload.Requests, func(a, b int) bool {
		return payload.Requests[a].Seq < payload.Requests[b].Seq
	})
	for _, rec := range i.keys {
		cp := *rec
		cp.Scopes = cloneStrings(rec.Scopes)
		payload.Keys = append(payload.Keys, cp)
	}
	sort.Slice(payload.Keys, func(a, b int) bool {
		return payload.Keys[a].Seq < payload.Keys[b].Seq
	})

	rawPayload, err := json.Marshal(&payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(rawPayload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(digest[:]),
		Payload:  rawPayload,
	}
	return json.Marshal(&envelope)
}

type restoredState struct {
	requests map[string]*Request
	keys     map[string]*keyRecord
	reqSeq   int64
	keySeq   int64
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (i *ServiceAccountKeyIssuer) Restore(data []byte) error {
	next, err := decodeSnapshot(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	i.mu.Lock()
	i.requests = next.requests
	i.keys = next.keys
	i.reqSeq = next.reqSeq
	i.keySeq = next.keySeq
	i.mu.Unlock()
	return nil
}

func decodeSnapshot(data []byte) (*restoredState, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("empty snapshot")
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d", envelope.Version)
	}
	if len(envelope.Payload) == 0 || len(envelope.Checksum) != sha256.Size*2 {
		return nil, errors.New("snapshot seal is missing")
	}
	digest := sha256.Sum256(envelope.Payload)
	if hex.EncodeToString(digest[:]) != envelope.Checksum {
		return nil, errors.New("snapshot checksum mismatch")
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.ReqSeq < 0 || payload.KeySeq < 0 {
		return nil, errors.New("negative counter")
	}

	state := &restoredState{
		requests: make(map[string]*Request, len(payload.Requests)),
		keys:     make(map[string]*keyRecord, len(payload.Keys)),
		reqSeq:   payload.ReqSeq,
		keySeq:   payload.KeySeq,
	}

	for idx := range payload.Requests {
		req := payload.Requests[idx]
		if isBlank(req.ID) {
			return nil, errors.New("request without id")
		}
		if _, duplicated := state.requests[req.ID]; duplicated {
			return nil, fmt.Errorf("duplicate request id %q", req.ID)
		}
		req.Approvals = cloneStrings(req.Approvals)
		req.Spec.Scopes = cloneStrings(req.Spec.Scopes)
		state.requests[req.ID] = &req
		if req.Seq > state.reqSeq {
			state.reqSeq = req.Seq
		}
	}

	for idx := range payload.Keys {
		rec := payload.Keys[idx]
		if isBlank(rec.KeyID) {
			return nil, errors.New("key without id")
		}
		if _, duplicated := state.keys[rec.KeyID]; duplicated {
			return nil, fmt.Errorf("duplicate key id %q", rec.KeyID)
		}
		rec.Scopes = cloneStrings(rec.Scopes)
		state.keys[rec.KeyID] = &rec
		if rec.Seq > state.keySeq {
			state.keySeq = rec.Seq
		}
	}

	return state, nil
}
