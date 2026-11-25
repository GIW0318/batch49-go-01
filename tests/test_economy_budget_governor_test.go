package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-23"
)

func mustConfigure(t *testing.T, g *impl.EconomyGovernor, server string, budget int64, carry int) {
	t.Helper()
	if err := g.Configure(server, budget, carry); err != nil {
		t.Fatalf("configure %s: %v", server, err)
	}
}

func mustRoster(t *testing.T, g *impl.EconomyGovernor, server string, players []string, at int64) {
	t.Helper()
	if err := g.SetRoster(server, players, at); err != nil {
		t.Fatalf("set roster %s: %v", server, err)
	}
}

func TestConfigureValidatesInput(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	if err := g.Configure("", 100, 10); !errors.Is(err, impl.ErrInvalidServer) {
		t.Fatalf("blank server must fail with ErrInvalidServer, got %v", err)
	}
	if err := g.Configure("s1", 0, 10); !errors.Is(err, impl.ErrInvalidBudget) {
		t.Fatalf("zero budget must fail with ErrInvalidBudget, got %v", err)
	}
	if err := g.Configure("s1", -5, 10); !errors.Is(err, impl.ErrInvalidBudget) {
		t.Fatalf("negative budget must fail with ErrInvalidBudget, got %v", err)
	}
	for _, pct := range []int{-1, 101} {
		if err := g.Configure("s1", 100, pct); !errors.Is(err, impl.ErrInvalidCarryPct) {
			t.Fatalf("carry %d must fail with ErrInvalidCarryPct, got %v", pct, err)
		}
	}
	mustConfigure(t, g, "s1", 100, 10)
	if err := g.Configure("s1", 200, 10); !errors.Is(err, impl.ErrServerExists) {
		t.Fatalf("second configure must fail with ErrServerExists, got %v", err)
	}
}

func TestSetRosterValidatesAndLocks(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	if err := g.SetRoster("missing", []string{"p1"}, 0); !errors.Is(err, impl.ErrUnknownServer) {
		t.Fatalf("unknown server must fail with ErrUnknownServer, got %v", err)
	}
	mustConfigure(t, g, "s1", 400, 0)
	if err := g.SetRoster("s1", nil, 0); !errors.Is(err, impl.ErrEmptyRoster) {
		t.Fatalf("empty roster must fail with ErrEmptyRoster, got %v", err)
	}
	if err := g.SetRoster("s1", []string{"p1", "p1"}, 0); !errors.Is(err, impl.ErrDuplicatePlayer) {
		t.Fatalf("duplicate player must fail with ErrDuplicatePlayer, got %v", err)
	}
	mustRoster(t, g, "s1", []string{"p1", "p2"}, 0)
	if _, err := g.Grant("s1", "p1", 10, 0); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := g.SetRoster("s1", []string{"p1", "p2", "p3"}, 0); !errors.Is(err, impl.ErrRosterLocked) {
		t.Fatalf("roster change after a grant must fail with ErrRosterLocked, got %v", err)
	}
}

func TestGrantRequiresRoster(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	mustConfigure(t, g, "s1", 400, 0)
	if _, err := g.Grant("s1", "p1", 10, 0); !errors.Is(err, impl.ErrNoRoster) {
		t.Fatalf("grant without a roster must fail with ErrNoRoster, got %v", err)
	}
}

func TestGrantRejectsUnknownPlayerAndBadAmount(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	mustConfigure(t, g, "s1", 400, 0)
	mustRoster(t, g, "s1", []string{"p1", "p2"}, 0)
	if _, err := g.Grant("s1", "p9", 10, 0); !errors.Is(err, impl.ErrUnknownPlayer) {
		t.Fatalf("player outside the roster must fail with ErrUnknownPlayer, got %v", err)
	}
	for _, amount := range []int64{0, -7} {
		if _, err := g.Grant("s1", "p1", amount, 0); !errors.Is(err, impl.ErrInvalidAmount) {
			t.Fatalf("amount %d must fail with ErrInvalidAmount, got %v", amount, err)
		}
	}
}

func TestGrantWithinShareUpdatesUsage(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const day = int64(5)
	mustConfigure(t, g, "s1", 400, 0)
	mustRoster(t, g, "s1", []string{"p1", "p2", "p3", "p4"}, day*impl.DaySeconds)

	res, err := g.Grant("s1", "p1", 60, day*impl.DaySeconds)
	if err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if res.Granted != 60 || res.Decayed != 0 || res.Share != 100 || res.Day != day {
		t.Fatalf("first grant = %+v, want granted 60 decayed 0 share 100 day 5", res)
	}
	res, err = g.Grant("s1", "p1", 30, day*impl.DaySeconds+100)
	if err != nil {
		t.Fatalf("second grant: %v", err)
	}
	if res.Granted != 30 || res.Decayed != 0 {
		t.Fatalf("second grant = %+v, want granted 30 decayed 0", res)
	}
	u := g.Usage("s1", day*impl.DaySeconds+200)
	if u.Day != day || u.Budget != 400 || u.Spent != 90 || u.Players != 4 || u.Remain != 310 {
		t.Fatalf("usage = %+v, want day 5 budget 400 spent 90 players 4 remain 310", u)
	}
	if got := g.PlayerSpent("s1", "p1", day*impl.DaySeconds+200); got != 90 {
		t.Fatalf("player spent = %d, want 90", got)
	}
}

func TestGrantOverShareDecays(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{OverflowGrantPct: 25})
	const at = int64(100)
	mustConfigure(t, g, "s1", 400, 0)
	mustRoster(t, g, "s1", []string{"p1", "p2", "p3", "p4"}, at)

	res, err := g.Grant("s1", "p1", 140, at)
	if err != nil {
		t.Fatalf("over share grant: %v", err)
	}
	if res.Granted != 110 || res.Decayed != 30 || res.Share != 100 {
		t.Fatalf("over share grant = %+v, want granted 110 decayed 30 share 100", res)
	}
	res, err = g.Grant("s1", "p1", 10, at+1)
	if err != nil {
		t.Fatalf("second over share grant: %v", err)
	}
	if res.Granted != 2 || res.Decayed != 8 {
		t.Fatalf("second over share grant = %+v, want granted 2 decayed 8", res)
	}
	if got := g.PlayerSpent("s1", "p1", at+1); got != 112 {
		t.Fatalf("player spent = %d, want 112", got)
	}
}

func TestGrantDayRegressionRejected(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const day = int64(5)
	mustConfigure(t, g, "s1", 400, 0)
	mustRoster(t, g, "s1", []string{"p1", "p2"}, day*impl.DaySeconds)
	if _, err := g.Grant("s1", "p1", 10, day*impl.DaySeconds); err != nil {
		t.Fatalf("grant on day 5: %v", err)
	}
	if _, err := g.Grant("s1", "p1", 10, (day-1)*impl.DaySeconds); !errors.Is(err, impl.ErrDayRegression) {
		t.Fatalf("earlier day must fail with ErrDayRegression, got %v", err)
	}
	u := g.Usage("s1", day*impl.DaySeconds)
	if u.Spent != 10 {
		t.Fatalf("usage after rejected grant = %d, want 10", u.Spent)
	}
}

func TestDayBoundaryIsUTC(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const day = int64(5)
	start := day * impl.DaySeconds
	mustConfigure(t, g, "s1", 400, 0)
	mustRoster(t, g, "s1", []string{"p1", "p2", "p3", "p4"}, start)

	res, err := g.Grant("s1", "p1", 10, start)
	if err != nil {
		t.Fatalf("grant at day start: %v", err)
	}
	if res.Day != day {
		t.Fatalf("grant at day start landed on day %d, want %d", res.Day, day)
	}
	res, err = g.Grant("s1", "p1", 10, start+impl.DaySeconds-1)
	if err != nil {
		t.Fatalf("grant at day end: %v", err)
	}
	if res.Day != day {
		t.Fatalf("grant at day end landed on day %d, want %d", res.Day, day)
	}
	res, err = g.Grant("s1", "p1", 10, start+impl.DaySeconds)
	if err != nil {
		t.Fatalf("grant on the next day: %v", err)
	}
	if res.Day != day+1 {
		t.Fatalf("grant after midnight landed on day %d, want %d", res.Day, day+1)
	}
}

func TestRolloverCarriesUnusedBudget(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const day = int64(5)
	mustConfigure(t, g, "s1", 1000, 40)
	mustRoster(t, g, "s1", []string{"p1", "p2", "p3", "p4"}, day*impl.DaySeconds)
	if _, err := g.Grant("s1", "p1", 250, day*impl.DaySeconds); err != nil {
		t.Fatalf("grant p1: %v", err)
	}
	if _, err := g.Grant("s1", "p2", 150, day*impl.DaySeconds+10); err != nil {
		t.Fatalf("grant p2: %v", err)
	}
	res, err := g.Rollover("s1", (day+1)*impl.DaySeconds)
	if err != nil {
		t.Fatalf("rollover: %v", err)
	}
	if !res.Rolled || res.FromDay != day || res.ToDay != day+1 {
		t.Fatalf("rollover result = %+v, want rolled from day 5 to day 6", res)
	}
	if res.Carried != 240 || res.Budget != 1240 {
		t.Fatalf("carry = %d budget = %d, want carried 240 budget 1240", res.Carried, res.Budget)
	}
	u := g.Usage("s1", (day+1)*impl.DaySeconds)
	if u.Day != day+1 || u.Budget != 1240 || u.Spent != 0 || u.Remain != 1240 {
		t.Fatalf("usage after rollover = %+v, want day 6 budget 1240 spent 0", u)
	}
}

func TestGrantAfterRolloverUsesCarriedBudget(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const day = int64(5)
	mustConfigure(t, g, "s1", 1000, 40)
	mustRoster(t, g, "s1", []string{"p1", "p2", "p3", "p4"}, day*impl.DaySeconds)
	if _, err := g.Grant("s1", "p1", 250, day*impl.DaySeconds); err != nil {
		t.Fatalf("grant p1: %v", err)
	}
	if _, err := g.Grant("s1", "p2", 150, day*impl.DaySeconds+1); err != nil {
		t.Fatalf("grant p2: %v", err)
	}
	if _, err := g.Rollover("s1", (day+1)*impl.DaySeconds); err != nil {
		t.Fatalf("rollover: %v", err)
	}
	res, err := g.Grant("s1", "p2", 100, (day+1)*impl.DaySeconds+5)
	if err != nil {
		t.Fatalf("grant after rollover: %v", err)
	}
	if res.Granted != 100 || res.Day != day+1 || res.Share != 310 {
		t.Fatalf("grant after rollover = %+v, want granted 100 day 6 share 310", res)
	}
	u := g.Usage("s1", (day+1)*impl.DaySeconds+5)
	if u.Spent != 100 || u.Remain != 1140 {
		t.Fatalf("usage after rollover grant = %+v, want spent 100 remain 1140", u)
	}
}

func TestTotalSpentCappedByBudget(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{OverflowGrantPct: 100})
	const at = int64(50)
	mustConfigure(t, g, "s1", 100, 0)
	mustRoster(t, g, "s1", []string{"p1", "p2"}, at)
	if _, err := g.Grant("s1", "p1", 50, at); err != nil {
		t.Fatalf("grant p1: %v", err)
	}
	if _, err := g.Grant("s1", "p2", 50, at+1); err != nil {
		t.Fatalf("grant p2: %v", err)
	}
	u := g.Usage("s1", at+1)
	if u.Spent != 100 || u.Remain != 0 {
		t.Fatalf("usage = %+v, want spent 100 remain 0", u)
	}
	if got := g.Remain("s1", at+1); got != 0 {
		t.Fatalf("remain = %d, want 0", got)
	}
}

func TestConcurrentGrantsSameServer(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const at = int64(10)
	players := make([]string, 100)
	for i := range players {
		players[i] = "p" + strings.Repeat("0", i%2) + string(rune('A'+i/2))
	}
	mustConfigure(t, g, "s1", 10000, 0)
	mustRoster(t, g, "s1", players, at)

	var wg sync.WaitGroup
	errs := make(chan error, len(players))
	for i, player := range players {
		wg.Add(1)
		go func(i int, player string) {
			defer wg.Done()
			res, err := g.Grant("s1", player, 100, at+int64(i))
			if err == nil && res.Granted != 100 {
				err = errors.New("partial grant")
			}
			errs <- err
		}(i, player)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent grant failed: %v", err)
		}
	}
	u := g.Usage("s1", at)
	if u.Spent != 10000 || u.Remain != 0 || u.Players != 100 {
		t.Fatalf("usage = %+v, want spent 10000 remain 0 players 100", u)
	}
}

func TestConcurrentGrantsDifferentServers(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const at = int64(10)
	players := []string{"p1", "p2", "p3", "p4"}
	for _, server := range []string{"s1", "s2"} {
		mustConfigure(t, g, server, 1000, 0)
		mustRoster(t, g, server, players, at)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			server := "s1"
			if i%2 == 1 {
				server = "s2"
			}
			if _, err := g.Grant(server, players[i%len(players)], 100, at+int64(i)); err != nil {
				t.Errorf("grant on %s: %v", server, err)
			}
		}(i)
	}
	wg.Wait()
	for _, server := range []string{"s1", "s2"} {
		u := g.Usage(server, at)
		if u.Spent != 400 {
			t.Fatalf("%s spent = %d, want 400", server, u.Spent)
		}
	}
}

func TestSnapshotRestorePreservesState(t *testing.T) {
	opts := impl.Options{OverflowGrantPct: 25}
	g := impl.NewEconomyGovernor(opts)
	const at = int64(5 * impl.DaySeconds)
	mustConfigure(t, g, "s1", 500, 20)
	mustRoster(t, g, "s1", []string{"p1", "p2"}, at)
	if _, err := g.Grant("s1", "p1", 100, at); err != nil {
		t.Fatalf("grant: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	restored := impl.NewEconomyGovernor(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	u := restored.Usage("s1", at)
	if u.Budget != 500 || u.Spent != 100 || u.Players != 2 || u.Day != 5 {
		t.Fatalf("restored usage = %+v, want budget 500 spent 100 players 2 day 5", u)
	}
	if got := restored.PlayerSpent("s1", "p1", at); got != 100 {
		t.Fatalf("restored player spent = %d, want 100", got)
	}
	if got := strings.Join(restored.Players("s1", at), ","); got != "p1,p2" {
		t.Fatalf("restored roster = %q, want p1,p2", got)
	}
	res, err := restored.Grant("s1", "p1", 100, at+1)
	if err != nil {
		t.Fatalf("grant after restore: %v", err)
	}
	if res.Granted != 100 {
		t.Fatalf("grant after restore granted %d, want 100", res.Granted)
	}
}

func TestRestoreRejectsGarbage(t *testing.T) {
	g := impl.NewEconomyGovernor(impl.Options{})
	const at = int64(5 * impl.DaySeconds)
	mustConfigure(t, g, "s1", 500, 20)
	mustRoster(t, g, "s1", []string{"p1", "p2"}, at)
	if _, err := g.Grant("s1", "p1", 100, at); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := g.Restore([]byte("not a snapshot at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("garbage snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	u := g.Usage("s1", at)
	if u.Spent != 100 || u.Budget != 500 {
		t.Fatalf("failed restore changed usage: %+v", u)
	}
}
