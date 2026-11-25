// Package task validates isolation profiles made of namespaces, mounts and
// user id mappings, and canonicalises them for comparison.
package task

import (
	"errors"
	"sync"
)

var errNotImplemented = errors.New("not implemented")

// Errors returned by the validator.
var (
	ErrBadProfile = errors.New("profile cannot be normalised")
)

// Reason is the machine readable outcome of one validation.
type Reason string

// Validation reasons.
const (
	ReasonOK                 Reason = "ok"
	ReasonBlankName          Reason = "blank_name"
	ReasonNoNamespace        Reason = "no_namespace"
	ReasonUnknownNamespace   Reason = "unknown_namespace"
	ReasonDuplicateNamespace Reason = "duplicate_namespace"
	ReasonBadMount           Reason = "bad_mount"
	ReasonMountOverlap       Reason = "mount_overlap"
	ReasonInvalidIDMap       Reason = "invalid_id_map"
	ReasonReadOnlyWritable   Reason = "readonly_rootfs_writable_mount"
	ReasonUserMapUnavailable Reason = "user_map_unavailable"
)

// MountSpec is one bind mount declared by a profile.
type MountSpec struct {
	Source   string
	Target   string
	Writable bool
}

// IDMap is one user namespace id range.
type IDMap struct {
	ContainerID int
	HostID      int
	Size        int
}

// Profile is one isolation profile to validate.
type Profile struct {
	Name           string
	Namespaces     []string
	Mounts         []MountSpec
	ReadOnlyRootFS bool
	UserMaps       []IDMap
	RunAsUser      int
}

// Verdict is the outcome of one validation.
type Verdict struct {
	Name   string
	OK     bool
	Reason Reason
	Digest string
	Fields []string
}

// IsolationProfileValidator validates and canonicalises isolation profiles.
type IsolationProfileValidator struct {
	mu sync.Mutex
}

// NewIsolationProfileValidator builds an empty validator.
func NewIsolationProfileValidator() *IsolationProfileValidator {
	return &IsolationProfileValidator{}
}

// Validate checks one profile and reports a field addressable reason.
func (v *IsolationProfileValidator) Validate(p Profile) (Verdict, Reason, error) {
	return Verdict{}, "", errNotImplemented
}

// Normalize returns the canonical profile and its digest.
func (v *IsolationProfileValidator) Normalize(p Profile) (Profile, string, error) {
	return Profile{}, "", errNotImplemented
}

// Digest returns the canonical digest of one profile.
func (v *IsolationProfileValidator) Digest(p Profile) (string, error) {
	return "", errNotImplemented
}

// Compatible reports whether two profiles may share the same isolation setup.
func (v *IsolationProfileValidator) Compatible(a, b Profile) bool { return false }

// Explain renders one reason as an operator readable sentence.
func (v *IsolationProfileValidator) Explain(reason Reason) string { return "" }

// Batch validates many profiles without letting one failure hide the others.
func (v *IsolationProfileValidator) Batch(ps []Profile) []Verdict { return nil }

// Namespaces lists the namespace names the validator knows.
func (v *IsolationProfileValidator) Namespaces() []string { return nil }

// DefaultProfile returns the baseline profile used when the caller has none.
func (v *IsolationProfileValidator) DefaultProfile() Profile { return Profile{} }

// ValidateNamespace checks one namespace name against the known set.
func (v *IsolationProfileValidator) ValidateNamespace(name string) error {
	return errNotImplemented
}

// Severity grades how badly one reason should block a rollout.
func (v *IsolationProfileValidator) Severity(reason Reason) int { return 0 }

// Reasons lists every reason the validator may return.
func (v *IsolationProfileValidator) Reasons() []Reason { return nil }
