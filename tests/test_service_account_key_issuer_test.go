package task

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-10"
)

type issuerClock struct {
	mu sync.Mutex
	at time.Time
}

func newIssuerClock() *issuerClock {
	return &issuerClock{at: time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)}
}

func (c *issuerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *issuerClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func issuerOptions(clock *issuerClock) impl.Options {
	return impl.Options{
		RequiredApprovals:  2,
		MaxTTL:             24 * time.Hour,
		ActivationDelay:    5 * time.Minute,
		MaxPendingRequests: 3,
		MaxKeysPerAccount:  3,
		Now:                clock.Now,
	}
}

func deploySpec() impl.KeySpec {
	return impl.KeySpec{Name: "deploy-bot", Scopes: []string{"read", "deploy"}, TTL: time.Hour}
}

func approveTwo(t *testing.T, i *impl.ServiceAccountKeyIssuer, requestID string) impl.Request {
	t.Helper()
	if _, err := i.Approve(requestID, "reviewer-a"); err != nil {
		t.Fatalf("first approval: %v", err)
	}
	req, err := i.Approve(requestID, "reviewer-b")
	if err != nil {
		t.Fatalf("second approval: %v", err)
	}
	return req
}

func TestRequestCreatesPendingRequest(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if req.ID == "" || req.AccountID != "acct-1" || req.RequesterID != "user-1" {
		t.Fatalf("request identity = %+v, want acct-1 requested by user-1", req)
	}
	if req.State != impl.RequestPending {
		t.Fatalf("request state = %q, want %q", req.State, impl.RequestPending)
	}
	if req.Required != 2 {
		t.Fatalf("required approvals = %d, want the configured 2", req.Required)
	}
	if len(req.Approvals) != 0 {
		t.Fatalf("fresh request must have no approvals, got %v", req.Approvals)
	}
	if req.Seq <= 0 {
		t.Fatalf("request sequence = %d, want a positive number", req.Seq)
	}
	if req.Spec.Name != "deploy-bot" || len(req.Spec.Scopes) != 2 {
		t.Fatalf("request spec = %+v, want the submitted spec", req.Spec)
	}
	if _, ok := i.Get(req.ID); !ok {
		t.Fatalf("request must be readable through Get")
	}
	if got := len(i.List("acct-1")); got != 0 {
		t.Fatalf("keys before issuance = %d, want 0", got)
	}
}

func TestRequestRejectsBadInput(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	if _, err := i.Request("  ", "user-1", deploySpec()); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank account must fail with ErrBlankID, got %v", err)
	}
	if _, err := i.Request("acct-1", "", deploySpec()); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank requester must fail with ErrBlankID, got %v", err)
	}
	noName := deploySpec()
	noName.Name = "  "
	if _, err := i.Request("acct-1", "user-1", noName); !errors.Is(err, impl.ErrBadSpec) {
		t.Fatalf("blank key name must fail with ErrBadSpec, got %v", err)
	}
	noScopes := deploySpec()
	noScopes.Scopes = nil
	if _, err := i.Request("acct-1", "user-1", noScopes); !errors.Is(err, impl.ErrBadSpec) {
		t.Fatalf("empty scopes must fail with ErrBadSpec, got %v", err)
	}
	noTTL := deploySpec()
	noTTL.TTL = 0
	if _, err := i.Request("acct-1", "user-1", noTTL); !errors.Is(err, impl.ErrBadSpec) {
		t.Fatalf("zero ttl must fail with ErrBadSpec, got %v", err)
	}
	if got := len(i.List("acct-1")); got != 0 {
		t.Fatalf("rejected requests must not issue keys, got %d", got)
	}
}

func TestRequestRejectsTTLAboveMax(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	tooLong := deploySpec()
	tooLong.TTL = 25 * time.Hour
	if _, err := i.Request("acct-1", "user-1", tooLong); !errors.Is(err, impl.ErrTTLTooLong) {
		t.Fatalf("ttl above the hard limit must fail with ErrTTLTooLong, got %v", err)
	}
	atLimit := deploySpec()
	atLimit.TTL = 24 * time.Hour
	if _, err := i.Request("acct-1", "user-1", atLimit); err != nil {
		t.Fatalf("ttl at the hard limit must be accepted, got %v", err)
	}
}

func TestRequestEnforcesPendingCap(t *testing.T) {
	clock := newIssuerClock()
	opts := issuerOptions(clock)
	opts.MaxPendingRequests = 2
	i := impl.NewServiceAccountKeyIssuer(opts)
	first, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if _, err := i.Request("acct-1", "user-1", deploySpec()); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if _, err := i.Request("acct-1", "user-1", deploySpec()); !errors.Is(err, impl.ErrRequestLimit) {
		t.Fatalf("third pending request must fail with ErrRequestLimit, got %v", err)
	}
	approveTwo(t, i, first.ID)
	if _, err := i.Issue(first.ID); err != nil {
		t.Fatalf("issue the first request: %v", err)
	}
	if _, err := i.Request("acct-1", "user-1", deploySpec()); err != nil {
		t.Fatalf("requesting after a ticket is issued must succeed, got %v", err)
	}
}

func TestApproveRejectsSelfApproval(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := i.Approve(req.ID, "user-1"); !errors.Is(err, impl.ErrSelfApproval) {
		t.Fatalf("self approval must fail with ErrSelfApproval, got %v", err)
	}
	got, _ := i.Get(req.ID)
	if got.State != impl.RequestPending || len(got.Approvals) != 0 {
		t.Fatalf("request after a self approval = %+v, want pending with no votes", got)
	}
}

func TestApproveReachesQuorum(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	first, err := i.Approve(req.ID, "reviewer-a")
	if err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if first.State != impl.RequestPending || len(first.Approvals) != 1 || first.Approvals[0] != "reviewer-a" {
		t.Fatalf("request after one approval = %+v, want pending with reviewer-a", first)
	}
	second, err := i.Approve(req.ID, "reviewer-b")
	if err != nil {
		t.Fatalf("second approval: %v", err)
	}
	if second.State != impl.RequestApproved || len(second.Approvals) != 2 {
		t.Fatalf("request after quorum = %+v, want approved with two votes", second)
	}
	if _, err := i.Approve(req.ID, "reviewer-c"); !errors.Is(err, impl.ErrRequestClosed) {
		t.Fatalf("approval after quorum must fail with ErrRequestClosed, got %v", err)
	}
}

func TestApproveUnknownRequest(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	if _, err := i.Approve("req-404", "reviewer-a"); !errors.Is(err, impl.ErrRequestNotFound) {
		t.Fatalf("unknown request must fail with ErrRequestNotFound, got %v", err)
	}
	if _, err := i.Approve("", "reviewer-a"); !errors.Is(err, impl.ErrBlankID) {
		t.Fatalf("blank request id must fail with ErrBlankID, got %v", err)
	}
}

func TestIssueRequiresApproval(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := i.Issue(req.ID); !errors.Is(err, impl.ErrNotApproved) {
		t.Fatalf("issuing a pending request must fail with ErrNotApproved, got %v", err)
	}
	if _, err := i.Issue("req-404"); !errors.Is(err, impl.ErrRequestNotFound) {
		t.Fatalf("issuing an unknown request must fail with ErrRequestNotFound, got %v", err)
	}
	if got := len(i.List("acct-1")); got != 0 {
		t.Fatalf("failed issuance must not create keys, got %d", got)
	}
}

func TestIssueReturnsPlaintextOnceAndPrefix(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	approveTwo(t, i, req.ID)
	key, err := i.Issue(req.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if key.KeyID == "" || key.RequestID != req.ID || key.AccountID != "acct-1" {
		t.Fatalf("issued key identity = %+v, want acct-1 from the request", key)
	}
	if len(key.Plaintext) < 32 {
		t.Fatalf("plaintext length = %d, want at least 32 characters", len(key.Plaintext))
	}
	if key.Prefix != key.Plaintext[:8] {
		t.Fatalf("prefix = %q, want the first eight characters of the plaintext", key.Prefix)
	}
	if len(key.Scopes) != 2 || key.Scopes[0] != "read" {
		t.Fatalf("issued scopes = %v, want the requested scopes", key.Scopes)
	}
	if key.State != impl.KeyPendingActivation {
		t.Fatalf("fresh key state = %q, want %q", key.State, impl.KeyPendingActivation)
	}
	if !key.ActivateAt.Equal(clock.Now().Add(issuerOptions(clock).ActivationDelay)) {
		t.Fatalf("activation time = %v, want the configured delay applied", key.ActivateAt)
	}
	if !key.ExpiresAt.Equal(clock.Now().Add(deploySpec().TTL)) {
		t.Fatalf("expiry = %v, want issued at plus the requested ttl", key.ExpiresAt)
	}
	if key.Seq <= 0 {
		t.Fatalf("key sequence = %d, want a positive number", key.Seq)
	}
	if _, ok := i.LookupKey(key.KeyID); !ok {
		t.Fatalf("issued key must be readable through LookupKey")
	}
	meta := i.List("acct-1")
	if len(meta) != 1 || meta[0].KeyID != key.KeyID || meta[0].Prefix != key.Prefix {
		t.Fatalf("key list = %+v, want the issued key with its prefix", meta)
	}
	if strings.Contains(meta[0].Prefix, key.Plaintext[8:]) {
		t.Fatalf("key metadata must not expose the secret beyond the prefix")
	}
	if _, err := i.Issue(req.ID); !errors.Is(err, impl.ErrAlreadyIssued) {
		t.Fatalf("second issuance must fail with ErrAlreadyIssued, got %v", err)
	}
	closed, _ := i.Get(req.ID)
	if closed.State != impl.RequestIssued {
		t.Fatalf("request state after issuance = %q, want %q", closed.State, impl.RequestIssued)
	}
}

func TestActivateHonorsDelay(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	approveTwo(t, i, req.ID)
	key, err := i.Issue(req.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := i.Activate(key.KeyID); !errors.Is(err, impl.ErrTooEarly) {
		t.Fatalf("activation before the delay must fail with ErrTooEarly, got %v", err)
	}
	clock.Advance(issuerOptions(clock).ActivationDelay)
	if err := i.Activate(key.KeyID); err != nil {
		t.Fatalf("activate after the delay: %v", err)
	}
	meta, ok := i.LookupKey(key.KeyID)
	if !ok || meta.State != impl.KeyActive {
		t.Fatalf("key after activation = %+v ok=%v, want active", meta, ok)
	}
	if err := i.Activate("key-404"); !errors.Is(err, impl.ErrKeyNotFound) {
		t.Fatalf("activating an unknown key must fail with ErrKeyNotFound, got %v", err)
	}
}

func TestActivateRejectsExpiredKey(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	approveTwo(t, i, req.ID)
	key, err := i.Issue(req.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	clock.Advance(deploySpec().TTL + time.Second)
	if err := i.Activate(key.KeyID); !errors.Is(err, impl.ErrKeyExpired) {
		t.Fatalf("activating an expired key must fail with ErrKeyExpired, got %v", err)
	}
	meta := i.List("acct-1")
	if len(meta) != 1 || meta[0].State != impl.KeyExpired {
		t.Fatalf("key list after expiry = %+v, want the key reported as expired", meta)
	}
}

func TestDisableRequiresReason(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	approveTwo(t, i, req.ID)
	key, err := i.Issue(req.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	clock.Advance(issuerOptions(clock).ActivationDelay)
	if err := i.Activate(key.KeyID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := i.Disable(key.KeyID, "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank disable reason must fail with ErrBadReason, got %v", err)
	}
	meta, _ := i.LookupKey(key.KeyID)
	if meta.State != impl.KeyActive {
		t.Fatalf("failed disable must leave the key active, got %q", meta.State)
	}
	if err := i.Disable(key.KeyID, "rotated after review"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	meta, _ = i.LookupKey(key.KeyID)
	if meta.State != impl.KeyDisabled {
		t.Fatalf("key after disable = %q, want %q", meta.State, impl.KeyDisabled)
	}
	if err := i.Activate(key.KeyID); !errors.Is(err, impl.ErrKeyDisabled) {
		t.Fatalf("activating a disabled key must fail with ErrKeyDisabled, got %v", err)
	}
	if err := i.Disable("key-404", "gone"); !errors.Is(err, impl.ErrKeyNotFound) {
		t.Fatalf("disabling an unknown key must fail with ErrKeyNotFound, got %v", err)
	}
}

func TestKeyLimitPerAccount(t *testing.T) {
	clock := newIssuerClock()
	opts := issuerOptions(clock)
	opts.MaxKeysPerAccount = 1
	i := impl.NewServiceAccountKeyIssuer(opts)
	first, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	approveTwo(t, i, first.ID)
	approveTwo(t, i, second.ID)
	if _, err := i.Issue(first.ID); err != nil {
		t.Fatalf("issue first key: %v", err)
	}
	if _, err := i.Issue(second.ID); !errors.Is(err, impl.ErrKeyLimit) {
		t.Fatalf("second key must fail with ErrKeyLimit, got %v", err)
	}
	if got := len(i.List("acct-1")); got != 1 {
		t.Fatalf("key count after the limit rejection = %d, want 1", got)
	}
}

func TestSnapshotRestoreKeepsScheduleAndHidesPlaintext(t *testing.T) {
	clock := newIssuerClock()
	opts := issuerOptions(clock)
	i := impl.NewServiceAccountKeyIssuer(opts)
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	approveTwo(t, i, req.ID)
	key, err := i.Issue(req.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	data, err := i.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if strings.Contains(string(data), key.Plaintext) {
		t.Fatalf("snapshot must not carry the key plaintext")
	}
	if err := i.Restore([]byte("broken snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(i.List("acct-1")); got != 1 {
		t.Fatalf("rejected restore must leave the live key table intact, got %d", got)
	}
	restored := impl.NewServiceAccountKeyIssuer(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	meta := restored.List("acct-1")
	if len(meta) != 1 {
		t.Fatalf("restored key count = %d, want 1", len(meta))
	}
	if meta[0].KeyID != key.KeyID || meta[0].Prefix != key.Prefix || meta[0].State != impl.KeyPendingActivation {
		t.Fatalf("restored key meta = %+v, want the issued key with its prefix and state", meta[0])
	}
	if !meta[0].ActivateAt.Equal(key.ActivateAt) || !meta[0].ExpiresAt.Equal(key.ExpiresAt) {
		t.Fatalf("restored schedule = %v / %v, want %v / %v", meta[0].ActivateAt, meta[0].ExpiresAt, key.ActivateAt, key.ExpiresAt)
	}
	if err := restored.Activate(key.KeyID); !errors.Is(err, impl.ErrTooEarly) {
		t.Fatalf("activation before the delay after restore must fail with ErrTooEarly, got %v", err)
	}
	clock.Advance(opts.ActivationDelay)
	if err := restored.Activate(key.KeyID); err != nil {
		t.Fatalf("activate after the delay on the restored issuer: %v", err)
	}
	if _, err := restored.Issue(req.ID); !errors.Is(err, impl.ErrAlreadyIssued) {
		t.Fatalf("the issued request must stay issued after restore, got %v", err)
	}
}

func TestConcurrentApprovalsAndIssue(t *testing.T) {
	clock := newIssuerClock()
	i := impl.NewServiceAccountKeyIssuer(issuerOptions(clock))
	req, err := i.Request("acct-1", "user-1", deploySpec())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	voted, rejected := 0, 0
	for n := 0; n < 6; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := i.Approve(req.ID, "reviewer-"+string(rune('a'+n)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				voted++
			case errors.Is(err, impl.ErrRequestClosed):
				rejected++
			}
		}(n)
	}
	wg.Wait()
	if voted != 2 {
		t.Fatalf("concurrent approvals recorded %d votes, want exactly the quorum 2", voted)
	}
	if rejected != 4 {
		t.Fatalf("concurrent approvals rejected %d submissions, want 4", rejected)
	}
	got, _ := i.Get(req.ID)
	if got.State != impl.RequestApproved || len(got.Approvals) != 2 {
		t.Fatalf("request after the approval race = %+v, want approved with exactly 2 votes", got)
	}
	var iw sync.WaitGroup
	issued, duplicated := 0, 0
	for n := 0; n < 4; n++ {
		iw.Add(1)
		go func() {
			defer iw.Done()
			_, err := i.Issue(req.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				issued++
			case errors.Is(err, impl.ErrAlreadyIssued):
				duplicated++
			}
		}()
	}
	iw.Wait()
	if issued != 1 {
		t.Fatalf("concurrent issuance created %d keys, want exactly 1", issued)
	}
	if duplicated != 3 {
		t.Fatalf("concurrent issuance reported %d already issued errors, want 3", duplicated)
	}
	if got := len(i.List("acct-1")); got != 1 {
		t.Fatalf("key count after the issuance race = %d, want 1", got)
	}
}
