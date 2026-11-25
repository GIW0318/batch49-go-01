// Package task picks the step-up method for a sensitive operation and keeps
// the ladder from collapsing to the weakest rung.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Authentication methods, ordered from strongest to weakest.
const (
	MethodSecurityKey  = "security_key"
	MethodPlatformAuth = "platform_authenticator"
	MethodTOTP         = "totp"
	MethodPush         = "push"
	MethodSMS          = "sms"
)

// Method strengths. The ladder never moves up to a weaker rung on its own.
const (
	StrengthSecurityKey  = 5
	StrengthPlatformAuth = 4
	StrengthTOTP         = 3
	StrengthPush         = 2
	StrengthSMS          = 1
)

// Challenge states.
const (
	ChallengeOpen      = "open"
	ChallengeCompleted = "completed"
	ChallengeLocked    = "locked"
	ChallengeAbandoned = "abandoned"
)

// Result reasons.
const (
	ReasonVerified     = "verified"
	ReasonAttemptLimit = "attempt_limit"
)

// DowngradeUnavailable is the only reason a weaker rung may be chosen.
const DowngradeUnavailable = "method_unavailable"

// Errors reported by the ladder.
var (
	ErrBlankID           = errors.New("user id and operation are required")
	ErrUnknownMethod     = errors.New("unknown authentication method")
	ErrMethodEnrolled    = errors.New("method is already enrolled")
	ErrMethodNotEnrolled = errors.New("method is not enrolled")
	ErrBadReason         = errors.New("disable reason is required")
	ErrNoMethod          = errors.New("no usable authentication method")
	ErrChallengeNotFound = errors.New("challenge not found")
	ErrChallengeClosed   = errors.New("challenge is already closed")
	ErrChallengeExpired  = errors.New("challenge has expired")
	ErrChallengeLimit    = errors.New("concurrent challenge limit reached for this user")
	ErrMethodNotAllowed  = errors.New("submitted method is not the one the challenge is on")
	ErrBadProof          = errors.New("proof is empty")
	ErrAttemptLimit      = errors.New("attempt limit reached for this challenge")
	ErrNoStrongerMethod  = errors.New("no stronger method is available")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

const snapshotVersion = 1

var methodLadder = []Method{
	{Kind: MethodSecurityKey, Strength: StrengthSecurityKey},
	{Kind: MethodPlatformAuth, Strength: StrengthPlatformAuth},
	{Kind: MethodTOTP, Strength: StrengthTOTP},
	{Kind: MethodPush, Strength: StrengthPush},
	{Kind: MethodSMS, Strength: StrengthSMS},
}

var knownMethods = func() map[string]Method {
	methods := make(map[string]Method, len(methodLadder))
	for _, method := range methodLadder {
		methods[method.Kind] = method
	}
	return methods
}()

// Method is one rung of the ladder as offered to a user.
type Method struct {
	Kind     string `json:"kind"`
	Strength int    `json:"strength"`
}

// Proof is what the user presents for one method.
type Proof struct {
	Code      string `json:"code"`
	Assertion string `json:"assertion"`
	DeviceID  string `json:"device_id"`
}

// Downgrade records why the ladder left a stronger rung.
type Downgrade struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Challenge is one step-up attempt for an operation.
type Challenge struct {
	ID         string      `json:"id"`
	UserID     string      `json:"user_id"`
	Operation  string      `json:"operation"`
	Method     string      `json:"method"`
	Strength   int         `json:"strength"`
	Attempts   int         `json:"attempts"`
	Downgrades []Downgrade `json:"downgrades"`
	State      string      `json:"state"`
	ExpiresAt  time.Time   `json:"expires_at"`
	Seq        int64       `json:"seq"`
}

// Result is one completed step-up.
type Result struct {
	ChallengeID string    `json:"challenge_id"`
	UserID      string    `json:"user_id"`
	Operation   string    `json:"operation"`
	Method      string    `json:"method"`
	Strength    int       `json:"strength"`
	Verified    bool      `json:"verified"`
	Reason      string    `json:"reason"`
	ExpiresAt   time.Time `json:"expires_at"`
	Seq         int64     `json:"seq"`
}

type methodRecord struct {
	Method   string `json:"method"`
	Strength int    `json:"strength"`
	Disabled bool   `json:"disabled"`
	Reason   string `json:"reason"`
}

type snapshotData struct {
	Version     int                                 `json:"version"`
	Seq         int64                               `json:"seq"`
	Enrollments map[string]map[string]*methodRecord `json:"enrollments"`
	Challenges  map[string]*Challenge               `json:"challenges"`
	Results     map[string]*Result                  `json:"results"`
}

// Options bounds the ladder and supplies the clock.
type Options struct {
	MaxChallengesPerUser int
	MaxAttemptsPerMethod int
	ChallengeTTL         time.Duration
	ResultTTL            time.Duration
	Now                  func() time.Time
}

// StepUpMethodLadder owns enrolments, challenges and the reusable results.
type StepUpMethodLadder struct {
	mu          sync.RWMutex
	opts        Options
	seq         int64
	enrollments map[string]map[string]*methodRecord
	challenges  map[string]*Challenge
	results     map[string]*Result
}

// NewStepUpMethodLadder builds a ladder with the given options.
func NewStepUpMethodLadder(opts Options) *StepUpMethodLadder {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxChallengesPerUser <= 0 {
		opts.MaxChallengesPerUser = 10
	}
	if opts.MaxAttemptsPerMethod <= 0 {
		opts.MaxAttemptsPerMethod = 3
	}
	if opts.ChallengeTTL <= 0 {
		opts.ChallengeTTL = 10 * time.Minute
	}
	if opts.ResultTTL <= 0 {
		opts.ResultTTL = 30 * time.Minute
	}

	return &StepUpMethodLadder{
		opts:        opts,
		enrollments: make(map[string]map[string]*methodRecord),
		challenges:  make(map[string]*Challenge),
		results:     make(map[string]*Result),
	}
}

// Enroll registers one method for a user.
func (l *StepUpMethodLadder) Enroll(userID, method string) error {
	if isBlank(userID) {
		return ErrBlankID
	}
	info, ok := knownMethods[method]
	if !ok {
		return ErrUnknownMethod
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	userMethods := l.enrollments[userID]
	if userMethods == nil {
		userMethods = make(map[string]*methodRecord)
		l.enrollments[userID] = userMethods
	}
	if _, enrolled := userMethods[method]; enrolled {
		return ErrMethodEnrolled
	}

	userMethods[method] = &methodRecord{
		Method:   method,
		Strength: info.Strength,
	}
	return nil
}

// Revoke unregisters one method for a user.
func (l *StepUpMethodLadder) Revoke(userID, method string) error {
	if isBlank(userID) {
		return ErrBlankID
	}
	if _, ok := knownMethods[method]; !ok {
		return ErrUnknownMethod
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	userMethods := l.enrollments[userID]
	if _, enrolled := userMethods[method]; !enrolled {
		return ErrMethodNotEnrolled
	}

	delete(userMethods, method)
	if len(userMethods) == 0 {
		delete(l.enrollments, userID)
	}
	return nil
}

// Disable marks an enrolled method temporarily unavailable and records why.
func (l *StepUpMethodLadder) Disable(userID, method, reason string) error {
	if isBlank(userID) {
		return ErrBlankID
	}
	if _, ok := knownMethods[method]; !ok {
		return ErrUnknownMethod
	}
	if isBlank(reason) {
		return ErrBadReason
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	record, ok := l.enrollments[userID][method]
	if !ok {
		return ErrMethodNotEnrolled
	}

	record.Disabled = true
	record.Reason = strings.TrimSpace(reason)
	return nil
}

// Enable brings a disabled method back into the ladder.
func (l *StepUpMethodLadder) Enable(userID, method string) error {
	if isBlank(userID) {
		return ErrBlankID
	}
	if _, ok := knownMethods[method]; !ok {
		return ErrUnknownMethod
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	record, ok := l.enrollments[userID][method]
	if !ok {
		return ErrMethodNotEnrolled
	}

	record.Disabled = false
	record.Reason = ""
	return nil
}

// Available lists the usable methods of a user, strongest first.
func (l *StepUpMethodLadder) Available(userID string) []Method {
	methods := make([]Method, 0, len(methodLadder))
	if isBlank(userID) {
		return methods
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	userMethods := l.enrollments[userID]
	for _, method := range methodLadder {
		if record, ok := userMethods[method.Kind]; ok && !record.Disabled {
			methods = append(methods, method)
		}
	}
	return methods
}

// Begin starts a challenge on the strongest usable method.
func (l *StepUpMethodLadder) Begin(userID, operation string) (Challenge, error) {
	if isBlank(userID) || isBlank(operation) {
		return Challenge{}, ErrBlankID
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	userMethods := l.enrollments[userID]
	selectedIndex := -1
	for i, method := range methodLadder {
		if record, ok := userMethods[method.Kind]; ok && !record.Disabled {
			selectedIndex = i
			break
		}
	}
	if selectedIndex == -1 {
		return Challenge{}, ErrNoMethod
	}

	openForUser := 0
	for _, challenge := range l.challenges {
		if challenge.UserID == userID && isActiveChallenge(challenge, now) {
			openForUser++
		}
	}
	if openForUser >= l.opts.MaxChallengesPerUser {
		return Challenge{}, ErrChallengeLimit
	}

	selected := methodLadder[selectedIndex]
	downgrades := make([]Downgrade, 0, selectedIndex)
	for i := 0; i < selectedIndex; i++ {
		downgrades = append(downgrades, Downgrade{
			From:   methodLadder[i].Kind,
			To:     selected.Kind,
			Reason: DowngradeUnavailable,
		})
	}

	l.seq++
	challenge := &Challenge{
		ID:         fmt.Sprintf("ch-%d", l.seq),
		UserID:     userID,
		Operation:  operation,
		Method:     selected.Kind,
		Strength:   selected.Strength,
		Attempts:   0,
		Downgrades: downgrades,
		State:      ChallengeOpen,
		ExpiresAt:  now.Add(l.opts.ChallengeTTL),
		Seq:        l.seq,
	}
	l.challenges[challenge.ID] = challenge
	return cloneChallenge(challenge), nil
}

// Submit presents a proof for the method the challenge is on.
func (l *StepUpMethodLadder) Submit(challengeID, method string, proof Proof) (Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	challenge, ok := l.challenges[challengeID]
	if !ok {
		return Result{}, ErrChallengeNotFound
	}
	if challenge.State != ChallengeOpen {
		return Result{}, ErrChallengeClosed
	}
	if !now.Before(challenge.ExpiresAt) {
		return Result{}, ErrChallengeExpired
	}
	if method != challenge.Method {
		return Result{}, ErrMethodNotAllowed
	}

	challenge.Attempts++
	if isEmptyProof(proof) {
		if challenge.Attempts >= l.opts.MaxAttemptsPerMethod {
			challenge.State = ChallengeLocked
			result := l.recordResultLocked(challenge, false, ReasonAttemptLimit, now)
			return *result, errors.Join(ErrBadProof, ErrAttemptLimit)
		}
		return Result{}, ErrBadProof
	}

	challenge.State = ChallengeCompleted
	result := l.recordResultLocked(challenge, true, ReasonVerified, now)
	return *result, nil
}

// Escalate moves an open challenge to the next stronger usable method.
func (l *StepUpMethodLadder) Escalate(challengeID string) (Challenge, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	challenge, ok := l.challenges[challengeID]
	if !ok {
		return Challenge{}, ErrChallengeNotFound
	}
	if challenge.State != ChallengeOpen {
		return Challenge{}, ErrChallengeClosed
	}
	if !now.Before(challenge.ExpiresAt) {
		return Challenge{}, ErrChallengeExpired
	}

	userMethods := l.enrollments[challenge.UserID]
	var stronger *Method
	for i := range methodLadder {
		candidate := methodLadder[i]
		if candidate.Strength <= challenge.Strength {
			continue
		}
		if record, ok := userMethods[candidate.Kind]; ok && !record.Disabled {
			stronger = &candidate
			break
		}
	}
	if stronger == nil {
		return Challenge{}, ErrNoStrongerMethod
	}

	challenge.Method = stronger.Kind
	challenge.Strength = stronger.Strength
	challenge.Attempts = 0
	return cloneChallenge(challenge), nil
}

// Abandon closes a challenge without a result.
func (l *StepUpMethodLadder) Abandon(challengeID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	challenge, ok := l.challenges[challengeID]
	if !ok {
		return ErrChallengeNotFound
	}
	if challenge.State != ChallengeOpen {
		return ErrChallengeClosed
	}

	challenge.State = ChallengeAbandoned
	return nil
}

// Reuse returns a still valid completed result for the operation.
func (l *StepUpMethodLadder) Reuse(userID, operation string) (Result, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := l.now()
	var best Result
	found := false
	for _, candidate := range l.results {
		if !candidate.Verified || candidate.UserID != userID || candidate.Operation != operation {
			continue
		}
		if !now.Before(candidate.ExpiresAt) {
			continue
		}
		if !found || candidate.Strength > best.Strength ||
			(candidate.Strength == best.Strength && candidate.Seq > best.Seq) {
			best = *candidate
			found = true
		}
	}
	return best, found
}

// LookupChallenge reads one challenge record.
func (l *StepUpMethodLadder) LookupChallenge(challengeID string) (Challenge, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	challenge, ok := l.challenges[challengeID]
	if !ok {
		return Challenge{}, false
	}
	return cloneChallenge(challenge), true
}

// Snapshot serialises enrolments, challenges, results and the counters.
func (l *StepUpMethodLadder) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	payload := snapshotData{
		Version:     snapshotVersion,
		Seq:         l.seq,
		Enrollments: make(map[string]map[string]*methodRecord, len(l.enrollments)),
		Challenges:  make(map[string]*Challenge, len(l.challenges)),
		Results:     make(map[string]*Result, len(l.results)),
	}

	for userID, methods := range l.enrollments {
		copiedMethods := make(map[string]*methodRecord, len(methods))
		for method, record := range methods {
			recordCopy := *record
			copiedMethods[method] = &recordCopy
		}
		payload.Enrollments[userID] = copiedMethods
	}
	for id, challenge := range l.challenges {
		challengeCopy := cloneChallenge(challenge)
		payload.Challenges[id] = &challengeCopy
	}
	for id, result := range l.results {
		resultCopy := *result
		payload.Results[id] = &resultCopy
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *StepUpMethodLadder) Restore(data []byte) error {
	var payload snapshotData
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(payload); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq = payload.Seq
	l.enrollments = payload.Enrollments
	l.challenges = payload.Challenges
	l.results = payload.Results
	return nil
}

func (l *StepUpMethodLadder) now() time.Time {
	if l.opts.Now == nil {
		return time.Now()
	}
	return l.opts.Now()
}

func (l *StepUpMethodLadder) recordResultLocked(challenge *Challenge, verified bool, reason string, now time.Time) *Result {
	l.seq++
	result := &Result{
		ChallengeID: challenge.ID,
		UserID:      challenge.UserID,
		Operation:   challenge.Operation,
		Method:      challenge.Method,
		Strength:    challenge.Strength,
		Verified:    verified,
		Reason:      reason,
		ExpiresAt:   now.Add(l.opts.ResultTTL),
		Seq:         l.seq,
	}
	l.results[result.ChallengeID] = result
	return result
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func isEmptyProof(proof Proof) bool {
	return strings.TrimSpace(proof.Code) == "" &&
		strings.TrimSpace(proof.Assertion) == "" &&
		strings.TrimSpace(proof.DeviceID) == ""
}

func isActiveChallenge(challenge *Challenge, now time.Time) bool {
	return challenge.State == ChallengeOpen && now.Before(challenge.ExpiresAt)
}

func cloneChallenge(challenge *Challenge) Challenge {
	copyValue := *challenge
	if challenge.Downgrades != nil {
		copyValue.Downgrades = append([]Downgrade(nil), challenge.Downgrades...)
	}
	return copyValue
}

func validateSnapshot(payload snapshotData) error {
	if payload.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported snapshot version", ErrBadSnapshot)
	}
	if payload.Seq < 0 {
		return fmt.Errorf("%w: negative sequence", ErrBadSnapshot)
	}

	for userID, methods := range payload.Enrollments {
		if isBlank(userID) || methods == nil {
			return fmt.Errorf("%w: invalid enrolment", ErrBadSnapshot)
		}
		for mapMethod, record := range methods {
			if record == nil || mapMethod != record.Method {
				return fmt.Errorf("%w: invalid enrolment record", ErrBadSnapshot)
			}
			info, ok := knownMethods[record.Method]
			if !ok || record.Strength != info.Strength {
				return fmt.Errorf("%w: invalid enrolment method", ErrBadSnapshot)
			}
			if record.Disabled && isBlank(record.Reason) {
				return fmt.Errorf("%w: missing disable reason", ErrBadSnapshot)
			}
		}
	}

	for id, challenge := range payload.Challenges {
		if challenge == nil || id != challenge.ID {
			return fmt.Errorf("%w: invalid challenge id", ErrBadSnapshot)
		}
		if isBlank(challenge.UserID) || isBlank(challenge.Operation) {
			return fmt.Errorf("%w: invalid challenge owner", ErrBadSnapshot)
		}
		info, ok := knownMethods[challenge.Method]
		if !ok || challenge.Strength != info.Strength {
			return fmt.Errorf("%w: invalid challenge method", ErrBadSnapshot)
		}
		if challenge.Attempts < 0 || challenge.Seq <= 0 || challenge.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: invalid challenge counters or expiry", ErrBadSnapshot)
		}
		switch challenge.State {
		case ChallengeOpen, ChallengeCompleted, ChallengeLocked, ChallengeAbandoned:
		default:
			return fmt.Errorf("%w: invalid challenge state", ErrBadSnapshot)
		}
		for _, downgrade := range challenge.Downgrades {
			from, fromOK := knownMethods[downgrade.From]
			to, toOK := knownMethods[downgrade.To]
			if !fromOK || !toOK || from.Strength <= to.Strength || downgrade.Reason != DowngradeUnavailable {
				return fmt.Errorf("%w: invalid downgrade record", ErrBadSnapshot)
			}
		}
	}

	for id, result := range payload.Results {
		if result == nil || id != result.ChallengeID {
			return fmt.Errorf("%w: invalid result id", ErrBadSnapshot)
		}
		if isBlank(result.UserID) || isBlank(result.Operation) {
			return fmt.Errorf("%w: invalid result owner", ErrBadSnapshot)
		}
		info, ok := knownMethods[result.Method]
		if !ok || result.Strength != info.Strength {
			return fmt.Errorf("%w: invalid result method", ErrBadSnapshot)
		}
		if result.Seq <= 0 || result.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: invalid result counters or expiry", ErrBadSnapshot)
		}
		switch result.Reason {
		case ReasonVerified:
			if !result.Verified {
				return fmt.Errorf("%w: unverified verified result", ErrBadSnapshot)
			}
		case ReasonAttemptLimit:
			if result.Verified {
				return fmt.Errorf("%w: verified locked result", ErrBadSnapshot)
			}
		default:
			return fmt.Errorf("%w: invalid result reason", ErrBadSnapshot)
		}
		if challenge, ok := payload.Challenges[result.ChallengeID]; !ok ||
			challenge.UserID != result.UserID || challenge.Operation != result.Operation {
			return fmt.Errorf("%w: result does not match challenge", ErrBadSnapshot)
		}
	}

	return nil
}
