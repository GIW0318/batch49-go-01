// Package task narrows seccomp allow lists from observed syscall usage and
// blocks attempts to escape the enforced profile.
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

// Workload lifecycle states.
const (
	StateAudit       = "audit"
	StateEnforced    = "enforced"
	StateQuarantined = "quarantined"
)

// Decisions returned by Check.
const (
	ActionAllow = "allow"
	ActionDeny  = "deny"
	ActionKill  = "kill"
)

// Reason codes attached to a decision and to a recorded attempt.
const (
	ReasonAuditMode   = "audit_mode"
	ReasonAllowed     = "allowed"
	ReasonBlocked     = "blocked_syscall"
	ReasonEscape      = "escape_dangerous_syscall"
	ReasonQuarantined = "quarantined"
)

// snapshotVersion tags the serialised state format.
const snapshotVersion = 1

// Call is one syscall invocation. Arg0 is the only argument that profiles may
// constrain.
type Call struct {
	Name string `json:"name"`
	Arg0 uint64 `json:"arg0"`
}

// Rule allows one syscall name. A non zero mask constrains Arg0 so that
// Arg0&Arg0Mask must equal Arg0Value.
type Rule struct {
	Name      string `json:"name"`
	Arg0Mask  uint64 `json:"arg0_mask"`
	Arg0Value uint64 `json:"arg0_value"`
}

// Profile is an immutable generation of allowed rules.
type Profile struct {
	Generation int    `json:"generation"`
	Rules      []Rule `json:"rules"`
}

// WorkloadSpec is the registration input for one container workload.
type WorkloadSpec struct {
	ID      string `json:"id"`
	Allowed []Rule `json:"allowed"`
}

// Workload is the externally visible state of one container.
type Workload struct {
	ID      string  `json:"id"`
	State   string  `json:"state"`
	Profile Profile `json:"profile"`
	Strikes int     `json:"strikes"`
	Seq     int64   `json:"seq"`
}

// Observation is the aggregated audit window entry for one syscall name.
type Observation struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	FirstSeq int64  `json:"first_seq"`
}

// Draft is a proposed narrower profile that has not been enforced yet.
type Draft struct {
	WorkloadID string   `json:"workload_id"`
	Allowed    []Rule   `json:"allowed"`
	Removed    []string `json:"removed"`
	Generation int      `json:"generation"`
	Digest     string   `json:"digest"`
}

// Decision is the outcome of one runtime check.
type Decision struct {
	Action      string `json:"action"`
	Reason      string `json:"reason"`
	Strikes     int    `json:"strikes"`
	Quarantined bool   `json:"quarantined"`
}

// Attempt records one blocked syscall for later review.
type Attempt struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Seq    int64  `json:"seq"`
}

// Limits bounds the guard tables.
type Limits struct {
	MaxWorkloads int
	MaxRules     int
	MaxObserved  int
	MaxStrikes   int
}

// Errors returned by the guard.
var (
	ErrBlankWorkload      = errors.New("workload id is required")
	ErrDuplicateWorkload  = errors.New("workload already enrolled")
	ErrUnknownWorkload    = errors.New("unknown workload")
	ErrEmptyProfile       = errors.New("profile must allow at least one syscall")
	ErrInvalidCall        = errors.New("syscall call or rule is invalid")
	ErrNotAuditing        = errors.New("workload is not in the audit window")
	ErrNoObservation      = errors.New("audit window has no observation")
	ErrNoDraft            = errors.New("no profile draft for this workload")
	ErrWidenRejected      = errors.New("narrowed profile would widen the allow list")
	ErrAlreadyQuarantined = errors.New("workload is already quarantined")
	ErrNotQuarantined     = errors.New("workload is not quarantined")
	ErrReasonRequired     = errors.New("quarantine reason is required")
	ErrLimitExceeded      = errors.New("limit exceeded")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// baselineSyscalls are process teardown / signal-return calls that must stay
// allowed even when the audit window never observed them.
var baselineSyscalls = map[string]struct{}{
	"exit_group":   {},
	"rt_sigreturn": {},
}

// dangerousSyscalls are escape primitives: an enforced miss kills the
// workload immediately instead of waiting for the strike threshold.
var dangerousSyscalls = map[string]struct{}{
	"bpf":            {},
	"ptrace":         {},
	"unshare":        {},
	"setns":          {},
	"mount":          {},
	"openat2":        {},
	"io_uring_setup": {},
}

// workloadState is the mutable, guard-owned record for one container.
type workloadState struct {
	Workload         Workload      `json:"workload"`
	Observations     []Observation `json:"observations"`
	Draft            *Draft        `json:"draft,omitempty"`
	Attempts         []Attempt     `json:"attempts,omitempty"`
	QuarantineReason string        `json:"quarantine_reason,omitempty"`

	// obsIndex maps an observed call name to its slot in Observations.
	obsIndex map[string]int `json:"-"`
}

// snapshotEnvelope is the versioned, whole-state serialisation.
type snapshotEnvelope struct {
	Version   int                       `json:"version"`
	Workloads map[string]*workloadState `json:"workloads"`
}

// SeccompEscapeGuard owns workload state, audit windows and quarantine tickets.
// A single mutex serialises every transition so Observe accumulation and
// Check/Promote profile swaps always observe one complete state.
type SeccompEscapeGuard struct {
	mu        sync.RWMutex
	limits    Limits
	workloads map[string]*workloadState
}

// NewSeccompEscapeGuard builds a guard with the given caps.
func NewSeccompEscapeGuard(limits Limits) *SeccompEscapeGuard {
	return &SeccompEscapeGuard{
		limits:    limits,
		workloads: make(map[string]*workloadState),
	}
}

// nextSeq allocates the next monotonic event sequence for a workload. The
// caller must hold g.mu.
func (w *workloadState) nextSeq() int64 {
	w.Workload.Seq++
	return w.Workload.Seq
}

// Enroll registers a workload in the audit state with its first profile.
func (g *SeccompEscapeGuard) Enroll(spec WorkloadSpec) error {
	id := strings.TrimSpace(spec.ID)
	if id == "" {
		return ErrBlankWorkload
	}
	if len(spec.Allowed) == 0 {
		return ErrEmptyProfile
	}

	rules := make([]Rule, 0, len(spec.Allowed))
	seen := make(map[string]struct{}, len(spec.Allowed))
	for _, rule := range spec.Allowed {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return ErrInvalidCall
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrInvalidCall
		}
		seen[name] = struct{}{}
		rule.Name = name
		rules = append(rules, rule)
	}
	if g.limits.MaxRules > 0 && len(rules) > g.limits.MaxRules {
		return ErrLimitExceeded
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.workloads[id]; exists {
		return ErrDuplicateWorkload
	}
	if g.limits.MaxWorkloads > 0 && len(g.workloads) >= g.limits.MaxWorkloads {
		return ErrLimitExceeded
	}

	g.workloads[id] = &workloadState{
		Workload: Workload{
			ID:    id,
			State: StateAudit,
			Profile: Profile{
				Generation: 1,
				Rules:      rules,
			},
		},
		Observations: make([]Observation, 0),
		Attempts:     make([]Attempt, 0),
		obsIndex:     make(map[string]int),
	}
	return nil
}

// Observe records one audit window syscall for a workload.
func (g *SeccompEscapeGuard) Observe(workloadID string, c Call) error {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return ErrInvalidCall
	}
	id := strings.TrimSpace(workloadID)

	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.workloads[id]
	if !ok {
		return ErrUnknownWorkload
	}
	if w.Workload.State != StateAudit {
		return ErrNotAuditing
	}

	if idx, known := w.obsIndex[name]; known {
		w.nextSeq()
		w.Observations[idx].Count++
		return nil
	}

	if g.limits.MaxObserved > 0 && len(w.Observations) >= g.limits.MaxObserved {
		return ErrLimitExceeded
	}

	seq := w.nextSeq()
	w.Observations = append(w.Observations, Observation{
		Name:     name,
		Count:    1,
		FirstSeq: seq,
	})
	if w.obsIndex == nil {
		w.obsIndex = make(map[string]int)
	}
	w.obsIndex[name] = len(w.Observations) - 1
	return nil
}

// Observations returns the audit window in first seen order.
func (g *SeccompEscapeGuard) Observations(workloadID string) ([]Observation, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.RLock()
	defer g.mu.RUnlock()

	w, ok := g.workloads[id]
	if !ok {
		return nil, ErrUnknownWorkload
	}
	out := make([]Observation, len(w.Observations))
	copy(out, w.Observations)
	return out, nil
}

// Propose derives a narrower profile from the current audit window.
func (g *SeccompEscapeGuard) Propose(workloadID string) (Draft, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.workloads[id]
	if !ok {
		return Draft{}, ErrUnknownWorkload
	}
	if w.Workload.State != StateAudit {
		return Draft{}, ErrNotAuditing
	}
	if len(w.Observations) == 0 {
		return Draft{}, ErrNoObservation
	}

	observed := make(map[string]struct{}, len(w.Observations))
	for _, observation := range w.Observations {
		observed[observation.Name] = struct{}{}
	}

	allowed := make([]Rule, 0, len(w.Workload.Profile.Rules))
	removed := make([]string, 0)
	for _, rule := range w.Workload.Profile.Rules {
		if _, seen := observed[rule.Name]; seen {
			allowed = append(allowed, rule)
			continue
		}
		if _, baseline := baselineSyscalls[rule.Name]; baseline {
			allowed = append(allowed, rule)
			continue
		}
		removed = append(removed, rule.Name)
	}
	sort.Strings(removed)

	draft := Draft{
		WorkloadID: id,
		Allowed:    allowed,
		Removed:    removed,
		Generation: w.Workload.Profile.Generation + 1,
	}
	draft.Digest = draftDigest(draft)
	stored := cloneDraft(&draft)
	w.Draft = &stored
	return cloneDraft(&draft), nil
}

// Promote turns the pending draft into the enforced profile.
func (g *SeccompEscapeGuard) Promote(workloadID string) (Profile, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.workloads[id]
	if !ok {
		return Profile{}, ErrUnknownWorkload
	}
	if w.Workload.State != StateAudit {
		return Profile{}, ErrNotAuditing
	}
	if w.Draft == nil {
		return Profile{}, ErrNoDraft
	}

	current := make(map[string]Rule, len(w.Workload.Profile.Rules))
	for _, rule := range w.Workload.Profile.Rules {
		current[rule.Name] = rule
	}
	for _, rule := range w.Draft.Allowed {
		existing, ok := current[rule.Name]
		if !ok || existing != rule {
			return Profile{}, ErrWidenRejected
		}
	}

	generation := w.Workload.Profile.Generation + 1
	rules := append([]Rule(nil), w.Draft.Allowed...)
	w.Workload.Profile = Profile{Generation: generation, Rules: rules}
	w.Workload.State = StateEnforced
	w.Draft = nil

	return Profile{Generation: generation, Rules: append([]Rule(nil), rules...)}, nil
}

// Check decides one runtime syscall.
func (g *SeccompEscapeGuard) Check(workloadID string, c Call) (Decision, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.workloads[id]
	if !ok {
		return Decision{}, ErrUnknownWorkload
	}

	switch w.Workload.State {
	case StateAudit:
		// Audit mode observes but never judges: every call is allowed and no
		// strike is counted.
		return Decision{
			Action:  ActionAllow,
			Reason:  ReasonAuditMode,
			Strikes: w.Workload.Strikes,
		}, nil

	case StateQuarantined:
		// A quarantined workload is out of service: deny without advancing
		// strikes or recording another violation.
		return Decision{
			Action:      ActionDeny,
			Reason:      ReasonQuarantined,
			Strikes:     w.Workload.Strikes,
			Quarantined: true,
		}, nil

	case StateEnforced:
		for _, rule := range w.Workload.Profile.Rules {
			if ruleAllows(rule, c) {
				return Decision{
					Action:  ActionAllow,
					Reason:  ReasonAllowed,
					Strikes: w.Workload.Strikes,
				}, nil
			}
		}

		w.Workload.Strikes++
		strikes := w.Workload.Strikes

		reason := ReasonBlocked
		if _, dangerous := dangerousSyscalls[c.Name]; dangerous {
			reason = ReasonEscape
		}
		w.Attempts = append(w.Attempts, Attempt{
			Name:   c.Name,
			Reason: reason,
			Seq:    w.nextSeq(),
		})

		if reason == ReasonEscape {
			w.Workload.State = StateQuarantined
			w.QuarantineReason = ReasonEscape
			return Decision{
				Action:      ActionKill,
				Reason:      ReasonEscape,
				Strikes:     strikes,
				Quarantined: true,
			}, nil
		}

		if g.limits.MaxStrikes > 0 && strikes >= g.limits.MaxStrikes {
			w.Workload.State = StateQuarantined
			w.QuarantineReason = "strike_limit_exceeded"
			return Decision{
				Action:      ActionKill,
				Reason:      ReasonBlocked,
				Strikes:     strikes,
				Quarantined: true,
			}, nil
		}

		return Decision{
			Action:  ActionDeny,
			Reason:  ReasonBlocked,
			Strikes: strikes,
		}, nil
	}

	return Decision{
		Action:      ActionDeny,
		Reason:      ReasonQuarantined,
		Strikes:     w.Workload.Strikes,
		Quarantined: true,
	}, nil
}

// Strikes returns the accumulated blocked call count.
func (g *SeccompEscapeGuard) Strikes(workloadID string) (int, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.RLock()
	defer g.mu.RUnlock()

	w, ok := g.workloads[id]
	if !ok {
		return 0, ErrUnknownWorkload
	}
	return w.Workload.Strikes, nil
}

// Quarantine moves a workload out of service with a reason.
func (g *SeccompEscapeGuard) Quarantine(workloadID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	id := strings.TrimSpace(workloadID)

	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.workloads[id]
	if !ok {
		return ErrUnknownWorkload
	}
	if w.Workload.State == StateQuarantined {
		return ErrAlreadyQuarantined
	}
	w.Workload.State = StateQuarantined
	w.QuarantineReason = strings.TrimSpace(reason)
	return nil
}

// Release returns a quarantined workload to a fresh audit window.
func (g *SeccompEscapeGuard) Release(workloadID string) error {
	id := strings.TrimSpace(workloadID)

	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.workloads[id]
	if !ok {
		return ErrUnknownWorkload
	}
	if w.Workload.State != StateQuarantined {
		return ErrNotQuarantined
	}

	w.Workload.State = StateAudit
	w.Workload.Strikes = 0
	w.Observations = make([]Observation, 0)
	w.obsIndex = make(map[string]int)
	w.Draft = nil
	w.QuarantineReason = ""
	return nil
}

// State returns the lifecycle state of a workload.
func (g *SeccompEscapeGuard) State(workloadID string) (string, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.RLock()
	defer g.mu.RUnlock()

	w, ok := g.workloads[id]
	if !ok {
		return "", ErrUnknownWorkload
	}
	return w.Workload.State, nil
}

// Workload returns a copy of the workload record.
func (g *SeccompEscapeGuard) Workload(workloadID string) (Workload, bool) {
	id := strings.TrimSpace(workloadID)

	g.mu.RLock()
	defer g.mu.RUnlock()

	w, ok := g.workloads[id]
	if !ok {
		return Workload{}, false
	}
	out := w.Workload
	out.Profile.Rules = append([]Rule(nil), w.Workload.Profile.Rules...)
	return out, true
}

// Attempts returns blocked syscalls for a workload in order.
func (g *SeccompEscapeGuard) Attempts(workloadID string) ([]Attempt, error) {
	id := strings.TrimSpace(workloadID)

	g.mu.RLock()
	defer g.mu.RUnlock()

	w, ok := g.workloads[id]
	if !ok {
		return nil, ErrUnknownWorkload
	}
	out := make([]Attempt, len(w.Attempts))
	copy(out, w.Attempts)
	return out, nil
}

// Snapshot serialises workloads, profiles, windows and attempt logs.
func (g *SeccompEscapeGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	envelope := snapshotEnvelope{
		Version:   snapshotVersion,
		Workloads: make(map[string]*workloadState, len(g.workloads)),
	}
	for id, w := range g.workloads {
		envelope.Workloads[id] = w
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state from a snapshot. The payload is parsed and
// validated before the live table is touched, so a bad snapshot leaves the
// running guard exactly as it was.
func (g *SeccompEscapeGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	next := make(map[string]*workloadState, len(envelope.Workloads))
	for key, w := range envelope.Workloads {
		if w == nil {
			return ErrBadSnapshot
		}
		id := strings.TrimSpace(w.Workload.ID)
		if id == "" {
			id = strings.TrimSpace(key)
		}
		if id == "" {
			return ErrBadSnapshot
		}
		if !validState(w.Workload.State) {
			return ErrBadSnapshot
		}
		if w.Workload.Profile.Generation < 1 {
			return ErrBadSnapshot
		}
		if w.Workload.Strikes < 0 || w.Workload.Seq < 0 {
			return ErrBadSnapshot
		}
		if err := validateSnapshotRules(w.Workload.Profile.Rules); err != nil {
			return err
		}
		if w.Draft != nil {
			if err := validateSnapshotRules(w.Draft.Allowed); err != nil {
				return err
			}
		}

		index := make(map[string]int, len(w.Observations))
		for i, observation := range w.Observations {
			name := strings.TrimSpace(observation.Name)
			if name == "" || observation.Count < 1 {
				return ErrBadSnapshot
			}
			if _, duplicate := index[name]; duplicate {
				return ErrBadSnapshot
			}
			index[name] = i
			w.Observations[i].Name = name
		}

		w.Workload.ID = id
		w.obsIndex = index
		if w.Observations == nil {
			w.Observations = make([]Observation, 0)
		}
		if w.Attempts == nil {
			w.Attempts = make([]Attempt, 0)
		}
		entry := *w
		next[id] = &entry
	}

	g.mu.Lock()
	g.workloads = next
	g.mu.Unlock()
	return nil
}

// ruleAllows reports whether a call matches a rule, applying the optional
// Arg0 mask/value constraint when the mask is non zero.
func ruleAllows(rule Rule, c Call) bool {
	if rule.Name != c.Name {
		return false
	}
	if rule.Arg0Mask != 0 {
		return c.Arg0&rule.Arg0Mask == rule.Arg0Value
	}
	return true
}

// validState reports whether s is one of the lifecycle states.
func validState(s string) bool {
	switch s {
	case StateAudit, StateEnforced, StateQuarantined:
		return true
	default:
		return false
	}
}

// validateSnapshotRules checks the same invariants as Enroll and maps any
// violation onto ErrBadSnapshot.
func validateSnapshotRules(rules []Rule) error {
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			return ErrBadSnapshot
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrBadSnapshot
		}
		seen[name] = struct{}{}
	}
	return nil
}

// draftDigest hashes the stable draft content so repeated proposals over an
// unchanged window carry the same non empty digest.
func draftDigest(draft Draft) string {
	payload := struct {
		WorkloadID string   `json:"workload_id"`
		Generation int      `json:"generation"`
		Allowed    []Rule   `json:"allowed"`
		Removed    []string `json:"removed"`
	}{
		WorkloadID: draft.WorkloadID,
		Generation: draft.Generation,
		Allowed:    draft.Allowed,
		Removed:    draft.Removed,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// cloneDraft returns a copy whose slices do not alias the stored draft.
func cloneDraft(draft *Draft) Draft {
	if draft == nil {
		return Draft{}
	}
	out := *draft
	out.Allowed = append([]Rule(nil), draft.Allowed...)
	out.Removed = append([]string(nil), draft.Removed...)
	return out
}
