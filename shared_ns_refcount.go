// Package task manages the lifecycle of shared namespaces with reference
// counting and spec compatibility checks.
package task

import (
	"errors"
	"strings"
	"sync"
)

var errNotImplemented = errors.New("not implemented")

// Errors returned by the reference counter.
var (
	ErrBadNamespace     = errors.New("namespace name is required")
	ErrBadKind          = errors.New("namespace kind is not supported")
	ErrNamespaceExists  = errors.New("namespace already exists")
	ErrUnknownNamespace = errors.New("namespace is unknown")
	ErrBadMember        = errors.New("member name is required")
	ErrSpecConflict     = errors.New("member spec conflicts with the namespace spec")
	ErrNotMember        = errors.New("member has not joined the namespace")
	ErrNamespaceBusy    = errors.New("namespace still has members")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// NsKind is the kernel namespace kind a shared namespace represents.
type NsKind string

// Supported namespace kinds.
const (
	KindNetwork NsKind = "net"
	KindIPC     NsKind = "ipc"
	KindUTS     NsKind = "uts"
	KindPID     NsKind = "pid"
)

// NsSpec is the sharing specification every member must agree on.
type NsSpec struct {
	Mode   string
	Owner  int
	Labels []string
}

// SpecConflictError names the spec fields that differ.
type SpecConflictError struct {
	Fields []string
}

// Error implements the error interface.
func (e *SpecConflictError) Error() string {
	return "namespace spec conflict: " + strings.Join(e.Fields, ", ")
}

// Unwrap lets callers match ErrSpecConflict with errors.Is.
func (e *SpecConflictError) Unwrap() error { return ErrSpecConflict }

// SharedNsRefcount owns the namespace table and the per namespace member sets.
type SharedNsRefcount struct {
	mu      sync.Mutex
	kind    map[string]NsKind
	spec    map[string]NsSpec
	members map[string]map[string]NsSpec
}

// NewSharedNsRefcount builds an empty reference counter.
func NewSharedNsRefcount() *SharedNsRefcount {
	return &SharedNsRefcount{
		kind:    make(map[string]NsKind),
		spec:    make(map[string]NsSpec),
		members: make(map[string]map[string]NsSpec),
	}
}

// Create registers one shared namespace with its kind.
func (r *SharedNsRefcount) Create(ns string, kind NsKind) error { return errNotImplemented }

// Join adds one member to a namespace after checking its spec.
func (r *SharedNsRefcount) Join(ns string, member string, want NsSpec) error {
	return errNotImplemented
}

// Leave removes one member and decrements the reference count.
func (r *SharedNsRefcount) Leave(ns string, member string) error { return errNotImplemented }

// Refs reports the reference count of one namespace.
func (r *SharedNsRefcount) Refs(ns string) int { return 0 }

// Destroy removes a namespace whose reference count reached zero.
func (r *SharedNsRefcount) Destroy(ns string) error { return errNotImplemented }

// Members lists the members of one namespace ordered by name.
func (r *SharedNsRefcount) Members(ns string) []string { return nil }

// Kind reports the kind of one namespace.
func (r *SharedNsRefcount) Kind(ns string) (NsKind, error) { return "", errNotImplemented }

// Spec reports the canonical sharing spec of one namespace.
func (r *SharedNsRefcount) Spec(ns string) (NsSpec, error) { return NsSpec{}, errNotImplemented }

// Namespaces lists the shared namespaces ordered by name.
func (r *SharedNsRefcount) Namespaces() []string { return nil }

// Reconcile drops members that are no longer present in the alive set.
func (r *SharedNsRefcount) Reconcile(alive []string) error { return errNotImplemented }

// Snapshot serialises the namespace table, specs and member sets.
func (r *SharedNsRefcount) Snapshot() ([]byte, error) { return nil, errNotImplemented }

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *SharedNsRefcount) Restore(data []byte) error { return errNotImplemented }
