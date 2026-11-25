// Package task handles leaked API key evidence: evidence grading, graduated
// revocation and replacement key provisioning for one managed key estate.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Confidence grades one piece of leak evidence.
type Confidence string

// Evidence confidence levels.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Incident states kept by the responder.
const (
	IncidentSuspected     = "suspected"
	IncidentObserving     = "observing"
	IncidentShadowRevoked = "shadow_revoked"
	IncidentHardRevoked   = "hard_revoked"
	IncidentRestored      = "restored"
)

// Action kinds returned by Triage.
const (
	ActionMonitor      = "monitor"
	ActionEscalate     = "escalate"
	ActionShadowRevoke = "shadow_revoke"
)

// Errors returned by the responder.
var (
	ErrBlankKeyID     = errors.New("key id, source and evidence are required")
	ErrBadConfidence  = errors.New("unknown confidence level")
	ErrUnknownIncident = errors.New("unknown incident")
	ErrInvalidState   = errors.New("incident is not in a state that allows this operation")
	ErrBlankActor     = errors.New("operator id is required")
	ErrIncidentCap    = errors.New("incident cap reached")
	ErrReplacementCap = errors.New("replacement key cap reached")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Incident is one leak case tracked for a single key.
type Incident struct {
	ID         string     `json:"id"`
	KeyID      string     `json:"key_id"`
	State      string     `json:"state"`
	Confidence Confidence `json:"confidence"`
	Source     string     `json:"source"`
	Evidence   []string   `json:"evidence"`
	Actor      string     `json:"actor"`
	Seq        int64      `json:"seq"`
}

// Action is the triage outcome for one incident.
type Action struct {
	IncidentID  string `json:"incident_id"`
	Kind        string `json:"kind"`
	NotifyOwner bool   `json:"notify_owner"`
}

// IssuedKey is a replacement key provisioned ahead of the hard revoke.
type IssuedKey struct {
	ID         string `json:"id"`
	IncidentID string `json:"incident_id"`
	KeyID      string `json:"key_id"`
	Active     bool   `json:"active"`
	Seq        int64  `json:"seq"`
}

// Limits bounds the tables held by the responder.
type Limits struct {
	MaxIncidents    int
	MaxReplacements int
}

// ApiKeyLeakResponder owns the incident table and the replacement key table.
type ApiKeyLeakResponder struct {
	limits Limits
}

// NewApiKeyLeakResponder builds a responder with the given caps.
func NewApiKeyLeakResponder(limits Limits) *ApiKeyLeakResponder {
	return &ApiKeyLeakResponder{limits: limits}
}

// Report accepts one piece of leak evidence.
func (r *ApiKeyLeakResponder) Report(keyID, source string, confidence Confidence, evidence string) (Incident, error) {
	return Incident{}, errNotImplemented
}

// Triage decides the next action for an incident.
func (r *ApiKeyLeakResponder) Triage(incidentID string) (Action, error) {
	return Action{}, errNotImplemented
}

// ShadowRevoke applies a reversible revoke to the leaked key.
func (r *ApiKeyLeakResponder) ShadowRevoke(incidentID string) error {
	return errNotImplemented
}

// HardRevoke is the irreversible revoke that needs an operator id.
func (r *ApiKeyLeakResponder) HardRevoke(incidentID, actorID string) error {
	return errNotImplemented
}

// Restore closes a false positive and keeps the audit trail.
func (r *ApiKeyLeakResponder) Restore(incidentID, actorID string) error {
	return errNotImplemented
}

// PrepareReplacement provisions an inactive replacement key.
func (r *ApiKeyLeakResponder) PrepareReplacement(incidentID string) (IssuedKey, error) {
	return IssuedKey{}, errNotImplemented
}

// IncidentState reads the state of one incident.
func (r *ApiKeyLeakResponder) IncidentState(incidentID string) (string, error) {
	return "", errNotImplemented
}

// Incidents lists the incidents of one key, oldest first.
func (r *ApiKeyLeakResponder) Incidents(keyID string) []Incident {
	return nil
}

// Replacements lists the replacement keys of one key, oldest first.
func (r *ApiKeyLeakResponder) Replacements(keyID string) []IssuedKey {
	return nil
}

// KeyUsable reports whether traffic with this key may be served right now.
func (r *ApiKeyLeakResponder) KeyUsable(keyID string) bool {
	return false
}

// Snapshot serialises incidents, replacements and the sequence counter.
func (r *ApiKeyLeakResponder) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (r *ApiKeyLeakResponder) LoadSnapshot(data []byte) error {
	return errNotImplemented
}
