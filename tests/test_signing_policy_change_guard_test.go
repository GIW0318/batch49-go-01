package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-e-14"
)

func basePolicy() impl.Policy {
	return impl.Policy{Version: 1, RequireSigned: true, AllowedAlgs: []string{"ed25519"}, MinKeyBits: 256}
}

func relaxPolicy() impl.Policy {
	return impl.Policy{Version: 2, RequireSigned: true, AllowedAlgs: []string{"ed25519", "rsa-sha256"}, MinKeyBits: 256}
}

func restrictPolicy() impl.Policy {
	return impl.Policy{Version: 2, RequireSigned: true, AllowedAlgs: []string{"rsa-sha256"}, MinKeyBits: 256}
}

func testArtifacts() []impl.Artifact {
	return []impl.Artifact{
		{ID: "art-rsa", Namespace: "prod", Signed: true, Alg: "rsa-sha256", KeyBits: 4096},
		{ID: "art-unsigned", Namespace: "prod", Signed: false},
		{ID: "art-ed", Namespace: "staging", Signed: true, Alg: "ed25519", KeyBits: 256},
	}
}

func newGuard() *impl.SigningPolicyChangeGuard {
	return impl.NewSigningPolicyChangeGuard(basePolicy(), testArtifacts())
}

func validToken(change string) impl.Approval {
	return impl.Approval{ChangeID: change, Approver: "alice", ExpiresAt: time.Now().Add(time.Hour)}
}

func stageAndApprove(t *testing.T, g *impl.SigningPolicyChangeGuard, next impl.Policy) string {
	t.Helper()
	impact, err := g.Stage(next)
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	if err := g.Approve(impact.ChangeID, validToken(impact.ChangeID)); err != nil {
		t.Fatalf("approve must succeed, got %v", err)
	}
	return impact.ChangeID
}

func TestStageRejectsNonIncrementalVersion(t *testing.T) {
	g := newGuard()
	if _, err := g.Stage(impl.Policy{Version: 5, AllowedAlgs: []string{"ed25519"}}); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("version jump error = %v, want ErrBadVersion", err)
	}
	if _, err := g.Stage(basePolicy()); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("same version error = %v, want ErrBadVersion", err)
	}
}

func TestStageReportsRelaxationArtifacts(t *testing.T) {
	g := newGuard()
	impact, err := g.Stage(relaxPolicy())
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	if impact.ChangeID != "policy-v2" {
		t.Fatalf("change id = %q, want policy-v2", impact.ChangeID)
	}
	if impact.FromVersion != 1 || impact.ToVersion != 2 {
		t.Fatalf("impact versions = %d -> %d, want 1 -> 2", impact.FromVersion, impact.ToVersion)
	}
	if len(impact.Relaxations) != 1 || impact.Relaxations[0] != "art-rsa" {
		t.Fatalf("relaxations = %v, want [art-rsa]", impact.Relaxations)
	}
	if len(impact.Restrictions) != 0 {
		t.Fatalf("restrictions = %v, want none", impact.Restrictions)
	}
	if !impact.IsRelaxation() {
		t.Fatalf("impact lost a denied to allowed transition")
	}
}

func TestStageReportsRestrictionArtifacts(t *testing.T) {
	g := newGuard()
	impact, err := g.Stage(restrictPolicy())
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	if len(impact.Restrictions) != 1 || impact.Restrictions[0] != "art-ed" {
		t.Fatalf("restrictions = %v, want [art-ed]", impact.Restrictions)
	}
	if len(impact.Relaxations) != 1 || impact.Relaxations[0] != "art-rsa" {
		t.Fatalf("relaxations = %v, want [art-rsa]", impact.Relaxations)
	}
}

func TestActivateRelaxationRequiresApproval(t *testing.T) {
	g := newGuard()
	impact, err := g.Stage(relaxPolicy())
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	if err := g.Activate(impact.ChangeID, "prod"); !errors.Is(err, impl.ErrNotApproved) {
		t.Fatalf("unapproved activation error = %v, want ErrNotApproved", err)
	}
	effective, err := g.Effective("prod")
	if err != nil {
		t.Fatalf("effective must succeed, got %v", err)
	}
	if effective.Version != 1 {
		t.Fatalf("effective version = %d, want the base version 1", effective.Version)
	}
}

func TestApproveRejectsTokenForOtherChange(t *testing.T) {
	g := newGuard()
	impact, err := g.Stage(relaxPolicy())
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	if err := g.Approve("policy-v9", validToken("policy-v9")); !errors.Is(err, impl.ErrUnknownChange) {
		t.Fatalf("unknown change approval error = %v, want ErrUnknownChange", err)
	}
	if err := g.Approve(impact.ChangeID, validToken("policy-v3")); !errors.Is(err, impl.ErrBadToken) {
		t.Fatalf("mismatched token error = %v, want ErrBadToken", err)
	}
}

func TestApproveRejectsExpiredToken(t *testing.T) {
	g := newGuard()
	impact, err := g.Stage(relaxPolicy())
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	expired := impl.Approval{ChangeID: impact.ChangeID, Approver: "alice", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := g.Approve(impact.ChangeID, expired); !errors.Is(err, impl.ErrTokenExpired) {
		t.Fatalf("expired token error = %v, want ErrTokenExpired", err)
	}
}

func TestActivateUnknownChangeFails(t *testing.T) {
	g := newGuard()
	if err := g.Activate("policy-v9", "prod"); !errors.Is(err, impl.ErrUnknownChange) {
		t.Fatalf("unknown change activation error = %v, want ErrUnknownChange", err)
	}
}

func TestEffectiveFallsBackToBasePolicy(t *testing.T) {
	g := newGuard()
	effective, err := g.Effective("prod")
	if err != nil {
		t.Fatalf("effective must succeed, got %v", err)
	}
	if effective.Version != 1 {
		t.Fatalf("effective version = %d, want 1", effective.Version)
	}
	if !effective.RequireSigned || effective.MinKeyBits != 256 {
		t.Fatalf("effective policy = %+v, want the base policy", effective)
	}
	if len(effective.AllowedAlgs) != 1 || effective.AllowedAlgs[0] != "ed25519" {
		t.Fatalf("effective algs = %v, want [ed25519]", effective.AllowedAlgs)
	}
}

func TestEffectiveReturnsActivatedPolicy(t *testing.T) {
	g := newGuard()
	change := stageAndApprove(t, g, relaxPolicy())
	if err := g.Activate(change, "prod"); err != nil {
		t.Fatalf("activation must succeed, got %v", err)
	}
	effective, err := g.Effective("prod")
	if err != nil {
		t.Fatalf("effective must succeed, got %v", err)
	}
	if effective.Version != 2 {
		t.Fatalf("effective version = %d, want 2", effective.Version)
	}
	hasRSA := false
	for _, alg := range effective.AllowedAlgs {
		if alg == "rsa-sha256" {
			hasRSA = true
		}
	}
	if !hasRSA {
		t.Fatalf("effective algs = %v, want rsa-sha256 to be allowed", effective.AllowedAlgs)
	}
	untouched, err := g.Effective("staging")
	if err != nil {
		t.Fatalf("effective must succeed for staging, got %v", err)
	}
	if untouched.Version != 1 {
		t.Fatalf("staging version = %d, want it still pinned to the base version", untouched.Version)
	}
}

func TestRollbackRestoresBasePolicy(t *testing.T) {
	g := newGuard()
	change := stageAndApprove(t, g, relaxPolicy())
	if err := g.Activate(change, "prod"); err != nil {
		t.Fatalf("activation must succeed, got %v", err)
	}
	if err := g.Rollback(change); err != nil {
		t.Fatalf("rollback must succeed, got %v", err)
	}
	effective, err := g.Effective("prod")
	if err != nil {
		t.Fatalf("effective must succeed, got %v", err)
	}
	if effective.Version != 1 {
		t.Fatalf("effective version after rollback = %d, want 1", effective.Version)
	}
	if got := len(g.ActiveScopes(change)); got != 0 {
		t.Fatalf("active scopes after rollback = %d, want 0", got)
	}
}

func TestRollbackUnknownChangeFails(t *testing.T) {
	g := newGuard()
	if err := g.Rollback("policy-v9"); !errors.Is(err, impl.ErrUnknownChange) {
		t.Fatalf("unknown rollback error = %v, want ErrUnknownChange", err)
	}
}

func TestImpactOfReturnsStagedImpact(t *testing.T) {
	g := newGuard()
	impact, err := g.Stage(relaxPolicy())
	if err != nil {
		t.Fatalf("stage must succeed, got %v", err)
	}
	stored, err := g.ImpactOf(impact.ChangeID)
	if err != nil {
		t.Fatalf("impact of must succeed, got %v", err)
	}
	if len(stored.Relaxations) != 1 || stored.Relaxations[0] != "art-rsa" {
		t.Fatalf("stored relaxations = %v, want [art-rsa]", stored.Relaxations)
	}
	if stored.ToVersion != 2 || stored.FromVersion != 1 {
		t.Fatalf("stored versions = %d -> %d, want 1 -> 2", stored.FromVersion, stored.ToVersion)
	}
	if _, err := g.ImpactOf("policy-v9"); !errors.Is(err, impl.ErrUnknownChange) {
		t.Fatalf("unknown impact error = %v, want ErrUnknownChange", err)
	}
}

func TestEvaluateFollowsEffectivePolicy(t *testing.T) {
	g := newGuard()
	change := stageAndApprove(t, g, relaxPolicy())
	if err := g.Activate(change, "prod"); err != nil {
		t.Fatalf("activation must succeed, got %v", err)
	}
	if got, err := g.Evaluate("art-rsa"); err != nil || got != impl.DecisionAllow {
		t.Fatalf("rsa artifact decision = %q, %v, want allow", got, err)
	}
	if got, err := g.Evaluate("art-unsigned"); err != nil || got != impl.DecisionDeny {
		t.Fatalf("unsigned artifact decision = %q, %v, want deny", got, err)
	}
	if got, err := g.Evaluate("art-ed"); err != nil || got != impl.DecisionAllow {
		t.Fatalf("ed25519 artifact decision = %q, %v, want allow", got, err)
	}
}

func TestActivateUnknownScopeFails(t *testing.T) {
	g := newGuard()
	change := stageAndApprove(t, g, relaxPolicy())
	if err := g.Activate(change, "ghost"); !errors.Is(err, impl.ErrUnknownScope) {
		t.Fatalf("unknown scope error = %v, want ErrUnknownScope", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newGuard()
	if err := g.Restore([]byte("!!not-a-snapshot!!")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
	effective, err := g.Effective("prod")
	if err != nil {
		t.Fatalf("effective after corrupt restore must succeed, got %v", err)
	}
	if effective.Version != 1 {
		t.Fatalf("effective version after corrupt restore = %d, want the base version", effective.Version)
	}
}
