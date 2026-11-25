// Package task runs batch ACL changes on managed paths as one transaction and
// keeps the derived mask in step with the stored entries.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Perm is a permission bit set carried by an ACL entry.
type Perm uint16

// Permission bits understood by the executor.
const (
	PermNone      Perm = 0
	PermRead      Perm = 1 << 0
	PermWrite     Perm = 1 << 1
	PermExecute   Perm = 1 << 2
	PermDelete    Perm = 1 << 3
	PermReadACL   Perm = 1 << 4
	PermChangeACL Perm = 1 << 5
	PermTakeOwn   Perm = 1 << 6
	PermAll       Perm = PermRead | PermWrite | PermExecute | PermDelete | PermReadACL | PermChangeACL | PermTakeOwn
)

// EntryKind names the class of one ACL entry.
type EntryKind string

// Entry classes used on a managed path.
const (
	KindOwner EntryKind = "owner"
	KindUser  EntryKind = "user"
	KindGroup EntryKind = "group"
	KindMask  EntryKind = "mask"
	KindOther EntryKind = "other"
)

// Entry is a single ACL entry. Default marks a directory default entry that
// only applies to objects created later.
type Entry struct {
	Kind    EntryKind `json:"kind"`
	ID      uint32    `json:"id"`
	Bits    Perm      `json:"bits"`
	Default bool      `json:"default"`
}

// Principal is the acting account together with its group expansion.
type Principal struct {
	UID    uint32   `json:"uid"`
	GID    uint32   `json:"gid"`
	Groups []uint32 `json:"groups"`
}

// GrantRequest is one batch of ACL edits aimed at a single path.
type GrantRequest struct {
	Actor   Principal `json:"actor"`
	Path    string    `json:"path"`
	Ticket  string    `json:"ticket"`
	Adds    []Entry   `json:"adds"`
	Removes []Entry   `json:"removes"`
}

// AclDiff is the preview of a grant request.
type AclDiff struct {
	Path       string  `json:"path"`
	Before     []Entry `json:"before"`
	After      []Entry `json:"after"`
	Added      []Entry `json:"added"`
	Removed    []Entry `json:"removed"`
	BeforeMask Perm    `json:"before_mask"`
	AfterMask  Perm    `json:"after_mask"`
	BeforeRev  uint64  `json:"before_rev"`
	AfterRev   uint64  `json:"after_rev"`
}

// ApplyResult reports what an applied batch changed.
type ApplyResult struct {
	Path    string `json:"path"`
	Rev     uint64 `json:"rev"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Mask    Perm   `json:"mask"`
	Seq     uint64 `json:"seq"`
}

// AuditRecord is one line of the change log.
type AuditRecord struct {
	Seq     uint64 `json:"seq"`
	Path    string `json:"path"`
	Ticket  string `json:"ticket"`
	Actor   uint32 `json:"actor"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Rev     uint64 `json:"rev"`
}

// Limits caps how much state one executor keeps.
type Limits struct {
	MaxPaths          int `json:"max_paths"`
	MaxEntriesPerPath int `json:"max_entries_per_path"`
}

// Errors reported by the executor.
var (
	ErrBlankPath      = errors.New("path is required")
	ErrUnknownPath    = errors.New("path is not managed")
	ErrPathLimit      = errors.New("managed path limit reached")
	ErrEntryLimit     = errors.New("entry limit reached for path")
	ErrNoActor        = errors.New("actor principal is required")
	ErrBlankTicket    = errors.New("change ticket is required")
	ErrTicketReused   = errors.New("change ticket already used on path")
	ErrNotAllowed     = errors.New("actor lacks change ACL permission")
	ErrGrantTooBroad  = errors.New("actor cannot grant permissions it does not hold")
	ErrUnknownEntry   = errors.New("entry does not exist on path")
	ErrDuplicateEntry = errors.New("duplicate entry")
	ErrMaskEntry      = errors.New("mask entry is derived and cannot be edited")
	ErrBadEntry       = errors.New("entry is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
	ErrConflict       = errors.New("path is being changed by another request")
)

type pathState struct {
	entries []Entry
	rev     uint64
	tickets map[string]struct{}
}

// AclGrantTx applies batch ACL edits path by path.
type AclGrantTx struct {
	mu     sync.Mutex
	limits Limits
	paths  map[string]*pathState
	seq    uint64
	audit  []AuditRecord
	busy   map[string]bool
}

// NewAclGrantTx builds an executor with the given limits.
func NewAclGrantTx(l Limits) *AclGrantTx {
	return &AclGrantTx{
		limits: l,
		paths:  make(map[string]*pathState),
		busy:   make(map[string]bool),
	}
}

func (x *AclGrantTx) initLocked() {
	if x.paths == nil {
		x.paths = make(map[string]*pathState)
	}
	if x.busy == nil {
		x.busy = make(map[string]bool)
	}
}

func (x *AclGrantTx) pathLimitReachedLocked() bool {
	return x.limits.MaxPaths > 0 && len(x.paths) >= x.limits.MaxPaths
}

func (x *AclGrantTx) entryLimitExceeded(n int) bool {
	return x.limits.MaxEntriesPerPath > 0 && n > x.limits.MaxEntriesPerPath
}

// AddPath registers a managed path with its owner and initial entries.
func (x *AclGrantTx) AddPath(path string, owner uint32, entries []Entry) error {
	if strings.TrimSpace(path) == "" {
		return ErrBlankPath
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	if _, exists := x.paths[path]; exists {
		return ErrDuplicateEntry
	}
	if x.pathLimitReachedLocked() {
		return ErrPathLimit
	}

	canonical, _, err := buildEntries(entries, &owner)
	if err != nil {
		return err
	}
	if x.entryLimitExceeded(len(canonical)) {
		return ErrEntryLimit
	}

	x.paths[path] = &pathState{
		entries: canonical,
		tickets: make(map[string]struct{}),
	}
	return nil
}

// Preview validates a batch and returns the difference it would produce.
func (x *AclGrantTx) Preview(req GrantRequest) (AclDiff, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	ps, err := x.requestStateLocked(req)
	if err != nil {
		return AclDiff{}, err
	}

	planned, err := x.plan(req, ps, true, false)
	if err != nil {
		return AclDiff{}, err
	}
	if x.entryLimitExceeded(len(planned.entries)) {
		return AclDiff{}, ErrEntryLimit
	}

	return AclDiff{
		Path:       req.Path,
		Before:     cloneEntries(ps.entries),
		After:      planned.entries,
		Added:      planned.added,
		Removed:    planned.removed,
		BeforeMask: storedMask(ps.entries),
		AfterMask:  planned.mask,
		BeforeRev:  ps.rev,
		AfterRev:   ps.rev + 1,
	}, nil
}

// Apply validates a batch and commits it atomically.
func (x *AclGrantTx) Apply(req GrantRequest) (ApplyResult, error) {
	if strings.TrimSpace(req.Path) == "" {
		return ApplyResult{}, ErrBlankPath
	}
	if !x.beginWrite(req.Path) {
		return ApplyResult{}, ErrConflict
	}
	defer x.endWrite(req.Path)

	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	ps, err := x.requestStateLocked(req)
	if err != nil {
		return ApplyResult{}, err
	}

	planned, err := x.plan(req, ps, true, true)
	if err != nil {
		return ApplyResult{}, err
	}
	if x.entryLimitExceeded(len(planned.entries)) {
		return ApplyResult{}, ErrEntryLimit
	}

	rev := ps.rev + 1
	seq := x.seq + 1

	ps.entries = planned.entries
	ps.rev = rev
	ps.tickets[req.Ticket] = struct{}{}
	x.seq = seq
	x.audit = append(x.audit, AuditRecord{
		Seq:     seq,
		Path:    req.Path,
		Ticket:  req.Ticket,
		Actor:   req.Actor.UID,
		Added:   len(planned.added),
		Removed: len(planned.removed),
		Rev:     rev,
	})

	return ApplyResult{
		Path:    req.Path,
		Rev:     rev,
		Added:   len(planned.added),
		Removed: len(planned.removed),
		Mask:    planned.mask,
		Seq:     seq,
	}, nil
}

// Revoke removes the direct entries that belong to one principal.
func (x *AclGrantTx) Revoke(path string, p Principal) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, ErrBlankPath
	}
	if !x.beginWrite(path) {
		return 0, ErrConflict
	}
	defer x.endWrite(path)

	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	ps, exists := x.paths[path]
	if !exists {
		return 0, ErrUnknownPath
	}

	groups := make(map[uint32]struct{}, len(p.Groups)+1)
	groups[p.GID] = struct{}{}
	for _, gid := range p.Groups {
		groups[gid] = struct{}{}
	}

	work := withoutMask(ps.entries)
	kept := make([]Entry, 0, len(work))
	removed := 0
	for _, e := range work {
		match := false
		switch e.Kind {
		case KindUser:
			match = e.ID == p.UID
		case KindGroup:
			_, match = groups[e.ID]
		}
		if match {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed == 0 {
		return 0, ErrUnknownEntry
	}

	canonical, _, err := buildEntries(kept, nil)
	if err != nil {
		return 0, err
	}
	if x.entryLimitExceeded(len(canonical)) {
		return 0, ErrEntryLimit
	}

	rev := ps.rev + 1
	seq := x.seq + 1

	ps.entries = canonical
	ps.rev = rev
	x.seq = seq
	x.audit = append(x.audit, AuditRecord{
		Seq:     seq,
		Path:    path,
		Ticket:  "",
		Actor:   p.UID,
		Added:   0,
		Removed: removed,
		Rev:     rev,
	})

	return removed, nil
}

// Effective returns the permissions a subject really holds on a path.
func (x *AclGrantTx) Effective(path string, uid, gid uint32, groups []uint32) (Perm, error) {
	if strings.TrimSpace(path) == "" {
		return PermNone, ErrBlankPath
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	ps, exists := x.paths[path]
	if !exists {
		return PermNone, ErrUnknownPath
	}
	return effectiveOf(ps.entries, uid, gid, groups), nil
}

// Entries returns the canonical entry list of a path.
func (x *AclGrantTx) Entries(path string) ([]Entry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrBlankPath
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	ps, exists := x.paths[path]
	if !exists {
		return nil, ErrUnknownPath
	}
	return cloneEntries(ps.entries), nil
}

// Revision returns the grant watermark of a path.
func (x *AclGrantTx) Revision(path string) (uint64, error) {
	if strings.TrimSpace(path) == "" {
		return 0, ErrBlankPath
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	ps, exists := x.paths[path]
	if !exists {
		return 0, ErrUnknownPath
	}
	return ps.rev, nil
}

const snapshotVersion = 1

type snapshotEnvelope struct {
	Version int             `json:"version"`
	Digest  string          `json:"digest"`
	Payload json.RawMessage `json:"payload"`
}

type snapshotBody struct {
	Seq   uint64         `json:"seq"`
	Paths []snapshotPath `json:"paths"`
	Audit []AuditRecord  `json:"audit"`
}

type snapshotPath struct {
	Path    string   `json:"path"`
	Entries []Entry  `json:"entries"`
	Rev     uint64   `json:"rev"`
	Tickets []string `json:"tickets"`
}

// Snapshot serialises the whole executor state.
func (x *AclGrantTx) Snapshot() ([]byte, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	body := snapshotBody{
		Seq:   x.seq,
		Audit: cloneAudit(x.audit),
	}

	paths := make([]string, 0, len(x.paths))
	for path := range x.paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		ps := x.paths[path]
		tickets := make([]string, 0, len(ps.tickets))
		for ticket := range ps.tickets {
			tickets = append(tickets, ticket)
		}
		sort.Strings(tickets)
		body.Paths = append(body.Paths, snapshotPath{
			Path:    path,
			Entries: cloneEntries(ps.entries),
			Rev:     ps.rev,
			Tickets: tickets,
		})
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	env := snapshotEnvelope{
		Version: snapshotVersion,
		Digest:  hex.EncodeToString(sum[:]),
		Payload: payload,
	}
	return json.Marshal(env)
}

// Restore replaces the executor state with a previous snapshot.
func (x *AclGrantTx) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var env snapshotEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return ErrBadSnapshot
	}
	if env.Version != snapshotVersion || env.Digest == "" || len(env.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(env.Payload)
	if !strings.EqualFold(env.Digest, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var body snapshotBody
	if err := json.Unmarshal(env.Payload, &body); err != nil {
		return ErrBadSnapshot
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	if x.limits.MaxPaths > 0 && len(body.Paths) > x.limits.MaxPaths {
		return ErrPathLimit
	}

	restored := make(map[string]*pathState, len(body.Paths))
	for _, sp := range body.Paths {
		if strings.TrimSpace(sp.Path) == "" {
			return ErrBadSnapshot
		}
		if _, exists := restored[sp.Path]; exists {
			return ErrBadSnapshot
		}
		if x.entryLimitExceeded(len(sp.Entries)) {
			return ErrEntryLimit
		}

		entries, _, err := validateStoredEntries(sp.Entries)
		if err != nil {
			return ErrBadSnapshot
		}

		tickets := make(map[string]struct{}, len(sp.Tickets))
		for _, ticket := range sp.Tickets {
			if strings.TrimSpace(ticket) == "" {
				return ErrBadSnapshot
			}
			if _, exists := tickets[ticket]; exists {
				return ErrBadSnapshot
			}
			tickets[ticket] = struct{}{}
		}

		restored[sp.Path] = &pathState{
			entries: entries,
			rev:     sp.Rev,
			tickets: tickets,
		}
	}
	if x.limits.MaxPaths > 0 && len(restored) > x.limits.MaxPaths {
		return ErrPathLimit
	}

	var lastSeq uint64
	for _, rec := range body.Audit {
		if rec.Seq == 0 || rec.Seq <= lastSeq || rec.Seq > body.Seq {
			return ErrBadSnapshot
		}
		ps, exists := restored[rec.Path]
		if !exists || rec.Rev == 0 || rec.Rev > ps.rev {
			return ErrBadSnapshot
		}
		lastSeq = rec.Seq
	}

	for _, active := range x.busy {
		if active {
			return ErrConflict
		}
	}

	for path, restoredPath := range restored {
		if current, exists := x.paths[path]; exists && current.rev > restoredPath.rev {
			restoredPath.rev = current.rev
		}
	}
	seq := body.Seq
	if x.seq > seq {
		seq = x.seq
	}

	x.paths = restored
	x.seq = seq
	x.audit = cloneAudit(body.Audit)
	return nil
}

// Audit returns the change log in ascending sequence order.
func (x *AclGrantTx) Audit() []AuditRecord {
	x.mu.Lock()
	defer x.mu.Unlock()

	out := cloneAudit(x.audit)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Paths returns the managed paths in ascending order.
func (x *AclGrantTx) Paths() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()

	paths := make([]string, 0, len(x.paths))
	for path := range x.paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

type entryKey struct {
	kind EntryKind
	id   uint32
}

func entryKeyOf(e Entry) entryKey {
	return entryKey{kind: e.Kind, id: e.ID}
}

func kindOrder(kind EntryKind) int {
	switch kind {
	case KindOwner:
		return 0
	case KindMask:
		return 1
	case KindUser:
		return 2
	case KindGroup:
		return 3
	case KindOther:
		return 4
	default:
		return 5
	}
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		oi, oj := kindOrder(entries[i].Kind), kindOrder(entries[j].Kind)
		if oi != oj {
			return oi < oj
		}
		if entries[i].Default != entries[j].Default {
			return !entries[i].Default
		}
		if entries[i].ID != entries[j].ID {
			return entries[i].ID < entries[j].ID
		}
		if entries[i].Bits != entries[j].Bits {
			return entries[i].Bits < entries[j].Bits
		}
		return false
	})
}

func buildEntries(input []Entry, owner *uint32) ([]Entry, Perm, error) {
	canonical := make([]Entry, 0, len(input)+1)
	seen := make(map[entryKey]struct{}, len(input))
	owners, others := 0, 0

	for _, e := range input {
		switch e.Kind {
		case KindOwner:
			owners++
		case KindUser, KindGroup:
		case KindOther:
			others++
		case KindMask:
			return nil, PermNone, ErrMaskEntry
		default:
			return nil, PermNone, ErrBadEntry
		}

		key := entryKeyOf(e)
		if _, exists := seen[key]; exists {
			if e.Kind == KindOwner || e.Kind == KindOther {
				return nil, PermNone, ErrBadEntry
			}
			return nil, PermNone, ErrDuplicateEntry
		}
		seen[key] = struct{}{}
		canonical = append(canonical, e)
	}

	if owners != 1 || others != 1 {
		return nil, PermNone, ErrBadEntry
	}
	if owner != nil {
		for _, e := range canonical {
			if e.Kind == KindOwner && e.ID != *owner {
				return nil, PermNone, ErrBadEntry
			}
		}
	}

	mask := rawMask(canonical)
	canonical = append(canonical, Entry{Kind: KindMask, ID: 0, Bits: mask})
	sortEntries(canonical)
	return canonical, mask, nil
}

func rawMask(entries []Entry) Perm {
	var mask Perm
	for _, e := range entries {
		if e.Kind == KindUser || e.Kind == KindGroup {
			mask |= e.Bits
		}
	}
	return mask
}

func storedMask(entries []Entry) Perm {
	for _, e := range entries {
		if e.Kind == KindMask {
			return e.Bits
		}
	}
	return rawMask(entries)
}

func withoutMask(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Kind != KindMask {
			out = append(out, e)
		}
	}
	return out
}

func cloneEntries(entries []Entry) []Entry {
	return append([]Entry(nil), entries...)
}

func cloneAudit(records []AuditRecord) []AuditRecord {
	return append([]AuditRecord(nil), records...)
}

type planResult struct {
	entries []Entry
	added   []Entry
	removed []Entry
	mask    Perm
}

func (x *AclGrantTx) requestStateLocked(req GrantRequest) (*pathState, error) {
	if strings.TrimSpace(req.Path) == "" {
		return nil, ErrBlankPath
	}
	ps, exists := x.paths[req.Path]
	if !exists {
		return nil, ErrUnknownPath
	}
	if strings.TrimSpace(req.Ticket) == "" {
		return nil, ErrBlankTicket
	}
	if req.Actor.UID == 0 && req.Actor.GID == 0 && len(req.Actor.Groups) == 0 {
		return nil, ErrNoActor
	}
	if _, used := ps.tickets[req.Ticket]; used {
		return nil, ErrTicketReused
	}
	return ps, nil
}

func (x *AclGrantTx) plan(req GrantRequest, ps *pathState, requireChangeACL, enforceSubset bool) (planResult, error) {
	var effective Perm
	if requireChangeACL || enforceSubset {
		effective = effectiveOf(ps.entries, req.Actor.UID, req.Actor.GID, req.Actor.Groups)
		if requireChangeACL && effective&PermChangeACL == 0 {
			return planResult{}, ErrNotAllowed
		}
	}

	work := withoutMask(ps.entries)
	removed := make([]Entry, 0, len(req.Removes))

	for _, target := range req.Removes {
		switch target.Kind {
		case KindOwner, KindUser, KindGroup, KindOther:
		case KindMask:
			return planResult{}, ErrMaskEntry
		default:
			return planResult{}, ErrBadEntry
		}

		index := -1
		for i := range work {
			if work[i].Kind == target.Kind && work[i].ID == target.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return planResult{}, ErrUnknownEntry
		}
		removed = append(removed, work[index])
		work = append(work[:index], work[index+1:]...)
	}

	added := make([]Entry, 0, len(req.Adds))
	seen := make(map[entryKey]struct{}, len(work)+len(req.Adds))
	for _, e := range work {
		seen[entryKeyOf(e)] = struct{}{}
	}

	for _, addition := range req.Adds {
		switch addition.Kind {
		case KindOwner, KindUser, KindGroup, KindOther:
		case KindMask:
			return planResult{}, ErrMaskEntry
		default:
			return planResult{}, ErrBadEntry
		}

		if enforceSubset && addition.Kind != KindGroup && addition.Bits&^effective != 0 {
			return planResult{}, ErrGrantTooBroad
		}

		key := entryKeyOf(addition)
		if _, exists := seen[key]; exists {
			return planResult{}, ErrDuplicateEntry
		}
		seen[key] = struct{}{}
		work = append(work, addition)
		added = append(added, addition)
	}

	canonical, mask, err := buildEntries(work, nil)
	if err != nil {
		return planResult{}, err
	}
	return planResult{
		entries: canonical,
		added:   added,
		removed: removed,
		mask:    mask,
	}, nil
}

func effectiveOf(entries []Entry, uid, gid uint32, groups []uint32) Perm {
	mask := storedMask(entries)

	membership := make(map[uint32]struct{}, len(groups)+1)
	membership[gid] = struct{}{}
	for _, group := range groups {
		membership[group] = struct{}{}
	}

	var effective Perm
	matched := false
	for _, e := range entries {
		if e.Default {
			continue
		}
		switch e.Kind {
		case KindOwner:
			if e.ID == uid {
				effective |= e.Bits
				matched = true
			}
		case KindUser:
			if e.ID == uid {
				effective |= e.Bits & mask
				matched = true
			}
		case KindGroup:
			if _, member := membership[e.ID]; member {
				effective |= e.Bits & mask
				matched = true
			}
		}
	}
	if !matched {
		for _, e := range entries {
			if e.Kind == KindOther && !e.Default {
				effective |= e.Bits
				break
			}
		}
	}
	return effective
}

func validateStoredEntries(entries []Entry) ([]Entry, Perm, error) {
	masks := 0
	raw := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == KindMask {
			masks++
			continue
		}
		raw = append(raw, e)
	}
	if masks != 1 {
		return nil, PermNone, ErrBadSnapshot
	}

	canonical, mask, err := buildEntries(raw, nil)
	if err != nil || len(canonical) != len(entries) {
		return nil, PermNone, ErrBadSnapshot
	}
	for i := range entries {
		if canonical[i] != entries[i] {
			return nil, PermNone, ErrBadSnapshot
		}
	}
	return canonical, mask, nil
}

func (x *AclGrantTx) beginWrite(path string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.initLocked()
	if x.busy[path] {
		return false
	}
	x.busy[path] = true
	return true
}

func (x *AclGrantTx) endWrite(path string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.busy, path)
}
