// Package task schedules the passkey re-registration campaign that follows a
// tenant login domain move, keeping the old and the new relying party id alive
// side by side until the overlap window closes.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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

// userRecord is the stored migration state for one user in one campaign.
type userRecord struct {
	UserID        string       `json:"user_id"`
	State         string       `json:"state"`
	NeedsReenroll bool         `json:"needs_reenroll,omitempty"`
	Migrated      []Credential `json:"migrated,omitempty"`
	Legacy        []Credential `json:"legacy,omitempty"`
	Seq           int64        `json:"seq"`
}

// campaignRecord is the stored campaign aggregate together with its users.
type campaignRecord struct {
	Campaign
	Progress map[string]*userRecord `json:"progress"`
}

// snapshotCampaign is the serialised form of one campaign.
type snapshotCampaign struct {
	ID         string                 `json:"id"`
	TenantID   string                 `json:"tenant_id"`
	FromDomain string                 `json:"from_domain"`
	ToDomain   string                 `json:"to_domain"`
	State      string                 `json:"state"`
	StartedAt  time.Time              `json:"started_at"`
	Deadline   time.Time              `json:"deadline"`
	Progress   map[string]*userRecord `json:"progress"`
}

// snapshotFile is the whole-state envelope used by Snapshot/Restore.
type snapshotFile struct {
	Version   int                `json:"version"`
	NextID    int64              `json:"next_id"`
	Campaigns []snapshotCampaign `json:"campaigns"`
}

const snapshotVersion = 1

// RpIdMigrationCampaign owns the campaigns, the per user progress and the clock.
// A single mutex serialises every mutation, which in particular makes
// same-user Record calls and same-campaign Start calls linearisable.
type RpIdMigrationCampaign struct {
	mu        sync.Mutex
	limits    Limits
	now       time.Time
	nextID    int64
	campaigns map[string]*campaignRecord
}

// NewRpIdMigrationCampaign builds a campaign manager with the given caps.
func NewRpIdMigrationCampaign(limits Limits) *RpIdMigrationCampaign {
	return &RpIdMigrationCampaign{
		limits:    limits,
		campaigns: make(map[string]*campaignRecord),
	}
}

// SetNow pins the clock used by the manager.
func (c *RpIdMigrationCampaign) SetNow(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// nowLocked resolves the pinned clock, falling back to the wall clock.
// The caller must hold c.mu.
func (c *RpIdMigrationCampaign) nowLocked() time.Time {
	if c.now.IsZero() {
		return time.Now()
	}
	return c.now
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func (c *RpIdMigrationCampaign) ensureMapsLocked() {
	if c.campaigns == nil {
		c.campaigns = make(map[string]*campaignRecord)
	}
}

// Plan registers a new campaign for a tenant moving between two domains.
func (c *RpIdMigrationCampaign) Plan(tenantID, fromDomain, toDomain string) (Campaign, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Whitespace-only values count as blank, but non blank inputs are kept
	// verbatim so Campaign reads back exactly what Plan was given.
	if isBlank(tenantID) || isBlank(fromDomain) || isBlank(toDomain) {
		return Campaign{}, ErrBlankTenant
	}
	if fromDomain == toDomain {
		return Campaign{}, ErrDomainUnchanged
	}

	c.ensureMapsLocked()
	c.nextID++
	startedAt := c.nowLocked()
	rec := &campaignRecord{
		Campaign: Campaign{
			ID:         fmt.Sprintf("rp-campaign-%06d", c.nextID),
			TenantID:   tenantID,
			FromDomain: fromDomain,
			ToDomain:   toDomain,
			State:      CampaignPlanned,
			StartedAt:  startedAt,
			Deadline:   startedAt.Add(time.Duration(c.limits.MaxOverlapHours) * time.Hour),
		},
		Progress: make(map[string]*userRecord),
	}
	c.campaigns[rec.ID] = rec
	return rec.Campaign, nil
}

// Start flips a planned campaign into the active state.
func (c *RpIdMigrationCampaign) Start(campaignID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return ErrCampaignNotFound
	}
	if rec.State != CampaignPlanned {
		return ErrCampaignNotActive
	}

	active := 0
	for _, other := range c.campaigns {
		if other.TenantID == rec.TenantID && other.State == CampaignActive {
			active++
		}
	}
	if active >= c.limits.MaxActiveCampaignsPerTenant {
		return ErrActiveCampaignLimit
	}

	rec.State = CampaignActive
	return nil
}

// ImportLegacy seeds the old domain credentials still held by one user.
// It is idempotent per credential id: repeated imports never overwrite an
// existing credential and duplicate ids inside the batch collapse to one.
func (c *RpIdMigrationCampaign) ImportLegacy(campaignID, userID string, creds []Credential) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return ErrCampaignNotFound
	}
	if isBlank(userID) {
		return ErrBlankUser
	}
	for _, cred := range creds {
		if cred.RPID != rec.FromDomain {
			return ErrCredentialDomain
		}
	}

	user := rec.userOrNew(userID)

	merged := make(map[string]Credential, len(user.Legacy)+len(creds))
	for _, cred := range user.Legacy {
		merged[cred.ID] = cred
	}
	fresh := make([]Credential, 0, len(creds))
	for _, cred := range creds {
		if _, exists := merged[cred.ID]; exists {
			continue
		}
		merged[cred.ID] = cred
		fresh = append(fresh, cred)
	}
	if len(merged) > c.limits.MaxCredentialsPerUser {
		return ErrLegacyLimit
	}

	user.Legacy = append(user.Legacy, fresh...)
	rec.Users = len(rec.Progress)
	return nil
}

// Record stores one credential registered under the new relying party id.
func (c *RpIdMigrationCampaign) Record(campaignID, userID string, cred Credential) (UserProgress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return UserProgress{}, ErrCampaignNotFound
	}
	if rec.State != CampaignActive {
		return UserProgress{}, ErrCampaignNotActive
	}
	if isBlank(userID) {
		return UserProgress{}, ErrBlankUser
	}
	if cred.RPID != rec.ToDomain {
		return UserProgress{}, ErrCredentialDomain
	}

	user := rec.userOrNew(userID)
	for _, existing := range user.Migrated {
		if existing.ID == cred.ID {
			return UserProgress{}, ErrDuplicateCredential
		}
	}
	if len(user.Migrated) >= c.limits.MaxCredentialsPerUser {
		return UserProgress{}, ErrCredentialLimit
	}

	user.Migrated = append(user.Migrated, cred)
	user.Seq++
	view := progressView(rec, user)
	user.State = view.State
	rec.Users = len(rec.Progress)
	return view, nil
}

// Status reads the migration progress of one user.
func (c *RpIdMigrationCampaign) Status(campaignID, userID string) (UserProgress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return UserProgress{}, ErrCampaignNotFound
	}
	user, ok := rec.Progress[userID]
	if !ok {
		return UserProgress{
			CampaignID: campaignID,
			UserID:     userID,
			State:      ProgressPending,
			Migrated:   []Credential{},
			Legacy:     []Credential{},
			Remaining:  0,
			Deadline:   rec.Deadline,
		}, nil
	}
	return progressView(rec, user), nil
}

// Progress lists every user tracked by one campaign, ordered by user id.
func (c *RpIdMigrationCampaign) Progress(campaignID string) []UserProgress {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return nil
	}

	userIDs := make([]string, 0, len(rec.Progress))
	for userID := range rec.Progress {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)

	out := make([]UserProgress, 0, len(userIDs))
	for _, userID := range userIDs {
		out = append(out, progressView(rec, rec.Progress[userID]))
	}
	return out
}

// SetDeadline sets the shutdown time of the overlap window.
func (c *RpIdMigrationCampaign) SetDeadline(campaignID string, at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return ErrCampaignNotFound
	}
	if rec.State == CampaignClosed || rec.State == CampaignRolledBack {
		return ErrCampaignNotActive
	}
	if at.Before(rec.StartedAt) {
		return ErrDeadlineInvalid
	}
	maxOverlap := time.Duration(c.limits.MaxOverlapHours) * time.Hour
	if at.Sub(rec.StartedAt) > maxOverlap {
		return ErrDeadlineTooLong
	}

	rec.Deadline = at
	return nil
}

// LoginPolicy tells whether a login on domain with the given rp id is accepted.
func (c *RpIdMigrationCampaign) LoginPolicy(campaignID, userID, domain, rpID string) (LoginVerdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return LoginVerdict{}, ErrCampaignNotFound
	}

	// A swept campaign is closed; an active campaign whose deadline already
	// passed behaves as expired even before Sweep runs.
	expired := rec.State == CampaignClosed
	if rec.State == CampaignActive && c.nowLocked().After(rec.Deadline) {
		expired = true
	}

	switch domain {
	case rec.ToDomain:
		// The new domain must never accept a credential bound to any other
		// RP id, in particular the old one.
		if rpID != rec.ToDomain {
			return LoginVerdict{Allowed: false, Path: PathNewRP, Reason: ReasonRPIDMismatch}, nil
		}
		if rec.State == CampaignRolledBack {
			// New-domain credentials were only marked for re-enrolment, so
			// they are not directly trusted until registered again.
			return LoginVerdict{Allowed: false, Path: PathRecover, Reason: ReasonUserPending}, nil
		}
		return LoginVerdict{Allowed: true, Path: PathNewRP, Reason: ReasonRPIDMatch}, nil

	case rec.FromDomain:
		// A new-RP credential is never an old-domain credential.
		if rpID != rec.FromDomain {
			return LoginVerdict{Allowed: false, Path: PathOldRP, Reason: ReasonRPIDMismatch}, nil
		}
		if expired {
			// The overlap window is gone: route the user into recovery
			// instead of hard locking them out. The verdict stays
			// non-admitting for the direct old-RP login.
			return LoginVerdict{Allowed: false, Path: PathRecover, Reason: ReasonCampaignClosed}, nil
		}
		// During the overlap (and after a rollback) legacy RP credentials
		// keep working on the old domain.
		return LoginVerdict{Allowed: true, Path: PathOldRP, Reason: ReasonRPIDMatch}, nil

	default:
		path := ""
		switch rpID {
		case rec.ToDomain:
			path = PathNewRP
		case rec.FromDomain:
			path = PathOldRP
		}
		return LoginVerdict{Allowed: false, Path: path, Reason: ReasonRPIDMismatch}, nil
	}
}

// Rollback aborts a running campaign and keeps the users for re-registration.
func (c *RpIdMigrationCampaign) Rollback(campaignID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return ErrCampaignNotFound
	}
	if rec.State != CampaignActive {
		return ErrCampaignNotActive
	}

	rec.State = CampaignRolledBack
	for _, user := range rec.Progress {
		if len(user.Migrated) > 0 {
			// Preserve Migrated (including PublicKey and authenticator
			// metadata) but flag the holder as needing re-enrolment. The
			// credentials are never copied into Legacy.
			user.NeedsReenroll = true
			user.State = ProgressNeedsEnroll
		}
	}
	return nil
}

// Sweep closes the active campaigns whose deadline has passed.
func (c *RpIdMigrationCampaign) Sweep(now time.Time) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	closed := 0
	for _, rec := range c.campaigns {
		if rec.State == CampaignActive && now.After(rec.Deadline) {
			rec.State = CampaignClosed
			closed++
		}
	}
	return closed, nil
}

// Campaign reads one campaign record.
func (c *RpIdMigrationCampaign) Campaign(campaignID string) (Campaign, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	rec, ok := c.campaigns[campaignID]
	if !ok {
		return Campaign{}, false
	}
	out := rec.Campaign
	out.Users = len(rec.Progress)
	return out, true
}

// Snapshot serialises the campaigns, the per user progress and the deadlines.
func (c *RpIdMigrationCampaign) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := snapshotFile{
		Version:   snapshotVersion,
		NextID:    c.nextID,
		Campaigns: make([]snapshotCampaign, 0, len(c.campaigns)),
	}

	ids := make([]string, 0, len(c.campaigns))
	for id := range c.campaigns {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		rec := c.campaigns[id]
		sc := snapshotCampaign{
			ID:         rec.ID,
			TenantID:   rec.TenantID,
			FromDomain: rec.FromDomain,
			ToDomain:   rec.ToDomain,
			State:      rec.State,
			StartedAt:  rec.StartedAt,
			Deadline:   rec.Deadline,
			Progress:   make(map[string]*userRecord, len(rec.Progress)),
		}
		for userID, user := range rec.Progress {
			clone := *user
			clone.Migrated = append([]Credential(nil), user.Migrated...)
			clone.Legacy = append([]Credential(nil), user.Legacy...)
			sc.Progress[userID] = &clone
		}
		out.Campaigns = append(out.Campaigns, sc)
	}

	return json.Marshal(out)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *RpIdMigrationCampaign) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var snap *snapshotFile
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if snap == nil {
		return fmt.Errorf("%w: null payload", ErrBadSnapshot)
	}
	if snap.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported snapshot version %d", ErrBadSnapshot, snap.Version)
	}

	next := make(map[string]*campaignRecord, len(snap.Campaigns))
	for i := range snap.Campaigns {
		sc := snap.Campaigns[i]
		if sc.ID == "" || isBlank(sc.TenantID) || isBlank(sc.FromDomain) || isBlank(sc.ToDomain) {
			return fmt.Errorf("%w: campaign at index %d is missing identity fields", ErrBadSnapshot, i)
		}
		if sc.FromDomain == sc.ToDomain {
			return fmt.Errorf("%w: campaign %q has unchanged domains", ErrBadSnapshot, sc.ID)
		}
		switch sc.State {
		case CampaignPlanned, CampaignActive, CampaignClosed, CampaignRolledBack:
		default:
			return fmt.Errorf("%w: campaign %q has unknown state %q", ErrBadSnapshot, sc.ID, sc.State)
		}
		if sc.StartedAt.IsZero() || sc.Deadline.IsZero() {
			return fmt.Errorf("%w: campaign %q is missing its times", ErrBadSnapshot, sc.ID)
		}
		if _, dup := next[sc.ID]; dup {
			return fmt.Errorf("%w: duplicate campaign %q", ErrBadSnapshot, sc.ID)
		}

		progress := make(map[string]*userRecord, len(sc.Progress))
		for userID, user := range sc.Progress {
			if user == nil {
				return fmt.Errorf("%w: campaign %q user %q has empty record", ErrBadSnapshot, sc.ID, userID)
			}
			if user.UserID == "" {
				user.UserID = userID
			}
			if user.Seq < 0 {
				return fmt.Errorf("%w: campaign %q user %q has negative seq", ErrBadSnapshot, sc.ID, userID)
			}
			progress[userID] = user
		}

		next[sc.ID] = &campaignRecord{
			Campaign: Campaign{
				ID:         sc.ID,
				TenantID:   sc.TenantID,
				FromDomain: sc.FromDomain,
				ToDomain:   sc.ToDomain,
				State:      sc.State,
				StartedAt:  sc.StartedAt,
				Deadline:   sc.Deadline,
				Users:      len(progress),
			},
			Progress: progress,
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.campaigns = next
	c.nextID = snap.NextID
	return nil
}

// userOrNew returns the stored user record, creating it on first write.
// The caller must hold c.mu.
func (rec *campaignRecord) userOrNew(userID string) *userRecord {
	if rec.Progress == nil {
		rec.Progress = make(map[string]*userRecord)
	}
	user, ok := rec.Progress[userID]
	if !ok {
		user = &userRecord{UserID: userID, State: ProgressPending}
		rec.Progress[userID] = user
	}
	return user
}

// progressView projects the stored user state into the read model, deriving
// remaining count and state from the credential lists.
// The caller must hold c.mu.
func progressView(rec *campaignRecord, user *userRecord) UserProgress {
	migrated := append([]Credential(nil), user.Migrated...)
	legacy := append([]Credential(nil), user.Legacy...)

	remaining := len(legacy) - len(migrated)
	if remaining < 0 {
		remaining = 0
	}

	state := ProgressPending
	switch {
	case user.NeedsReenroll || user.State == ProgressNeedsEnroll:
		state = ProgressNeedsEnroll
	case len(migrated) == 0:
		state = ProgressPending
	case remaining == 0:
		state = ProgressMigrated
	default:
		state = ProgressPartial
	}

	return UserProgress{
		CampaignID: rec.ID,
		UserID:     user.UserID,
		State:      state,
		Migrated:   migrated,
		Legacy:     legacy,
		Remaining:  remaining,
		Deadline:   rec.Deadline,
		Seq:        user.Seq,
	}
}
