// Package task schedules the passkey re-registration campaign that follows a
// tenant login domain move, keeping the old and the new relying party id alive
// side by side until the overlap window closes.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Campaign lifecycle states.
const (
	CampaignPlanned    = "planned"
	CampaignActive     = "active"
	CampaignRolledBack = "rolled_back"
	CampaignClosed     = "closed"
)

// Per user migration states.
const (
	ProgressPending     = "pending"
	ProgressPartial     = "partial"
	ProgressMigrated    = "migrated"
	ProgressNeedsEnroll = "needs_reenroll"
)

// Reason codes carried by a login verdict.
const (
	ReasonRPIDMatch      = "reason_rp_id_match"
	ReasonRPIDMismatch   = "reason_rp_id_mismatch"
	ReasonCampaignClosed = "reason_campaign_closed"
	ReasonUserPending    = "reason_user_pending"
)

// Login paths reported by LoginPolicy.
const (
	PathOldRP   = "old_rp"
	PathNewRP   = "new_rp"
	PathRecover = "recovery"
)

// Errors returned by the campaign manager.
var (
	ErrBlankTenant         = errors.New("tenant id is required")
	ErrDomainUnchanged     = errors.New("from domain and to domain must differ")
	ErrCampaignNotFound    = errors.New("campaign not found")
	ErrCampaignNotActive   = errors.New("campaign is not active")
	ErrActiveCampaignLimit = errors.New("active campaign limit reached for tenant")
	ErrCredentialLimit     = errors.New("credential limit reached for user")
	ErrLegacyLimit         = errors.New("legacy credential limit reached for user")
	ErrDuplicateCredential = errors.New("credential already recorded")
	ErrCredentialDomain    = errors.New("credential rp id does not belong to the expected domain")
	ErrDeadlineInvalid     = errors.New("deadline is before campaign start")
	ErrDeadlineTooLong     = errors.New("deadline exceeds the allowed overlap window")
	ErrBlankUser           = errors.New("user id is required")
	ErrBadSnapshot         = errors.New("snapshot payload is invalid")
)

// Limits bounds the campaign tables.
type Limits struct {
	MaxActiveCampaignsPerTenant int
	MaxCredentialsPerUser       int
	MaxOverlapHours             int
}

// Credential is one passkey registration bound to a relying party id.
type Credential struct {
	ID           string    `json:"id"`
	RPID         string    `json:"rp_id"`
	PublicKey    string    `json:"public_key"`
	SignCount    uint32    `json:"sign_count"`
	RegisteredAt time.Time `json:"registered_at"`
}

// Campaign is one tenant wide move from a source domain to a target domain.
type Campaign struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	FromDomain string    `json:"from_domain"`
	ToDomain   string    `json:"to_domain"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	Deadline   time.Time `json:"deadline"`
	Users      int       `json:"users"`
}

// UserProgress records what one user has migrated so far.
type UserProgress struct {
	CampaignID string       `json:"campaign_id"`
	UserID     string       `json:"user_id"`
	State      string       `json:"state"`
	Migrated   []Credential `json:"migrated"`
	Legacy     []Credential `json:"legacy"`
	Remaining  int          `json:"remaining"`
	Deadline   time.Time    `json:"deadline"`
	Seq        int64        `json:"seq"`
}

// LoginVerdict answers whether one login attempt is accepted during the move.
type LoginVerdict struct {
	Allowed bool   `json:"allowed"`
	Path    string `json:"path"`
	Reason  string `json:"reason"`
}

// RpIdMigrationCampaign owns the campaigns, the per user progress and the clock.
type RpIdMigrationCampaign struct {
	limits Limits
	now    time.Time
}

// NewRpIdMigrationCampaign builds a campaign manager with the given caps.
func NewRpIdMigrationCampaign(limits Limits) *RpIdMigrationCampaign {
	return &RpIdMigrationCampaign{limits: limits}
}

// SetNow pins the clock used by the manager.
func (c *RpIdMigrationCampaign) SetNow(now time.Time) {
	c.now = now
}

// Plan registers a new campaign for a tenant moving between two domains.
func (c *RpIdMigrationCampaign) Plan(tenantID, fromDomain, toDomain string) (Campaign, error) {
	return Campaign{}, errNotImplemented
}

// Start flips a planned campaign into the active state.
func (c *RpIdMigrationCampaign) Start(campaignID string) error {
	return errNotImplemented
}

// ImportLegacy seeds the old domain credentials still held by one user.
func (c *RpIdMigrationCampaign) ImportLegacy(campaignID, userID string, creds []Credential) error {
	return errNotImplemented
}

// Record stores one credential registered under the new relying party id.
func (c *RpIdMigrationCampaign) Record(campaignID, userID string, cred Credential) (UserProgress, error) {
	return UserProgress{}, errNotImplemented
}

// Status reads the migration progress of one user.
func (c *RpIdMigrationCampaign) Status(campaignID, userID string) (UserProgress, error) {
	return UserProgress{}, errNotImplemented
}

// Progress lists every user tracked by one campaign, ordered by user id.
func (c *RpIdMigrationCampaign) Progress(campaignID string) []UserProgress {
	return nil
}

// SetDeadline sets the shutdown time of the overlap window.
func (c *RpIdMigrationCampaign) SetDeadline(campaignID string, at time.Time) error {
	return errNotImplemented
}

// LoginPolicy tells whether a login on domain with the given rp id is accepted.
func (c *RpIdMigrationCampaign) LoginPolicy(campaignID, userID, domain, rpID string) (LoginVerdict, error) {
	return LoginVerdict{}, errNotImplemented
}

// Rollback aborts a running campaign and keeps the users for re-registration.
func (c *RpIdMigrationCampaign) Rollback(campaignID string) error {
	return errNotImplemented
}

// Sweep closes the active campaigns whose deadline has passed.
func (c *RpIdMigrationCampaign) Sweep(now time.Time) (int, error) {
	return 0, errNotImplemented
}

// Campaign reads one campaign record.
func (c *RpIdMigrationCampaign) Campaign(campaignID string) (Campaign, bool) {
	return Campaign{}, false
}

// Snapshot serialises the campaigns, the per user progress and the deadlines.
func (c *RpIdMigrationCampaign) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *RpIdMigrationCampaign) Restore(data []byte) error {
	return errNotImplemented
}
