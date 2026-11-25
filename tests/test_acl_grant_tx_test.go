package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-safety-d-01"
)

func d01BaseEntries() []impl.Entry {
	return []impl.Entry{
		{Kind: impl.KindOwner, ID: 1000, Bits: impl.PermRead | impl.PermWrite | impl.PermExecute | impl.PermChangeACL},
		{Kind: impl.KindUser, ID: 2001, Bits: impl.PermRead | impl.PermChangeACL},
		{Kind: impl.KindGroup, ID: 200, Bits: impl.PermRead},
		{Kind: impl.KindOther, ID: 0, Bits: impl.PermRead},
	}
}

func d01NewPath(t *testing.T, x *impl.AclGrantTx, path string) {
	t.Helper()
	if err := x.AddPath(path, 1000, d01BaseEntries()); err != nil {
		t.Fatalf("AddPath(%q) must succeed, got %v", path, err)
	}
}

func d01Operator() impl.Principal {
	return impl.Principal{UID: 2001, GID: 200, Groups: []uint32{200}}
}

func TestAddPathStoresCanonicalEntries(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	err := x.AddPath("/srv/app", 1000, []impl.Entry{
		{Kind: impl.KindOther, ID: 0, Bits: impl.PermNone},
		{Kind: impl.KindGroup, ID: 200, Bits: impl.PermRead},
		{Kind: impl.KindUser, ID: 2001, Bits: impl.PermRead | impl.PermChangeACL},
		{Kind: impl.KindOwner, ID: 1000, Bits: impl.PermRead | impl.PermWrite | impl.PermChangeACL},
	})
	if err != nil {
		t.Fatalf("AddPath must succeed, got %v", err)
	}
	got, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("entry count = %d, want 5 (owner, mask, user, group, other)", len(got))
	}
	wantKinds := []impl.EntryKind{impl.KindOwner, impl.KindMask, impl.KindUser, impl.KindGroup, impl.KindOther}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("entry %d kind = %q, want %q (order: owner, mask, user asc, group asc, other)", i, got[i].Kind, want)
		}
	}
	if got[0].ID != 1000 {
		t.Fatalf("owner entry id = %d, want 1000", got[0].ID)
	}
	wantMask := impl.PermRead | impl.PermChangeACL
	if got[1].Bits != wantMask {
		t.Fatalf("mask bits = %d, want %d (union of user and group entries)", got[1].Bits, wantMask)
	}
	if got[2].ID != 2001 || got[3].ID != 200 {
		t.Fatalf("named entries must be sorted by id, got user=%d group=%d", got[2].ID, got[3].ID)
	}
}

func TestApplyCommitsBatchAndBumpsRevision(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	d01NewPath(t, x, "/srv/app")
	res, err := x.Apply(impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1001",
		Adds:   []impl.Entry{{Kind: impl.KindUser, ID: 2002, Bits: impl.PermRead}},
	})
	if err != nil {
		t.Fatalf("Apply must succeed, got %v", err)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Fatalf("apply result added=%d removed=%d, want 1/0", res.Added, res.Removed)
	}
	if res.Rev != 1 {
		t.Fatalf("revision after first apply = %d, want 1", res.Rev)
	}
	if res.Seq != 1 {
		t.Fatalf("audit sequence = %d, want 1", res.Seq)
	}
	if _, err := x.Revision("/srv/app"); err != nil {
		t.Fatalf("Revision must succeed, got %v", err)
	}
	if rev, _ := x.Revision("/srv/app"); rev != 1 {
		t.Fatalf("stored revision = %d, want 1", rev)
	}
	entries, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("entry count = %d, want 6 after adding one named user", len(entries))
	}
	audit := x.Audit()
	if len(audit) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(audit))
	}
	if audit[0].Ticket != "OPS-1001" || audit[0].Seq != 1 {
		t.Fatalf("audit line = %+v, want ticket OPS-1001 and seq 1", audit[0])
	}
}

func TestPreviewDoesNotMutatePath(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	d01NewPath(t, x, "/srv/app")
	diff, err := x.Preview(impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1002",
		Adds: []impl.Entry{
			{Kind: impl.KindUser, ID: 2002, Bits: impl.PermRead},
			{Kind: impl.KindGroup, ID: 300, Bits: impl.PermRead | impl.PermWrite},
		},
	})
	if err != nil {
		t.Fatalf("Preview must succeed, got %v", err)
	}
	if len(diff.Added) != 2 {
		t.Fatalf("preview added = %d, want 2", len(diff.Added))
	}
	if len(diff.After) != 7 || len(diff.Before) != 5 {
		t.Fatalf("preview before/after = %d/%d, want 5/7", len(diff.Before), len(diff.After))
	}
	if diff.BeforeRev != 0 || diff.AfterRev != 1 {
		t.Fatalf("preview revisions = %d/%d, want 0/1", diff.BeforeRev, diff.AfterRev)
	}
	wantMask := impl.PermRead | impl.PermChangeACL | impl.PermWrite
	if diff.AfterMask != wantMask {
		t.Fatalf("preview after mask = %d, want %d", diff.AfterMask, wantMask)
	}
	entries, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("preview must not persist entries, count = %d, want 5", len(entries))
	}
	if rev, _ := x.Revision("/srv/app"); rev != 0 {
		t.Fatalf("preview must not bump revision, got %d", rev)
	}
}

func TestApplyRejectsActorWithoutChangeACL(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	d01NewPath(t, x, "/srv/app")
	_, err := x.Apply(impl.GrantRequest{
		Actor:  impl.Principal{UID: 2002, GID: 999},
		Path:   "/srv/app",
		Ticket: "OPS-1003",
		Adds:   []impl.Entry{{Kind: impl.KindUser, ID: 2003, Bits: impl.PermRead}},
	})
	if !errors.Is(err, impl.ErrNotAllowed) {
		t.Fatalf("actor without change ACL must get ErrNotAllowed, got %v", err)
	}
	entries, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("rejected request must not change entries, count = %d", len(entries))
	}
	if rev, _ := x.Revision("/srv/app"); rev != 0 {
		t.Fatalf("rejected request must not bump revision, got %d", rev)
	}
}

func TestApplyRejectsGrantBeyondActorPermissions(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	entries := []impl.Entry{
		{Kind: impl.KindOwner, ID: 1000, Bits: impl.PermAll},
		{Kind: impl.KindUser, ID: 2003, Bits: impl.PermRead | impl.PermWrite | impl.PermChangeACL},
		{Kind: impl.KindOther, ID: 0, Bits: impl.PermNone},
	}
	if err := x.AddPath("/srv/app", 1000, entries); err != nil {
		t.Fatalf("AddPath must succeed, got %v", err)
	}
	_, err := x.Apply(impl.GrantRequest{
		Actor:  impl.Principal{UID: 2003, GID: 999},
		Path:   "/srv/app",
		Ticket: "OPS-1004",
		Adds:   []impl.Entry{{Kind: impl.KindUser, ID: 3001, Bits: impl.PermRead | impl.PermWrite | impl.PermExecute}},
	})
	if !errors.Is(err, impl.ErrGrantTooBroad) {
		t.Fatalf("granting execute without holding it must get ErrGrantTooBroad, got %v", err)
	}
	if _, err := x.Entries("/srv/app"); err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if rev, _ := x.Revision("/srv/app"); rev != 0 {
		t.Fatalf("rejected request must not bump revision, got %d", rev)
	}
}

func TestMaskRecomputedFromStoredEntryBits(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	entries := []impl.Entry{
		{Kind: impl.KindOwner, ID: 1000, Bits: impl.PermAll},
		{Kind: impl.KindUser, ID: 2001, Bits: impl.PermRead | impl.PermWrite | impl.PermChangeACL},
		{Kind: impl.KindGroup, ID: 200, Bits: impl.PermRead},
		{Kind: impl.KindOther, ID: 0, Bits: impl.PermNone},
	}
	if err := x.AddPath("/srv/app", 1000, entries); err != nil {
		t.Fatalf("AddPath must succeed, got %v", err)
	}
	if _, err := x.Apply(impl.GrantRequest{
		Actor:  impl.Principal{UID: 2001, GID: 200},
		Path:   "/srv/app",
		Ticket: "OPS-1005",
		Adds:   []impl.Entry{{Kind: impl.KindGroup, ID: 300, Bits: impl.PermRead | impl.PermWrite}},
	}); err != nil {
		t.Fatalf("Apply must succeed, got %v", err)
	}
	got, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	var mask impl.Perm
	found := false
	for _, e := range got {
		if e.Kind == impl.KindMask {
			mask = e.Bits
			found = true
		}
	}
	if !found {
		t.Fatalf("mask entry missing after apply")
	}
	want := impl.PermRead | impl.PermWrite | impl.PermChangeACL
	if mask != want {
		t.Fatalf("mask = %d, want %d after adding a group with read and write", mask, want)
	}
	if perm, err := x.Effective("/srv/app", 4000, 999, []uint32{300}); err != nil {
		t.Fatalf("Effective must succeed, got %v", err)
	} else if perm != impl.PermRead|impl.PermWrite {
		t.Fatalf("group 300 effective = %d, want read and write", perm)
	}
}

func TestEffectiveResolvesOwnerNamedAndOther(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	d01NewPath(t, x, "/srv/app")
	owner, err := x.Effective("/srv/app", 1000, 999, nil)
	if err != nil {
		t.Fatalf("Effective for owner must succeed, got %v", err)
	}
	if owner != impl.PermRead|impl.PermWrite|impl.PermExecute|impl.PermChangeACL {
		t.Fatalf("owner effective = %d, want its own bits unmasked", owner)
	}
	named, err := x.Effective("/srv/app", 2001, 999, nil)
	if err != nil {
		t.Fatalf("Effective for named user must succeed, got %v", err)
	}
	if named != impl.PermRead|impl.PermChangeACL {
		t.Fatalf("named user effective = %d, want read and change ACL", named)
	}
	viaGroup, err := x.Effective("/srv/app", 5000, 999, []uint32{200})
	if err != nil {
		t.Fatalf("Effective via group must succeed, got %v", err)
	}
	if viaGroup != impl.PermRead {
		t.Fatalf("group member effective = %d, want read", viaGroup)
	}
	other, err := x.Effective("/srv/app", 6000, 999, nil)
	if err != nil {
		t.Fatalf("Effective for stranger must succeed, got %v", err)
	}
	if other != impl.PermRead {
		t.Fatalf("stranger effective = %d, want other bits read", other)
	}
}

func TestRevokeRemovesPrincipalEntries(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	entries := []impl.Entry{
		{Kind: impl.KindOwner, ID: 1000, Bits: impl.PermAll},
		{Kind: impl.KindUser, ID: 2011, Bits: impl.PermRead},
		{Kind: impl.KindGroup, ID: 210, Bits: impl.PermRead},
		{Kind: impl.KindGroup, ID: 211, Bits: impl.PermWrite},
		{Kind: impl.KindGroup, ID: 212, Bits: impl.PermRead | impl.PermWrite},
		{Kind: impl.KindOther, ID: 0, Bits: impl.PermNone},
	}
	if err := x.AddPath("/srv/data", 1000, entries); err != nil {
		t.Fatalf("AddPath must succeed, got %v", err)
	}
	n, err := x.Revoke("/srv/data", impl.Principal{UID: 2011, GID: 210, Groups: []uint32{211}})
	if err != nil {
		t.Fatalf("Revoke must succeed, got %v", err)
	}
	if n != 3 {
		t.Fatalf("revoked entries = %d, want 3 (user 2011, group 210, group 211)", n)
	}
	got, err := x.Entries("/srv/data")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("entry count after revoke = %d, want 4 (owner, mask, group 212, other)", len(got))
	}
	var mask impl.Perm
	for _, e := range got {
		if e.Kind == impl.KindMask {
			mask = e.Bits
		}
	}
	if mask != impl.PermRead|impl.PermWrite {
		t.Fatalf("mask after revoke = %d, want read and write from group 212", mask)
	}
	if rev, _ := x.Revision("/srv/data"); rev != 1 {
		t.Fatalf("revoke must bump revision to 1, got %d", rev)
	}
}

func TestUnknownPathIsRejected(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	if _, err := x.Preview(impl.GrantRequest{Actor: d01Operator(), Path: "/missing", Ticket: "T"}); !errors.Is(err, impl.ErrUnknownPath) {
		t.Fatalf("Preview on unknown path must get ErrUnknownPath, got %v", err)
	}
	if _, err := x.Apply(impl.GrantRequest{Actor: d01Operator(), Path: "/missing", Ticket: "T", Adds: []impl.Entry{{Kind: impl.KindUser, ID: 2, Bits: impl.PermRead}}}); !errors.Is(err, impl.ErrUnknownPath) {
		t.Fatalf("Apply on unknown path must get ErrUnknownPath, got %v", err)
	}
	if _, err := x.Effective("/missing", 1, 1, nil); !errors.Is(err, impl.ErrUnknownPath) {
		t.Fatalf("Effective on unknown path must get ErrUnknownPath, got %v", err)
	}
	if _, err := x.Revoke("/missing", d01Operator()); !errors.Is(err, impl.ErrUnknownPath) {
		t.Fatalf("Revoke on unknown path must get ErrUnknownPath, got %v", err)
	}
}

func TestEntryLimitRollsBackWholeBatch(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 6})
	d01NewPath(t, x, "/srv/app")
	if _, err := x.Apply(impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1006",
		Adds: []impl.Entry{
			{Kind: impl.KindUser, ID: 2002, Bits: impl.PermRead},
			{Kind: impl.KindUser, ID: 2003, Bits: impl.PermRead},
			{Kind: impl.KindGroup, ID: 300, Bits: impl.PermRead},
		},
	}); !errors.Is(err, impl.ErrEntryLimit) {
		t.Fatalf("batch crossing the entry limit must get ErrEntryLimit, got %v", err)
	}
	entries, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("entry count = %d, want 5 after rollback", len(entries))
	}
	if rev, _ := x.Revision("/srv/app"); rev != 0 {
		t.Fatalf("failed apply must leave revision at 0, got %d", rev)
	}
	if audit := x.Audit(); len(audit) != 0 {
		t.Fatalf("failed apply must leave no audit line, got %d", len(audit))
	}
}

func TestConcurrentSameTicketAppliesOnce(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 16})
	d01NewPath(t, x, "/srv/app")
	req := impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1007",
		Adds:   []impl.Entry{{Kind: impl.KindUser, ID: 2002, Bits: impl.PermRead}},
	}
	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	var mu sync.Mutex
	success, rejected := 0, 0
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, err := x.Apply(req)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, impl.ErrTicketReused) || errors.Is(err, impl.ErrConflict):
				rejected++
			}
		}()
	}
	close(start)
	wg.Wait()
	if success != 1 {
		t.Fatalf("successful applies = %d, want exactly 1 for one ticket", success)
	}
	if rejected != workers-1 {
		t.Fatalf("rejected applies = %d, want %d", rejected, workers-1)
	}
	entries, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("entry count = %d, want 6 after exactly one apply", len(entries))
	}
}

func TestTicketReuseIsRejected(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	d01NewPath(t, x, "/srv/app")
	req := impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1008",
		Adds:   []impl.Entry{{Kind: impl.KindUser, ID: 2002, Bits: impl.PermRead}},
	}
	if _, err := x.Apply(req); err != nil {
		t.Fatalf("first apply must succeed, got %v", err)
	}
	if _, err := x.Apply(req); !errors.Is(err, impl.ErrTicketReused) {
		t.Fatalf("second apply with the same ticket must get ErrTicketReused, got %v", err)
	}
	if rev, _ := x.Revision("/srv/app"); rev != 1 {
		t.Fatalf("rejected replay must leave revision at 1, got %d", rev)
	}
}

func TestRemovalsRunBeforeAdds(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	entries := []impl.Entry{
		{Kind: impl.KindOwner, ID: 1000, Bits: impl.PermAll},
		{Kind: impl.KindUser, ID: 2001, Bits: impl.PermRead | impl.PermWrite | impl.PermChangeACL},
		{Kind: impl.KindUser, ID: 2011, Bits: impl.PermRead},
		{Kind: impl.KindOther, ID: 0, Bits: impl.PermNone},
	}
	if err := x.AddPath("/srv/app", 1000, entries); err != nil {
		t.Fatalf("AddPath must succeed, got %v", err)
	}
	res, err := x.Apply(impl.GrantRequest{
		Actor:   impl.Principal{UID: 2001, GID: 200},
		Path:    "/srv/app",
		Ticket:  "OPS-1009",
		Removes: []impl.Entry{{Kind: impl.KindUser, ID: 2011, Bits: impl.PermRead}},
		Adds:    []impl.Entry{{Kind: impl.KindUser, ID: 2011, Bits: impl.PermRead | impl.PermWrite}},
	})
	if err != nil {
		t.Fatalf("remove then add of the same entry must succeed, got %v", err)
	}
	if res.Removed != 1 || res.Added != 1 {
		t.Fatalf("apply result removed/added = %d/%d, want 1/1", res.Removed, res.Added)
	}
	got, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	var bits impl.Perm
	found := false
	for _, e := range got {
		if e.Kind == impl.KindUser && e.ID == 2011 {
			bits = e.Bits
			found = true
		}
	}
	if !found {
		t.Fatalf("user 2011 must still be present after remove-then-add")
	}
	if bits != impl.PermRead|impl.PermWrite {
		t.Fatalf("user 2011 bits = %d, want read and write from the add side", bits)
	}
}

func TestSnapshotRestorePreservesState(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	d01NewPath(t, x, "/srv/app")
	if _, err := x.Apply(impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1010",
		Adds:   []impl.Entry{{Kind: impl.KindGroup, ID: 300, Bits: impl.PermRead | impl.PermWrite}},
	}); err != nil {
		t.Fatalf("Apply must succeed, got %v", err)
	}
	before, err := x.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries must succeed, got %v", err)
	}
	blob, err := x.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	if len(blob) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	y := impl.NewAclGrantTx(impl.Limits{MaxPaths: 4, MaxEntriesPerPath: 12})
	if err := y.Restore(blob); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	after, err := y.Entries("/srv/app")
	if err != nil {
		t.Fatalf("Entries after restore must succeed, got %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("entry count after restore = %d, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("entry %d after restore = %+v, want %+v", i, after[i], before[i])
		}
	}
	if rev, _ := y.Revision("/srv/app"); rev != 1 {
		t.Fatalf("revision after restore = %d, want 1", rev)
	}
	if _, err := y.Apply(impl.GrantRequest{
		Actor:  d01Operator(),
		Path:   "/srv/app",
		Ticket: "OPS-1010",
		Adds:   []impl.Entry{{Kind: impl.KindUser, ID: 2002, Bits: impl.PermRead}},
	}); !errors.Is(err, impl.ErrTicketReused) {
		t.Fatalf("restored ticket must stay spent, got %v", err)
	}
}

func TestPathLimitIsEnforced(t *testing.T) {
	x := impl.NewAclGrantTx(impl.Limits{MaxPaths: 2, MaxEntriesPerPath: 12})
	for _, path := range []string{"/srv/a", "/srv/b"} {
		if err := x.AddPath(path, 1000, d01BaseEntries()); err != nil {
			t.Fatalf("AddPath(%q) must succeed, got %v", path, err)
		}
	}
	if err := x.AddPath("/srv/c", 1000, d01BaseEntries()); !errors.Is(err, impl.ErrPathLimit) {
		t.Fatalf("third path must get ErrPathLimit, got %v", err)
	}
	paths := x.Paths()
	if len(paths) != 2 {
		t.Fatalf("managed paths = %d, want 2", len(paths))
	}
	if paths[0] != "/srv/a" || paths[1] != "/srv/b" {
		t.Fatalf("paths = %v, want ascending /srv/a, /srv/b", paths)
	}
}
