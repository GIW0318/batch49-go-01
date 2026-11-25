package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-fix-a-01"
)

func fabricConfig() impl.FabricConfig {
	return impl.FabricConfig{
		HomeRegion:              "home",
		Replicas:                []string{"r1", "r2"},
		HomeRPID:                "login.example.com",
		MaxCredentialsPerRegion: 4,
		ReplayTolerance:         3,
		AckWindow:               2,
	}
}

func homeCredential(id string) impl.Credential {
	return impl.Credential{
		ID:             id,
		UserID:         "user-" + id,
		RPID:           "login.example.com",
		AAGUID:         "aaguid-0001",
		SignCount:      7,
		BackupEligible: true,
	}
}

func assertionFor(id string, count uint32) impl.Assertion {
	return impl.Assertion{
		CredentialID: id,
		UserID:       "user-" + id,
		RPID:         "login.example.com",
		Origin:       "https://login.example.com",
		SignCount:    count,
		UserVerified: true,
	}
}

func TestRegisterCreatesActiveCredentialAtHome(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	set, err := f.Register("home", homeCredential("cred-1"))
	if err != nil {
		t.Fatalf("register must succeed, got %v", err)
	}
	if set.CredentialID != "cred-1" {
		t.Fatalf("replica set credential = %q, want cred-1", set.CredentialID)
	}
	if set.Epoch != 1 {
		t.Fatalf("first registration epoch = %d, want 1", set.Epoch)
	}
	if len(set.Regions) != 1 || set.Regions[0] != "home" {
		t.Fatalf("fresh credential must be visible at home only, got %v", set.Regions)
	}
	cred, ok := f.Credential("cred-1")
	if !ok {
		t.Fatalf("registered credential must be retrievable")
	}
	if cred.State != impl.CredentialActive {
		t.Fatalf("state = %q, want %q", cred.State, impl.CredentialActive)
	}
	if cred.SignCount != 7 || !cred.BackupEligible || cred.AAGUID != "aaguid-0001" {
		t.Fatalf("credential attributes were not preserved: %+v", cred)
	}
	status := f.Status("home")
	if status.Region != "home" || status.Records != 1 || status.Epoch != 1 {
		t.Fatalf("home status = %+v, want one record at epoch 1", status)
	}
	if status.Mode != impl.ReplicaActive {
		t.Fatalf("home mode = %q, want %q", status.Mode, impl.ReplicaActive)
	}
	regions := f.Regions()
	if len(regions) != 3 || regions[0] != "home" || regions[1] != "r1" || regions[2] != "r2" {
		t.Fatalf("regions = %v, want home, r1, r2", regions)
	}
}

func TestRegisterRejectsBlankFields(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	blankID := homeCredential("")
	if _, err := f.Register("home", blankID); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank credential id must be rejected, got %v", err)
	}
	blankUser := homeCredential("cred-2")
	blankUser.UserID = "   "
	if _, err := f.Register("home", blankUser); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank user id must be rejected, got %v", err)
	}
	blankRP := homeCredential("cred-3")
	blankRP.RPID = ""
	if _, err := f.Register("home", blankRP); !errors.Is(err, impl.ErrBlankField) {
		t.Fatalf("blank rp id must be rejected, got %v", err)
	}
	if got := f.Status("home").Records; got != 0 {
		t.Fatalf("rejected registrations left %d records, want 0", got)
	}
}

func TestRegisterRejectsForeignHomeRegion(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("r1", homeCredential("cred-1")); !errors.Is(err, impl.ErrNotHomeRegion) {
		t.Fatalf("registration outside the home region must fail, got %v", err)
	}
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("home registration must still work after a rejected foreign write, got %v", err)
	}
	if got := f.Status("r1").Records; got != 0 {
		t.Fatalf("replica r1 received %d records, want 0", got)
	}
}

func TestRegisterRejectsRPIDMismatch(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	cred := homeCredential("cred-1")
	cred.RPID = "login.other.example"
	if _, err := f.Register("home", cred); !errors.Is(err, impl.ErrRPIDMismatch) {
		t.Fatalf("foreign rp id must be rejected, got %v", err)
	}
	if got := f.Status("home").Records; got != 0 {
		t.Fatalf("rejected rp id left %d records, want 0", got)
	}
}

func TestRegisterRejectsDuplicateCredential(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("first registration must succeed, got %v", err)
	}
	if _, err := f.Register("home", homeCredential("cred-1")); !errors.Is(err, impl.ErrDuplicateCredential) {
		t.Fatalf("duplicate credential id must be rejected, got %v", err)
	}
	status := f.Status("home")
	if status.Records != 1 {
		t.Fatalf("duplicate registration changed the record count to %d", status.Records)
	}
	if status.Epoch != 1 {
		t.Fatalf("duplicate registration consumed an epoch, status epoch = %d", status.Epoch)
	}
}

func TestReplicateCopiesRecordsInEpochOrder(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	for _, id := range []string{"cred-1", "cred-2"} {
		if _, err := f.Register("home", homeCredential(id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	applied, err := f.Replicate("home", "r1", 1)
	if err != nil {
		t.Fatalf("replicate one record: %v", err)
	}
	if applied != 1 {
		t.Fatalf("first replicate applied %d records, want 1", applied)
	}
	status := f.Status("r1")
	if status.Epoch != 1 || status.Records != 1 {
		t.Fatalf("r1 after first replicate = %+v, want epoch 1 with 1 record", status)
	}
	applied, err = f.Replicate("home", "r1", 1)
	if err != nil {
		t.Fatalf("replicate second record: %v", err)
	}
	if applied != 1 {
		t.Fatalf("second replicate applied %d records, want 1", applied)
	}
	status = f.Status("r1")
	if status.Epoch != 2 || status.Records != 2 {
		t.Fatalf("r1 after second replicate = %+v, want epoch 2 with 2 records", status)
	}
}

func TestReplicateIsIdempotent(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	for _, id := range []string{"cred-1", "cred-2"} {
		if _, err := f.Register("home", homeCredential(id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if applied, err := f.Replicate("home", "r2", 0); err != nil || applied != 2 {
		t.Fatalf("full replicate = (%d, %v), want (2, nil)", applied, err)
	}
	applied, err := f.Replicate("home", "r2", 0)
	if err != nil {
		t.Fatalf("repeated replicate must not fail, got %v", err)
	}
	if applied != 0 {
		t.Fatalf("repeated replicate applied %d records, want 0", applied)
	}
	if got := f.Status("r2").Records; got != 2 {
		t.Fatalf("repeated replicate left %d records, want 2", got)
	}
}

func TestReplicateRejectsUnknownRegion(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if applied, err := f.Replicate("home", "r9", 0); !errors.Is(err, impl.ErrUnknownRegion) || applied != 0 {
		t.Fatalf("unknown target region = (%d, %v), want (0, ErrUnknownRegion)", applied, err)
	}
	if applied, err := f.Replicate("r1", "r2", 0); !errors.Is(err, impl.ErrNotHomeRegion) || applied != 0 {
		t.Fatalf("replication from a replica = (%d, %v), want (0, ErrNotHomeRegion)", applied, err)
	}
}

func TestReplicateHonoursReplicaCapacity(t *testing.T) {
	cfg := fabricConfig()
	cfg.MaxCredentialsPerRegion = 2
	f := impl.NewPasskeyRegionFabric(cfg)
	for _, id := range []string{"cred-1", "cred-2", "cred-3"} {
		if _, err := f.Register("home", homeCredential(id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	applied, err := f.Replicate("home", "r1", 0)
	if !errors.Is(err, impl.ErrCredentialLimit) {
		t.Fatalf("overflowing replicate must report ErrCredentialLimit, got %v", err)
	}
	if applied != 0 {
		t.Fatalf("overflowing replicate applied %d records, want an atomic refusal", applied)
	}
	status := f.Status("r1")
	if status.Records != 0 || status.Epoch != 0 {
		t.Fatalf("r1 = %+v, want untouched after the refusal", status)
	}
}

func TestAuthenticateRejectsLaggingReplica(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := f.Authenticate("r1", assertionFor("cred-1", 8)); !errors.Is(err, impl.ErrRegionLagging) {
		t.Fatalf("login on an empty replica must report ErrRegionLagging, got %v", err)
	}
	if _, err := f.Authenticate("r9", assertionFor("cred-1", 8)); !errors.Is(err, impl.ErrUnknownRegion) {
		t.Fatalf("login on an unknown region must report ErrUnknownRegion, got %v", err)
	}
	if _, err := f.Authenticate("home", assertionFor("cred-9", 8)); !errors.Is(err, impl.ErrCredentialNotFound) {
		t.Fatalf("login with an unknown credential must report ErrCredentialNotFound, got %v", err)
	}
}

func TestAuthenticateGrantsAfterReplication(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := f.Replicate("home", "r1", 0); err != nil {
		t.Fatalf("replicate: %v", err)
	}
	grant, err := f.Authenticate("r1", assertionFor("cred-1", 8))
	if err != nil {
		t.Fatalf("assertion on the caught up replica must be accepted, got %v", err)
	}
	if grant.CredentialID != "cred-1" || grant.UserID != "user-cred-1" || grant.Region != "r1" {
		t.Fatalf("grant identity = %+v", grant)
	}
	if grant.Epoch != 1 {
		t.Fatalf("grant epoch = %d, want the credential epoch 1", grant.Epoch)
	}
	if grant.Seq <= 0 {
		t.Fatalf("grant sequence = %d, want a positive monotonic number", grant.Seq)
	}
	cred, ok := f.Credential("cred-1")
	if !ok || cred.SignCount != 8 {
		t.Fatalf("accepted assertion must advance the stored counter, got %+v", cred)
	}
	if _, err := f.Authenticate("r1", assertionFor("cred-1", 9)); err != nil {
		t.Fatalf("a further advancing assertion must be accepted, got %v", err)
	}
}

func TestAuthenticateRejectsNonIncreasingCounter(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := f.Authenticate("home", assertionFor("cred-1", 7)); !errors.Is(err, impl.ErrCounterReplay) {
		t.Fatalf("equal counter must report ErrCounterReplay, got %v", err)
	}
	if _, err := f.Authenticate("home", assertionFor("cred-1", 3)); !errors.Is(err, impl.ErrCounterReplay) {
		t.Fatalf("lower counter must report ErrCounterReplay, got %v", err)
	}
	badUser := assertionFor("cred-1", 8)
	badUser.UserID = "user-other"
	if _, err := f.Authenticate("home", badUser); !errors.Is(err, impl.ErrUserMismatch) {
		t.Fatalf("foreign user must report ErrUserMismatch, got %v", err)
	}
}

func TestRevokeIsImmediateAndIdempotentAtHome(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	stamp, err := f.Revoke("cred-1", "user reported a lost device")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if stamp.CredentialID != "cred-1" || stamp.Reason != "user reported a lost device" {
		t.Fatalf("stamp = %+v", stamp)
	}
	if stamp.Epoch != 2 {
		t.Fatalf("revocation epoch = %d, want the next ledger epoch 2", stamp.Epoch)
	}
	if _, err := f.Authenticate("home", assertionFor("cred-1", 8)); !errors.Is(err, impl.ErrCredentialRevoked) {
		t.Fatalf("home login after revoke must report ErrCredentialRevoked, got %v", err)
	}
	again, err := f.Revoke("cred-1", "duplicate ticket")
	if err != nil {
		t.Fatalf("repeated revoke must not fail, got %v", err)
	}
	if again.Epoch != stamp.Epoch || again.Reason != stamp.Reason {
		t.Fatalf("repeated revoke returned %+v, want the original stamp %+v", again, stamp)
	}
	if _, err := f.Revoke("cred-9", "unknown"); !errors.Is(err, impl.ErrCredentialNotFound) {
		t.Fatalf("revoke of an unknown credential must report ErrCredentialNotFound, got %v", err)
	}
}

func TestPropagateTombstoneBlocksReplicaLogin(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	if _, err := f.Register("home", homeCredential("cred-1")); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := f.Replicate("home", "r1", 0); err != nil {
		t.Fatalf("replicate: %v", err)
	}
	stamp, err := f.Revoke("cred-1", "rotation")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	applied, err := f.Propagate("r1", stamp.Epoch)
	if err != nil {
		t.Fatalf("propagate: %v", err)
	}
	if applied != 1 {
		t.Fatalf("first propagate applied %d tombstones, want 1", applied)
	}
	if got := f.Status("r1").Revocations; got != 1 {
		t.Fatalf("r1 revocation count = %d, want 1", got)
	}
	if _, err := f.Authenticate("r1", assertionFor("cred-1", 8)); !errors.Is(err, impl.ErrCredentialRevoked) {
		t.Fatalf("replica login after the tombstone must report ErrCredentialRevoked, got %v", err)
	}
	if applied, err := f.Propagate("r1", stamp.Epoch); err != nil || applied != 0 {
		t.Fatalf("repeated propagate = (%d, %v), want (0, nil)", applied, err)
	}
	if applied, err := f.Propagate("r9", 1); !errors.Is(err, impl.ErrUnknownRegion) || applied != 0 {
		t.Fatalf("propagate to an unknown region = (%d, %v)", applied, err)
	}
}

func TestSnapshotRestoreKeepsLedgerAndRejectsCorrupt(t *testing.T) {
	f := impl.NewPasskeyRegionFabric(fabricConfig())
	for _, id := range []string{"cred-1", "cred-2"} {
		if _, err := f.Register("home", homeCredential(id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if _, err := f.Replicate("home", "r1", 0); err != nil {
		t.Fatalf("replicate: %v", err)
	}
	stamp, err := f.Revoke("cred-2", "compromised")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := f.Propagate("r1", stamp.Epoch); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	data, err := f.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewPasskeyRegionFabric(fabricConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, want := restored.Status("home"), f.Status("home"); got != want {
		t.Fatalf("restored home status = %+v, want %+v", got, want)
	}
	if got, want := restored.Status("r1"), f.Status("r1"); got != want {
		t.Fatalf("restored r1 status = %+v, want %+v", got, want)
	}
	cred, ok := restored.Credential("cred-2")
	if !ok || cred.State != impl.CredentialRevoked {
		t.Fatalf("restored credential = %+v (found=%v), want revoked", cred, ok)
	}
	if _, err := restored.Authenticate("r1", assertionFor("cred-2", 8)); !errors.Is(err, impl.ErrCredentialRevoked) {
		t.Fatalf("restored replica must keep rejecting the revoked credential, got %v", err)
	}
	before := restored.Status("home")
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if after := restored.Status("home"); after != before {
		t.Fatalf("failed restore mutated the state: %+v -> %+v", before, after)
	}
}
