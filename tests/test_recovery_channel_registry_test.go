package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-a-06"
)

func testOptions() impl.Options {
	return impl.Options{
		MaxChannelsPerUser: 4,
		MaxVerifyAttempts:  3,
		Cooldown:           30 * time.Minute,
		Now:                func() time.Time { return time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC) },
	}
}

func emailChannel(id string) impl.Channel {
	return impl.Channel{ID: id, Kind: impl.KindEmail, Address: id + "@example.test"}
}

func findAll(channels []impl.Channel, id string) (impl.Channel, bool) {
	for _, ch := range channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return impl.Channel{}, false
}

func TestAddRegistersUnverifiedChannel(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	p, err := r.Add("user-1", emailChannel("ch-1"))
	if err != nil {
		t.Fatalf("register channel: %v", err)
	}
	if p.ChannelID != "ch-1" || p.UserID != "user-1" {
		t.Fatalf("pending identity = user %q channel %q, want user-1 / ch-1", p.UserID, p.ChannelID)
	}
	if p.Proof == "" {
		t.Fatalf("pending must carry the proof to deliver")
	}
	if p.AttemptsLeft != testOptions().MaxVerifyAttempts {
		t.Fatalf("attempts left = %d, want %d", p.AttemptsLeft, testOptions().MaxVerifyAttempts)
	}
	if p.Seq <= 0 {
		t.Fatalf("pending sequence = %d, want a positive number", p.Seq)
	}
	channels := r.Channels("user-1")
	if len(channels) != 1 {
		t.Fatalf("channel count = %d, want 1", len(channels))
	}
	if channels[0].State != impl.ChannelUnverified {
		t.Fatalf("new channel state = %q, want %q", channels[0].State, impl.ChannelUnverified)
	}
	if channels[0].Address != "ch-1@example.test" {
		t.Fatalf("address = %q, want the registered address", channels[0].Address)
	}
	usable, err := r.Usable("user-1")
	if err != nil {
		t.Fatalf("usable list: %v", err)
	}
	if len(usable) != 0 {
		t.Fatalf("unverified channel must not be usable, got %d entries", len(usable))
	}
}

func TestAddRejectsBlankIdentityAndUnknownKind(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	if _, err := r.Add("   ", emailChannel("ch-1")); !errors.Is(err, impl.ErrBlankIdentity) {
		t.Fatalf("blank user id must fail with ErrBlankIdentity, got %v", err)
	}
	blankID := emailChannel("")
	if _, err := r.Add("user-1", blankID); !errors.Is(err, impl.ErrBlankIdentity) {
		t.Fatalf("blank channel id must fail with ErrBlankIdentity, got %v", err)
	}
	pigeon := impl.Channel{ID: "ch-2", Kind: "carrier_pigeon", Address: "loft-9"}
	if _, err := r.Add("user-1", pigeon); !errors.Is(err, impl.ErrBadChannelKind) {
		t.Fatalf("unknown channel kind must fail with ErrBadChannelKind, got %v", err)
	}
	if got := len(r.Channels("user-1")); got != 0 {
		t.Fatalf("rejected registrations must not be stored, got %d channels", got)
	}
}

func TestAddRejectsDuplicateChannelID(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	if _, err := r.Add("user-1", emailChannel("ch-1")); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := r.Add("user-2", emailChannel("ch-1")); !errors.Is(err, impl.ErrChannelExists) {
		t.Fatalf("reused channel id must fail with ErrChannelExists, got %v", err)
	}
	if got := len(r.Channels("user-2")); got != 0 {
		t.Fatalf("user-2 must have no channels, got %d", got)
	}
}

func TestAddEnforcesPerUserChannelLimit(t *testing.T) {
	opts := testOptions()
	opts.MaxChannelsPerUser = 2
	r := impl.NewRecoveryChannelRegistry(opts)
	for _, id := range []string{"ch-1", "ch-2"} {
		if _, err := r.Add("user-3", emailChannel(id)); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if _, err := r.Add("user-3", emailChannel("ch-3")); !errors.Is(err, impl.ErrChannelLimit) {
		t.Fatalf("third channel must fail with ErrChannelLimit, got %v", err)
	}
	if got := len(r.Channels("user-3")); got != 2 {
		t.Fatalf("channel count after the rejected add = %d, want the cap 2", got)
	}
}

func TestVerifyWrongProofExhaustsAttempts(t *testing.T) {
	opts := testOptions()
	opts.MaxVerifyAttempts = 3
	r := impl.NewRecoveryChannelRegistry(opts)
	p, err := r.Add("user-4", emailChannel("ch-1"))
	if err != nil {
		t.Fatalf("register channel: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if err := r.Verify("user-4", "ch-1", "proof-wrong"); !errors.Is(err, impl.ErrBadProof) {
			t.Fatalf("wrong proof %d must fail with ErrBadProof, got %v", i, err)
		}
	}
	channels := r.Channels("user-4")
	if len(channels) != 1 || channels[0].Attempts != 2 {
		t.Fatalf("attempt counter = %+v, want exactly 2 recorded attempts", channels)
	}
	if err := r.Verify("user-4", "ch-1", "proof-wrong"); !errors.Is(err, impl.ErrVerifyLocked) {
		t.Fatalf("last allowed attempt must fail with ErrVerifyLocked, got %v", err)
	}
	if err := r.Verify("user-4", "ch-1", p.Proof); !errors.Is(err, impl.ErrVerifyLocked) {
		t.Fatalf("correct proof must stay locked out, got %v", err)
	}
	channels = r.Channels("user-4")
	if channels[0].State != impl.ChannelUnverified {
		t.Fatalf("locked channel state = %q, want %q", channels[0].State, impl.ChannelUnverified)
	}
}

func TestVerifyPromotesChannelToActive(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	p, err := r.Add("user-5", emailChannel("ch-1"))
	if err != nil {
		t.Fatalf("register channel: %v", err)
	}
	if err := r.Verify("user-5", "ch-1", p.Proof); err != nil {
		t.Fatalf("verify channel: %v", err)
	}
	channels := r.Channels("user-5")
	if len(channels) != 1 || channels[0].State != impl.ChannelActive {
		t.Fatalf("channel after verify = %+v, want a single active channel", channels)
	}
	usable, err := r.Usable("user-5")
	if err != nil {
		t.Fatalf("usable list: %v", err)
	}
	if len(usable) != 1 || usable[0].ID != "ch-1" {
		t.Fatalf("usable channels = %+v, want exactly ch-1", usable)
	}
	if _, err := r.Primary("user-5"); !errors.Is(err, impl.ErrNoPrimary) {
		t.Fatalf("no primary was chosen yet, want ErrNoPrimary, got %v", err)
	}
}

func TestVerifyUnknownChannelFails(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	if err := r.Verify("user-6", "ch-404", "proof"); !errors.Is(err, impl.ErrChannelNotFound) {
		t.Fatalf("unknown channel must fail with ErrChannelNotFound, got %v", err)
	}
	if err := r.Verify("user-6", "ch-1", ""); !errors.Is(err, impl.ErrChannelNotFound) {
		t.Fatalf("empty proof on a missing channel must fail with ErrChannelNotFound, got %v", err)
	}
}

func TestMarkBounceRemovesChannelFromRecovery(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	p1, err := r.Add("user-7", emailChannel("ch-1"))
	if err != nil {
		t.Fatalf("register ch-1: %v", err)
	}
	if err := r.Verify("user-7", "ch-1", p1.Proof); err != nil {
		t.Fatalf("verify ch-1: %v", err)
	}
	p2, err := r.Add("user-7", emailChannel("ch-2"))
	if err != nil {
		t.Fatalf("register ch-2: %v", err)
	}
	if err := r.Verify("user-7", "ch-2", p2.Proof); err != nil {
		t.Fatalf("verify ch-2: %v", err)
	}
	if err := r.MarkBounce("ch-1", "mailbox does not exist"); err != nil {
		t.Fatalf("mark bounce: %v", err)
	}
	usable, err := r.Usable("user-7")
	if err != nil {
		t.Fatalf("usable list: %v", err)
	}
	if len(usable) != 1 || usable[0].ID != "ch-2" {
		t.Fatalf("bounced channel must not be selectable, got %+v", usable)
	}
	if err := r.MarkBounce("ch-2", "   "); !errors.Is(err, impl.ErrBadDetail) {
		t.Fatalf("blank bounce detail must fail with ErrBadDetail, got %v", err)
	}
	if err := r.MarkBounce("ch-404", "gone"); !errors.Is(err, impl.ErrChannelNotFound) {
		t.Fatalf("unknown channel bounce must fail with ErrChannelNotFound, got %v", err)
	}
}

func TestRemoveDropsOneOfSeveralChannels(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	for _, id := range []string{"ch-1", "ch-2"} {
		p, err := r.Add("user-8", emailChannel(id))
		if err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if err := r.Verify("user-8", id, p.Proof); err != nil {
			t.Fatalf("verify %s: %v", id, err)
		}
	}
	if err := r.Remove("user-8", "ch-1"); err != nil {
		t.Fatalf("remove channel: %v", err)
	}
	channels := r.Channels("user-8")
	if len(channels) != 1 || channels[0].ID != "ch-2" {
		t.Fatalf("channels after remove = %+v, want only ch-2", channels)
	}
	usable, err := r.Usable("user-8")
	if err != nil {
		t.Fatalf("usable list: %v", err)
	}
	if len(usable) != 1 || usable[0].ID != "ch-2" {
		t.Fatalf("usable channels after remove = %+v, want only ch-2", usable)
	}
	if err := r.Remove("user-8", "ch-1"); !errors.Is(err, impl.ErrChannelNotFound) {
		t.Fatalf("removing an already removed channel must fail with ErrChannelNotFound, got %v", err)
	}
	if err := r.Remove("user-8", "ch-404"); !errors.Is(err, impl.ErrChannelNotFound) {
		t.Fatalf("removing an unknown channel must fail with ErrChannelNotFound, got %v", err)
	}
}

func TestSetPrimaryRejectsUnverifiedChannel(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	if _, err := r.Add("user-9", emailChannel("ch-1")); err != nil {
		t.Fatalf("register channel: %v", err)
	}
	if err := r.SetPrimary("user-9", "ch-1"); !errors.Is(err, impl.ErrNotVerified) {
		t.Fatalf("unverified primary must fail with ErrNotVerified, got %v", err)
	}
	if err := r.SetPrimary("user-9", "ch-404"); !errors.Is(err, impl.ErrChannelNotFound) {
		t.Fatalf("unknown primary must fail with ErrChannelNotFound, got %v", err)
	}
	if _, err := r.Primary("user-9"); !errors.Is(err, impl.ErrNoPrimary) {
		t.Fatalf("failed switch must leave the account without a primary, got %v", err)
	}
}

func TestUsableKeepsRegistrationOrder(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	for _, id := range []string{"ch-3", "ch-1", "ch-2"} {
		p, err := r.Add("user-10", emailChannel(id))
		if err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
		if err := r.Verify("user-10", id, p.Proof); err != nil {
			t.Fatalf("verify %s: %v", id, err)
		}
	}
	usable, err := r.Usable("user-10")
	if err != nil {
		t.Fatalf("usable list: %v", err)
	}
	want := []string{"ch-3", "ch-1", "ch-2"}
	if len(usable) != len(want) {
		t.Fatalf("usable count = %d, want %d", len(usable), len(want))
	}
	for i, id := range want {
		if usable[i].ID != id {
			t.Fatalf("usable[%d] = %q, want %q in registration order", i, usable[i].ID, id)
		}
	}
	channels := r.Channels("user-10")
	for i, id := range want {
		if channels[i].ID != id {
			t.Fatalf("channels[%d] = %q, want %q in registration order", i, channels[i].ID, id)
		}
	}
}

func TestConcurrentAddSameChannelKeepsOne(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, duplicated := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Add("user-11", emailChannel("ch-dup"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, impl.ErrChannelExists):
				duplicated++
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("concurrent registrations succeeded %d times, want exactly 1", created)
	}
	if duplicated != 7 {
		t.Fatalf("concurrent registrations reported %d duplicate errors, want 7", duplicated)
	}
	if got := len(r.Channels("user-11")); got != 1 {
		t.Fatalf("channel count after the race = %d, want 1", got)
	}
}

func TestSnapshotRestoreKeepsStateAndAttempts(t *testing.T) {
	opts := testOptions()
	r := impl.NewRecoveryChannelRegistry(opts)
	p1, err := r.Add("user-12", emailChannel("ch-1"))
	if err != nil {
		t.Fatalf("register ch-1: %v", err)
	}
	if err := r.Verify("user-12", "ch-1", p1.Proof); err != nil {
		t.Fatalf("verify ch-1: %v", err)
	}
	if _, err := r.Add("user-12", emailChannel("ch-2")); err != nil {
		t.Fatalf("register ch-2: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := r.Verify("user-12", "ch-2", "proof-wrong"); !errors.Is(err, impl.ErrBadProof) {
			t.Fatalf("wrong proof %d: %v", i, err)
		}
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewRecoveryChannelRegistry(opts)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	channels := restored.Channels("user-12")
	if len(channels) != 2 {
		t.Fatalf("restored channel count = %d, want 2", len(channels))
	}
	active, ok := findAll(channels, "ch-1")
	if !ok || active.State != impl.ChannelActive {
		t.Fatalf("restored ch-1 = %+v ok=%v, want an active channel", active, ok)
	}
	pending, ok := findAll(channels, "ch-2")
	if !ok || pending.State != impl.ChannelUnverified || pending.Attempts != 2 {
		t.Fatalf("restored ch-2 = %+v ok=%v, want unverified with 2 attempts", pending, ok)
	}
	if err := restored.Verify("user-12", "ch-2", "proof-wrong"); !errors.Is(err, impl.ErrVerifyLocked) {
		t.Fatalf("the third attempt after restore must stay exhausted, got %v", err)
	}
	usable, err := restored.Usable("user-12")
	if err != nil {
		t.Fatalf("usable list after restore: %v", err)
	}
	if len(usable) != 1 || usable[0].ID != "ch-1" {
		t.Fatalf("usable after restore = %+v, want only ch-1", usable)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	r := impl.NewRecoveryChannelRegistry(testOptions())
	if err := r.Restore([]byte("}{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(r.Channels("user-13")); got != 0 {
		t.Fatalf("failed restore must not populate channels, got %d", got)
	}
	if _, err := r.Add("user-13", emailChannel("ch-1")); err != nil {
		t.Fatalf("registry must keep working after a rejected restore, got %v", err)
	}
}

func TestConcurrentWritesAndSnapshotReaders(t *testing.T) {
	opts := testOptions()
	opts.MaxChannelsPerUser = 10
	r := impl.NewRecoveryChannelRegistry(opts)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	var readers sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				_, _ = r.Usable("user-14")
				_ = r.Channels("user-14")
			}
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := r.Add("user-14", emailChannel(fmt.Sprintf("ch-%02d", i))); err == nil {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	close(done)
	readers.Wait()
	if created != 10 {
		t.Fatalf("concurrent registrations succeeded %d times, want 10", created)
	}
	if got := len(r.Channels("user-14")); got != 10 {
		t.Fatalf("channel count after the race = %d, want 10", got)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot after concurrent writes: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot after concurrent writes must not be empty")
	}
}
