// Package task grades login requests into risk tiers before authentication and
// picks the matching challenge.
package task

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Risk tiers.
const (
	TierLow    = "low"
	TierMedium = "medium"
	TierHigh   = "high"
	TierSevere = "severe"
)

// Challenge kinds.
const (
	ChallengeNone  = "none"
	ChallengeSMS   = "sms"
	ChallengeTOTP  = "totp"
	ChallengeBlock = "block"
)

// Decision results.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Reasons attached to a tier or a decision.
const (
	ReasonClean            = "reason_clean"
	ReasonSoftSignals      = "reason_soft_signals"
	ReasonHardRule         = "reason_hard_rule"
	ReasonOperatorOverride = "reason_operator_override"
	ReasonCodeMismatch     = "reason_code_mismatch"
	ReasonLocked           = "reason_attempts_exhausted"
)

// RegionBlocked is the hard rule region that always lands in the severe tier.
const RegionBlocked = "blocked"

// MaxAttempts is the number of rejected proofs a challenge tolerates.
const MaxAttempts = 3

// Errors returned by the tier engine.
var (
	ErrBadEvent          = errors.New("login event is missing required fields")
	ErrUnknownTier       = errors.New("unknown tier")
	ErrBlankDevice       = errors.New("user id and device id are required")
	ErrBadTTL            = errors.New("device trust ttl must be positive")
	ErrDeviceLimit       = errors.New("trusted device limit reached for this user")
	ErrBlankField        = errors.New("actor and reason are required")
	ErrAlreadyOverridden = errors.New("tier was already overridden")
	ErrTooManyAttempts   = errors.New("challenge attempts exhausted")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// LoginEvent is one login request entering the tier engine.
type LoginEvent struct {
	UserID      string    `json:"user_id"`
	DeviceID    string    `json:"device_id"`
	Region      string    `json:"region"`
	Hour        int       `json:"hour"`
	NewDevice   bool      `json:"new_device"`
	UnusualHour bool      `json:"unusual_hour"`
	NewNetwork  bool      `json:"new_network"`
	LeakedCred  bool      `json:"leaked_credential"`
	At          time.Time `json:"at"`
}

// Tier is one graded login record.
type Tier struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	Level      string `json:"level"`
	Score      int    `json:"score"`
	Reason     string `json:"reason"`
	Overridden bool   `json:"overridden"`
	Seq        int64  `json:"seq"`
}

// Challenge is one issued challenge for a tier.
type Challenge struct {
	TierID   string `json:"tier_id"`
	UserID   string `json:"user_id"`
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	Attempts int    `json:"attempts"`
}

// Proof is the answer a client sends back for a challenge.
type Proof struct {
	Code string `json:"code"`
}

// Decision is the outcome of one proof check.
type Decision struct {
	TierID   string `json:"tier_id"`
	Result   string `json:"result"`
	Reason   string `json:"reason"`
	Attempts int    `json:"attempts"`
	Locked   bool   `json:"locked"`
}

// Limits bounds the tier engine tables.
type Limits struct {
	MaxDevicesPerUser int
	MaxTiersPerUser   int
}

// tierRecord is the internal form of a tier: the public record plus the
// fingerprint of the event that produced it. The fingerprint makes repeated
// submissions of the same event idempotent without counting them again.
type tierRecord struct {
	Tier        Tier   `json:"tier"`
	Fingerprint string `json:"fingerprint"`
}

type challengeState struct {
	TierID   string `json:"tier_id"`
	UserID   string `json:"user_id"`
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	Attempts int    `json:"attempts"`
	Locked   bool   `json:"locked"`
}

// snapshotState is the on-disk representation. Checksum covers the whole
// payload, so a truncated or byte-flipped snapshot is rejected atomically.
type snapshotState struct {
	Version    int                             `json:"version"`
	Seq        int64                           `json:"seq"`
	Tiers      map[string][]tierRecord         `json:"tiers"`
	Trust      map[string]map[string]time.Time `json:"trust"`
	Challenges map[string]challengeState       `json:"challenges"`
	Checksum   string                          `json:"checksum,omitempty"`
}

const snapshotVersion = 1

// LoginRiskTier owns the tier table, the trusted device table and the clock.
type LoginRiskTier struct {
	mu sync.Mutex

	limits    Limits
	now       func() time.Time
	seq       int64
	tiers     map[string][]tierRecord
	tierByID  map[string]string
	trust     map[string]map[string]time.Time
	challenge map[string]*challengeState
}

// NewLoginRiskTier builds a tier engine with the given caps and clock.
func NewLoginRiskTier(limits Limits, now func() time.Time) *LoginRiskTier {
	if now == nil {
		now = time.Now
	}
	return &LoginRiskTier{
		limits:    limits,
		now:       now,
		tiers:     make(map[string][]tierRecord),
		tierByID:  make(map[string]string),
		trust:     make(map[string]map[string]time.Time),
		challenge: make(map[string]*challengeState),
	}
}

// Classify grades one login event into a risk tier.
func (r *LoginRiskTier) Classify(e LoginEvent) (Tier, error) {
	if strings.TrimSpace(e.UserID) == "" ||
		e.Hour < 0 || e.Hour > 23 ||
		strings.TrimSpace(e.Region) == "" {
		return Tier{}, ErrBadEvent
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	hardRule := strings.EqualFold(strings.TrimSpace(e.Region), RegionBlocked) || e.LeakedCred
	score := 0
	if !hardRule {
		if e.NewDevice {
			score++
		}
		if e.UnusualHour {
			score++
		}
		if e.NewNetwork {
			score++
		}
	}

	level := tierForScore(score)
	reason := ReasonSoftSignals
	if score == 0 {
		reason = ReasonClean
	}
	exempted := false
	if hardRule {
		level = TierSevere
		reason = ReasonHardRule
	} else if level == TierMedium && r.trustedLocked(e.UserID, e.DeviceID) {
		// A trusted device waives only the medium tier. High and severe
		// signals still have to go through the normal challenge path.
		level = TierLow
		exempted = true
	}

	fingerprint := eventFingerprint(e, exempted)
	if existing, ok := r.findByFingerprintLocked(e.UserID, fingerprint); ok {
		return existing, nil
	}

	r.seq++
	tier := Tier{
		ID:     "tier-" + formatSeq(r.seq),
		UserID: e.UserID,
		Level:  level,
		Score:  score,
		Reason: reason,
		Seq:    r.seq,
	}
	r.appendTierLocked(tierRecord{Tier: tier, Fingerprint: fingerprint})
	return tier, nil
}

// Challenge issues the challenge that matches a tier.
func (r *LoginRiskTier) Challenge(tierID, userID string) (Challenge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tier, ok := r.tierLocked(tierID)
	if !ok {
		return Challenge{}, ErrUnknownTier
	}
	ch := r.challengeLocked(tier)
	return ch, nil
}

// Verify checks one proof against the challenge of a tier.
func (r *LoginRiskTier) Verify(tierID string, proof Proof) (Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tier, ok := r.tierLocked(tierID)
	if !ok {
		return Decision{}, ErrUnknownTier
	}

	state := r.challenge[tierID]
	if state == nil {
		ch := r.challengeLocked(tier)
		state = r.challenge[ch.TierID]
	}

	if state.Kind == ChallengeNone {
		return Decision{TierID: tierID, Result: DecisionAllow, Reason: ReasonClean}, nil
	}
	if state.Kind == ChallengeBlock {
		return Decision{
			TierID:   tierID,
			Result:   DecisionDeny,
			Reason:   ReasonLocked,
			Attempts: state.Attempts,
			Locked:   true,
		}, nil
	}
	if state.Locked || state.Attempts >= MaxAttempts {
		return Decision{}, ErrTooManyAttempts
	}

	if subtle.ConstantTimeCompare([]byte(state.Code), []byte(proof.Code)) == 1 {
		return Decision{
			TierID:   tierID,
			Result:   DecisionAllow,
			Reason:   ReasonClean,
			Attempts: state.Attempts,
		}, nil
	}

	state.Attempts++
	locked := state.Attempts >= MaxAttempts
	state.Locked = locked
	reason := ReasonCodeMismatch
	if locked {
		reason = ReasonLocked
	}
	return Decision{
		TierID:   tierID,
		Result:   DecisionDeny,
		Reason:   reason,
		Attempts: state.Attempts,
		Locked:   locked,
	}, nil
}

// TrustDevice marks a device as trusted for the given ttl.
func (r *LoginRiskTier) TrustDevice(userID, deviceID string, ttl time.Duration) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(deviceID) == "" {
		return ErrBlankDevice
	}
	if ttl <= 0 {
		return ErrBadTTL
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	devices := r.trust[userID]
	if devices == nil {
		devices = make(map[string]time.Time)
		r.trust[userID] = devices
	}
	r.purgeExpiredLocked(devices, now)

	if _, exists := devices[deviceID]; !exists {
		if r.limits.MaxDevicesPerUser <= 0 ||
			len(devices) >= r.limits.MaxDevicesPerUser {
			return ErrDeviceLimit
		}
	}
	devices[deviceID] = now.Add(ttl)
	return nil
}

// Override records an operator decision that downgrades a tier to a challenge.
func (r *LoginRiskTier) Override(tierID, actorID, reason string) error {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" {
		return ErrBlankField
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	userID, ok := r.tierByID[tierID]
	if !ok {
		return ErrUnknownTier
	}
	records := r.tiers[userID]
	for i := range records {
		if records[i].Tier.ID != tierID {
			continue
		}
		if records[i].Tier.Overridden {
			return ErrAlreadyOverridden
		}
		records[i].Tier.Overridden = true
		records[i].Tier.Level = TierMedium
		records[i].Tier.Reason = ReasonOperatorOverride
		r.tiers[userID] = records
		// The override changes the challenge matrix for this tier.
		delete(r.challenge, tierID)
		return nil
	}
	return ErrUnknownTier
}

// Tiers lists the tier records of one user, oldest first.
func (r *LoginRiskTier) Tiers(userID string) []Tier {
	r.mu.Lock()
	defer r.mu.Unlock()

	records := r.tiers[userID]
	if len(records) == 0 {
		return nil
	}
	out := make([]Tier, len(records))
	for i := range records {
		out[i] = records[i].Tier
	}
	return out
}

// Trusted reports whether a device is still inside its trust window.
func (r *LoginRiskTier) Trusted(userID, deviceID string) bool {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(deviceID) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trustedLocked(userID, deviceID)
}

// Snapshot serialises tiers, trusted device expiry and the sequence watermark.
func (r *LoginRiskTier) Snapshot() ([]byte, error) {
	r.mu.Lock()
	state := snapshotState{
		Version:    snapshotVersion,
		Seq:        r.seq,
		Tiers:      make(map[string][]tierRecord, len(r.tiers)),
		Trust:      make(map[string]map[string]time.Time, len(r.trust)),
		Challenges: make(map[string]challengeState, len(r.challenge)),
	}
	for user, records := range r.tiers {
		state.Tiers[user] = append([]tierRecord(nil), records...)
	}
	for user, devices := range r.trust {
		copied := make(map[string]time.Time, len(devices))
		for device, expiry := range devices {
			copied[device] = expiry
		}
		state.Trust[user] = copied
	}
	for tierID, ch := range r.challenge {
		state.Challenges[tierID] = *ch
	}
	r.mu.Unlock()

	sum, err := state.checksum()
	if err != nil {
		return nil, err
	}
	state.Checksum = sum
	return json.Marshal(state)
}

// LoadSnapshot replaces the whole state with a snapshot, all or nothing.
func (r *LoginRiskTier) LoadSnapshot(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	sum, err := state.checksum()
	if err != nil || subtle.ConstantTimeCompare([]byte(sum), []byte(state.Checksum)) != 1 {
		return ErrBadSnapshot
	}
	if state.Seq < 0 {
		return ErrBadSnapshot
	}

	tiers := make(map[string][]tierRecord, len(state.Tiers))
	tierByID := make(map[string]string)
	var maxSeq int64
	for user, records := range state.Tiers {
		for _, record := range records {
			t := record.Tier
			if t.ID == "" || !knownTier(t.Level) {
				return ErrBadSnapshot
			}
			if _, duplicate := tierByID[t.ID]; duplicate {
				return ErrBadSnapshot
			}
			if t.UserID == "" {
				t.UserID = user
				record.Tier = t
			}
			if record.Tier.UserID != user {
				return ErrBadSnapshot
			}
			if t.Seq <= 0 || t.Seq > state.Seq {
				return ErrBadSnapshot
			}
			if t.Seq > maxSeq {
				maxSeq = t.Seq
			}
			tierByID[t.ID] = user
			tiers[user] = append(tiers[user], record)
		}
	}
	if maxSeq > state.Seq {
		return ErrBadSnapshot
	}

	trust := make(map[string]map[string]time.Time, len(state.Trust))
	for user, devices := range state.Trust {
		copied := make(map[string]time.Time, len(devices))
		for device, expiry := range devices {
			copied[device] = expiry
		}
		trust[user] = copied
	}

	challenges := make(map[string]*challengeState, len(state.Challenges))
	for tierID, ch := range state.Challenges {
		if _, ok := tierByID[tierID]; !ok {
			continue
		}
		copied := ch
		challenges[tierID] = &copied
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Enforce the configured hard caps on restored state as well.
	r.trimLoadedLocked(tiers, tierByID, trust, challenges)
	r.seq = state.Seq
	r.tiers = tiers
	r.tierByID = tierByID
	r.trust = trust
	r.challenge = challenges
	return nil
}

func tierForScore(score int) string {
	switch {
	case score <= 0:
		return TierLow
	case score == 1:
		return TierMedium
	case score == 2:
		return TierHigh
	default:
		return TierSevere
	}
}

func knownTier(level string) bool {
	switch level {
	case TierLow, TierMedium, TierHigh, TierSevere:
		return true
	default:
		return false
	}
}

func challengeKind(level string) string {
	switch level {
	case TierMedium:
		return ChallengeSMS
	case TierHigh:
		return ChallengeTOTP
	case TierSevere:
		return ChallengeBlock
	default:
		return ChallengeNone
	}
}

func formatSeq(seq int64) string {
	const digits = "0123456789"
	if seq == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for seq > 0 {
		i--
		buf[i] = digits[seq%10]
		seq /= 10
	}
	return string(buf[i:])
}

func eventFingerprint(e LoginEvent, exempted bool) string {
	payload, _ := json.Marshal(struct {
		Event    LoginEvent `json:"event"`
		Exempted bool       `json:"exempted"`
	}{Event: e, Exempted: exempted})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func challengeCode(tierID, userID string) string {
	sum := sha256.Sum256([]byte("login-risk-tier/v1|" + tierID + "|" + userID))
	return hex.EncodeToString(sum[:6])
}

func (s snapshotState) checksum() (string, error) {
	s.Checksum = ""
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (r *LoginRiskTier) findByFingerprintLocked(userID, fingerprint string) (Tier, bool) {
	records := r.tiers[userID]
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Fingerprint == fingerprint {
			return records[i].Tier, true
		}
	}
	return Tier{}, false
}

func (r *LoginRiskTier) appendTierLocked(record tierRecord) {
	cap := r.limits.MaxTiersPerUser
	if cap <= 0 {
		return
	}
	userID := record.Tier.UserID
	records := append(r.tiers[userID], record)
	if len(records) > cap {
		drop := len(records) - cap
		for _, old := range records[:drop] {
			delete(r.tierByID, old.Tier.ID)
			delete(r.challenge, old.Tier.ID)
		}
		records = records[drop:]
	}
	r.tiers[userID] = records
	r.tierByID[record.Tier.ID] = userID
}

func (r *LoginRiskTier) tierLocked(tierID string) (Tier, bool) {
	userID, ok := r.tierByID[tierID]
	if !ok {
		return Tier{}, false
	}
	for _, record := range r.tiers[userID] {
		if record.Tier.ID == tierID {
			return record.Tier, true
		}
	}
	return Tier{}, false
}

func (r *LoginRiskTier) challengeLocked(tier Tier) Challenge {
	if existing := r.challenge[tier.ID]; existing != nil {
		return Challenge{
			TierID:   existing.TierID,
			UserID:   existing.UserID,
			Kind:     existing.Kind,
			Code:     existing.Code,
			Attempts: existing.Attempts,
		}
	}
	state := &challengeState{
		TierID: tier.ID,
		UserID: tier.UserID,
		Kind:   challengeKind(tier.Level),
		Code:   challengeCode(tier.ID, tier.UserID),
	}
	r.challenge[tier.ID] = state
	return Challenge{
		TierID: state.TierID,
		UserID: state.UserID,
		Kind:   state.Kind,
		Code:   state.Code,
	}
}

func (r *LoginRiskTier) trustedLocked(userID, deviceID string) bool {
	devices := r.trust[userID]
	if devices == nil {
		return false
	}
	now := r.now()
	r.purgeExpiredLocked(devices, now)
	expiry, ok := devices[deviceID]
	if !ok {
		return false
	}
	return now.Before(expiry)
}

func (r *LoginRiskTier) purgeExpiredLocked(devices map[string]time.Time, now time.Time) {
	for device, expiry := range devices {
		if !now.Before(expiry) {
			delete(devices, device)
		}
	}
}

func (r *LoginRiskTier) trimLoadedLocked(
	tiers map[string][]tierRecord,
	tierByID map[string]string,
	trust map[string]map[string]time.Time,
	challenges map[string]*challengeState,
) {
	tierCap := r.limits.MaxTiersPerUser
	for user, records := range tiers {
		if tierCap <= 0 || len(records) > tierCap {
			drop := len(records)
			if tierCap > 0 {
				drop -= tierCap
			}
			for _, old := range records[:drop] {
				delete(tierByID, old.Tier.ID)
				delete(challenges, old.Tier.ID)
			}
			records = records[drop:]
		}
		if len(records) == 0 {
			delete(tiers, user)
			continue
		}
		tiers[user] = records
	}

	deviceCap := r.limits.MaxDevicesPerUser
	now := r.now()
	for user, devices := range trust {
		for device, expiry := range devices {
			if !now.Before(expiry) {
				delete(devices, device)
			}
		}
		if deviceCap <= 0 {
			delete(trust, user)
			continue
		}
		for device := range devices {
			if len(devices) <= deviceCap {
				break
			}
			delete(devices, device)
		}
		if len(devices) == 0 {
			delete(trust, user)
		}
	}
}
