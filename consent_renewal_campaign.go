// Package task runs a re-consent campaign after a policy version upgrade:
// it scopes an audience per tenant and version, tracks who accepted, and
// narrows what an unaccepted user may still do once the grace period is over.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Campaign statuses.
const (
	CampaignActive = "active"
	CampaignPaused = "paused"
)

// Gate verdicts reported by FeatureAllowed.
const (
	GateAllow          = "allow"
	GateFeatureBlocked = "feature_blocked"
	GateSensitiveBlock = "sensitive_blocked"
)

// Errors reported by the campaign store.
var (
	ErrBlankArgument     = errors.New("tenant, version, campaign and user are required")
	ErrBadVersion        = errors.New("source and target policy versions must differ")
	ErrDuplicateCampaign = errors.New("a live campaign already covers this tenant and target version")
	ErrCampaignNotFound  = errors.New("campaign not found")
	ErrEmptyBatch        = errors.New("enrolment batch is empty")
	ErrBatchLimit        = errors.New("enrolment batch exceeds the configured size")
	ErrAudienceLimit     = errors.New("campaign audience exceeds the configured size")
	ErrPaused            = errors.New("campaign is paused")
	ErrNotEnrolled       = errors.New("user is not enrolled in this campaign")
	ErrVersionMismatch   = errors.New("accepted version does not match the campaign target")
	ErrBadReason         = errors.New("exemption reason is required")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// Options bounds one campaign run.
type Options struct {
	GracePeriod time.Duration
	MaxAudience int
	MaxBatch    int
}

// Campaign is one re-consent run for a tenant moving from one policy version to
// the next.
type Campaign struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	Status      string    `json:"status"`
	GraceUntil  time.Time `json:"grace_until"`
	Enrolled    int       `json:"enrolled"`
	Accepted    int       `json:"accepted"`
	Exempted    int       `json:"exempted"`
	Seq         int64     `json:"seq"`
}

// Receipt is the proof that one user accepted one version.
type Receipt struct {
	CampaignID string    `json:"campaign_id"`
	UserID     string    `json:"user_id"`
	Version    string    `json:"version"`
	AcceptedAt time.Time `json:"accepted_at"`
	Seq        int64     `json:"seq"`
}

// Stats is the progress snapshot of one campaign. CompletionBPS is the
// completion rate in basis points.
type Stats struct {
	Enrolled      int `json:"enrolled"`
	Accepted      int `json:"accepted"`
	Exempted      int `json:"exempted"`
	CompletionBPS int `json:"completion_bps"`
}

// ConsentRenewalCampaign owns the campaigns, the audience lists and the receipts.
type ConsentRenewalCampaign struct {
	opts Options
}

// NewConsentRenewalCampaign builds a store with the given options.
func NewConsentRenewalCampaign(opts Options) *ConsentRenewalCampaign {
	return &ConsentRenewalCampaign{opts: opts}
}

// Plan opens a campaign for one tenant and one target version.
func (c *ConsentRenewalCampaign) Plan(tenantID, fromVersion, toVersion string) (Campaign, error) {
	return Campaign{}, errNotImplemented
}

// PlanAt opens a campaign with a caller supplied issue time.
func (c *ConsentRenewalCampaign) PlanAt(tenantID, fromVersion, toVersion string, now time.Time) (Campaign, error) {
	return Campaign{}, errNotImplemented
}

// Lookup reads one campaign.
func (c *ConsentRenewalCampaign) Lookup(campaignID string) (Campaign, bool) {
	return Campaign{}, false
}

// Enroll adds users to the campaign audience and reports how many were new.
func (c *ConsentRenewalCampaign) Enroll(campaignID string, userIDs []string) (int, error) {
	return 0, errNotImplemented
}

// Accept records one user consent for one policy version.
func (c *ConsentRenewalCampaign) Accept(campaignID, userID, version string) (Receipt, error) {
	return Receipt{}, errNotImplemented
}

// AcceptAt records one user consent at a caller supplied instant.
func (c *ConsentRenewalCampaign) AcceptAt(campaignID, userID, version string, now time.Time) (Receipt, error) {
	return Receipt{}, errNotImplemented
}

// Exempt takes one enrolled user out of the campaign with a reason.
func (c *ConsentRenewalCampaign) Exempt(campaignID, userID, reason string) error {
	return errNotImplemented
}

// Progress reports the campaign counters.
func (c *ConsentRenewalCampaign) Progress(campaignID string) (Stats, error) {
	return Stats{}, errNotImplemented
}

// Pause stops enrolment and acceptance for a campaign.
func (c *ConsentRenewalCampaign) Pause(campaignID string) error {
	return errNotImplemented
}

// Resume reopens a paused campaign.
func (c *ConsentRenewalCampaign) Resume(campaignID string) error {
	return errNotImplemented
}

// FeatureAllowed answers whether one user may use one feature right now, and
// which gate produced the answer.
func (c *ConsentRenewalCampaign) FeatureAllowed(campaignID, userID, feature string, now time.Time) (bool, string) {
	return false, ""
}

// Snapshot serialises campaigns, audiences, receipts and the sequence.
func (c *ConsentRenewalCampaign) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole store with a snapshot, all or nothing.
func (c *ConsentRenewalCampaign) Restore(data []byte) error {
	return errNotImplemented
}
