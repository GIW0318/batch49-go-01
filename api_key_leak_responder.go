// Package task handles leaked API key evidence: evidence grading, graduated
// revocation and replacement key provisioning for one managed key estate.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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
	ErrBlankKeyID      = errors.New("key id, source and evidence are required")
	ErrBadConfidence   = errors.New("unknown confidence level")
	ErrUnknownIncident = errors.New("unknown incident")
	ErrInvalidState    = errors.New("incident is not in a state that allows this operation")
	ErrBlankActor      = errors.New("operator id is required")
	ErrIncidentCap     = errors.New("incident cap reached")
	ErrReplacementCap  = errors.New("replacement key cap reached")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
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
	mu                    sync.Mutex
	limits                Limits
	seq                   int64
	incidents             map[string]*Incident
	activeIncidentByKey   map[string]string
	replacements          map[string]*IssuedKey
	replacementByIncident map[string]string
}

// NewApiKeyLeakResponder builds a responder with the given caps.
func NewApiKeyLeakResponder(limits Limits) *ApiKeyLeakResponder {
	if limits.MaxIncidents < 0 {
		limits.MaxIncidents = 0
	}
	if limits.MaxReplacements < 0 {
		limits.MaxReplacements = 0
	}
	return &ApiKeyLeakResponder{
		limits:                limits,
		incidents:             make(map[string]*Incident),
		activeIncidentByKey:   make(map[string]string),
		replacements:          make(map[string]*IssuedKey),
		replacementByIncident: make(map[string]string),
	}
}

const snapshotVersion = 1

type snapshotData struct {
	Version      int         `json:"version"`
	Seq          int64       `json:"seq"`
	Incidents    []Incident  `json:"incidents"`
	Replacements []IssuedKey `json:"replacements"`
}

func blankText(value string) bool {
	return strings.TrimSpace(value) == ""
}

func knownConfidence(confidence Confidence) bool {
	switch confidence {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		return true
	default:
		return false
	}
}

func knownIncidentState(state string) bool {
	switch state {
	case IncidentSuspected, IncidentObserving, IncidentShadowRevoked,
		IncidentHardRevoked, IncidentRestored:
		return true
	default:
		return false
	}
}

func cloneIncident(incident *Incident) Incident {
	copy := *incident
	copy.Evidence = append([]string(nil), incident.Evidence...)
	return copy
}

func (r *ApiKeyLeakResponder) nextIDLocked(prefix string) (string, int64) {
	r.seq++
	return fmt.Sprintf("%s-%d", prefix, r.seq), r.seq
}

// Report accepts one piece of leak evidence.
func (r *ApiKeyLeakResponder) Report(keyID, source string, confidence Confidence, evidence string) (Incident, error) {
	keyID = strings.TrimSpace(keyID)
	source = strings.TrimSpace(source)
	if keyID == "" || source == "" || blankText(evidence) {
		return Incident{}, ErrBlankKeyID
	}
	if !knownConfidence(confidence) {
		return Incident{}, ErrBadConfidence
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if incidentID, ok := r.activeIncidentByKey[keyID]; ok {
		if incident := r.incidents[incidentID]; incident != nil {
			incident.Evidence = append(incident.Evidence, evidence)
			return cloneIncident(incident), nil
		}
		delete(r.activeIncidentByKey, keyID)
	}

	if len(r.incidents) >= r.limits.MaxIncidents {
		return Incident{}, ErrIncidentCap
	}

	incidentID, seq := r.nextIDLocked("incident")
	incident := &Incident{
		ID:         incidentID,
		KeyID:      keyID,
		State:      IncidentSuspected,
		Confidence: confidence,
		Source:     source,
		Evidence:   []string{evidence},
		Seq:        seq,
	}
	r.incidents[incidentID] = incident
	r.activeIncidentByKey[keyID] = incidentID
	return cloneIncident(incident), nil
}

// Triage decides the next action for an incident.
func (r *ApiKeyLeakResponder) Triage(incidentID string) (Action, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	incident, ok := r.incidents[incidentID]
	if !ok {
		return Action{}, ErrUnknownIncident
	}
	if incident.State != IncidentSuspected {
		return Action{}, ErrInvalidState
	}

	action := Action{IncidentID: incident.ID}
	switch incident.Confidence {
	case ConfidenceLow:
		action.Kind = ActionMonitor
		action.NotifyOwner = false
		incident.State = IncidentObserving
	case ConfidenceMedium:
		action.Kind = ActionEscalate
		action.NotifyOwner = true
		incident.State = IncidentObserving
	case ConfidenceHigh:
		action.Kind = ActionShadowRevoke
		action.NotifyOwner = true
		incident.State = IncidentShadowRevoked
	default:
		return Action{}, ErrBadConfidence
	}
	return action, nil
}

// ShadowRevoke applies a reversible revoke to the leaked key.
func (r *ApiKeyLeakResponder) ShadowRevoke(incidentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	incident, ok := r.incidents[incidentID]
	if !ok {
		return ErrUnknownIncident
	}
	if incident.State == IncidentShadowRevoked {
		return nil
	}
	if incident.State != IncidentSuspected && incident.State != IncidentObserving {
		return ErrInvalidState
	}
	incident.State = IncidentShadowRevoked
	return nil
}

// HardRevoke is the irreversible revoke that needs an operator id.
func (r *ApiKeyLeakResponder) HardRevoke(incidentID, actorID string) error {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return ErrBlankActor
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	incident, ok := r.incidents[incidentID]
	if !ok {
		return ErrUnknownIncident
	}
	if incident.State != IncidentShadowRevoked {
		return ErrInvalidState
	}
	incident.State = IncidentHardRevoked
	incident.Actor = actorID
	return nil
}

// Restore closes a false positive and keeps the audit trail.
func (r *ApiKeyLeakResponder) Restore(incidentID, actorID string) error {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return ErrBlankActor
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	incident, ok := r.incidents[incidentID]
	if !ok {
		return ErrUnknownIncident
	}
	if incident.State != IncidentShadowRevoked && incident.State != IncidentHardRevoked {
		return ErrInvalidState
	}
	incident.State = IncidentRestored
	incident.Actor = actorID
	delete(r.activeIncidentByKey, incident.KeyID)
	return nil
}

// PrepareReplacement provisions an inactive replacement key.
func (r *ApiKeyLeakResponder) PrepareReplacement(incidentID string) (IssuedKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	incident, ok := r.incidents[incidentID]
	if !ok {
		return IssuedKey{}, ErrUnknownIncident
	}
	if replacementID, exists := r.replacementByIncident[incidentID]; exists {
		if replacement := r.replacements[replacementID]; replacement != nil {
			return *replacement, nil
		}
	}
	if incident.State != IncidentShadowRevoked {
		return IssuedKey{}, ErrInvalidState
	}
	if len(r.replacements) >= r.limits.MaxReplacements {
		return IssuedKey{}, ErrReplacementCap
	}

	replacementID, seq := r.nextIDLocked("replacement")
	replacement := &IssuedKey{
		ID:         replacementID,
		IncidentID: incidentID,
		KeyID:      incident.KeyID,
		Active:     false,
		Seq:        seq,
	}
	r.replacements[replacementID] = replacement
	r.replacementByIncident[incidentID] = replacementID
	return *replacement, nil
}

// IncidentState reads the state of one incident.
func (r *ApiKeyLeakResponder) IncidentState(incidentID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	incident, ok := r.incidents[incidentID]
	if !ok {
		return "", ErrUnknownIncident
	}
	return incident.State, nil
}

// Incidents lists the incidents of one key, oldest first.
func (r *ApiKeyLeakResponder) Incidents(keyID string) []Incident {
	keyID = strings.TrimSpace(keyID)

	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]Incident, 0)
	for _, incident := range r.incidents {
		if incident.KeyID != keyID {
			continue
		}
		result = append(result, cloneIncident(incident))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Seq < result[j].Seq
	})
	return result
}

// Replacements lists the replacement keys of one key, oldest first.
func (r *ApiKeyLeakResponder) Replacements(keyID string) []IssuedKey {
	keyID = strings.TrimSpace(keyID)

	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]IssuedKey, 0)
	for _, replacement := range r.replacements {
		if replacement.KeyID != keyID {
			continue
		}
		result = append(result, *replacement)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Seq < result[j].Seq
	})
	return result
}

// KeyUsable reports whether traffic with this key may be served right now.
func (r *ApiKeyLeakResponder) KeyUsable(keyID string) bool {
	keyID = strings.TrimSpace(keyID)

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, incident := range r.incidents {
		if incident.KeyID != keyID {
			continue
		}
		if incident.State == IncidentShadowRevoked || incident.State == IncidentHardRevoked {
			return false
		}
	}
	return true
}

// Snapshot serialises incidents, replacements and the sequence counter.
func (r *ApiKeyLeakResponder) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	snapshot := snapshotData{
		Version:      snapshotVersion,
		Seq:          r.seq,
		Incidents:    make([]Incident, 0, len(r.incidents)),
		Replacements: make([]IssuedKey, 0, len(r.replacements)),
	}
	for _, incident := range r.incidents {
		snapshot.Incidents = append(snapshot.Incidents, cloneIncident(incident))
	}
	sort.Slice(snapshot.Incidents, func(i, j int) bool {
		return snapshot.Incidents[i].Seq < snapshot.Incidents[j].Seq
	})
	for _, replacement := range r.replacements {
		snapshot.Replacements = append(snapshot.Replacements, *replacement)
	}
	sort.Slice(snapshot.Replacements, func(i, j int) bool {
		return snapshot.Replacements[i].Seq < snapshot.Replacements[j].Seq
	})
	return json.Marshal(&snapshot)
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (r *ApiKeyLeakResponder) LoadSnapshot(data []byte) error {
	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}

	seq, incidents, activeByKey, replacements, replacementByIncident, err :=
		buildSnapshotState(&snapshot, r.limits)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.seq = seq
	r.incidents = incidents
	r.activeIncidentByKey = activeByKey
	r.replacements = replacements
	r.replacementByIncident = replacementByIncident
	r.mu.Unlock()
	return nil
}

func buildSnapshotState(
	snapshot *snapshotData,
	limits Limits,
) (int64, map[string]*Incident, map[string]string, map[string]*IssuedKey, map[string]string, error) {
	if snapshot.Version != snapshotVersion || snapshot.Seq < 0 {
		return 0, nil, nil, nil, nil, ErrBadSnapshot
	}
	maxIncidents := limits.MaxIncidents
	if maxIncidents < 0 {
		maxIncidents = 0
	}
	maxReplacements := limits.MaxReplacements
	if maxReplacements < 0 {
		maxReplacements = 0
	}
	if len(snapshot.Incidents) > maxIncidents {
		return 0, nil, nil, nil, nil, ErrBadSnapshot
	}
	if len(snapshot.Replacements) > maxReplacements {
		return 0, nil, nil, nil, nil, ErrBadSnapshot
	}

	incidents := make(map[string]*Incident, len(snapshot.Incidents))
	activeByKey := make(map[string]string)
	for i := range snapshot.Incidents {
		incident := snapshot.Incidents[i]
		if blankText(incident.ID) || blankText(incident.KeyID) || blankText(incident.Source) {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		if !knownIncidentState(incident.State) || !knownConfidence(incident.Confidence) {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		if incident.Seq <= 0 || incident.Seq > snapshot.Seq {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		if len(incident.Evidence) == 0 {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		for _, evidence := range incident.Evidence {
			if blankText(evidence) {
				return 0, nil, nil, nil, nil, ErrBadSnapshot
			}
		}
		if (incident.State == IncidentHardRevoked || incident.State == IncidentRestored) &&
			blankText(incident.Actor) {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		incident.ID = strings.TrimSpace(incident.ID)
		incident.KeyID = strings.TrimSpace(incident.KeyID)
		incident.Source = strings.TrimSpace(incident.Source)
		incident.Actor = strings.TrimSpace(incident.Actor)
		incident.Evidence = append([]string(nil), incident.Evidence...)
		if _, duplicate := incidents[incident.ID]; duplicate {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		if incident.State != IncidentRestored {
			if _, duplicate := activeByKey[incident.KeyID]; duplicate {
				return 0, nil, nil, nil, nil, ErrBadSnapshot
			}
			activeByKey[incident.KeyID] = incident.ID
		}

		incidents[incident.ID] = &incident
	}

	replacements := make(map[string]*IssuedKey, len(snapshot.Replacements))
	replacementByIncident := make(map[string]string, len(snapshot.Replacements))
	for i := range snapshot.Replacements {
		replacement := snapshot.Replacements[i]
		if blankText(replacement.ID) || blankText(replacement.IncidentID) || blankText(replacement.KeyID) {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		replacement.ID = strings.TrimSpace(replacement.ID)
		replacement.IncidentID = strings.TrimSpace(replacement.IncidentID)
		replacement.KeyID = strings.TrimSpace(replacement.KeyID)
		if replacement.Seq <= 0 || replacement.Seq > snapshot.Seq || replacement.Active {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		incident, ok := incidents[replacement.IncidentID]
		if !ok || incident.KeyID != strings.TrimSpace(replacement.KeyID) {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, duplicate := replacements[replacement.ID]; duplicate {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, duplicate := replacementByIncident[replacement.IncidentID]; duplicate {
			return 0, nil, nil, nil, nil, ErrBadSnapshot
		}

		replacements[replacement.ID] = &replacement
		replacementByIncident[replacement.IncidentID] = replacement.ID
	}

	return snapshot.Seq, incidents, activeByKey, replacements, replacementByIncident, nil
}
