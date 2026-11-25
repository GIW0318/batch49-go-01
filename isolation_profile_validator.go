// Package task validates isolation profiles made of namespaces, mounts and
// user id mappings, and canonicalises them for comparison.
package task

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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

var knownNamespaces = []string{
	"cgroup",
	"ipc",
	"mnt",
	"mount",
	"net",
	"pid",
	"time",
	"user",
	"uts",
}

var knownNamespaceSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(knownNamespaces))
	for _, name := range knownNamespaces {
		m[name] = struct{}{}
	}
	return m
}()

var allReasons = []Reason{
	ReasonOK,
	ReasonBlankName,
	ReasonNoNamespace,
	ReasonUnknownNamespace,
	ReasonDuplicateNamespace,
	ReasonBadMount,
	ReasonMountOverlap,
	ReasonInvalidIDMap,
	ReasonReadOnlyWritable,
	ReasonUserMapUnavailable,
}

const blankNamePlaceholder = "broken"

// NewIsolationProfileValidator builds an empty validator.
func NewIsolationProfileValidator() *IsolationProfileValidator {
	return &IsolationProfileValidator{}
}

// Validate checks one profile and reports a field addressable reason.
func (v *IsolationProfileValidator) Validate(p Profile) (Verdict, Reason, error) {
	p = cloneProfile(p)
	name := strings.TrimSpace(p.Name)

	if name == "" {
		return Verdict{
			Name:   blankNamePlaceholder,
			OK:     false,
			Reason: ReasonBlankName,
			Fields: []string{"name"},
		}, ReasonBlankName, nil
	}

	if len(p.Namespaces) == 0 {
		return Verdict{
			Name:   name,
			OK:     false,
			Reason: ReasonNoNamespace,
			Fields: []string{"namespaces"},
		}, ReasonNoNamespace, nil
	}

	normalized, digest, err := normalizeProfile(p)
	if err != nil {
		return Verdict{Name: name, Reason: Reason("")}, Reason(""), err
	}

	verdict := Verdict{Name: name, Digest: digest}

	for _, namespace := range p.Namespaces {
		if _, ok := knownNamespaceSet[namespace]; !ok {
			verdict.Reason = ReasonUnknownNamespace
			verdict.Fields = appendUnique(verdict.Fields, namespace)
		}
	}
	if verdict.Reason != "" {
		return verdict, verdict.Reason, nil
	}

	seenNamespaces := make(map[string]struct{}, len(normalized.Namespaces))
	for _, namespace := range p.Namespaces {
		if _, exists := seenNamespaces[namespace]; exists {
			verdict.Reason = ReasonDuplicateNamespace
			verdict.Fields = appendUnique(verdict.Fields, namespace)
			continue
		}
		seenNamespaces[namespace] = struct{}{}
	}
	if verdict.Reason != "" {
		return verdict, verdict.Reason, nil
	}

	for _, mount := range p.Mounts {
		if strings.TrimSpace(mount.Source) == "" || !isAbsoluteTarget(mount.Target) {
			verdict.Reason = ReasonBadMount
			verdict.Fields = []string{"mounts"}
			return verdict, verdict.Reason, nil
		}
	}

	seenTargets := make(map[string]struct{}, len(p.Mounts))
	for _, mount := range p.Mounts {
		if _, exists := seenTargets[mount.Target]; exists {
			verdict.Reason = ReasonMountOverlap
			verdict.Fields = appendUnique(verdict.Fields, mount.Target)
			continue
		}
		seenTargets[mount.Target] = struct{}{}
	}
	if verdict.Reason != "" {
		return verdict, verdict.Reason, nil
	}

	for _, idMap := range p.UserMaps {
		if idMap.ContainerID < 0 || idMap.HostID < 0 || idMap.Size <= 0 {
			verdict.Reason = ReasonInvalidIDMap
			verdict.Fields = []string{"user_maps"}
			return verdict, verdict.Reason, nil
		}
	}

	if p.ReadOnlyRootFS {
		for _, mount := range p.Mounts {
			if mount.Writable {
				verdict.Reason = ReasonReadOnlyWritable
				verdict.Fields = appendUnique(verdict.Fields, "readonly_rootfs")
				verdict.Fields = appendUnique(verdict.Fields, "mounts")
				verdict.Fields = appendUnique(verdict.Fields, mount.Target)
			}
		}
		if verdict.Reason != "" {
			return verdict, verdict.Reason, nil
		}
	}

	if requiresUserMap(p) && !userIsMapped(p.UserMaps, p.RunAsUser) {
		verdict.Reason = ReasonUserMapUnavailable
		verdict.Fields = []string{"user_maps", "run_as_user"}
		return verdict, verdict.Reason, nil
	}

	verdict.OK = true
	verdict.Reason = ReasonOK
	verdict.Fields = nil
	return verdict, ReasonOK, nil
}

// Normalize returns the canonical profile and its digest.
func (v *IsolationProfileValidator) Normalize(p Profile) (Profile, string, error) {
	return normalizeProfile(cloneProfile(p))
}

// Digest returns the canonical digest of one profile.
func (v *IsolationProfileValidator) Digest(p Profile) (string, error) {
	_, digest, err := normalizeProfile(cloneProfile(p))
	return digest, err
}

// Compatible reports whether two profiles may share the same isolation setup.
func (v *IsolationProfileValidator) Compatible(a, b Profile) bool {
	a = cloneProfile(a)
	b = cloneProfile(b)

	if !sameStringSet(a.Namespaces, b.Namespaces) {
		return false
	}

	aFlags, aOK := mountWritableFlags(a.Mounts)
	bFlags, bOK := mountWritableFlags(b.Mounts)
	if !aOK || !bOK {
		return false
	}
	for target, writable := range aFlags {
		if other, exists := bFlags[target]; exists && other != writable {
			return false
		}
	}
	return true
}

// Explain renders one reason as an operator readable sentence.
func (v *IsolationProfileValidator) Explain(reason Reason) string {
	switch reason {
	case ReasonOK:
		return "profile is valid"
	case ReasonBlankName:
		return "profile name must not be blank"
	case ReasonNoNamespace:
		return "profile must declare at least one namespace"
	case ReasonUnknownNamespace:
		return "profile declares a namespace outside the known set"
	case ReasonDuplicateNamespace:
		return "profile declares the same namespace more than once"
	case ReasonBadMount:
		return "profile contains a mount with a blank source or a non-absolute target"
	case ReasonMountOverlap:
		return "more than one mount targets the same path"
	case ReasonInvalidIDMap:
		return "profile contains a negative id range or a zero-length id range"
	case ReasonReadOnlyWritable:
		return "a read-only root filesystem cannot contain a writable mount"
	case ReasonUserMapUnavailable:
		return "the user id mapping does not cover the image runtime user"
	default:
		return "unrecognised isolation profile validation reason"
	}
}

// Batch validates many profiles without letting one failure hide the others.
func (v *IsolationProfileValidator) Batch(ps []Profile) []Verdict {
	snapshot := make([]Profile, len(ps))
	for i, p := range ps {
		snapshot[i] = cloneProfile(p)
	}

	verdicts := make([]Verdict, len(snapshot))
	for i, p := range snapshot {
		verdict, _, _ := v.Validate(p)
		verdicts[i] = verdict
	}
	return verdicts
}

// Namespaces lists the namespace names the validator knows.
func (v *IsolationProfileValidator) Namespaces() []string {
	return append([]string(nil), knownNamespaces...)
}

// DefaultProfile returns the baseline profile used when the caller has none.
func (v *IsolationProfileValidator) DefaultProfile() Profile {
	return Profile{
		Name:           "default",
		Namespaces:     []string{"cgroup", "ipc", "mnt", "mount", "net", "pid", "time", "uts"},
		ReadOnlyRootFS: true,
	}
}

// ValidateNamespace checks one namespace name against the known set.
func (v *IsolationProfileValidator) ValidateNamespace(name string) error {
	if _, ok := knownNamespaceSet[name]; ok {
		return nil
	}
	return fmt.Errorf("unknown namespace %q", name)
}

// Severity grades how badly one reason should block a rollout.
func (v *IsolationProfileValidator) Severity(reason Reason) int {
	switch reason {
	case ReasonOK:
		return 0
	case ReasonUserMapUnavailable:
		return 1
	case ReasonUnknownNamespace, ReasonDuplicateNamespace:
		return 2
	case ReasonMountOverlap, ReasonBadMount:
		return 3
	case ReasonBlankName, ReasonNoNamespace, ReasonInvalidIDMap, ReasonReadOnlyWritable:
		return 4
	default:
		return -1
	}
}

// Reasons lists every reason the validator may return.
func (v *IsolationProfileValidator) Reasons() []Reason {
	return append([]Reason(nil), allReasons...)
}

func cloneProfile(p Profile) Profile {
	clone := p
	if p.Namespaces != nil {
		clone.Namespaces = append([]string(nil), p.Namespaces...)
	}
	if p.Mounts != nil {
		clone.Mounts = append([]MountSpec(nil), p.Mounts...)
	}
	if p.UserMaps != nil {
		clone.UserMaps = append([]IDMap(nil), p.UserMaps...)
	}
	return clone
}

func normalizeProfile(p Profile) (Profile, string, error) {
	p = cloneProfile(p)
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Namespaces) == 0 {
		return Profile{}, "", fmt.Errorf("%w: name and at least one namespace are required", ErrBadProfile)
	}

	if len(p.Namespaces) > 0 {
		sort.Strings(p.Namespaces)
		p.Namespaces = dedupeStrings(p.Namespaces)
	} else {
		p.Namespaces = nil
	}

	if len(p.Mounts) > 0 {
		sort.Slice(p.Mounts, func(i, j int) bool {
			a, b := p.Mounts[i], p.Mounts[j]
			if a.Target != b.Target {
				return a.Target < b.Target
			}
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return !a.Writable && b.Writable
		})
		p.Mounts = dedupeMounts(p.Mounts)
	} else {
		p.Mounts = nil
	}

	if len(p.UserMaps) > 0 {
		sort.Slice(p.UserMaps, func(i, j int) bool {
			a, b := p.UserMaps[i], p.UserMaps[j]
			if a.ContainerID != b.ContainerID {
				return a.ContainerID < b.ContainerID
			}
			if a.HostID != b.HostID {
				return a.HostID < b.HostID
			}
			return a.Size < b.Size
		})
		p.UserMaps = dedupeIDMaps(p.UserMaps)
	} else {
		p.UserMaps = nil
	}

	encoded, err := json.Marshal(p)
	if err != nil {
		return Profile{}, "", fmt.Errorf("%w: %v", ErrBadProfile, err)
	}
	sum := sha256.Sum256(encoded)
	return p, fmt.Sprintf("%x", sum[:8]), nil
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func dedupeMounts(values []MountSpec) []MountSpec {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func dedupeIDMaps(values []IDMap) []IDMap {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func isAbsoluteTarget(target string) bool {
	return target != "" && strings.HasPrefix(target, "/")
}

func appendUnique(fields []string, value string) []string {
	for _, field := range fields {
		if field == value {
			return fields
		}
	}
	return append(fields, value)
}

func requiresUserMap(p Profile) bool {
	if len(p.UserMaps) > 0 {
		return true
	}
	for _, namespace := range p.Namespaces {
		if namespace == "user" {
			return true
		}
	}
	return false
}

func userIsMapped(mappings []IDMap, user int) bool {
	for _, mapping := range mappings {
		if user >= mapping.ContainerID && user-mapping.ContainerID < mapping.Size {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	aSet := make(map[string]struct{}, len(a))
	for _, value := range a {
		aSet[value] = struct{}{}
	}
	bSet := make(map[string]struct{}, len(b))
	for _, value := range b {
		bSet[value] = struct{}{}
	}
	if len(aSet) != len(bSet) {
		return false
	}
	for value := range aSet {
		if _, ok := bSet[value]; !ok {
			return false
		}
	}
	return true
}

func mountWritableFlags(mounts []MountSpec) (map[string]bool, bool) {
	flags := make(map[string]bool, len(mounts))
	for _, mount := range mounts {
		if writable, exists := flags[mount.Target]; exists && writable != mount.Writable {
			return nil, false
		}
		flags[mount.Target] = mount.Writable
	}
	return flags, true
}
