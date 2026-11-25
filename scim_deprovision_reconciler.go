// Package task reconciles the identity provider roster against the accounts
// that still exist in the downstream applications and drives deprovisioning.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Downstream account states.
const (
	AccountActive   = "active"
	AccountGrace    = "grace"
	AccountPending  = "pending"
	AccountDisabled = "disabled"
)

// Action kinds planned for one reconciliation round.
const (
	ActionRevokeSessions = "revoke_sessions"
	ActionDisable        = "disable"
	ActionRestore        = "restore"
)

// Errors returned by the reconciler.
var (
	ErrBlankTenant     = errors.New("tenant id is required")
	ErrBlankApp        = errors.New("application id is required")
	ErrRunInProgress   = errors.New("a reconciliation run is already in flight for this app")
	ErrUnknownRun      = errors.New("reconciliation run not found")
	ErrUnknownAccount  = errors.New("account is not part of this run")
	ErrNotConfirmed    = errors.New("account has not been confirmed for disable")
	ErrGraceNotElapsed = errors.New("grace period has not elapsed")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Limits bounds one reconciliation round.
type Limits struct {
	MaxAccountsPerRun int
	GracePeriod       time.Duration
}

// Account is one downstream application account.
type Account struct {
	ID          string    `json:"id"`
	AppID       string    `json:"app_id"`
	UserName    string    `json:"user_name"`
	State       string    `json:"state"`
	Sessions    int       `json:"sessions"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	GraceUntil  time.Time `json:"grace_until"`
}

// Action is one planned step against one account.
type Action struct {
	AccountID string `json:"account_id"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
}

// RunReport describes one reconciliation round that was opened.
type RunReport struct {
	RunID      string `json:"run_id"`
	TenantID   string `json:"tenant_id"`
	AppID      string `json:"app_id"`
	Upstream   int    `json:"upstream"`
	Downstream int    `json:"downstream"`
	Orphans    int    `json:"orphans"`
	Cursor     int    `json:"cursor"`
	Completed  bool   `json:"completed"`
}

// Result is the per call outcome of Execute.
type Result struct {
	RunID     string `json:"run_id"`
	Revoked   int    `json:"revoked"`
	Disabled  int    `json:"disabled"`
	Restored  int    `json:"restored"`
	Failed    int    `json:"failed"`
	Cursor    int    `json:"cursor"`
	Completed bool   `json:"completed"`
}

// ScimDeprovisionReconciler owns the rosters, the accounts and the open runs.
type ScimDeprovisionReconciler struct {
	limits Limits
	now    time.Time
}

// NewScimDeprovisionReconciler builds a reconciler with the given caps.
func NewScimDeprovisionReconciler(limits Limits) *ScimDeprovisionReconciler {
	return &ScimDeprovisionReconciler{limits: limits}
}

// SetNow pins the clock used by the reconciler.
func (r *ScimDeprovisionReconciler) SetNow(now time.Time) {
	r.now = now
}

// SetUpstream replaces the identity provider roster of one tenant.
func (r *ScimDeprovisionReconciler) SetUpstream(tenantID string, userNames []string) error {
	return errNotImplemented
}

// SetDownstream replaces the account list of one application.
func (r *ScimDeprovisionReconciler) SetDownstream(tenantID, appID string, accounts []Account) error {
	return errNotImplemented
}

// Run opens one reconciliation round for one application.
func (r *ScimDeprovisionReconciler) Run(tenantID, appID string) (RunReport, error) {
	return RunReport{}, errNotImplemented
}

// Plan lists the steps one reconciliation round would take, as a dry run.
func (r *ScimDeprovisionReconciler) Plan(tenantID, appID string) ([]Action, error) {
	return nil, errNotImplemented
}

// Execute performs the confirmed steps of one round, up to the per call cap.
func (r *ScimDeprovisionReconciler) Execute(runID string) (Result, error) {
	return Result{}, errNotImplemented
}

// Orphans lists the accounts waiting for deprovisioning, ordered by account id.
func (r *ScimDeprovisionReconciler) Orphans(tenantID, appID string) ([]Account, error) {
	return nil, errNotImplemented
}

// Accounts lists every downstream account of one application, ordered by id.
func (r *ScimDeprovisionReconciler) Accounts(tenantID, appID string) ([]Account, error) {
	return nil, errNotImplemented
}

// Confirm records the second confirmation needed before an account is disabled.
func (r *ScimDeprovisionReconciler) Confirm(runID, accountID string) error {
	return errNotImplemented
}

// Ops returns the ordered operation log of one round.
func (r *ScimDeprovisionReconciler) Ops(runID string) []string {
	return nil
}

// SetDisableFault injects a downstream disable failure for one account.
func (r *ScimDeprovisionReconciler) SetDisableFault(tenantID, appID, accountID string, fail bool) error {
	return errNotImplemented
}

// Snapshot serialises the rosters, the accounts and the open runs.
func (r *ScimDeprovisionReconciler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *ScimDeprovisionReconciler) Restore(data []byte) error {
	return errNotImplemented
}
