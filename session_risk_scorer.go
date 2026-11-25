// Package task scores behavioural deviation for established sessions and
// decides when a challenge has to be raised and latched.
package task

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event types understood by the scorer.
const (
	EventLoginGeo     = "login_geo"
	EventDeviceChange = "device_change"
	EventUaChange     = "ua_change"
	EventCommandBurst = "command_burst"
)

// Reason codes returned by Explain.
const (
	ReasonBaselineDeviation = "reason_baseline_deviation"
	ReasonSaturated         = "reason_saturated"
	ReasonChallengeLatched  = "reason_challenge_latched"
)

// Challenge levels.
const (
	ChallengeStepUp = "step_up"
	ChallengeBlock  = "block"
)

// Scoring anchors.
const (
	SlotCount       = 6
	SlotHours       = 4
	InitialBaseline = 1.0
	BaselineAlpha   = 0.2
	ScoreAlpha      = 0.3
	DeviationScale  = 10.0
	DefaultMaxScore = 60

	snapshotFormatVersion = 1
	defaultSessionLimit   = 64
	defaultQueueLimit     = 128
	scoreEpsilon          = 1e-9
)

// Errors returned by the scorer.
var (
	ErrBlankSession   = errors.New("session id is required")
	ErrUnknownSession = errors.New("unknown session")
	ErrBadEvent       = errors.New("unknown event type")
	ErrBadChallenge   = errors.New("unknown challenge")
	ErrQueueFull      = errors.New("session event queue is full")
	ErrSessionLimit   = errors.New("session limit reached for this user")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ScorerConfig bounds one scorer instance.
type ScorerConfig struct {
	MaxSessionsPerUser int
	MaxQueuePerSession int
	MaxScore           int
	ChallengeThreshold int
}

// Event is one behavioural observation inside a session.
type Event struct {
	UserID   string
	Type     string
	Weight   float64
	At       time.Time
	TimeZone string
}

// Score is the deviation score snapshot of a session.
type Score struct {
	SessionID string
	UserID    string
	Value     float64
	Saturated bool
	Version   uint64
	UpdatedAt time.Time
}

// Reason explains one contribution to a score.
type Reason struct {
	Code   string
	Detail string
	Delta  float64
}

// ChallengeState is the latched challenge of a session.
type ChallengeState struct {
	SessionID   string
	Level       string
	ChallengeID string
	Latched     bool
	IssuedAt    time.Time
}

type sessionState struct {
	score              Score
	sequence           uint64
	reasons            []Reason
	challenge          *ChallengeState
	issuedChallengeIDs []string
}

type scorerRoot struct {
	nextSessionSequence uint64
	baselines           map[string]map[string]map[int]float64
	sessions            map[string]*sessionState
	userSessions        map[string][]string
}

type snapshotData struct {
	FormatVersion       int                                   `json:"format_version"`
	NextSessionSequence uint64                                `json:"next_session_sequence"`
	Baselines           map[string]map[string]map[int]float64 `json:"baselines"`
	Sessions            map[string]sessionSnapshot            `json:"sessions"`
	UserSessions        map[string][]string                   `json:"user_sessions"`
}

type sessionSnapshot struct {
	Score              Score           `json:"score"`
	Sequence           uint64          `json:"sequence"`
	Reasons            []Reason        `json:"reasons"`
	Challenge          *ChallengeState `json:"challenge,omitempty"`
	IssuedChallengeIDs []string        `json:"issued_challenge_ids,omitempty"`
}

// SessionRiskScorer owns per-user baselines and per-session scores.
type SessionRiskScorer struct {
	cfg    ScorerConfig
	root   atomic.Pointer[scorerRoot]
	queues sync.Map
}

// NewSessionRiskScorer builds a scorer with the given bounds.
func NewSessionRiskScorer(cfg ScorerConfig) *SessionRiskScorer {
	cfg = normalizeConfig(cfg)
	s := &SessionRiskScorer{cfg: cfg}
	s.root.Store(newScorerRoot())
	return s
}

func normalizeConfig(cfg ScorerConfig) ScorerConfig {
	if cfg.MaxScore <= 0 {
		cfg.MaxScore = DefaultMaxScore
	}
	if cfg.MaxSessionsPerUser <= 0 {
		cfg.MaxSessionsPerUser = defaultSessionLimit
	}
	if cfg.MaxQueuePerSession <= 0 {
		cfg.MaxQueuePerSession = defaultQueueLimit
	}
	if cfg.ChallengeThreshold < 0 {
		cfg.ChallengeThreshold = 0
	}
	return cfg
}

func newScorerRoot() *scorerRoot {
	return &scorerRoot{
		baselines:    make(map[string]map[string]map[int]float64),
		sessions:     make(map[string]*sessionState),
		userSessions: make(map[string][]string),
	}
}

func knownEventType(eventType string) bool {
	switch eventType {
	case EventLoginGeo, EventDeviceChange, EventUaChange, EventCommandBurst:
		return true
	default:
		return false
	}
}

func blankSession(sessionID string) bool {
	return strings.TrimSpace(sessionID) == ""
}

func eventSlot(e Event) (int, error) {
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}

	if timeZone := strings.TrimSpace(e.TimeZone); timeZone != "" {
		location, err := time.LoadLocation(timeZone)
		if err != nil {
			location, err = offsetLocation(timeZone)
		}
		if err != nil {
			return 0, fmt.Errorf("%w: time zone %q", ErrBadEvent, e.TimeZone)
		}
		at = at.In(location)
	}

	slot := at.Hour() / SlotHours
	if slot < 0 || slot >= SlotCount {
		return 0, fmt.Errorf("%w: slot %d is outside 0..%d", ErrBadEvent, slot, SlotCount-1)
	}
	return slot, nil
}

func offsetLocation(timeZone string) (*time.Location, error) {
	text := strings.ToUpper(strings.TrimSpace(timeZone))
	text = strings.TrimPrefix(text, "UTC")
	text = strings.TrimPrefix(text, "GMT")
	if text == "" || text == "Z" {
		return time.UTC, nil
	}
	if hours, err := strconv.Atoi(text); err == nil && hours >= -23 && hours <= 23 {
		return time.FixedZone(timeZone, hours*60*60), nil
	}
	if len(text) < 2 || (text[0] != '+' && text[0] != '-') {
		return nil, fmt.Errorf("unsupported time zone %q", timeZone)
	}

	sign := 1
	if text[0] == '-' {
		sign = -1
	}
	body := text[1:]
	hours, minutes := 0, 0
	var err error

	if strings.Contains(body, ":") {
		parts := strings.Split(body, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("unsupported time zone offset %q", timeZone)
		}
		if hours, err = strconv.Atoi(parts[0]); err != nil {
			return nil, err
		}
		if minutes, err = strconv.Atoi(parts[1]); err != nil {
			return nil, err
		}
	} else {
		switch len(body) {
		case 2:
			if hours, err = strconv.Atoi(body); err != nil {
				return nil, err
			}
		case 4:
			if hours, err = strconv.Atoi(body[:2]); err != nil {
				return nil, err
			}
			if minutes, err = strconv.Atoi(body[2:]); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported time zone offset %q", timeZone)
		}
	}

	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return nil, fmt.Errorf("time zone offset %q is out of range", timeZone)
	}
	offset := sign * (hours*60*60 + minutes*60)
	return time.FixedZone(timeZone, offset), nil
}

func validateEvent(e Event) error {
	if !knownEventType(e.Type) {
		return fmt.Errorf("%w: %q", ErrBadEvent, e.Type)
	}
	if math.IsNaN(e.Weight) || math.IsInf(e.Weight, 0) || e.Weight < 0 {
		return fmt.Errorf("%w: weight must be finite and non-negative", ErrBadEvent)
	}
	if _, err := eventSlot(e); err != nil {
		return err
	}
	return nil
}

func (r *scorerRoot) clone() *scorerRoot {
	next := newScorerRoot()
	next.nextSessionSequence = r.nextSessionSequence

	for userID, eventTypes := range r.baselines {
		copiedTypes := make(map[string]map[int]float64, len(eventTypes))
		for eventType, slots := range eventTypes {
			copiedSlots := make(map[int]float64, len(slots))
			for slot, value := range slots {
				copiedSlots[slot] = value
			}
			copiedTypes[eventType] = copiedSlots
		}
		next.baselines[userID] = copiedTypes
	}

	for userID, sessionIDs := range r.userSessions {
		next.userSessions[userID] = append([]string(nil), sessionIDs...)
	}

	for sessionID, session := range r.sessions {
		copied := *session
		copied.reasons = append([]Reason(nil), session.reasons...)
		copied.issuedChallengeIDs = append([]string(nil), session.issuedChallengeIDs...)
		if session.challenge != nil {
			challenge := *session.challenge
			copied.challenge = &challenge
		}
		next.sessions[sessionID] = &copied
	}

	return next
}

func (s *SessionRiskScorer) queueGate(sessionID string) chan struct{} {
	gate, ok := s.queues.Load(sessionID)
	if ok {
		return gate.(chan struct{})
	}
	created := make(chan struct{}, s.cfg.MaxQueuePerSession)
	actual, _ := s.queues.LoadOrStore(sessionID, created)
	return actual.(chan struct{})
}

func (s *SessionRiskScorer) acquireQueueSlot(sessionID string) (func(), error) {
	gate := s.queueGate(sessionID)
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	default:
		return nil, ErrQueueFull
	}
}

// Observe folds one event into the session score.
func (s *SessionRiskScorer) Observe(sessionID string, e Event) (Score, error) {
	if blankSession(sessionID) {
		return Score{}, ErrBlankSession
	}
	if err := validateEvent(e); err != nil {
		return Score{}, err
	}
	slot, err := eventSlot(e)
	if err != nil {
		return Score{}, err
	}

	release, err := s.acquireQueueSlot(sessionID)
	if err != nil {
		return Score{}, err
	}
	defer release()

	var score Score
	for {
		current := s.root.Load()
		next := current.clone()
		score, err = next.applyObserve(sessionID, e, slot, s.cfg)
		if err != nil {
			return Score{}, err
		}
		if s.root.CompareAndSwap(current, next) {
			return score, nil
		}
	}
}

func (r *scorerRoot) applyObserve(sessionID string, e Event, slot int, cfg ScorerConfig) (Score, error) {
	session, exists := r.sessions[sessionID]
	userID := e.UserID

	if !exists {
		sessionIDs := r.userSessions[userID]
		if len(sessionIDs) >= cfg.MaxSessionsPerUser {
			return Score{}, ErrSessionLimit
		}

		r.nextSessionSequence++
		session = &sessionState{
			sequence: r.nextSessionSequence,
			score: Score{
				SessionID: sessionID,
				UserID:    userID,
				Version:   0,
			},
		}
		r.sessions[sessionID] = session
		r.userSessions[userID] = append(r.userSessions[userID], sessionID)
	} else {
		userID = session.score.UserID
	}

	typeBaselines, ok := r.baselines[userID]
	if !ok {
		typeBaselines = make(map[string]map[int]float64)
		r.baselines[userID] = typeBaselines
	}
	slotBaselines, ok := typeBaselines[e.Type]
	if !ok {
		slotBaselines = make(map[int]float64)
		typeBaselines[e.Type] = slotBaselines
	}

	baseline, ok := slotBaselines[slot]
	if !ok || !finitePositive(baseline) {
		baseline = InitialBaseline
	}

	deviation := relativeDeviation(e.Weight, baseline)
	instantScore := deviation * DeviationScale
	contribution := ScoreAlpha * instantScore
	previous := session.score.Value
	rawScore := (1-ScoreAlpha)*previous + contribution
	maxScore := float64(cfg.MaxScore)

	saturated := rawScore >= maxScore-scoreEpsilon
	value := rawScore
	if saturated {
		value = maxScore
	}

	session.score = Score{
		SessionID: sessionID,
		UserID:    userID,
		Value:     value,
		Saturated: saturated,
		Version:   session.score.Version + 1,
		UpdatedAt: time.Now().UTC(),
	}

	session.reasons = []Reason{{
		Code:   ReasonBaselineDeviation,
		Delta:  contribution,
		Detail: fmt.Sprintf("type=%s slot=%d weight=%g baseline=%.6f deviation=%.6f", e.Type, slot, e.Weight, baseline, deviation),
	}}
	if saturated {
		session.reasons = append(session.reasons, Reason{
			Code:   ReasonSaturated,
			Delta:  math.Max(0, rawScore-maxScore),
			Detail: fmt.Sprintf("raw_score=%.6f max_score=%g", rawScore, maxScore),
		})
	}

	if (session.challenge == nil || !session.challenge.Latched) &&
		cfg.ChallengeThreshold > 0 &&
		session.score.Value >= float64(cfg.ChallengeThreshold) {
		challenge := newChallengeState(sessionID, ChallengeStepUp)
		session.challenge = &challenge
		session.issuedChallengeIDs = append(session.issuedChallengeIDs, challenge.ChallengeID)
	}

	slotBaselines[slot] = (1-BaselineAlpha)*baseline + BaselineAlpha*e.Weight
	return session.score, nil
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func relativeDeviation(weight, baseline float64) float64 {
	if baseline == 0 {
		if weight == 0 {
			return 0
		}
		return math.Abs(weight)
	}
	return math.Abs(weight-baseline) / baseline
}

// Score returns the current score of a session.
func (s *SessionRiskScorer) Score(sessionID string) (Score, error) {
	if blankSession(sessionID) {
		return Score{}, ErrBlankSession
	}
	root := s.root.Load()
	session, ok := root.sessions[sessionID]
	if !ok {
		return Score{}, ErrUnknownSession
	}
	return session.score, nil
}

// Explain returns the reasons behind the current score.
func (s *SessionRiskScorer) Explain(sessionID string) []Reason {
	if blankSession(sessionID) {
		return nil
	}
	root := s.root.Load()
	session, ok := root.sessions[sessionID]
	if !ok {
		return nil
	}

	reasons := append([]Reason(nil), session.reasons...)
	if session.challenge != nil && session.challenge.Latched {
		reasons = append(reasons, Reason{
			Code:   ReasonChallengeLatched,
			Delta:  0,
			Detail: fmt.Sprintf("level=%s challenge_id=%s", session.challenge.Level, session.challenge.ChallengeID),
		})
	}
	return reasons
}

func validChallengeLevel(level string) bool {
	return level == ChallengeStepUp || level == ChallengeBlock
}

func newChallengeID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("challenge-%d", time.Now().UnixNano())
	}
	return "challenge-" + hex.EncodeToString(raw)
}

func newChallengeState(sessionID, level string) ChallengeState {
	return ChallengeState{
		SessionID:   sessionID,
		Level:       level,
		ChallengeID: newChallengeID(),
		Latched:     true,
		IssuedAt:    time.Now().UTC(),
	}
}

// Challenge latches a challenge on a session.
func (s *SessionRiskScorer) Challenge(sessionID, level string) error {
	if blankSession(sessionID) {
		return ErrBlankSession
	}

	challenge := newChallengeState(sessionID, level)
	for {
		current := s.root.Load()
		if _, ok := current.sessions[sessionID]; !ok {
			return ErrUnknownSession
		}
		if !validChallengeLevel(level) {
			return fmt.Errorf("%w: level %q", ErrBadChallenge, level)
		}

		next := current.clone()
		session := next.sessions[sessionID]
		if session.challenge != nil && session.challenge.Latched {
			if session.challenge.Level != level {
				session.challenge.Level = level
			}
		} else {
			session.challenge = &challenge
			session.issuedChallengeIDs = append(session.issuedChallengeIDs, challenge.ChallengeID)
		}
		if s.root.CompareAndSwap(current, next) {
			return nil
		}
	}
}

// Ack acknowledges a latched challenge.
func (s *SessionRiskScorer) Ack(sessionID, challengeID string) error {
	if blankSession(sessionID) {
		return ErrBlankSession
	}
	if strings.TrimSpace(challengeID) == "" {
		return ErrBadChallenge
	}

	for {
		current := s.root.Load()
		session, ok := current.sessions[sessionID]
		if !ok {
			return ErrUnknownSession
		}
		if session.challenge == nil || !session.challenge.Latched ||
			!containsString(session.issuedChallengeIDs, challengeID) {
			return ErrBadChallenge
		}

		next := current.clone()
		nextSession := next.sessions[sessionID]
		nextSession.challenge = nil
		nextSession.issuedChallengeIDs = nil
		if s.root.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Snapshot serialises the scorer state.
func (s *SessionRiskScorer) Snapshot() ([]byte, error) {
	current := s.root.Load()
	data := snapshotData{
		FormatVersion:       snapshotFormatVersion,
		NextSessionSequence: current.nextSessionSequence,
		Baselines:           current.baselines,
		Sessions:            make(map[string]sessionSnapshot, len(current.sessions)),
		UserSessions:        current.userSessions,
	}

	for sessionID, session := range current.sessions {
		data.Sessions[sessionID] = sessionSnapshot{
			Score:              session.score,
			Sequence:           session.sequence,
			Reasons:            append([]Reason(nil), session.reasons...),
			Challenge:          session.challenge,
			IssuedChallengeIDs: append([]string(nil), session.issuedChallengeIDs...),
		}
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	return payload, nil
}

// Restore replaces the scorer state from a snapshot.
func (s *SessionRiskScorer) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	root, err := snapshot.toRoot(s.cfg)
	if err != nil {
		return err
	}
	s.root.Store(root)
	return nil
}

func (snapshot snapshotData) toRoot(cfg ScorerConfig) (*scorerRoot, error) {
	if snapshot.FormatVersion != snapshotFormatVersion {
		return nil, fmt.Errorf("%w: unsupported format version %d", ErrBadSnapshot, snapshot.FormatVersion)
	}

	root := newScorerRoot()
	root.nextSessionSequence = snapshot.NextSessionSequence

	for userID, eventTypes := range snapshot.Baselines {
		if userID == "" {
			return nil, fmt.Errorf("%w: blank baseline user", ErrBadSnapshot)
		}
		copiedTypes := make(map[string]map[int]float64, len(eventTypes))
		for eventType, slots := range eventTypes {
			if !knownEventType(eventType) {
				return nil, fmt.Errorf("%w: event type %q", ErrBadSnapshot, eventType)
			}
			copiedSlots := make(map[int]float64, len(slots))
			for slot, value := range slots {
				if slot < 0 || slot >= SlotCount {
					return nil, fmt.Errorf("%w: slot %d", ErrBadSnapshot, slot)
				}
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return nil, fmt.Errorf("%w: baseline value for %s/%d", ErrBadSnapshot, eventType, slot)
				}
				copiedSlots[slot] = value
			}
			copiedTypes[eventType] = copiedSlots
		}
		root.baselines[userID] = copiedTypes
	}

	for sessionID, item := range snapshot.Sessions {
		if blankSession(sessionID) {
			return nil, fmt.Errorf("%w: blank session id", ErrBadSnapshot)
		}
		score := item.Score
		if score.SessionID == "" {
			score.SessionID = sessionID
		} else if score.SessionID != sessionID {
			return nil, fmt.Errorf("%w: session id mismatch", ErrBadSnapshot)
		}
		if score.Version == 0 {
			return nil, fmt.Errorf("%w: session %s has zero version", ErrBadSnapshot, sessionID)
		}
		if math.IsNaN(score.Value) || math.IsInf(score.Value, 0) || score.Value < 0 {
			return nil, fmt.Errorf("%w: session %s has invalid score", ErrBadSnapshot, sessionID)
		}
		if score.Value > float64(cfg.MaxScore)+scoreEpsilon {
			score.Value = float64(cfg.MaxScore)
			score.Saturated = true
		}

		state := &sessionState{
			score:              score,
			sequence:           item.Sequence,
			reasons:            append([]Reason(nil), item.Reasons...),
			issuedChallengeIDs: append([]string(nil), item.IssuedChallengeIDs...),
		}
		if item.Challenge != nil {
			challenge := *item.Challenge
			if challenge.Latched {
				if challenge.ChallengeID == "" || !validChallengeLevel(challenge.Level) {
					return nil, fmt.Errorf("%w: invalid challenge for %s", ErrBadSnapshot, sessionID)
				}
				challenge.SessionID = sessionID
				state.challenge = &challenge
				if !containsString(state.issuedChallengeIDs, challenge.ChallengeID) {
					state.issuedChallengeIDs = append(state.issuedChallengeIDs, challenge.ChallengeID)
				}
			}
		}
		root.sessions[sessionID] = state
	}

	for userID, sessionIDs := range snapshot.UserSessions {
		if userID == "" {
			return nil, fmt.Errorf("%w: blank session-list user", ErrBadSnapshot)
		}
		seen := make(map[string]struct{}, len(sessionIDs))
		copiedIDs := make([]string, 0, len(sessionIDs))
		for _, sessionID := range sessionIDs {
			state, ok := root.sessions[sessionID]
			if !ok {
				return nil, fmt.Errorf("%w: session %s missing from snapshot", ErrBadSnapshot, sessionID)
			}
			if state.score.UserID != userID {
				return nil, fmt.Errorf("%w: session %s user mismatch", ErrBadSnapshot, sessionID)
			}
			if _, duplicate := seen[sessionID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate session %s", ErrBadSnapshot, sessionID)
			}
			seen[sessionID] = struct{}{}
			copiedIDs = append(copiedIDs, sessionID)
		}
		root.userSessions[userID] = copiedIDs
	}

	for sessionID, state := range root.sessions {
		userID := state.score.UserID
		ids := root.userSessions[userID]
		if !containsString(ids, sessionID) {
			root.userSessions[userID] = append(ids, sessionID)
		}
	}

	return root, nil
}

// Baseline returns the per-user, per-event-type, per-slot baseline.
func (s *SessionRiskScorer) Baseline(userID, eventType string, slot int) (float64, error) {
	if !knownEventType(eventType) {
		return 0, fmt.Errorf("%w: %q", ErrBadEvent, eventType)
	}
	if slot < 0 || slot >= SlotCount {
		return 0, fmt.Errorf("%w: slot %d is outside 0..%d", ErrBadEvent, slot, SlotCount-1)
	}

	root := s.root.Load()
	if eventTypes, ok := root.baselines[userID]; ok {
		if slots, ok := eventTypes[eventType]; ok {
			if value, ok := slots[slot]; ok && finitePositive(value) {
				return value, nil
			}
		}
	}
	return InitialBaseline, nil
}

// ChallengeState returns the latched challenge state of a session.
func (s *SessionRiskScorer) ChallengeState(sessionID string) (ChallengeState, bool) {
	if blankSession(sessionID) {
		return ChallengeState{}, false
	}
	root := s.root.Load()
	session, ok := root.sessions[sessionID]
	if !ok || session.challenge == nil || !session.challenge.Latched {
		return ChallengeState{}, false
	}
	return *session.challenge, true
}

// Sessions lists the session identifiers opened by a user.
func (s *SessionRiskScorer) Sessions(userID string) []string {
	root := s.root.Load()
	return append([]string{}, root.userSessions[userID]...)
}
