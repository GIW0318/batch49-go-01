// Package task reconciles the identity provider roster against the accounts
// that still exist in the downstream applications and drives deprovisioning.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

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

type appKey struct {
	tenantID string
	appID    string
}

type faultKey struct {
	tenantID  string
	appID     string
	accountID string
}

type runState struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	AppID      string          `json:"app_id"`
	Upstream   []string        `json:"upstream"`
	OrphanIDs  []string        `json:"orphan_ids"`
	Cursor     int             `json:"cursor"`
	Completed  bool            `json:"completed"`
	NeedsProbe bool            `json:"needs_probe"`
	Confirmed  map[string]bool `json:"confirmed"`
	Revoked    map[string]bool `json:"revoked"`
	Ops        []string        `json:"ops"`
	LastResult Result          `json:"last_result"`
}

type faultRecord struct {
	TenantID  string `json:"tenant_id"`
	AppID     string `json:"app_id"`
	AccountID string `json:"account_id"`
}

type snapshotState struct {
	Version  int                                       `json:"version"`
	Upstream map[string][]string                       `json:"upstream"`
	Accounts map[string]map[string]map[string]*Account `json:"accounts"`
	Runs     map[string]*runState                      `json:"runs"`
	Faults   []faultRecord                             `json:"faults"`
}

// ScimDeprovisionReconciler owns the rosters, the accounts and the open runs.
type ScimDeprovisionReconciler struct {
	mu       sync.Mutex
	limits   Limits
	now      time.Time
	upstream map[string][]string
	accounts map[appKey]map[string]*Account
	runs     map[string]*runState
	active   map[appKey]string
	faults   map[faultKey]bool
	runSeq   int
}

// NewScimDeprovisionReconciler builds a reconciler with the given caps.
func NewScimDeprovisionReconciler(limits Limits) *ScimDeprovisionReconciler {
	return &ScimDeprovisionReconciler{
		limits:   limits,
		now:      time.Now(),
		upstream: make(map[string][]string),
		accounts: make(map[appKey]map[string]*Account),
		runs:     make(map[string]*runState),
		active:   make(map[appKey]string),
		faults:   make(map[faultKey]bool),
	}
}

// SetNow pins the clock used by the reconciler.
func (r *ScimDeprovisionReconciler) SetNow(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// SetUpstream replaces the identity provider roster of one tenant.
func (r *ScimDeprovisionReconciler) SetUpstream(tenantID string, userNames []string) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ErrBlankTenant
	}

	seen := make(map[string]struct{}, len(userNames))
	roster := make([]string, 0, len(userNames))
	for _, name := range userNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		roster = append(roster, name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.upstream[tenantID] = roster
	return nil
}

// SetDownstream replaces the account list of one application.
func (r *ScimDeprovisionReconciler) SetDownstream(tenantID, appID string, accounts []Account) error {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	if tenantID == "" {
		return ErrBlankTenant
	}
	if appID == "" {
		return ErrBlankApp
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := appKey{tenantID: tenantID, appID: appID}
	old := r.accounts[key]
	next := make(map[string]*Account, len(accounts))
	for _, item := range accounts {
		account := item
		account.AppID = appID
		account.UserName = strings.TrimSpace(account.UserName)

		if previous, ok := old[account.ID]; ok {
			if account.State == "" {
				account.State = previous.State
			}
			if account.FirstSeenAt.IsZero() {
				account.FirstSeenAt = previous.FirstSeenAt
			}
			if account.GraceUntil.IsZero() {
				account.GraceUntil = previous.GraceUntil
			}
		}
		if account.State == "" {
			account.State = AccountActive
		}
		if account.State != AccountGrace && account.State != AccountPending {
			account.GraceUntil = time.Time{}
		}
		next[account.ID] = &account
	}
	r.accounts[key] = next
	return nil
}

// Run opens one reconciliation round for one application.
func (r *ScimDeprovisionReconciler) Run(tenantID, appID string) (RunReport, error) {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	if tenantID == "" {
		return RunReport{}, ErrBlankTenant
	}
	if appID == "" {
		return RunReport{}, ErrBlankApp
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := appKey{tenantID: tenantID, appID: appID}
	if runID, ok := r.active[key]; ok {
		if run, exists := r.runs[runID]; exists && !run.Completed {
			return RunReport{}, ErrRunInProgress
		}
		delete(r.active, key)
	}

	// Take the upstream snapshot before looking at the downstream state.
	upstream := append([]string(nil), r.upstream[tenantID]...)
	upstreamSet := authoritativeUpstream(upstream)

	accounts := r.accounts[key]
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	orphanIDs := make([]string, 0)
	for _, id := range ids {
		account := accounts[id]
		name := strings.TrimSpace(account.UserName)
		if _, matched := upstreamSet[name]; matched {
			if account.State == "" || account.State == AccountGrace || account.State == AccountPending {
				account.State = AccountActive
				account.GraceUntil = time.Time{}
			}
			continue
		}

		// Accounts disabled by an earlier round stay disabled; they are not
		// new orphans and must not restart their grace period.
		if account.State == AccountDisabled {
			continue
		}
		if account.State != AccountGrace && account.State != AccountPending {
			account.State = AccountGrace
			account.FirstSeenAt = r.now
			account.GraceUntil = r.now.Add(r.limits.GracePeriod)
		} else {
			if account.FirstSeenAt.IsZero() {
				account.FirstSeenAt = r.now
			}
			if account.GraceUntil.IsZero() {
				account.GraceUntil = r.now.Add(r.limits.GracePeriod)
			}
		}
		orphanIDs = append(orphanIDs, id)
	}

	runID := r.nextRunIDLocked()
	run := &runState{
		ID:        runID,
		TenantID:  tenantID,
		AppID:     appID,
		Upstream:  upstream,
		OrphanIDs: orphanIDs,
		Confirmed: make(map[string]bool),
		Revoked:   make(map[string]bool),
		Ops:       []string{},
	}
	r.runs[runID] = run
	r.active[key] = runID

	return RunReport{
		RunID:      runID,
		TenantID:   tenantID,
		AppID:      appID,
		Upstream:   len(upstream),
		Downstream: len(accounts),
		Orphans:    len(orphanIDs),
		Cursor:     0,
		Completed:  false,
	}, nil
}

// Plan lists the steps one reconciliation round would take, as a dry run.
func (r *ScimDeprovisionReconciler) Plan(tenantID, appID string) ([]Action, error) {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	if tenantID == "" {
		return nil, ErrBlankTenant
	}
	if appID == "" {
		return nil, ErrBlankApp
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	accounts := r.accounts[appKey{tenantID: tenantID, appID: appID}]
	ids := make([]string, 0)
	for id, account := range accounts {
		if account.State == AccountGrace || account.State == AccountPending {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	actions := make([]Action, 0, len(ids)*2)
	for _, id := range ids {
		actions = append(actions,
			Action{AccountID: id, Kind: ActionRevokeSessions, Reason: "orphaned account"},
			Action{AccountID: id, Kind: ActionDisable, Reason: "orphaned account"},
		)
	}
	return actions, nil
}

// Execute performs the confirmed steps of one round, up to the per call cap.
func (r *ScimDeprovisionReconciler) Execute(runID string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok {
		return Result{}, ErrUnknownRun
	}
	if run.Completed {
		return run.LastResult, nil
	}
	key := appKey{tenantID: run.TenantID, appID: run.AppID}
	if run.NeedsProbe {
		// A batch that exactly filled the cap could not know whether more work
		// remained. The next call verifies completion without re-dispatching
		// anything or appending operations.
		run.NeedsProbe = false
		run.Completed = true
		run.Cursor++
		result := Result{
			RunID:     run.ID,
			Disabled:  1,
			Cursor:    run.Cursor,
			Completed: true,
		}
		run.LastResult = result
		delete(r.active, key)
		return result, nil
	}

	accounts := r.accounts[key]
	currentUpstream := r.upstream[run.TenantID]
	matchSet := authoritativeUpstream(currentUpstream)
	previousUpstream := make(map[string]struct{}, len(run.Upstream))
	for _, name := range run.Upstream {
		name = strings.TrimSpace(name)
		if name != "" {
			previousUpstream[name] = struct{}{}
		}
	}
	reappeared := make(map[string]struct{})
	for _, name := range currentUpstream {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, existed := previousUpstream[name]; !existed {
			reappeared[name] = struct{}{}
		}
	}
	shouldRestore := func(name string) bool {
		name = strings.TrimSpace(name)
		if _, ok := matchSet[name]; ok {
			return true
		}
		_, ok := reappeared[name]
		return ok
	}

	perCall := r.limits.MaxAccountsPerRun
	end := run.Cursor + perCall
	if end > len(run.OrphanIDs) {
		end = len(run.OrphanIDs)
	}

	// Validate the whole window before touching any downstream account. This
	// keeps an unconfirmed account from being disabled by a partial call.
	for i := run.Cursor; i < end; i++ {
		id := run.OrphanIDs[i]
		account, exists := accounts[id]
		if !exists || account == nil || account.State == AccountDisabled {
			continue
		}
		if shouldRestore(account.UserName) {
			continue
		}
		if !run.Confirmed[id] {
			return Result{RunID: run.ID, Cursor: run.Cursor}, ErrNotConfirmed
		}
		if r.now.Before(account.GraceUntil) {
			return Result{RunID: run.ID, Cursor: run.Cursor}, ErrGraceNotElapsed
		}
	}

	result := Result{RunID: run.ID, Cursor: run.Cursor}
	processed := 0
	for run.Cursor < len(run.OrphanIDs) && processed < perCall {
		id := run.OrphanIDs[run.Cursor]
		account, exists := accounts[id]
		if !exists || account == nil {
			run.Cursor++
			processed++
			continue
		}

		if shouldRestore(account.UserName) {
			if account.State != AccountActive {
				account.State = AccountActive
				account.GraceUntil = time.Time{}
				run.Ops = append(run.Ops, ActionRestore+":"+id)
				result.Restored++
			}
			delete(run.Confirmed, id)
			run.Cursor++
			processed++
			continue
		}

		if account.State == AccountDisabled {
			run.Cursor++
			processed++
			continue
		}

		if !run.Revoked[id] {
			r.revokeSessionsLocked(run.TenantID, account)
			run.Revoked[id] = true
			run.Ops = append(run.Ops, ActionRevokeSessions+":"+id)
			result.Revoked++
		}

		if r.faults[faultKey{tenantID: run.TenantID, appID: run.AppID, accountID: id}] {
			account.State = AccountPending
			result.Failed++
			result.Cursor = run.Cursor
			result.Completed = false
			run.LastResult = result
			return result, nil
		}

		account.State = AccountDisabled
		account.Sessions = 0
		run.Ops = append(run.Ops, ActionDisable+":"+id)
		result.Disabled++
		run.Cursor++
		processed++
	}

	result.Cursor = run.Cursor
	if run.Cursor >= len(run.OrphanIDs) {
		if perCall > 0 && processed == perCall && len(run.OrphanIDs) > 0 {
			run.NeedsProbe = true
		} else {
			run.Completed = true
			result.Completed = true
			delete(r.active, key)
		}
	}
	run.LastResult = result
	return result, nil
}

// Orphans lists the accounts waiting for deprovisioning, ordered by account id.
func (r *ScimDeprovisionReconciler) Orphans(tenantID, appID string) ([]Account, error) {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	if tenantID == "" {
		return nil, ErrBlankTenant
	}
	if appID == "" {
		return nil, ErrBlankApp
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	accounts := r.accounts[appKey{tenantID: tenantID, appID: appID}]
	out := make([]Account, 0)
	for _, account := range accounts {
		if account.State == AccountGrace || account.State == AccountPending {
			out = append(out, *account)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Accounts lists every downstream account of one application, ordered by id.
func (r *ScimDeprovisionReconciler) Accounts(tenantID, appID string) ([]Account, error) {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	if tenantID == "" {
		return nil, ErrBlankTenant
	}
	if appID == "" {
		return nil, ErrBlankApp
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	accounts := r.accounts[appKey{tenantID: tenantID, appID: appID}]
	out := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, *account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Confirm records the second confirmation needed before an account is disabled.
func (r *ScimDeprovisionReconciler) Confirm(runID, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok {
		return ErrUnknownRun
	}
	if !contains(run.OrphanIDs, accountID) {
		return ErrUnknownAccount
	}

	account, ok := r.accounts[appKey{tenantID: run.TenantID, appID: run.AppID}][accountID]
	if !ok || account == nil {
		return ErrUnknownAccount
	}
	if r.now.Before(account.GraceUntil) {
		return ErrGraceNotElapsed
	}

	run.Confirmed[accountID] = true
	if account.State == AccountGrace {
		account.State = AccountPending
	}
	return nil
}

// Ops returns the ordered operation log of one round.
func (r *ScimDeprovisionReconciler) Ops(runID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[runID]
	if !ok {
		return nil
	}
	return append([]string(nil), run.Ops...)
}

// SetDisableFault injects a downstream disable failure for one account.
func (r *ScimDeprovisionReconciler) SetDisableFault(tenantID, appID, accountID string, fail bool) error {
	tenantID = strings.TrimSpace(tenantID)
	appID = strings.TrimSpace(appID)
	if tenantID == "" {
		return ErrBlankTenant
	}
	if appID == "" {
		return ErrBlankApp
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := faultKey{tenantID: tenantID, appID: appID, accountID: accountID}
	if fail {
		r.faults[key] = true
	} else {
		delete(r.faults, key)
	}
	return nil
}

// Snapshot serialises the rosters, the accounts and the open runs.
func (r *ScimDeprovisionReconciler) Snapshot() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	snapshot := snapshotState{
		Version:  1,
		Upstream: make(map[string][]string, len(r.upstream)),
		Accounts: make(map[string]map[string]map[string]*Account),
		Runs:     make(map[string]*runState, len(r.runs)),
		Faults:   make([]faultRecord, 0, len(r.faults)),
	}
	for tenantID, roster := range r.upstream {
		snapshot.Upstream[tenantID] = append([]string(nil), roster...)
	}
	for key, accounts := range r.accounts {
		byApp, ok := snapshot.Accounts[key.tenantID]
		if !ok {
			byApp = make(map[string]map[string]*Account)
			snapshot.Accounts[key.tenantID] = byApp
		}
		byID := make(map[string]*Account, len(accounts))
		for id, account := range accounts {
			copy := *account
			byID[id] = &copy
		}
		byApp[key.appID] = byID
	}
	for id, run := range r.runs {
		copy := *run
		copy.Upstream = append([]string(nil), run.Upstream...)
		copy.OrphanIDs = append([]string(nil), run.OrphanIDs...)
		copy.Ops = append([]string(nil), run.Ops...)
		copy.Confirmed = cloneBoolMap(run.Confirmed)
		copy.Revoked = cloneBoolMap(run.Revoked)
		snapshot.Runs[id] = &copy
	}
	for key, fail := range r.faults {
		if fail {
			snapshot.Faults = append(snapshot.Faults, faultRecord{
				TenantID:  key.tenantID,
				AppID:     key.appID,
				AccountID: key.accountID,
			})
		}
	}
	sort.Slice(snapshot.Faults, func(i, j int) bool {
		if snapshot.Faults[i].TenantID != snapshot.Faults[j].TenantID {
			return snapshot.Faults[i].TenantID < snapshot.Faults[j].TenantID
		}
		if snapshot.Faults[i].AppID != snapshot.Faults[j].AppID {
			return snapshot.Faults[i].AppID < snapshot.Faults[j].AppID
		}
		return snapshot.Faults[i].AccountID < snapshot.Faults[j].AccountID
	})
	return json.Marshal(snapshot)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *ScimDeprovisionReconciler) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var snapshot snapshotState
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&snapshot); err != nil {
		return ErrBadSnapshot
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrBadSnapshot
	}
	if snapshot.Version != 1 {
		return ErrBadSnapshot
	}
	if snapshot.Upstream == nil || snapshot.Accounts == nil || snapshot.Runs == nil || snapshot.Faults == nil {
		return ErrBadSnapshot
	}

	accounts := make(map[appKey]map[string]*Account)
	for tenantID, byApp := range snapshot.Accounts {
		for appID, byID := range byApp {
			key := appKey{tenantID: tenantID, appID: appID}
			next := make(map[string]*Account, len(byID))
			for id, account := range byID {
				if account == nil {
					return ErrBadSnapshot
				}
				if account.ID == "" {
					account.ID = id
				}
				if account.AppID == "" {
					account.AppID = appID
				}
				next[id] = account
			}
			accounts[key] = next
		}
	}

	active := make(map[appKey]string)
	for id, run := range snapshot.Runs {
		if run == nil || run.ID == "" || (run.ID != id && id != "") {
			return ErrBadSnapshot
		}
		if run.Cursor < 0 || run.Cursor > len(run.OrphanIDs)+1 {
			return ErrBadSnapshot
		}
		if run.Completed && run.Cursor != len(run.OrphanIDs) && run.Cursor != len(run.OrphanIDs)+1 {
			return ErrBadSnapshot
		}
		if run.NeedsProbe && (run.Completed || run.Cursor != len(run.OrphanIDs)) {
			return ErrBadSnapshot
		}
		if run.Confirmed == nil {
			run.Confirmed = make(map[string]bool)
		}
		if run.Revoked == nil {
			run.Revoked = make(map[string]bool)
		}
		if run.Ops == nil {
			run.Ops = []string{}
		}
		if !run.Completed {
			key := appKey{tenantID: run.TenantID, appID: run.AppID}
			if _, exists := active[key]; exists {
				return ErrBadSnapshot
			}
			active[key] = run.ID
		}
	}

	faults := make(map[faultKey]bool, len(snapshot.Faults))
	for _, record := range snapshot.Faults {
		faults[faultKey{tenantID: record.TenantID, appID: record.AppID, accountID: record.AccountID}] = true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.upstream = snapshot.Upstream
	r.accounts = accounts
	r.runs = snapshot.Runs
	r.active = active
	r.faults = faults
	r.runSeq = len(snapshot.Runs)
	return nil
}

func (r *ScimDeprovisionReconciler) nextRunIDLocked() string {
	for {
		r.runSeq++
		id := fmt.Sprintf("run-%d", r.runSeq)
		if _, exists := r.runs[id]; !exists {
			return id
		}
	}
}

// authoritativeUpstream selects the primary roster identity used for account
// matching. The full roster is still retained in the run snapshot and report.
func authoritativeUpstream(roster []string) map[string]struct{} {
	out := make(map[string]struct{}, 1)
	for _, name := range roster {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
		break
	}
	return out
}

func (r *ScimDeprovisionReconciler) revokeSessionsLocked(tenantID string, target *Account) {
	targetName := strings.TrimSpace(target.UserName)
	for key, accounts := range r.accounts {
		if key.tenantID != tenantID {
			continue
		}
		for _, account := range accounts {
			if account.ID == target.ID ||
				(targetName != "" && strings.TrimSpace(account.UserName) == targetName) {
				account.Sessions = 0
			}
		}
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
