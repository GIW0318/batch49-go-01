package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-18"
)

func atMinute(hour, minute int) int64 {
	return int64(hour*60+minute) * 60000
}

func townSchedule() []impl.ScheduleItem {
	return []impl.ScheduleItem{
		{SlotID: "morning", StartMin: 480, EndMin: 720, Place: "market", Behavior: "trade"},
		{SlotID: "afternoon", StartMin: 720, EndMin: 1080, Place: "field", Behavior: "farm"},
		{SlotID: "evening", StartMin: 1080, EndMin: 1440, Place: "home", Behavior: "rest"},
	}
}

func newTownGuard(capacity int) *impl.NpcScheduleGuard {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: capacity})
	_ = g.DefineSchedule("npc-1", townSchedule())
	return g
}

func TestDefineScheduleRejectsBlankNPC(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 2})
	if err := g.DefineSchedule("   ", townSchedule()); !errors.Is(err, impl.ErrBlankNPC) {
		t.Fatalf("blank npc id must fail with ErrBlankNPC, got %v", err)
	}
	if _, ok := g.State("   "); ok {
		t.Fatalf("a rejected schedule must not create state")
	}
}

func TestDefineScheduleRejectsOverlap(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 2})
	items := []impl.ScheduleItem{
		{SlotID: "a", StartMin: 480, EndMin: 720, Place: "market", Behavior: "trade"},
		{SlotID: "b", StartMin: 700, EndMin: 760, Place: "field", Behavior: "farm"},
	}
	if err := g.DefineSchedule("npc-1", items); !errors.Is(err, impl.ErrOverlap) {
		t.Fatalf("overlapping windows must fail with ErrOverlap, got %v", err)
	}
	if _, ok := g.State("npc-1"); ok {
		t.Fatalf("a rejected schedule must not create state")
	}
}

func TestDefineScheduleValidatesItems(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 2})
	if err := g.DefineSchedule("npc-1", nil); !errors.Is(err, impl.ErrBadSchedule) {
		t.Fatalf("an empty schedule must fail with ErrBadSchedule, got %v", err)
	}
	bad := []impl.ScheduleItem{
		{SlotID: "", StartMin: 480, EndMin: 720, Place: "market", Behavior: "trade"},
		{SlotID: "a", StartMin: 480, EndMin: 720, Place: "", Behavior: "trade"},
		{SlotID: "a", StartMin: 480, EndMin: 720, Place: "market", Behavior: ""},
		{SlotID: "a", StartMin: -1, EndMin: 720, Place: "market", Behavior: "trade"},
		{SlotID: "a", StartMin: 480, EndMin: 1441, Place: "market", Behavior: "trade"},
		{SlotID: "a", StartMin: 600, EndMin: 600, Place: "market", Behavior: "trade"},
	}
	for i, item := range bad {
		if err := g.DefineSchedule("npc-1", []impl.ScheduleItem{item}); !errors.Is(err, impl.ErrBadSchedule) {
			t.Fatalf("bad item %d must fail with ErrBadSchedule, got %v", i, err)
		}
	}
}

func TestTickAssignsSlotAndOccupiesPlace(t *testing.T) {
	g := newTownGuard(4)
	at := atMinute(8, 0)
	st, err := g.Tick("npc-1", at)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if st.NPCID != "npc-1" || st.SlotID != "morning" || st.Place != "market" || st.Behavior != "trade" {
		t.Fatalf("state = %+v, want morning/market/trade", st)
	}
	if st.Since != at {
		t.Fatalf("since = %d, want %d", st.Since, at)
	}
	if st.Waiting || st.Interrupted {
		t.Fatalf("fresh tick must not be waiting or interrupted: %+v", st)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy = %d, want 1", got)
	}
}

func TestTickBoundaryIsLeftClosed(t *testing.T) {
	g := newTownGuard(4)
	before, err := g.Tick("npc-1", atMinute(7, 59))
	if err != nil {
		t.Fatalf("tick before the window: %v", err)
	}
	if before.SlotID != "" || before.Place != "" {
		t.Fatalf("07:59 state = %+v, want idle before the left closed boundary", before)
	}
	after, err := g.Tick("npc-1", atMinute(8, 0))
	if err != nil {
		t.Fatalf("tick at the window start: %v", err)
	}
	if after.SlotID != "morning" {
		t.Fatalf("08:00 slot = %q, want morning; the start minute belongs to the window", after.SlotID)
	}
}

func TestTickIdleReleasesPlace(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 4})
	morningOnly := []impl.ScheduleItem{
		{SlotID: "morning", StartMin: 480, EndMin: 720, Place: "market", Behavior: "trade"},
	}
	if err := g.DefineSchedule("npc-1", morningOnly); err != nil {
		t.Fatalf("define schedule: %v", err)
	}
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("tick into the window: %v", err)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy before idle = %d, want 1", got)
	}
	st, err := g.Tick("npc-1", atMinute(13, 0))
	if err != nil {
		t.Fatalf("tick outside every window: %v", err)
	}
	if st.SlotID != "" || st.Place != "" || st.Behavior != "" {
		t.Fatalf("idle state = %+v, want empty slot and place", st)
	}
	if got := g.Occupancy("market"); got != 0 {
		t.Fatalf("market occupancy after idle = %d, want 0", got)
	}
}

func TestTickSameSlotKeepsSinceAndOccupancy(t *testing.T) {
	g := newTownGuard(4)
	first := atMinute(8, 0)
	if _, err := g.Tick("npc-1", first); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	st, err := g.Tick("npc-1", atMinute(9, 30))
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if st.SlotID != "morning" || st.Place != "market" {
		t.Fatalf("second tick state = %+v, want to stay in the morning slot", st)
	}
	if st.Since != first {
		t.Fatalf("since changed to %d on a same slot tick, want %d", st.Since, first)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy = %d, want 1 without double counting", got)
	}
}

func TestTickSwitchReleasesOldPlace(t *testing.T) {
	g := newTownGuard(4)
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("morning tick: %v", err)
	}
	st, err := g.Tick("npc-1", atMinute(12, 0))
	if err != nil {
		t.Fatalf("afternoon tick: %v", err)
	}
	if st.SlotID != "afternoon" || st.Place != "field" || st.Behavior != "farm" {
		t.Fatalf("afternoon state = %+v, want afternoon/field/farm", st)
	}
	if got := g.Occupancy("market"); got != 0 {
		t.Fatalf("market occupancy after the switch = %d, want 0", got)
	}
	if got := g.Occupancy("field"); got != 1 {
		t.Fatalf("field occupancy after the switch = %d, want 1", got)
	}
}

func TestTickQueuesWhenPlaceFull(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 1})
	if err := g.DefineSchedule("npc-1", townSchedule()); err != nil {
		t.Fatalf("define npc-1: %v", err)
	}
	if err := g.DefineSchedule("npc-2", townSchedule()); err != nil {
		t.Fatalf("define npc-2: %v", err)
	}
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("tick npc-1: %v", err)
	}
	st, err := g.Tick("npc-2", atMinute(8, 0))
	if err != nil {
		t.Fatalf("tick npc-2: %v", err)
	}
	if !st.Waiting {
		t.Fatalf("npc-2 state = %+v, want it queued for the full market", st)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy = %d, want the cap 1", got)
	}
	if got := g.Waiting("market"); got != 1 {
		t.Fatalf("market waiting count = %d, want 1", got)
	}
}

func TestReleaseAdmitsWaiter(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 1})
	if err := g.DefineSchedule("npc-1", townSchedule()); err != nil {
		t.Fatalf("define npc-1: %v", err)
	}
	if err := g.DefineSchedule("npc-2", townSchedule()); err != nil {
		t.Fatalf("define npc-2: %v", err)
	}
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("tick npc-1: %v", err)
	}
	if _, err := g.Tick("npc-2", atMinute(8, 0)); err != nil {
		t.Fatalf("tick npc-2: %v", err)
	}
	if err := g.Release("npc-1"); err != nil {
		t.Fatalf("release npc-1: %v", err)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy after release = %d, want the waiter admitted", got)
	}
	if got := g.Waiting("market"); got != 0 {
		t.Fatalf("market waiting count = %d, want 0", got)
	}
	st, ok := g.State("npc-2")
	if !ok {
		t.Fatalf("waiter state must stay readable")
	}
	if st.Waiting || st.Place != "market" {
		t.Fatalf("waiter state = %+v, want admitted into market", st)
	}
}

func TestInterruptValidatesInput(t *testing.T) {
	g := newTownGuard(4)
	if _, err := g.Tick("npc-1", atMinute(9, 0)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if err := g.Interrupt("npc-1", "   ", atMinute(11, 0)); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason must fail with ErrBadReason, got %v", err)
	}
	if err := g.Interrupt("npc-404", "raid", atMinute(11, 0)); !errors.Is(err, impl.ErrUnknownNPC) {
		t.Fatalf("interrupting an unknown npc must fail with ErrUnknownNPC, got %v", err)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("rejected interrupts must not move the npc, occupancy=%d", got)
	}
}

func TestInterruptFreesPlaceAndResume(t *testing.T) {
	g := newTownGuard(4)
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if err := g.Interrupt("npc-1", "combat", atMinute(11, 0)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if got := g.Occupancy("market"); got != 0 {
		t.Fatalf("market occupancy during the interrupt = %d, want 0", got)
	}
	during, err := g.Tick("npc-1", atMinute(9, 0))
	if err != nil {
		t.Fatalf("tick during the interrupt: %v", err)
	}
	if !during.Interrupted {
		t.Fatalf("tick during the interrupt = %+v, want the interrupted flag", during)
	}
	if got := g.Occupancy("market"); got != 0 {
		t.Fatalf("tick during the interrupt must not reoccupy the place, occupancy=%d", got)
	}
	if _, err := g.Resume("npc-1", atMinute(10, 0)); !errors.Is(err, impl.ErrStillInterrupted) {
		t.Fatalf("resuming before the window ends must fail with ErrStillInterrupted, got %v", err)
	}
	st, err := g.Resume("npc-1", atMinute(11, 30))
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if st.Interrupted || st.SlotID != "morning" || st.Place != "market" {
		t.Fatalf("resumed state = %+v, want morning/market", st)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy after resume = %d, want 1", got)
	}
	if _, err := g.Resume("npc-1", atMinute(11, 40)); !errors.Is(err, impl.ErrNotInterrupted) {
		t.Fatalf("resuming a running npc must fail with ErrNotInterrupted, got %v", err)
	}
}

func TestConcurrentTicksSerialize(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 4})
	if err := g.DefineSchedule("npc-race", townSchedule()); err != nil {
		t.Fatalf("define schedule: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := make([]error, 0, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Tick("npc-race", atMinute(8, 0)+int64(i)*1000)
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("concurrent ticks failed: %v", failures)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("market occupancy after concurrent ticks = %d, want 1", got)
	}
	st, ok := g.State("npc-race")
	if !ok {
		t.Fatalf("state must be readable after concurrent ticks")
	}
	if st.Waiting || st.Place != "market" {
		t.Fatalf("state after concurrent ticks = %+v, want a single market occupancy", st)
	}
}

func TestSnapshotRestoreKeepsOccupancyAndQueue(t *testing.T) {
	g := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 1})
	if err := g.DefineSchedule("npc-1", townSchedule()); err != nil {
		t.Fatalf("define npc-1: %v", err)
	}
	if err := g.DefineSchedule("npc-2", townSchedule()); err != nil {
		t.Fatalf("define npc-2: %v", err)
	}
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("tick npc-1: %v", err)
	}
	if _, err := g.Tick("npc-2", atMinute(8, 0)); err != nil {
		t.Fatalf("tick npc-2: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNpcScheduleGuard(impl.Options{DefaultPlaceCapacity: 1})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Occupancy("market"); got != 1 {
		t.Fatalf("restored occupancy = %d, want 1", got)
	}
	if got := restored.Waiting("market"); got != 1 {
		t.Fatalf("restored waiting count = %d, want 1", got)
	}
	waiter, ok := restored.State("npc-2")
	if !ok || !waiter.Waiting {
		t.Fatalf("restored waiter state = %+v ok=%v, want a queued npc", waiter, ok)
	}
	if err := restored.Release("npc-1"); err != nil {
		t.Fatalf("release after restore: %v", err)
	}
	if got := restored.Occupancy("market"); got != 1 {
		t.Fatalf("occupancy after admitting the restored waiter = %d, want 1", got)
	}
	if got := restored.Waiting("market"); got != 0 {
		t.Fatalf("waiting after admitting the restored waiter = %d, want 0", got)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	g := newTownGuard(4)
	if _, err := g.Tick("npc-1", atMinute(8, 0)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if err := g.Restore([]byte("{broken-payload")); !errors.Is(err, impl.ErrBadRestore) {
		t.Fatalf("corrupt snapshot must fail with ErrBadRestore, got %v", err)
	}
	if got := g.Occupancy("market"); got != 1 {
		t.Fatalf("a rejected restore touched live occupancy: %d", got)
	}
	st, ok := g.State("npc-1")
	if !ok || st.Place != "market" {
		t.Fatalf("live state after a rejected restore = %+v ok=%v, want market", st, ok)
	}
}
