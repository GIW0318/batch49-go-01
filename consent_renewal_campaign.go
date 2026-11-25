// Package task runs a re-consent campaign after a policy version upgrade:
// it scopes an audience per tenant and version, tracks who accepted, and
// narrows what an unaccepted user may still do once the grace period is over.
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

// feature classes understood by FeatureAllowed.
const (
	featureClassLogin     = "login"
	featureClassTerms     = "terms"
	featureClassSensitive = "sensitive"
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

// campaign is the mutable, lock-protected state of one run.
type campaign struct {
	info     Campaign
	users    map[string]struct{}
	receipts map[string]Receipt
	reasons  map[string]string
}

// ConsentRenewalCampaign owns the campaigns, the audience lists and the receipts.
type ConsentRenewalCampaign struct {
	mu    sync.Mutex
	opts  Options
	seq   int64
	camps map[string]*campaign
	// tenants maps tenant ID -> target version -> live campaign ID.
	tenants map[string]map[string]string
}

// NewConsentRenewalCampaign builds a store with the given options.
func NewConsentRenewalCampaign(opts Options) *ConsentRenewalCampaign {
	return &ConsentRenewalCampaign{
		opts:    opts,
		camps:   make(map[string]*campaign),
		tenants: make(map[string]map[string]string),
	}
}

func crIsBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func (c *ConsentRenewalCampaign) nextSeqLocked() int64 {
	c.seq++
	return c.seq
}

func (c *ConsentRenewalCampaign) campaignLocked(campaignID string) (*campaign, error) {
	run, ok := c.camps[campaignID]
	if !ok {
		return nil, ErrCampaignNotFound
	}
	return run, nil
}

// infoLocked returns a value copy of the campaign with counters derived from
// the authoritative maps so the numbers can never drift.
func (c *ConsentRenewalCampaign) infoLocked(run *campaign) Campaign {
	info := run.info
	info.Enrolled = len(run.users)
	info.Accepted = len(run.receipts)
	info.Exempted = len(run.reasons)
	return info
}

// Plan opens a campaign for one tenant and one target version.
func (c *ConsentRenewalCampaign) Plan(tenantID, fromVersion, toVersion string) (Campaign, error) {
	return c.PlanAt(tenantID, fromVersion, toVersion, time.Now())
}

// PlanAt opens a campaign with a caller supplied issue time.
func (c *ConsentRenewalCampaign) PlanAt(tenantID, fromVersion, toVersion string, now time.Time) (Campaign, error) {
	if crIsBlank(tenantID) || crIsBlank(fromVersion) || crIsBlank(toVersion) {
		return Campaign{}, ErrBlankArgument
	}
	if fromVersion == toVersion {
		return Campaign{}, ErrBadVersion
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if targets, ok := c.tenants[tenantID]; ok {
		if _, duplicate := targets[toVersion]; duplicate {
			return Campaign{}, ErrDuplicateCampaign
		}
	}

	seq := c.nextSeqLocked()
	id := fmt.Sprintf("campaign-%d", seq)
	info := Campaign{
		ID:          id,
		TenantID:    tenantID,
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		Status:      CampaignActive,
		GraceUntil:  now.Add(c.opts.GracePeriod),
		Seq:         seq,
	}

	c.camps[id] = &campaign{
		info:     info,
		users:    make(map[string]struct{}),
		receipts: make(map[string]Receipt),
		reasons:  make(map[string]string),
	}
	if c.tenants[tenantID] == nil {
		c.tenants[tenantID] = make(map[string]string)
	}
	c.tenants[tenantID][toVersion] = id

	return info, nil
}

// Lookup reads one campaign.
func (c *ConsentRenewalCampaign) Lookup(campaignID string) (Campaign, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	run, ok := c.camps[campaignID]
	if !ok {
		return Campaign{}, false
	}
	return c.infoLocked(run), true
}

// Enroll adds users to the campaign audience and reports how many were new.
func (c *ConsentRenewalCampaign) Enroll(campaignID string, userIDs []string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if crIsBlank(campaignID) {
		return 0, ErrBlankArgument
	}
	run, err := c.campaignLocked(campaignID)
	if err != nil {
		return 0, err
	}
	if run.info.Status == CampaignPaused {
		return 0, ErrPaused
	}
	if len(userIDs) == 0 {
		return 0, ErrEmptyBatch
	}

	unique := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if crIsBlank(userID) {
			return 0, ErrBlankArgument
		}
		if _, duplicated := seen[userID]; duplicated {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}

	// The batch cap is measured after in-batch deduplication.
	if len(unique) > c.opts.MaxBatch {
		return 0, ErrBatchLimit
	}

	// Only identities that are not already on the roster grow the audience.
	newUsers := make([]string, 0, len(unique))
	for _, userID := range unique {
		if _, enrolled := run.users[userID]; enrolled {
			continue
		}
		newUsers = append(newUsers, userID)
	}

	// Audience cap and the write happen in one critical section so concurrent
	// batches can never admit more than the configured audience.
	if len(run.users)+len(newUsers) > c.opts.MaxAudience {
		return 0, ErrAudienceLimit
	}

	for _, userID := range newUsers {
		run.users[userID] = struct{}{}
	}
	return len(newUsers), nil
}

// Accept records one user consent for one policy version.
func (c *ConsentRenewalCampaign) Accept(campaignID, userID, version string) (Receipt, error) {
	return c.AcceptAt(campaignID, userID, version, time.Now())
}

// AcceptAt records one user consent at a caller supplied instant.
func (c *ConsentRenewalCampaign) AcceptAt(campaignID, userID, version string, now time.Time) (Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if crIsBlank(campaignID) || crIsBlank(userID) || crIsBlank(version) {
		return Receipt{}, ErrBlankArgument
	}
	run, err := c.campaignLocked(campaignID)
	if err != nil {
		return Receipt{}, err
	}
	if run.info.Status == CampaignPaused {
		return Receipt{}, ErrPaused
	}
	if _, enrolled := run.users[userID]; !enrolled {
		return Receipt{}, ErrNotEnrolled
	}
	if version != run.info.ToVersion {
		return Receipt{}, ErrVersionMismatch
	}

	// One receipt per user and campaign: a repeated acceptance returns the
	// original receipt with its original instant and sequence number.
	if receipt, ok := run.receipts[userID]; ok {
		return receipt, nil
	}

	receipt := Receipt{
		CampaignID: campaignID,
		UserID:     userID,
		Version:    version,
		AcceptedAt: now,
		Seq:        c.nextSeqLocked(),
	}
	run.receipts[userID] = receipt
	return receipt, nil
}

// Exempt takes one enrolled user out of the completion denominator and gates
// with a reason. Repeating the exemption is idempotent.
func (c *ConsentRenewalCampaign) Exempt(campaignID, userID, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if crIsBlank(campaignID) || crIsBlank(userID) {
		return ErrBlankArgument
	}
	run, err := c.campaignLocked(campaignID)
	if err != nil {
		return err
	}
	if _, enrolled := run.users[userID]; !enrolled {
		return ErrNotEnrolled
	}
	if crIsBlank(reason) {
		return ErrBadReason
	}

	if _, exempted := run.reasons[userID]; !exempted {
		run.reasons[userID] = reason
	}
	return nil
}

// Progress reports the campaign counters.
func (c *ConsentRenewalCampaign) Progress(campaignID string) (Stats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if crIsBlank(campaignID) {
		return Stats{}, ErrBlankArgument
	}
	run, err := c.campaignLocked(campaignID)
	if err != nil {
		return Stats{}, err
	}

	enrolled := len(run.users)
	accepted := len(run.receipts)
	exempted := len(run.reasons)

	// Exempt users leave the completion denominator. Guard against a zero
	// denominator (empty audience or everyone exempt) explicitly.
	denominator := enrolled - exempted
	completionBPS := 0
	if denominator > 0 {
		completionBPS = int(int64(accepted) * 10000 / int64(denominator))
	}

	return Stats{
		Enrolled:      enrolled,
		Accepted:      accepted,
		Exempted:      exempted,
		CompletionBPS: completionBPS,
	}, nil
}

// Pause stops enrolment and acceptance for a campaign. It is idempotent and
// never moves the grace deadline.
func (c *ConsentRenewalCampaign) Pause(campaignID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if crIsBlank(campaignID) {
		return ErrBlankArgument
	}
	run, err := c.campaignLocked(campaignID)
	if err != nil {
		return err
	}
	run.info.Status = CampaignPaused
	return nil
}

// Resume reopens a paused campaign. It is idempotent and leaves the original
// grace deadline untouched.
func (c *ConsentRenewalCampaign) Resume(campaignID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if crIsBlank(campaignID) {
		return ErrBlankArgument
	}
	run, err := c.campaignLocked(campaignID)
	if err != nil {
		return err
	}
	run.info.Status = CampaignActive
	return nil
}

// FeatureAllowed answers whether one user may use one feature at the supplied
// instant, and which gate produced the answer.
//
// Accepted users and exempt users are always allowed. During the grace window
// an outstanding user only loses terms dependent features while login and
// sensitive operations stay available. Once the deadline has passed, sensitive
// operations are gated as well; login remains available so the user can reach
// the renewal screen.
func (c *ConsentRenewalCampaign) FeatureAllowed(campaignID, userID, feature string, now time.Time) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	run, ok := c.camps[campaignID]
	if !ok {
		return false, GateFeatureBlocked
	}
	if _, accepted := run.receipts[userID]; accepted {
		return true, GateAllow
	}
	if _, exempted := run.reasons[userID]; exempted {
		return true, GateAllow
	}

	graceOver := !now.Before(run.info.GraceUntil)

	switch classifyFeature(feature) {
	case featureClassLogin:
		// Login is never blocked, during or after the grace window.
		return true, GateAllow
	case featureClassSensitive:
		// Sensitive operations are only restricted after grace expires.
		if graceOver {
			return false, GateSensitiveBlock
		}
		return true, GateAllow
	default:
		// Terms dependent features stay gated until consent is recorded.
		return false, GateFeatureBlocked
	}
}

// classifyFeature maps a caller supplied feature label onto one of the gate
// classes. Matching is case insensitive and tolerates the common separators so
// that "sensitive-action", "sensitive_action" and "sensitive.action" all map
// to the sensitive gate. An unlabelled feature is treated as terms dependent,
// which is the safe default for a renewal campaign.
func classifyFeature(feature string) string {
	compact := compactFeatureName(feature)

	switch {
	case compact == "":
		// A blank feature represents base/product access (the login gate).
		return featureClassLogin
	case strings.Contains(compact, "login"),
		strings.Contains(compact, "signin"),
		strings.Contains(compact, "authenticat"):
		return featureClassLogin
	case strings.Contains(compact, "terms"),
		strings.Contains(compact, "tos"),
		strings.Contains(compact, "policy"),
		strings.Contains(compact, "agreement"),
		strings.Contains(compact, "consent"):
		return featureClassTerms
	case strings.Contains(compact, "sensitive"),
		strings.Contains(compact, "admin"),
		strings.Contains(compact, "security"),
		strings.Contains(compact, "password"),
		strings.Contains(compact, "credential"),
		strings.Contains(compact, "apikey"),
		strings.Contains(compact, "secret"),
		strings.Contains(compact, "pii"),
		strings.Contains(compact, "export"),
		strings.Contains(compact, "withdraw"),
		strings.Contains(compact, "payout"),
		strings.Contains(compact, "payment"),
		strings.Contains(compact, "transfer"),
		strings.Contains(compact, "deleteaccount"),
		strings.Contains(compact, "deactivate"),
		strings.Contains(compact, "impersonate"):
		return featureClassSensitive
	default:
		return featureClassTerms
	}
}

// compactFeatureName lowercases the label and drops every separator character
// so token matching is insensitive to "-", "_", ".", "/" and spaces.
func compactFeatureName(feature string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(feature) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// snapshotCampaign is the flat serialised form of one campaign. Campaign is
// embedded so the required counters, status and deadline sit at the same JSON
// level as the roster, receipts and exemption reasons.
type snapshotCampaign struct {
	Campaign
	Users    []string          `json:"users"`
	Receipts []Receipt         `json:"receipts"`
	Reasons  map[string]string `json:"reasons"`
}

type snapshotData struct {
	Format int                `json:"format"`
	Seq    int64              `json:"seq"`
	Camps  []snapshotCampaign `json:"campaigns"`
}

// Snapshot serialises campaigns, audiences, receipts, exemptions and the
// sequence as one point-in-time JSON payload.
func (c *ConsentRenewalCampaign) Snapshot() ([]byte, error) {
	c.mu.Lock()

	dto := snapshotData{
		Format: 1,
		Seq:    c.seq,
		Camps:  make([]snapshotCampaign, 0, len(c.camps)),
	}
	for _, run := range c.camps {
		info := c.infoLocked(run)
		entry := snapshotCampaign{
			Campaign: info,
			Users:    make([]string, 0, len(run.users)),
			Receipts: make([]Receipt, 0, len(run.receipts)),
			Reasons:  make(map[string]string, len(run.reasons)),
		}
		for userID := range run.users {
			entry.Users = append(entry.Users, userID)
		}
		sort.Strings(entry.Users)
		for _, receipt := range run.receipts {
			entry.Receipts = append(entry.Receipts, receipt)
		}
		sort.Slice(entry.Receipts, func(i, j int) bool {
			return entry.Receipts[i].Seq < entry.Receipts[j].Seq
		})
		for userID, reason := range run.reasons {
			entry.Reasons[userID] = reason
		}
		dto.Camps = append(dto.Camps, entry)
	}
	sort.Slice(dto.Camps, func(i, j int) bool {
		return dto.Camps[i].Seq < dto.Camps[j].Seq
	})
	c.mu.Unlock()

	data, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole store with a snapshot, all or nothing: a bad
// payload yields ErrBadSnapshot and leaves the live store untouched.
func (c *ConsentRenewalCampaign) Restore(data []byte) error {
	var dto snapshotData
	if err := json.Unmarshal(data, &dto); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if err := validateSnapshot(&dto); err != nil {
		return err
	}

	restored := make(map[string]*campaign, len(dto.Camps))
	tenants := make(map[string]map[string]string)
	var seq int64 = dto.Seq

	for _, entry := range dto.Camps {
		users := make(map[string]struct{}, len(entry.Users))
		for _, userID := range entry.Users {
			users[userID] = struct{}{}
		}

		receipts := make(map[string]Receipt, len(entry.Receipts))
		for _, receipt := range entry.Receipts {
			receipts[receipt.UserID] = receipt
			if receipt.Seq > seq {
				seq = receipt.Seq
			}
		}

		reasons := make(map[string]string, len(entry.Reasons))
		for userID, reason := range entry.Reasons {
			reasons[userID] = reason
		}

		info := entry.Campaign
		info.Enrolled = len(users)
		info.Accepted = len(receipts)
		info.Exempted = len(reasons)
		if info.Seq > seq {
			seq = info.Seq
		}

		restored[info.ID] = &campaign{
			info:     info,
			users:    users,
			receipts: receipts,
			reasons:  reasons,
		}

		if tenants[info.TenantID] == nil {
			tenants[info.TenantID] = make(map[string]string)
		}
		tenants[info.TenantID][info.ToVersion] = info.ID
	}

	c.mu.Lock()
	c.camps = restored
	c.tenants = tenants
	c.seq = seq
	c.mu.Unlock()
	return nil
}

// validateSnapshot verifies the decoded payload before any live state is
// touched. It returns ErrBadSnapshot for every inconsistency.
func validateSnapshot(dto *snapshotData) error {
	if dto.Seq < 0 {
		return ErrBadSnapshot
	}

	seenCampaigns := make(map[string]struct{}, len(dto.Camps))
	seenTenantTarget := make(map[string]string, len(dto.Camps))

	for i := range dto.Camps {
		entry := &dto.Camps[i]
		info := entry.Campaign

		if crIsBlank(info.ID) || crIsBlank(info.TenantID) ||
			crIsBlank(info.FromVersion) || crIsBlank(info.ToVersion) {
			return ErrBadSnapshot
		}
		if info.FromVersion == info.ToVersion {
			return ErrBadSnapshot
		}
		if info.Status != CampaignActive && info.Status != CampaignPaused {
			return ErrBadSnapshot
		}
		if _, duplicated := seenCampaigns[info.ID]; duplicated {
			return ErrBadSnapshot
		}
		seenCampaigns[info.ID] = struct{}{}

		targetKey := info.TenantID + "\x00" + info.ToVersion
		if otherID, duplicated := seenTenantTarget[targetKey]; duplicated && otherID != info.ID {
			return ErrBadSnapshot
		}
		seenTenantTarget[targetKey] = info.ID

		if info.Enrolled != len(entry.Users) ||
			info.Accepted != len(entry.Receipts) ||
			info.Exempted != len(entry.Reasons) {
			return ErrBadSnapshot
		}

		seenUsers := make(map[string]struct{}, len(entry.Users))
		for _, userID := range entry.Users {
			if crIsBlank(userID) {
				return ErrBadSnapshot
			}
			if _, duplicated := seenUsers[userID]; duplicated {
				return ErrBadSnapshot
			}
			seenUsers[userID] = struct{}{}
		}

		seenReceiptUsers := make(map[string]struct{}, len(entry.Receipts))
		for _, receipt := range entry.Receipts {
			if crIsBlank(receipt.UserID) || crIsBlank(receipt.Version) {
				return ErrBadSnapshot
			}
			if receipt.Seq <= 0 {
				return ErrBadSnapshot
			}
			if receipt.CampaignID != "" && receipt.CampaignID != info.ID {
				return ErrBadSnapshot
			}
			if receipt.Version != info.ToVersion {
				return ErrBadSnapshot
			}
			if _, ok := seenUsers[receipt.UserID]; !ok {
				return ErrBadSnapshot
			}
			if _, duplicated := seenReceiptUsers[receipt.UserID]; duplicated {
				return ErrBadSnapshot
			}
			seenReceiptUsers[receipt.UserID] = struct{}{}
		}

		for userID, reason := range entry.Reasons {
			if _, ok := seenUsers[userID]; !ok {
				return ErrBadSnapshot
			}
			if crIsBlank(reason) {
				return ErrBadSnapshot
			}
		}
	}

	return nil
}
