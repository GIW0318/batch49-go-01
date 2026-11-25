package task

import (
	"errors"
	"testing"

	impl "gsb/batch49-go-game-a-03"
)

func newReserver(capacity int, spillTTL int64, maxPending int) *impl.SlotReserver {
	return impl.NewSlotReserver(impl.Options{Capacity: capacity, SpillTTL: spillTTL, MaxReservations: maxPending})
}

func mustReserve(t *testing.T, reserver *impl.SlotReserver, owner, token string, slots int, ttl int64) impl.Reservation {
	t.Helper()
	reservation, err := reserver.Reserve(owner, token, slots, ttl)
	if err != nil {
		t.Fatalf("reserve %s/%s: %v", owner, token, err)
	}
	return reservation
}

func testReward(id string, slots int) impl.Reward {
	return impl.Reward{RewardID: id, Slots: slots, Items: []string{"item-" + id}, Amount: 1}
}

func TestReserveCreatesPendingReservation(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	reservation := mustReserve(t, reserver, "hero", "token-a", 2, 300)
	if reservation.State != impl.ReservationPending {
		t.Fatalf("state = %q, want pending", reservation.State)
	}
	if reservation.Slots != 2 || reservation.ExpireAt != 300 {
		t.Fatalf("reservation terms mismatch: %+v", reservation)
	}
	if reservation.Seq != 1 {
		t.Fatalf("first reservation seq = %d, want 1", reservation.Seq)
	}
	if got := reserver.Available("hero"); got != 4 {
		t.Fatalf("available = %d, want 4", got)
	}
	if got := reserver.Used("hero"); got != 0 {
		t.Fatalf("used = %d, want 0 before commit", got)
	}
	stored, ok := reserver.Reservation("hero", "token-a")
	if !ok || stored.State != impl.ReservationPending || stored.Slots != 2 {
		t.Fatalf("stored reservation = %+v ok=%t", stored, ok)
	}
}

func TestReserveRejectsBadInput(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	if _, err := reserver.Reserve(" ", "token-a", 1, 10); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank owner must return ErrBlankArg, got %v", err)
	}
	if _, err := reserver.Reserve("hero", "", 1, 10); !errors.Is(err, impl.ErrBlankArg) {
		t.Fatalf("blank token must return ErrBlankArg, got %v", err)
	}
	if _, err := reserver.Reserve("hero", "token-a", 0, 10); !errors.Is(err, impl.ErrBadSlots) {
		t.Fatalf("zero slots must return ErrBadSlots, got %v", err)
	}
	if _, err := reserver.Reserve("hero", "token-a", 1, 0); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("zero ttl must return ErrBadTTL, got %v", err)
	}
	if got := reserver.Available("hero"); got != 6 {
		t.Fatalf("rejected reserves must not consume slots, available = %d", got)
	}
}

func TestReserveEnforcesCapacity(t *testing.T) {
	reserver := newReserver(5, 50, 4)
	mustReserve(t, reserver, "hero", "t1", 3, 100)
	if _, err := reserver.Reserve("hero", "t2", 3, 100); !errors.Is(err, impl.ErrCapacityExceeded) {
		t.Fatalf("over capacity must return ErrCapacityExceeded, got %v", err)
	}
	if got := reserver.Available("hero"); got != 2 {
		t.Fatalf("available = %d, want 2", got)
	}
	mustReserve(t, reserver, "hero", "t2", 2, 100)
	if got := reserver.Available("hero"); got != 0 {
		t.Fatalf("available = %d, want 0", got)
	}
	if _, err := reserver.Reserve("hero", "t3", 1, 100); !errors.Is(err, impl.ErrCapacityExceeded) {
		t.Fatalf("full capacity must return ErrCapacityExceeded, got %v", err)
	}
}

func TestReserveTokenIsIdempotent(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	first := mustReserve(t, reserver, "hero", "token-a", 2, 300)
	again := mustReserve(t, reserver, "hero", "token-a", 2, 300)
	if again.Seq != first.Seq || again.Slots != first.Slots || again.ExpireAt != first.ExpireAt {
		t.Fatalf("repeat with the same terms must return the same reservation, got %+v then %+v", first, again)
	}
	if got := reserver.Available("hero"); got != 4 {
		t.Fatalf("idempotent reserve consumed slots twice, available = %d", got)
	}
	if _, err := reserver.Reserve("hero", "token-a", 3, 300); !errors.Is(err, impl.ErrTokenConflict) {
		t.Fatalf("same token with new terms must return ErrTokenConflict, got %v", err)
	}
}

func TestCommitConsumesSlots(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	mustReserve(t, reserver, "hero", "token-a", 2, 300)
	if err := reserver.Commit("hero", "token-a"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := reserver.Used("hero"); got != 2 {
		t.Fatalf("used = %d, want 2", got)
	}
	if got := reserver.Available("hero"); got != 4 {
		t.Fatalf("available = %d, want 4", got)
	}
	stored, ok := reserver.Reservation("hero", "token-a")
	if !ok || stored.State != impl.ReservationCommitted {
		t.Fatalf("committed reservation = %+v ok=%t", stored, ok)
	}
}

func TestCommitRejectsUnknownToken(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	if err := reserver.Commit("hero", "ghost"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("unknown token must return ErrUnknownToken, got %v", err)
	}
	mustReserve(t, reserver, "hero", "token-a", 1, 300)
	if err := reserver.Release("hero", "token-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := reserver.Commit("hero", "token-a"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("commit after release must return ErrUnknownToken, got %v", err)
	}
}

func TestReleaseFreesSlots(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	mustReserve(t, reserver, "hero", "token-a", 2, 300)
	if err := reserver.Release("hero", "token-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := reserver.Available("hero"); got != 6 {
		t.Fatalf("available after release = %d, want 6", got)
	}
	stored, ok := reserver.Reservation("hero", "token-a")
	if !ok || stored.State != impl.ReservationReleased {
		t.Fatalf("released reservation = %+v ok=%t", stored, ok)
	}
	if err := reserver.Release("hero", "token-a"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("second release must return ErrUnknownToken, got %v", err)
	}
}

func TestReleaseRejectsUnknownToken(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	if err := reserver.Release("hero", "ghost"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("unknown release must return ErrUnknownToken, got %v", err)
	}
	if got := reserver.Available("hero"); got != 6 {
		t.Fatalf("failed release changed capacity, available = %d", got)
	}
}

func TestTickReclaimsExpiredReservation(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	mustReserve(t, reserver, "hero", "token-a", 2, 100)
	if got := reserver.Tick("hero", 99); got != 0 {
		t.Fatalf("tick before the deadline reclaimed %d reservations", got)
	}
	if got := reserver.Available("hero"); got != 4 {
		t.Fatalf("available before expiry = %d, want 4", got)
	}
	if got := reserver.Tick("hero", 100); got != 1 {
		t.Fatalf("tick at the deadline reclaimed %d reservations, want 1", got)
	}
	if got := reserver.Available("hero"); got != 6 {
		t.Fatalf("available after expiry = %d, want 6", got)
	}
	stored, ok := reserver.Reservation("hero", "token-a")
	if !ok || stored.State != impl.ReservationExpired {
		t.Fatalf("expired reservation = %+v ok=%t", stored, ok)
	}
}

func TestTickIgnoresCommittedReservations(t *testing.T) {
	reserver := newReserver(6, 50, 4)
	mustReserve(t, reserver, "hero", "token-a", 2, 100)
	if err := reserver.Commit("hero", "token-a"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := reserver.Tick("hero", 500); got != 0 {
		t.Fatalf("tick must not reclaim committed reservations, got %d", got)
	}
	if got := reserver.Used("hero"); got != 2 {
		t.Fatalf("used after tick = %d, want 2", got)
	}
	if got := reserver.Available("hero"); got != 4 {
		t.Fatalf("available after tick = %d, want 4", got)
	}
}

func TestSpillCreatesEntry(t *testing.T) {
	reserver := newReserver(2, 50, 4)
	id, err := reserver.Spill("hero", testReward("daily", 3), 100)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if id == "" {
		t.Fatalf("spill id must not be empty")
	}
	entries := reserver.SpillEntries("hero")
	if len(entries) != 1 {
		t.Fatalf("spill entries = %d, want 1", len(entries))
	}
	if entries[0].ID != id || entries[0].Reward.RewardID != "daily" || entries[0].Reward.Slots != 3 {
		t.Fatalf("spill entry = %+v", entries[0])
	}
	if entries[0].ExpireAt != 150 {
		t.Fatalf("spill expiry = %d, want at plus ttl 150", entries[0].ExpireAt)
	}
	if got := reserver.Available("hero"); got != 2 {
		t.Fatalf("spilling must not consume bag slots, available = %d", got)
	}
}

func TestSpillRejectsDuplicateAndBadReward(t *testing.T) {
	reserver := newReserver(2, 50, 4)
	if _, err := reserver.Spill("hero", testReward("", 1), 10); !errors.Is(err, impl.ErrBadReward) {
		t.Fatalf("blank reward id must return ErrBadReward, got %v", err)
	}
	if _, err := reserver.Spill("hero", testReward("daily", 0), 10); !errors.Is(err, impl.ErrBadReward) {
		t.Fatalf("zero slot reward must return ErrBadReward, got %v", err)
	}
	if _, err := reserver.Spill("hero", testReward("daily", 2), 10); err != nil {
		t.Fatalf("first spill: %v", err)
	}
	if _, err := reserver.Spill("hero", testReward("daily", 2), 20); !errors.Is(err, impl.ErrRewardDuplicated) {
		t.Fatalf("duplicate reward must return ErrRewardDuplicated, got %v", err)
	}
	if got := len(reserver.SpillEntries("hero")); got != 1 {
		t.Fatalf("duplicate spill changed the buffer, entries = %d", got)
	}
}

func TestClaimSpillReturnsReward(t *testing.T) {
	reserver := newReserver(3, 50, 4)
	id, err := reserver.Spill("hero", testReward("daily", 2), 100)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	reward, err := reserver.ClaimSpill("hero", id, 120)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if reward.RewardID != "daily" || reward.Slots != 2 || reward.Amount != 1 {
		t.Fatalf("claimed reward = %+v", reward)
	}
	if got := reserver.Used("hero"); got != 2 {
		t.Fatalf("used after claim = %d, want 2", got)
	}
	if got := reserver.Available("hero"); got != 1 {
		t.Fatalf("available after claim = %d, want 1", got)
	}
	if got := len(reserver.SpillEntries("hero")); got != 0 {
		t.Fatalf("claimed entry must leave the buffer, entries = %d", got)
	}
	if _, err := reserver.ClaimSpill("hero", "missing", 120); !errors.Is(err, impl.ErrSpillUnknown) {
		t.Fatalf("second claim must return ErrSpillUnknown, got %v", err)
	}
}

func TestClaimSpillRejectsExpired(t *testing.T) {
	reserver := newReserver(4, 50, 4)
	id, err := reserver.Spill("hero", testReward("daily", 1), 100)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if _, err := reserver.ClaimSpill("hero", id, 150); !errors.Is(err, impl.ErrSpillExpired) {
		t.Fatalf("claim at the deadline must return ErrSpillExpired, got %v", err)
	}
	if got := len(reserver.SpillEntries("hero")); got != 1 {
		t.Fatalf("expired claim must keep the entry, entries = %d", got)
	}
	if got := reserver.Used("hero"); got != 0 {
		t.Fatalf("expired claim must not consume slots, used = %d", got)
	}
}

func TestSnapshotRestoreKeepsDeadlines(t *testing.T) {
	reserver := newReserver(4, 50, 4)
	mustReserve(t, reserver, "hero", "token-a", 2, 500)
	if _, err := reserver.Spill("hero", testReward("daily", 1), 100); err != nil {
		t.Fatalf("spill: %v", err)
	}
	data, err := reserver.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newReserver(4, 50, 4)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Available("hero"); got != 2 {
		t.Fatalf("restored available = %d, want 2", got)
	}
	if got := restored.Tick("hero", 499); got != 0 {
		t.Fatalf("tick before the restored deadline reclaimed %d", got)
	}
	if got := restored.Tick("hero", 500); got != 1 {
		t.Fatalf("tick at the restored deadline reclaimed %d, want 1", got)
	}
	entries := restored.SpillEntries("hero")
	if len(entries) != 1 || entries[0].ExpireAt != 150 {
		t.Fatalf("restored spill entries = %+v, want expiry 150", entries)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage restore must return ErrBadSnapshot, got %v", err)
	}
	if got := restored.Available("hero"); got != 4 {
		t.Fatalf("failed restore changed capacity, available = %d", got)
	}
}
