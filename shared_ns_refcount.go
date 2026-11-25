// Package task manages the lifecycle of shared namespaces with reference
// counting and spec compatibility checks.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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

func validKind(kind NsKind) bool {
	switch kind {
	case KindNetwork, KindIPC, KindUTS, KindPID:
		return true
	}
	return false
}

// normalizeSpec returns a copy of the spec with labels deduplicated and
// sorted so that specs can be compared field by field.
func normalizeSpec(spec NsSpec) NsSpec {
	labels := make([]string, 0, len(spec.Labels))
	seen := make(map[string]struct{}, len(spec.Labels))
	for _, label := range spec.Labels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return NsSpec{Mode: spec.Mode, Owner: spec.Owner, Labels: labels}
}

// specConflict compares the namespace spec with the wanted spec and reports
// the differing fields in the fixed order mode, owner, labels.
func specConflict(have, want NsSpec) *SpecConflictError {
	var fields []string
	if have.Mode != want.Mode {
		fields = append(fields, "mode")
	}
	if have.Owner != want.Owner {
		fields = append(fields, "owner")
	}
	if !equalLabels(have.Labels, want.Labels) {
		fields = append(fields, "labels")
	}
	if len(fields) == 0 {
		return nil
	}
	return &SpecConflictError{Fields: fields}
}

// equalLabels compares two already normalized label lists.
func equalLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Create registers one shared namespace with its kind.
func (r *SharedNsRefcount) Create(ns string, kind NsKind) error {
	if strings.TrimSpace(ns) == "" {
		return ErrBadNamespace
	}
	if !validKind(kind) {
		return ErrBadKind
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kind[ns]; ok {
		return ErrNamespaceExists
	}
	r.kind[ns] = kind
	r.members[ns] = make(map[string]NsSpec)
	return nil
}

// Join adds one member to a namespace after checking its spec.
func (r *SharedNsRefcount) Join(ns string, member string, want NsSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kind[ns]; !ok {
		return ErrUnknownNamespace
	}
	if strings.TrimSpace(member) == "" {
		return ErrBadMember
	}
	want = normalizeSpec(want)
	set := r.members[ns]
	if current, ok := set[member]; ok {
		// A repeated join only counts once, but a conflicting spec is
		// reported instead of being silently accepted.
		if conflict := specConflict(current, want); conflict != nil {
			return conflict
		}
		return nil
	}
	if len(set) == 0 {
		// The first member pins the namespace spec.
		r.spec[ns] = want
	} else if conflict := specConflict(r.spec[ns], want); conflict != nil {
		return conflict
	}
	set[member] = want
	return nil
}

// Leave removes one member and decrements the reference count.
func (r *SharedNsRefcount) Leave(ns string, member string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.members[ns]
	if !ok {
		return ErrUnknownNamespace
	}
	if _, ok := set[member]; !ok {
		return ErrNotMember
	}
	delete(set, member)
	if len(set) == 0 {
		// The next join becomes the first member again and re-pins the spec.
		delete(r.spec, ns)
	}
	return nil
}

// Refs reports the reference count of one namespace.
func (r *SharedNsRefcount) Refs(ns string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.members[ns])
}

// Destroy removes a namespace whose reference count reached zero.
func (r *SharedNsRefcount) Destroy(ns string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.members[ns]
	if !ok {
		return ErrUnknownNamespace
	}
	if len(set) != 0 {
		return ErrNamespaceBusy
	}
	delete(r.kind, ns)
	delete(r.spec, ns)
	delete(r.members, ns)
	return nil
}

// Members lists the members of one namespace ordered by name.
func (r *SharedNsRefcount) Members(ns string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.members[ns]
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Kind reports the kind of one namespace.
func (r *SharedNsRefcount) Kind(ns string) (NsKind, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kind, ok := r.kind[ns]
	if !ok {
		return "", ErrUnknownNamespace
	}
	return kind, nil
}

// Spec reports the canonical sharing spec of one namespace.
func (r *SharedNsRefcount) Spec(ns string) (NsSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kind[ns]; !ok {
		return NsSpec{}, ErrUnknownNamespace
	}
	spec := r.spec[ns]
	spec.Labels = append([]string(nil), spec.Labels...)
	return spec, nil
}

// Namespaces lists the shared namespaces ordered by name.
func (r *SharedNsRefcount) Namespaces() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.kind))
	for name := range r.kind {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Reconcile drops members that are no longer present in the alive set.
func (r *SharedNsRefcount) Reconcile(alive []string) error {
	keep := make(map[string]struct{}, len(alive))
	for _, name := range alive {
		keep[name] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for ns, set := range r.members {
		for member := range set {
			if _, ok := keep[member]; ok {
				continue
			}
			delete(set, member)
		}
		if len(set) == 0 {
			delete(r.spec, ns)
		}
	}
	return nil
}

// snapshotSpec is the serialised form of one NsSpec.
type snapshotSpec struct {
	Mode   string   `json:"mode"`
	Owner  int      `json:"owner"`
	Labels []string `json:"labels"`
}

// snapshotNamespace is the serialised form of one namespace entry.
type snapshotNamespace struct {
	Kind    NsKind        `json:"kind"`
	Spec    *snapshotSpec `json:"spec,omitempty"`
	Members []string      `json:"members"`
}

// snapshotFile is the serialised form of the whole table.
type snapshotFile struct {
	Namespaces map[string]snapshotNamespace `json:"namespaces"`
}

// Snapshot serialises the namespace table, specs and member sets.
func (r *SharedNsRefcount) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file := snapshotFile{Namespaces: make(map[string]snapshotNamespace, len(r.kind))}
	for ns, kind := range r.kind {
		entry := snapshotNamespace{
			Kind:    kind,
			Members: make([]string, 0, len(r.members[ns])),
		}
		for member := range r.members[ns] {
			entry.Members = append(entry.Members, member)
		}
		sort.Strings(entry.Members)
		if spec, ok := r.spec[ns]; ok {
			entry.Spec = &snapshotSpec{
				Mode:   spec.Mode,
				Owner:  spec.Owner,
				Labels: append([]string(nil), spec.Labels...),
			}
		}
		file.Namespaces[ns] = entry
	}
	return json.Marshal(file)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *SharedNsRefcount) Restore(data []byte) error {
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ErrBadSnapshot
	}
	if file.Namespaces == nil {
		return ErrBadSnapshot
	}
	kind := make(map[string]NsKind, len(file.Namespaces))
	spec := make(map[string]NsSpec, len(file.Namespaces))
	members := make(map[string]map[string]NsSpec, len(file.Namespaces))
	for ns, entry := range file.Namespaces {
		if strings.TrimSpace(ns) == "" || !validKind(entry.Kind) {
			return ErrBadSnapshot
		}
		set := make(map[string]NsSpec, len(entry.Members))
		if len(entry.Members) > 0 {
			if entry.Spec == nil {
				return ErrBadSnapshot
			}
			memberSpec := normalizeSpec(NsSpec{
				Mode:   entry.Spec.Mode,
				Owner:  entry.Spec.Owner,
				Labels: entry.Spec.Labels,
			})
			spec[ns] = memberSpec
			for _, member := range entry.Members {
				if strings.TrimSpace(member) == "" {
					return ErrBadSnapshot
				}
				if _, dup := set[member]; dup {
					return ErrBadSnapshot
				}
				set[member] = memberSpec
			}
		}
		kind[ns] = entry.Kind
		members[ns] = set
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kind = kind
	r.spec = spec
	r.members = members
	return nil
}
