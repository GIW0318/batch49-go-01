// Package task routes passwordless login credentials across the channels a
// user can actually receive, keeping track of channel health, suppression
// windows and per-user send budgets.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that is not built yet.
var errNotImplemented = errors.New("not implemented")

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
)

// RouterConfig bounds one router instance.
type RouterConfig struct {
	MaxAttemptsPerPlan int
	MaxSendsPerWindow  int
	Window             time.Duration
	MaxChannels        int
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

// PasswordlessDeliveryRouter owns plans, channel health and send budgets.
type PasswordlessDeliveryRouter struct {
	cfg RouterConfig
}

// NewPasswordlessDeliveryRouter builds a router with the given bounds.
func NewPasswordlessDeliveryRouter(cfg RouterConfig) *PasswordlessDeliveryRouter {
	return &PasswordlessDeliveryRouter{cfg: cfg}
}

// Route picks a channel and creates a delivery plan for the user.
func (r *PasswordlessDeliveryRouter) Route(userID string, req DeliveryRequest) (Plan, error) {
	return Plan{}, errNotImplemented
}

// Report records the adapter outcome for a plan.
func (r *PasswordlessDeliveryRouter) Report(planID string, rep DeliveryReport) error {
	return errNotImplemented
}

// Suppress keeps a channel out of the candidate list until the deadline.
func (r *PasswordlessDeliveryRouter) Suppress(channelID, reason string, until time.Time) error {
	return errNotImplemented
}

// Retry advances an existing plan to its next attempt.
func (r *PasswordlessDeliveryRouter) Retry(planID string) (Plan, error) {
	return Plan{}, errNotImplemented
}

// Health returns the rolling health of one channel.
func (r *PasswordlessDeliveryRouter) Health(channelID string) (ChannelHealth, error) {
	return ChannelHealth{}, errNotImplemented
}

// Snapshot serialises the router state.
func (r *PasswordlessDeliveryRouter) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the router state from a snapshot.
func (r *PasswordlessDeliveryRouter) Restore(data []byte) error {
	return errNotImplemented
}

// Lookup returns a plan by identifier.
func (r *PasswordlessDeliveryRouter) Lookup(planID string) (Plan, bool) {
	return Plan{}, false
}

// Preference returns the remembered channel order for a user.
func (r *PasswordlessDeliveryRouter) Preference(userID string) []string {
	return nil
}

// Usage returns how many sends the user consumed inside the window.
func (r *PasswordlessDeliveryRouter) Usage(userID string, at time.Time) int {
	return 0
}

// ClearSuppression removes a suppression deadline from a channel.
func (r *PasswordlessDeliveryRouter) ClearSuppression(channelID string) error {
	return errNotImplemented
}
