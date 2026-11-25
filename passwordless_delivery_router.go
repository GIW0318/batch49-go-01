// Package task routes passwordless login credentials across the channels a
// user can actually receive, keeping track of channel health, suppression
// windows and per-user send budgets.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Delivery channels supported by the router.
const (
	ChannelEmailLink = "email_link"
	ChannelEmailCode = "email_code"
	ChannelSmsCode   = "sms_code"
)

// Delivery outcomes reported by a channel adapter.
const (
	OutcomeDelivered = "delivered"
	OutcomeSoftFail  = "soft_fail"
	OutcomeHardFail  = "hard_fail"
	OutcomeBounce    = "bounce"
)

// Plan lifecycle states.
const (
	PlanPending   = "pending"
	PlanDelivered = "delivered"
	PlanExhausted = "exhausted"
)

// Health scoring anchors.
const (
	DefaultHealthScore   = 70
	HealthDeliveredDelta = 10
	HealthSoftFailDelta  = -5
	HealthHardFailDelta  = -25
)

// DefaultSuppression is how long a bounced or hard failed channel is skipped.
const DefaultSuppression = 30 * time.Minute

// Default configuration used when a caller leaves a bound at its zero value.
const (
	DefaultMaxAttemptsPerPlan = 3
	DefaultMaxSendsPerWindow  = 10
	DefaultMaxPlansPerUser    = 100
	DefaultRetryBackoff       = time.Minute
	MaxRetryBackoff           = 30 * time.Minute
	snapshotVersion           = 1
)

// Errors returned by the router.
var (
	ErrBlankUser      = errors.New("user id is required")
	ErrBadChannel     = errors.New("unknown delivery channel")
	ErrNoChannel      = errors.New("no usable delivery channel")
	ErrUnknownPlan    = errors.New("unknown delivery plan")
	ErrBadReport      = errors.New("delivery report is malformed")
	ErrAttemptsSpent  = errors.New("plan attempt budget is spent")
	ErrRateLimited    = errors.New("user send rate limit reached")
	ErrAlreadyFinal   = errors.New("plan is already final")
	ErrBadSuppression = errors.New("suppression deadline must be in the future")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
	ErrPlanLimit      = errors.New("too many plans for user")
	ErrChannelLimit   = errors.New("channel table limit reached")
)

// RouterConfig bounds one router instance.
type RouterConfig struct {
	MaxAttemptsPerPlan int
	MaxSendsPerWindow  int
	Window             time.Duration
	MaxChannels        int

	// MaxPlansPerUser bounds retained plan records for one user. Zero gets a
	// safe default from NewPasswordlessDeliveryRouter.
	MaxPlansPerUser int
}

// DeliveryRequest carries the caller preference for a single login attempt.
type DeliveryRequest struct {
	PreferOrder []string
	Now         time.Time
}

// Plan is one delivery plan for one user request.
type Plan struct {
	ID          string
	UserID      string
	Channel     string
	DeliveryID  string
	Attempt     int
	Status      string
	CreatedAt   time.Time
	NextRetryAt time.Time
}

// DeliveryReport is the adapter feedback for one plan.
type DeliveryReport struct {
	PlanID  string
	Channel string
	Outcome string
	Reason  string
	At      time.Time
}

// ChannelHealth is the rolling health of one delivery channel.
type ChannelHealth struct {
	ChannelID       string
	Score           int
	SoftFailures    int
	HardFailures    int
	SuppressedUntil time.Time
}

var defaultChannelOrder = []string{
	ChannelEmailLink,
	ChannelEmailCode,
	ChannelSmsCode,
}

var knownChannels = map[string]struct{}{
	ChannelEmailLink: {},
	ChannelEmailCode: {},
	ChannelSmsCode:   {},
}

type channelState struct {
	Score             int       `json:"score"`
	SoftFailures      int       `json:"soft_failures"`
	HardFailures      int       `json:"hard_failures"`
	SuppressedUntil   time.Time `json:"suppressed_until"`
	SuppressionReason string    `json:"suppression_reason,omitempty"`
}

type planState struct {
	Plan
	Candidates         []string  `json:"candidates"`
	Position           int       `json:"position"`
	LastOutcome        string    `json:"last_outcome,omitempty"`
	LastEventAt        time.Time `json:"last_event_at"`
	CredentialID       string    `json:"credential_id"`
	CredentialVersion  int       `json:"credential_version"`
	RevokedCredentials []string  `json:"revoked_credentials,omitempty"`
}

type routerState struct {
	Sequence    uint64                   `json:"sequence"`
	LogicalNow  time.Time                `json:"logical_now"`
	Channels    map[string]*channelState `json:"channels"`
	Preferences map[string][]string      `json:"preferences"`
	Sends       map[string][]time.Time   `json:"sends"`
	Plans       map[string]*planState    `json:"plans"`
	UserPlans   map[string][]string      `json:"user_plans"`
}

type snapshotData struct {
	Version     int                     `json:"version"`
	Sequence    uint64                  `json:"sequence"`
	LogicalNow  time.Time               `json:"logical_now"`
	Channels    map[string]channelState `json:"channels"`
	Preferences map[string][]string     `json:"preferences"`
	Sends       map[string][]time.Time  `json:"sends"`
	Plans       map[string]planSnapshot `json:"plans"`
	UserPlans   map[string][]string     `json:"user_plans"`
}

type planSnapshot struct {
	ID                 string    `json:"id"`
	UserID             string    `json:"user_id"`
	Channel            string    `json:"channel"`
	DeliveryID         string    `json:"delivery_id"`
	Attempt            int       `json:"attempt"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	NextRetryAt        time.Time `json:"next_retry_at"`
	Candidates         []string  `json:"candidates"`
	Position           int       `json:"position"`
	LastOutcome        string    `json:"last_outcome,omitempty"`
	LastEventAt        time.Time `json:"last_event_at"`
	CredentialID       string    `json:"credential_id"`
	CredentialVersion  int       `json:"credential_version"`
	RevokedCredentials []string  `json:"revoked_credentials,omitempty"`
}

// PasswordlessDeliveryRouter owns plans, channel health and send budgets.
type PasswordlessDeliveryRouter struct {
	mu    sync.Mutex
	cfg   RouterConfig
	state routerState
}

// NewPasswordlessDeliveryRouter builds a router with the given bounds.
func NewPasswordlessDeliveryRouter(cfg RouterConfig) *PasswordlessDeliveryRouter {
	if cfg.MaxAttemptsPerPlan <= 0 {
		cfg.MaxAttemptsPerPlan = DefaultMaxAttemptsPerPlan
	}
	if cfg.MaxSendsPerWindow <= 0 {
		cfg.MaxSendsPerWindow = DefaultMaxSendsPerWindow
	}
	if cfg.Window <= 0 {
		cfg.Window = time.Hour
	}
	if cfg.MaxChannels <= 0 {
		cfg.MaxChannels = len(defaultChannelOrder)
	}
	if cfg.MaxPlansPerUser <= 0 {
		cfg.MaxPlansPerUser = DefaultMaxPlansPerUser
	}

	return &PasswordlessDeliveryRouter{
		cfg:   cfg,
		state: newRouterState(),
	}
}

func newRouterState() routerState {
	return routerState{
		Channels:    make(map[string]*channelState),
		Preferences: make(map[string][]string),
		Sends:       make(map[string][]time.Time),
		Plans:       make(map[string]*planState),
		UserPlans:   make(map[string][]string),
	}
}

// Route picks a channel and creates a delivery plan for the user.
func (r *PasswordlessDeliveryRouter) Route(userID string, req DeliveryRequest) (Plan, error) {
	if strings.TrimSpace(userID) == "" {
		return Plan{}, ErrBlankUser
	}
	if err := validateChannels(req.PreferOrder); err != nil {
		return Plan{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := req.Now
	preference := req.PreferOrder
	if preference != nil {
		r.state.Preferences[userID] = append([]string(nil), preference...)
	} else {
		preference = r.state.Preferences[userID]
	}

	canonical := candidateOrder(preference)
	ranked := r.rankChannelsLocked(canonical)
	position := -1
	for i, channelID := range ranked {
		if r.channelAvailableLocked(channelID, now) {
			position = i
			break
		}
	}
	if position < 0 {
		return Plan{}, ErrNoChannel
	}

	if len(r.state.UserPlans[userID]) >= r.cfg.MaxPlansPerUser {
		return Plan{}, ErrPlanLimit
	}
	if err := r.checkRateLimitLocked(userID, now); err != nil {
		return Plan{}, err
	}

	channelID := ranked[position]
	if _, err := r.touchChannelLocked(channelID); err != nil {
		return Plan{}, err
	}

	planID := r.nextIDLocked("plan")
	deliveryID := r.nextIDLocked("delivery")
	credentialID := r.nextIDLocked("credential")
	plan := &planState{
		Plan: Plan{
			ID:         planID,
			UserID:     userID,
			Channel:    channelID,
			DeliveryID: deliveryID,
			Attempt:    1,
			Status:     PlanPending,
			CreatedAt:  now,
		},
		Candidates:        ranked,
		Position:          position,
		LastEventAt:       now,
		CredentialID:      credentialID,
		CredentialVersion: 1,
	}

	r.state.Plans[planID] = plan
	r.state.UserPlans[userID] = append(r.state.UserPlans[userID], planID)
	r.state.Sends[userID] = append(r.state.Sends[userID], now)
	r.observeTimeLocked(now)

	return plan.Plan, nil
}

// Report records the adapter outcome for a plan.
func (r *PasswordlessDeliveryRouter) Report(planID string, rep DeliveryReport) error {
	requestedID := planID
	if requestedID == "" {
		requestedID = rep.PlanID
	}
	if requestedID == "" {
		return ErrUnknownPlan
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	plan, ok := r.state.Plans[requestedID]
	if !ok {
		return ErrUnknownPlan
	}
	if planID != "" && rep.PlanID != "" && rep.PlanID != planID {
		return ErrBadReport
	}
	if !validOutcome(rep.Outcome) {
		return ErrBadReport
	}
	if rep.Channel != plan.Channel {
		return ErrBadReport
	}

	channel, err := r.touchChannelLocked(rep.Channel)
	if err != nil {
		return err
	}
	applyOutcomeHealth(channel, rep.Outcome)

	plan.LastOutcome = rep.Outcome
	plan.LastEventAt = maxTime(plan.LastEventAt, rep.At)
	r.observeTimeLocked(rep.At)

	switch rep.Outcome {
	case OutcomeDelivered:
		plan.Status = PlanDelivered
		plan.NextRetryAt = time.Time{}
	case OutcomeSoftFail:
		plan.NextRetryAt = rep.At.Add(r.backoffForAttemptLocked(plan.Attempt))
	case OutcomeHardFail, OutcomeBounce:
		reason := strings.TrimSpace(rep.Reason)
		if reason == "" {
			reason = "auto_" + rep.Outcome
		}
		channel.SuppressionReason = reason
		channel.SuppressedUntil = rep.At.Add(DefaultSuppression)
	}

	return nil
}

// Suppress keeps a channel out of the candidate list until the deadline.
func (r *PasswordlessDeliveryRouter) Suppress(channelID, reason string, until time.Time) error {
	if !isKnownChannel(channelID) {
		return ErrBadChannel
	}
	if strings.TrimSpace(reason) == "" {
		return ErrBadSuppression
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if until.IsZero() {
		return ErrBadSuppression
	}
	if !r.state.LogicalNow.IsZero() && until.Before(r.state.LogicalNow) {
		return ErrBadSuppression
	}

	if _, err := r.touchChannelLocked(channelID); err != nil {
		return err
	}
	r.state.Channels[channelID].SuppressedUntil = until
	r.state.Channels[channelID].SuppressionReason = reason
	return nil
}

// Retry advances an existing plan to its next attempt.
func (r *PasswordlessDeliveryRouter) Retry(planID string) (Plan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	plan, ok := r.state.Plans[planID]
	if !ok {
		return Plan{}, ErrUnknownPlan
	}
	if plan.Status == PlanDelivered || plan.Status == PlanExhausted {
		return Plan{}, ErrAlreadyFinal
	}
	if plan.Attempt >= r.cfg.MaxAttemptsPerPlan {
		plan.Status = PlanExhausted
		return Plan{}, ErrAttemptsSpent
	}

	now := plan.LastEventAt
	position, channelID, err := r.nextRetryChannelLocked(plan, now)
	if err != nil {
		return Plan{}, err
	}
	if err := r.checkRateLimitLocked(plan.UserID, now); err != nil {
		return Plan{}, err
	}
	if _, err := r.touchChannelLocked(channelID); err != nil {
		return Plan{}, err
	}

	r.rotateCredentialLocked(plan)
	plan.Position = position
	plan.Channel = channelID
	plan.Attempt++
	plan.Status = PlanPending
	plan.LastOutcome = ""
	plan.NextRetryAt = time.Time{}
	plan.LastEventAt = maxTime(plan.LastEventAt, now)
	r.state.Sends[plan.UserID] = append(r.state.Sends[plan.UserID], now)

	return plan.Plan, nil
}

// Health returns the rolling health of one channel.
func (r *PasswordlessDeliveryRouter) Health(channelID string) (ChannelHealth, error) {
	if !isKnownChannel(channelID) {
		return ChannelHealth{}, ErrBadChannel
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	health := ChannelHealth{
		ChannelID: channelID,
		Score:     DefaultHealthScore,
	}
	if channel, ok := r.state.Channels[channelID]; ok {
		health.Score = channel.Score
		health.SoftFailures = channel.SoftFailures
		health.HardFailures = channel.HardFailures
		health.SuppressedUntil = channel.SuppressedUntil
	}
	return health, nil
}

// Snapshot serialises the router state.
func (r *PasswordlessDeliveryRouter) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data := snapshotData{
		Version:     snapshotVersion,
		Sequence:    r.state.Sequence,
		LogicalNow:  r.state.LogicalNow,
		Channels:    make(map[string]channelState, len(r.state.Channels)),
		Preferences: clonePreferences(r.state.Preferences),
		Sends:       cloneSends(r.state.Sends),
		Plans:       make(map[string]planSnapshot, len(r.state.Plans)),
		UserPlans:   cloneUserPlans(r.state.UserPlans),
	}
	for id, channel := range r.state.Channels {
		data.Channels[id] = *channel
	}
	for id, plan := range r.state.Plans {
		data.Plans[id] = plan.toSnapshot()
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("snapshot encode: %w", err)
	}
	return payload, nil
}

// Restore replaces the router state from a snapshot.
func (r *PasswordlessDeliveryRouter) Restore(data []byte) error {
	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrBadSnapshot, err)
	}
	if err := snapshot.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(snapshot.Channels) > r.cfg.MaxChannels {
		return fmt.Errorf("%w: channel count exceeds configured cap", ErrBadSnapshot)
	}
	for userID, planIDs := range snapshot.UserPlans {
		if len(planIDs) > r.cfg.MaxPlansPerUser {
			return fmt.Errorf("%w: too many plans for user %q", ErrBadSnapshot, userID)
		}
	}

	state := newRouterState()
	state.Sequence = maxSequence(snapshot.Sequence, snapshot.Plans)
	state.LogicalNow = snapshot.LogicalNow
	for id, channel := range snapshot.Channels {
		channelCopy := channel
		state.Channels[id] = &channelCopy
	}
	state.Preferences = clonePreferences(snapshot.Preferences)
	state.Sends = cloneSends(snapshot.Sends)
	state.UserPlans = cloneUserPlans(snapshot.UserPlans)
	for id, planData := range snapshot.Plans {
		plan := planData.toPlanState()
		state.Plans[id] = plan
	}

	r.state = state
	return nil
}

func maxSequence(current uint64, plans map[string]planSnapshot) uint64 {
	max := current
	for _, plan := range plans {
		for _, id := range []string{plan.ID, plan.DeliveryID, plan.CredentialID} {
			if n := numericIDSuffix(id); n > max {
				max = n
			}
		}
	}
	return max
}

func numericIDSuffix(id string) uint64 {
	index := strings.LastIndex(id, "-")
	if index < 0 || index == len(id)-1 {
		return 0
	}
	var value uint64
	for _, r := range id[index+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		value = value*10 + uint64(r-'0')
	}
	return value
}

// Lookup returns a plan by identifier.
func (r *PasswordlessDeliveryRouter) Lookup(planID string) (Plan, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	plan, ok := r.state.Plans[planID]
	if !ok {
		return Plan{}, false
	}
	return plan.Plan, true
}

// Preference returns the remembered channel order for a user.
func (r *PasswordlessDeliveryRouter) Preference(userID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	preference, ok := r.state.Preferences[userID]
	if !ok {
		return nil
	}
	out := make([]string, len(preference))
	copy(out, preference)
	return out
}

// Usage returns how many sends the user consumed inside the window.
func (r *PasswordlessDeliveryRouter) Usage(userID string, at time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.observeTimeLocked(at)
	return len(r.recentSendsLocked(userID, at))
}

// ClearSuppression removes a suppression deadline from a channel.
func (r *PasswordlessDeliveryRouter) ClearSuppression(channelID string) error {
	if !isKnownChannel(channelID) {
		return ErrBadChannel
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if channel, ok := r.state.Channels[channelID]; ok {
		channel.SuppressedUntil = time.Time{}
		channel.SuppressionReason = ""
	}
	return nil
}

func validateChannels(channels []string) error {
	for _, channelID := range channels {
		if !isKnownChannel(channelID) {
			return ErrBadChannel
		}
	}
	return nil
}

func isKnownChannel(channelID string) bool {
	_, ok := knownChannels[channelID]
	return ok
}

func candidateOrder(preference []string) []string {
	order := make([]string, 0, len(preference)+len(defaultChannelOrder))
	seen := make(map[string]struct{}, len(preference)+len(defaultChannelOrder))
	for _, channelID := range preference {
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		order = append(order, channelID)
	}
	for _, channelID := range defaultChannelOrder {
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		order = append(order, channelID)
	}
	return order
}

func (r *PasswordlessDeliveryRouter) rankChannelsLocked(order []string) []string {
	ranked := append([]string(nil), order...)
	canonicalPosition := make(map[string]int, len(order))
	for i, channelID := range order {
		canonicalPosition[channelID] = i
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		leftScore := r.healthScoreLocked(ranked[i])
		rightScore := r.healthScoreLocked(ranked[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return canonicalPosition[ranked[i]] < canonicalPosition[ranked[j]]
	})
	return ranked
}

func (r *PasswordlessDeliveryRouter) healthScoreLocked(channelID string) int {
	if channel, ok := r.state.Channels[channelID]; ok {
		return channel.Score
	}
	return DefaultHealthScore
}

func (r *PasswordlessDeliveryRouter) channelAvailableLocked(channelID string, now time.Time) bool {
	channel, ok := r.state.Channels[channelID]
	if !ok {
		return true
	}
	return !now.Before(channel.SuppressedUntil)
}

func (r *PasswordlessDeliveryRouter) touchChannelLocked(channelID string) (*channelState, error) {
	if channel, ok := r.state.Channels[channelID]; ok {
		return channel, nil
	}
	if len(r.state.Channels) >= r.cfg.MaxChannels {
		return nil, ErrChannelLimit
	}
	channel := &channelState{Score: DefaultHealthScore}
	r.state.Channels[channelID] = channel
	return channel, nil
}

func applyOutcomeHealth(channel *channelState, outcome string) {
	switch outcome {
	case OutcomeDelivered:
		channel.Score += HealthDeliveredDelta
	case OutcomeSoftFail:
		channel.Score += HealthSoftFailDelta
		channel.SoftFailures++
	case OutcomeHardFail, OutcomeBounce:
		channel.Score += HealthHardFailDelta
		channel.HardFailures++
	}
	channel.Score = clampHealth(channel.Score)
}

func clampHealth(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func validOutcome(outcome string) bool {
	switch outcome {
	case OutcomeDelivered, OutcomeSoftFail, OutcomeHardFail, OutcomeBounce:
		return true
	default:
		return false
	}
}

func (r *PasswordlessDeliveryRouter) nextIDLocked(prefix string) string {
	r.state.Sequence++
	return fmt.Sprintf("%s-%d", prefix, r.state.Sequence)
}

func (r *PasswordlessDeliveryRouter) observeTimeLocked(now time.Time) {
	if now.After(r.state.LogicalNow) {
		r.state.LogicalNow = now
	}
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func (r *PasswordlessDeliveryRouter) backoffForAttemptLocked(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := DefaultRetryBackoff
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= MaxRetryBackoff {
			return MaxRetryBackoff
		}
	}
	if backoff > MaxRetryBackoff {
		return MaxRetryBackoff
	}
	return backoff
}

func (r *PasswordlessDeliveryRouter) nextRetryChannelLocked(plan *planState, now time.Time) (int, string, error) {
	if plan.Position < 0 || plan.Position >= len(plan.Candidates) {
		return -1, "", ErrNoChannel
	}
	current := plan.Candidates[plan.Position]
	forceDowngrade := plan.LastOutcome == OutcomeHardFail || plan.LastOutcome == OutcomeBounce
	if !forceDowngrade && r.channelAvailableLocked(current, now) {
		return plan.Position, current, nil
	}

	for position := plan.Position + 1; position < len(plan.Candidates); position++ {
		channelID := plan.Candidates[position]
		if r.channelAvailableLocked(channelID, now) {
			return position, channelID, nil
		}
	}
	return -1, "", ErrNoChannel
}

func (r *PasswordlessDeliveryRouter) checkRateLimitLocked(userID string, at time.Time) error {
	recent := r.recentSendsLocked(userID, at)
	if len(recent) >= r.cfg.MaxSendsPerWindow {
		return ErrRateLimited
	}
	return nil
}

func (r *PasswordlessDeliveryRouter) recentSendsLocked(userID string, at time.Time) []time.Time {
	sends := r.state.Sends[userID]
	if len(sends) == 0 {
		return nil
	}
	cutoff := at.Add(-r.cfg.Window)
	recent := make([]time.Time, 0, len(sends))
	for _, sentAt := range sends {
		if sentAt.After(cutoff) && !sentAt.After(at) {
			recent = append(recent, sentAt)
		}
	}
	return recent
}

func (r *PasswordlessDeliveryRouter) rotateCredentialLocked(plan *planState) {
	if plan.CredentialID != "" {
		plan.RevokedCredentials = append(plan.RevokedCredentials, plan.CredentialID)
	}
	plan.CredentialID = r.nextIDLocked("credential")
	plan.CredentialVersion++
}

func clonePreferences(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	out := make(map[string][]string, len(input))
	for userID, preference := range input {
		out[userID] = append([]string(nil), preference...)
	}
	return out
}

func cloneSends(input map[string][]time.Time) map[string][]time.Time {
	if input == nil {
		return nil
	}
	out := make(map[string][]time.Time, len(input))
	for userID, sends := range input {
		out[userID] = append([]time.Time(nil), sends...)
	}
	return out
}

func cloneUserPlans(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	out := make(map[string][]string, len(input))
	for userID, planIDs := range input {
		out[userID] = append([]string(nil), planIDs...)
	}
	return out
}

func (p planState) toSnapshot() planSnapshot {
	return planSnapshot{
		ID:                 p.ID,
		UserID:             p.UserID,
		Channel:            p.Channel,
		DeliveryID:         p.DeliveryID,
		Attempt:            p.Attempt,
		Status:             p.Status,
		CreatedAt:          p.CreatedAt,
		NextRetryAt:        p.NextRetryAt,
		Candidates:         append([]string(nil), p.Candidates...),
		Position:           p.Position,
		LastOutcome:        p.LastOutcome,
		LastEventAt:        p.LastEventAt,
		CredentialID:       p.CredentialID,
		CredentialVersion:  p.CredentialVersion,
		RevokedCredentials: append([]string(nil), p.RevokedCredentials...),
	}
}

func (p planSnapshot) toPlanState() *planState {
	return &planState{
		Plan: Plan{
			ID:          p.ID,
			UserID:      p.UserID,
			Channel:     p.Channel,
			DeliveryID:  p.DeliveryID,
			Attempt:     p.Attempt,
			Status:      p.Status,
			CreatedAt:   p.CreatedAt,
			NextRetryAt: p.NextRetryAt,
		},
		Candidates:         append([]string(nil), p.Candidates...),
		Position:           p.Position,
		LastOutcome:        p.LastOutcome,
		LastEventAt:        p.LastEventAt,
		CredentialID:       p.CredentialID,
		CredentialVersion:  p.CredentialVersion,
		RevokedCredentials: append([]string(nil), p.RevokedCredentials...),
	}
}

func (s snapshotData) validate() error {
	if s.Version != snapshotVersion {
		return errors.New("unsupported snapshot version")
	}
	for channelID, channel := range s.Channels {
		if !isKnownChannel(channelID) {
			return fmt.Errorf("unknown channel %q", channelID)
		}
		if channel.Score < 0 || channel.Score > 100 {
			return fmt.Errorf("channel %q score out of range", channelID)
		}
		if channel.SoftFailures < 0 || channel.HardFailures < 0 {
			return fmt.Errorf("channel %q has negative failure counter", channelID)
		}
	}
	for userID, preference := range s.Preferences {
		if strings.TrimSpace(userID) == "" {
			return errors.New("blank preference user")
		}
		if err := validateChannels(preference); err != nil {
			return err
		}
	}
	for planID, plan := range s.Plans {
		if planID == "" || plan.ID != planID {
			return errors.New("plan id mismatch")
		}
		if strings.TrimSpace(plan.UserID) == "" {
			return fmt.Errorf("plan %q has blank user", planID)
		}
		if !isKnownChannel(plan.Channel) {
			return fmt.Errorf("plan %q has bad channel", planID)
		}
		if plan.DeliveryID == "" || plan.Attempt < 1 {
			return fmt.Errorf("plan %q is missing delivery state", planID)
		}
		switch plan.Status {
		case PlanPending, PlanDelivered, PlanExhausted:
		default:
			return fmt.Errorf("plan %q has bad status", planID)
		}
		if len(plan.Candidates) == 0 || plan.Position < 0 || plan.Position >= len(plan.Candidates) {
			return fmt.Errorf("plan %q has bad candidate position", planID)
		}
		if plan.Candidates[plan.Position] != plan.Channel {
			return fmt.Errorf("plan %q channel position mismatch", planID)
		}
		for _, channelID := range plan.Candidates {
			if !isKnownChannel(channelID) {
				return fmt.Errorf("plan %q has unknown candidate", planID)
			}
		}
	}
	for userID, planIDs := range s.UserPlans {
		if strings.TrimSpace(userID) == "" {
			return errors.New("blank user plan index")
		}
		seen := make(map[string]struct{}, len(planIDs))
		for _, planID := range planIDs {
			if _, duplicate := seen[planID]; duplicate {
				return fmt.Errorf("user %q indexes plan %q more than once", userID, planID)
			}
			seen[planID] = struct{}{}
			plan, ok := s.Plans[planID]
			if !ok {
				return fmt.Errorf("user %q indexes missing plan %q", userID, planID)
			}
			if plan.UserID != userID {
				return fmt.Errorf("plan %q user index mismatch", planID)
			}
		}
	}
	return nil
}
