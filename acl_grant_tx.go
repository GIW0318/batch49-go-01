// Package task runs batch ACL changes on managed paths as one transaction and
// keeps the derived mask in step with the stored entries.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// AclGrantTx applies batch ACL edits path by path.
type AclGrantTx struct {
	limits Limits
}

// NewAclGrantTx builds an executor with the given limits.
func NewAclGrantTx(l Limits) *AclGrantTx {
	return &AclGrantTx{limits: l}
}

// AddPath registers a managed path with its owner and initial entries.
func (x *AclGrantTx) AddPath(path string, owner uint32, entries []Entry) error {
	return errNotImplemented
}

// Preview validates a batch and returns the difference it would produce.
func (x *AclGrantTx) Preview(req GrantRequest) (AclDiff, error) {
	return AclDiff{}, errNotImplemented
}

// Apply validates a batch and commits it atomically.
func (x *AclGrantTx) Apply(req GrantRequest) (ApplyResult, error) {
	return ApplyResult{}, errNotImplemented
}

// Revoke removes the direct entries that belong to one principal.
func (x *AclGrantTx) Revoke(path string, p Principal) (int, error) {
	return 0, errNotImplemented
}

// Effective returns the permissions a subject really holds on a path.
func (x *AclGrantTx) Effective(path string, uid, gid uint32, groups []uint32) (Perm, error) {
	return PermNone, errNotImplemented
}

// Entries returns the canonical entry list of a path.
func (x *AclGrantTx) Entries(path string) ([]Entry, error) {
	return nil, errNotImplemented
}

// Revision returns the grant watermark of a path.
func (x *AclGrantTx) Revision(path string) (uint64, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the whole executor state.
func (x *AclGrantTx) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the executor state with a previous snapshot.
func (x *AclGrantTx) Restore(data []byte) error {
	return errNotImplemented
}

// Audit returns the change log in ascending sequence order.
func (x *AclGrantTx) Audit() []AuditRecord {
	return nil
}

// Paths returns the managed paths in ascending order.
func (x *AclGrantTx) Paths() []string {
	return nil
}
