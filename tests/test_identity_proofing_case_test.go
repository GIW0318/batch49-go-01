package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch49-go-safety-a-27"
)

func proofLimits() impl.Limits {
	return impl.Limits{MaxRetries: 3, MaxCasesPerUser: 4}
}

func proofDoc() impl.Document {
	return impl.Document{Kind: "id-card", Number: "310101199001011234", Digest: "sha256-9f21", Issuer: "mps"}
}

// readyCase opens a case that already holds a document and a liveness frame.
func readyCase(t *testing.T, c *impl.IdentityProofingCase, user string, level impl.Level) impl.Case {
	t.Helper()
	cs, err := c.Start(user, level)
	if err != nil {
		t.Fatalf("start case: %v", err)
	}
	if err := c.SubmitDocument(cs.ID, proofDoc()); err != nil {
		t.Fatalf("submit document: %v", err)
	}
	if err := c.SubmitLiveness(cs.ID, []byte("frame-0001")); err != nil {
		t.Fatalf("submit liveness: %v", err)
	}
	return cs
}

func TestStartBeginsCollectedCase(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs, err := c.Start("user-1", impl.LevelStandard)
	if err != nil {
		t.Fatalf("start must succeed, got %v", err)
	}
	if cs.ID == "" {
		t.Fatalf("case id must not be empty")
	}
	if cs.UserID != "user-1" || cs.Level != impl.LevelStandard {
		t.Fatalf("case user=%q level=%d, want user-1/standard", cs.UserID, cs.Level)
	}
	if cs.State != impl.CaseCollected {
		t.Fatalf("case state = %q, want %q", cs.State, impl.CaseCollected)
	}
	if cs.Seq <= 0 {
		t.Fatalf("case seq = %d, want a positive number", cs.Seq)
	}
	state, err := c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseCollected {
		t.Fatalf("read back state = %q, want %q", state, impl.CaseCollected)
	}
}

func TestStartRejectsBlankUserAndBadLevel(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	if _, err := c.Start("   ", impl.LevelBasic); !errors.Is(err, impl.ErrBlankUser) {
		t.Fatalf("blank user must fail with ErrBlankUser, got %v", err)
	}
	if _, err := c.Start("user-2", impl.Level(9)); !errors.Is(err, impl.ErrBadLevel) {
		t.Fatalf("unknown level must fail with ErrBadLevel, got %v", err)
	}
	if _, err := c.CaseState("case-404"); !errors.Is(err, impl.ErrUnknownCase) {
		t.Fatalf("unknown case must fail with ErrUnknownCase, got %v", err)
	}
}

func TestStartEnforcesCaseLimit(t *testing.T) {
	c := impl.NewIdentityProofingCase(impl.Limits{MaxRetries: 3, MaxCasesPerUser: 2})
	for i := 0; i < 2; i++ {
		if _, err := c.Start("user-3", impl.LevelBasic); err != nil {
			t.Fatalf("case %d for the user must succeed, got %v", i, err)
		}
	}
	if _, err := c.Start("user-3", impl.LevelBasic); !errors.Is(err, impl.ErrCaseLimit) {
		t.Fatalf("third case must hit the per user cap, got %v", err)
	}
	if _, err := c.Start("user-4", impl.LevelBasic); err != nil {
		t.Fatalf("another user must not be affected by the cap, got %v", err)
	}
}

func TestSubmitRejectsEmptyInputs(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs, err := c.Start("user-5", impl.LevelStandard)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	blank := impl.Document{Kind: "id-card", Number: "1", Digest: "   "}
	if err := c.SubmitDocument(cs.ID, blank); !errors.Is(err, impl.ErrBadDocument) {
		t.Fatalf("blank document digest must fail with ErrBadDocument, got %v", err)
	}
	if err := c.SubmitDocument("case-404", proofDoc()); !errors.Is(err, impl.ErrUnknownCase) {
		t.Fatalf("document on an unknown case must fail with ErrUnknownCase, got %v", err)
	}
	if err := c.SubmitLiveness(cs.ID, nil); !errors.Is(err, impl.ErrEmptyFrame) {
		t.Fatalf("empty frame must fail with ErrEmptyFrame, got %v", err)
	}
	if err := c.SubmitLiveness(cs.ID, []byte{}); !errors.Is(err, impl.ErrEmptyFrame) {
		t.Fatalf("zero length frame must fail with ErrEmptyFrame, got %v", err)
	}
	state, err := c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseCollected {
		t.Fatalf("state after rejected inputs = %q, want %q", state, impl.CaseCollected)
	}
}

func TestSubmitDocumentLocksFirstDocument(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs, err := c.Start("user-6", impl.LevelHigh)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := c.SubmitDocument(cs.ID, proofDoc()); err != nil {
		t.Fatalf("first document: %v", err)
	}
	other := proofDoc()
	other.Digest = "sha256-0000"
	if err := c.SubmitDocument(cs.ID, other); !errors.Is(err, impl.ErrDocumentLocked) {
		t.Fatalf("second document must fail with ErrDocumentLocked, got %v", err)
	}
	if err := c.SubmitDocument(cs.ID, proofDoc()); err != nil {
		t.Fatalf("resubmitting the same document must be idempotent, got %v", err)
	}
}

func TestCaseMovesToComparingAfterBothInputs(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs, err := c.Start("user-7", impl.LevelStandard)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := c.SubmitDocument(cs.ID, proofDoc()); err != nil {
		t.Fatalf("submit document: %v", err)
	}
	state, err := c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseCollected {
		t.Fatalf("state with document only = %q, want %q", state, impl.CaseCollected)
	}
	if err := c.SubmitLiveness(cs.ID, []byte("frame-0002")); err != nil {
		t.Fatalf("submit liveness: %v", err)
	}
	state, err = c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseComparing {
		t.Fatalf("state with both inputs = %q, want %q", state, impl.CaseComparing)
	}
}

func TestApplyVendorResultRequiresComparing(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs, err := c.Start("user-8", impl.LevelStandard)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	result := impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.99}
	if _, err := c.ApplyVendorResult(cs.ID, result); !errors.Is(err, impl.ErrNotReady) {
		t.Fatalf("vendor result without inputs must fail with ErrNotReady, got %v", err)
	}
	if _, err := c.ApplyVendorResult("case-404", result); !errors.Is(err, impl.ErrUnknownCase) {
		t.Fatalf("vendor result on an unknown case must fail with ErrUnknownCase, got %v", err)
	}
	if err := c.SubmitDocument(cs.ID, proofDoc()); err != nil {
		t.Fatalf("submit document: %v", err)
	}
	if err := c.SubmitLiveness(cs.ID, []byte("frame-0003")); err != nil {
		t.Fatalf("submit liveness: %v", err)
	}
	if _, err := c.ApplyVendorResult(cs.ID, impl.VendorResult{Kind: "mystery", Score: 0.9}); !errors.Is(err, impl.ErrBadVendorResult) {
		t.Fatalf("unknown vendor kind must fail with ErrBadVendorResult, got %v", err)
	}
}

func TestThresholdForLevels(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	want := map[impl.Level]float64{
		impl.LevelBasic:    0.60,
		impl.LevelStandard: 0.75,
		impl.LevelHigh:     0.90,
	}
	for level, value := range want {
		got, err := c.ThresholdFor(level)
		if err != nil {
			t.Fatalf("threshold for level %d: %v", level, err)
		}
		if math.Abs(got-value) > 1e-9 {
			t.Fatalf("threshold for level %d = %v, want %v", level, got, value)
		}
	}
	if _, err := c.ThresholdFor(impl.Level(0)); !errors.Is(err, impl.ErrBadLevel) {
		t.Fatalf("threshold for level 0 must fail with ErrBadLevel, got %v", err)
	}
}

func TestVendorOutcomeByThreshold(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	basic := readyCase(t, c, "user-9", impl.LevelBasic)
	v, err := c.ApplyVendorResult(basic.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.65})
	if err != nil {
		t.Fatalf("apply vendor result on basic: %v", err)
	}
	if v.Outcome != impl.OutcomeApproved {
		t.Fatalf("basic outcome at 0.65 = %q, want %q", v.Outcome, impl.OutcomeApproved)
	}
	if math.Abs(v.Score-0.65) > 1e-9 || math.Abs(v.Threshold-0.60) > 1e-9 {
		t.Fatalf("verdict score=%v threshold=%v, want 0.65/0.60", v.Score, v.Threshold)
	}
	state, err := c.CaseState(basic.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseApproved {
		t.Fatalf("basic case state = %q, want %q", state, impl.CaseApproved)
	}

	high := readyCase(t, c, "user-10", impl.LevelHigh)
	v, err = c.ApplyVendorResult(high.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.65})
	if err != nil {
		t.Fatalf("apply vendor result on high: %v", err)
	}
	if v.Outcome != impl.OutcomeRejected {
		t.Fatalf("high outcome at 0.65 = %q, want %q", v.Outcome, impl.OutcomeRejected)
	}
	state, err = c.CaseState(high.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseRejected {
		t.Fatalf("high case state = %q, want %q", state, impl.CaseRejected)
	}
}

func TestVendorBoundaryAccepted(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs := readyCase(t, c, "user-11", impl.LevelStandard)
	v, err := c.ApplyVendorResult(cs.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.75})
	if err != nil {
		t.Fatalf("apply vendor result at the boundary: %v", err)
	}
	if v.Outcome != impl.OutcomeApproved {
		t.Fatalf("score equal to the threshold must pass, got %q", v.Outcome)
	}
	low := readyCase(t, c, "user-12", impl.LevelStandard)
	v, err = c.ApplyVendorResult(low.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.7499})
	if err != nil {
		t.Fatalf("apply vendor result just below the boundary: %v", err)
	}
	if v.Outcome != impl.OutcomeRejected {
		t.Fatalf("score below the threshold must fail, got %q", v.Outcome)
	}
}

func TestVendorRetryCapEnforced(t *testing.T) {
	c := impl.NewIdentityProofingCase(impl.Limits{MaxRetries: 2, MaxCasesPerUser: 4})
	cs := readyCase(t, c, "user-13", impl.LevelStandard)
	retry := impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorRetry, Score: 0.0}
	for i := 1; i <= 2; i++ {
		v, err := c.ApplyVendorResult(cs.ID, retry)
		if err != nil {
			t.Fatalf("retry %d must be accepted, got %v", i, err)
		}
		if !v.Pending {
			t.Fatalf("retry %d verdict must be pending", i)
		}
		if v.Retries != i {
			t.Fatalf("retry %d verdict retries = %d, want %d", i, v.Retries, i)
		}
	}
	if _, err := c.ApplyVendorResult(cs.ID, retry); !errors.Is(err, impl.ErrRetryExhausted) {
		t.Fatalf("retry past the cap must fail with ErrRetryExhausted, got %v", err)
	}
}

func TestReviewRequiresManualState(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	cs := readyCase(t, c, "user-14", impl.LevelStandard)
	v, err := c.ApplyVendorResult(cs.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorRefer, Score: 0.5})
	if err != nil {
		t.Fatalf("apply refer result: %v", err)
	}
	if v.Outcome != impl.OutcomeManual {
		t.Fatalf("refer outcome = %q, want %q", v.Outcome, impl.OutcomeManual)
	}
	state, err := c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseManual {
		t.Fatalf("state after refer = %q, want %q", state, impl.CaseManual)
	}
	if err := c.Review(cs.ID, "   ", true); !errors.Is(err, impl.ErrBlankReviewer) {
		t.Fatalf("review without a reviewer must fail with ErrBlankReviewer, got %v", err)
	}

	approved := readyCase(t, c, "user-15", impl.LevelBasic)
	if _, err := c.ApplyVendorResult(approved.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.99}); err != nil {
		t.Fatalf("approve case: %v", err)
	}
	if err := c.Review(approved.ID, "reviewer-1", true); !errors.Is(err, impl.ErrNotPending) {
		t.Fatalf("review of an approved case must fail with ErrNotPending, got %v", err)
	}
	if err := c.Review("case-404", "reviewer-1", true); !errors.Is(err, impl.ErrUnknownCase) {
		t.Fatalf("review of an unknown case must fail with ErrUnknownCase, got %v", err)
	}
}

func TestReviewApproveAndReject(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	refer := impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorRefer, Score: 0.5}
	approveCase := readyCase(t, c, "user-16", impl.LevelHigh)
	if _, err := c.ApplyVendorResult(approveCase.ID, refer); err != nil {
		t.Fatalf("refer for approval: %v", err)
	}
	if err := c.Review(approveCase.ID, "reviewer-2", true); err != nil {
		t.Fatalf("review approve: %v", err)
	}
	state, err := c.CaseState(approveCase.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseApproved {
		t.Fatalf("approved review left state %q, want %q", state, impl.CaseApproved)
	}
	st, err := c.Status("user-16")
	if err != nil {
		t.Fatalf("status after approval: %v", err)
	}
	if !st.Approved || st.State != impl.CaseApproved {
		t.Fatalf("projection = %+v, want an approved state", st)
	}

	rejectCase := readyCase(t, c, "user-17", impl.LevelHigh)
	if _, err := c.ApplyVendorResult(rejectCase.ID, refer); err != nil {
		t.Fatalf("refer for rejection: %v", err)
	}
	if err := c.Review(rejectCase.ID, "reviewer-2", false); err != nil {
		t.Fatalf("review reject: %v", err)
	}
	state, err = c.CaseState(rejectCase.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseRejected {
		t.Fatalf("rejected review left state %q, want %q", state, impl.CaseRejected)
	}
	st, err = c.Status("user-17")
	if err != nil {
		t.Fatalf("status after rejection: %v", err)
	}
	if st.Approved {
		t.Fatalf("rejected case must not be projected as approved")
	}
}

func TestStatusAndExpire(t *testing.T) {
	c := impl.NewIdentityProofingCase(proofLimits())
	if _, err := c.Status("user-404"); !errors.Is(err, impl.ErrUnknownUser) {
		t.Fatalf("status of an unknown user must fail with ErrUnknownUser, got %v", err)
	}
	cs := readyCase(t, c, "user-18", impl.LevelStandard)
	if _, err := c.ApplyVendorResult(cs.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorPass, Score: 0.95}); err != nil {
		t.Fatalf("apply pass: %v", err)
	}
	st, err := c.Status("user-18")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.CaseID != cs.ID || st.Level != impl.LevelStandard || st.State != impl.CaseApproved {
		t.Fatalf("projection = %+v, want case %q at approval", st, cs.ID)
	}
	if err := c.ExpireCase(cs.ID); err != nil {
		t.Fatalf("expire an approved case: %v", err)
	}
	state, err := c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state: %v", err)
	}
	if state != impl.CaseExpired {
		t.Fatalf("expired state = %q, want %q", state, impl.CaseExpired)
	}

	rejected := readyCase(t, c, "user-19", impl.LevelBasic)
	if _, err := c.ApplyVendorResult(rejected.ID, impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorFail, Score: 0.1}); err != nil {
		t.Fatalf("apply fail: %v", err)
	}
	if err := c.ExpireCase(rejected.ID); !errors.Is(err, impl.ErrNotApproved) {
		t.Fatalf("expiring a rejected case must fail with ErrNotApproved, got %v", err)
	}
	if err := c.ExpireCase("case-404"); !errors.Is(err, impl.ErrUnknownCase) {
		t.Fatalf("expiring an unknown case must fail with ErrUnknownCase, got %v", err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	limits := impl.Limits{MaxRetries: 3, MaxCasesPerUser: 4}
	c := impl.NewIdentityProofingCase(limits)
	cs := readyCase(t, c, "user-20", impl.LevelStandard)
	retry := impl.VendorResult{Vendor: "gov-check", Kind: impl.VendorRetry, Score: 0}
	for i := 0; i < 2; i++ {
		if _, err := c.ApplyVendorResult(cs.ID, retry); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	if err := c.LoadSnapshot([]byte("}{ broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	state, err := c.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("case state after a rejected snapshot: %v", err)
	}
	if state != impl.CaseComparing {
		t.Fatalf("state after a rejected snapshot = %q, want %q", state, impl.CaseComparing)
	}

	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewIdentityProofingCase(limits)
	if err := restored.LoadSnapshot(data); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	state, err = restored.CaseState(cs.ID)
	if err != nil {
		t.Fatalf("restored case state: %v", err)
	}
	if state != impl.CaseComparing {
		t.Fatalf("restored state = %q, want %q", state, impl.CaseComparing)
	}
	v, err := restored.ApplyVendorResult(cs.ID, retry)
	if err != nil {
		t.Fatalf("retry after restore: %v", err)
	}
	if v.Retries != 3 {
		t.Fatalf("retry counter after restore = %d, want 3", v.Retries)
	}
	if _, err := restored.ApplyVendorResult(cs.ID, retry); !errors.Is(err, impl.ErrRetryExhausted) {
		t.Fatalf("retry past the cap after restore must fail with ErrRetryExhausted, got %v", err)
	}
}
