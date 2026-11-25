package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch49-go-game-a-30"
)

func dmOptions() impl.Options {
	return impl.Options{MaxMessagesPerConversation: 10, MaxRetentionSeconds: 150}
}

func sendDM(t *testing.T, s *impl.DmCursorStore, from, to, body string, at int64) impl.Message {
	t.Helper()
	m, err := s.Send(from, to, body, at)
	if err != nil {
		t.Fatalf("send %s -> %s: %v", from, to, err)
	}
	return m
}

func TestSendAssignsMonotonicSeq(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	first := sendDM(t, s, "alice", "bob", "hello", 100)
	if first.Seq != 1 || first.From != "alice" || first.To != "bob" || first.Body != "hello" || first.At != 100 {
		t.Fatalf("first message = %+v", first)
	}
	second := sendDM(t, s, "bob", "alice", "hi", 110)
	if second.Seq != 2 {
		t.Fatalf("second message seq = %d, want 2", second.Seq)
	}
	thread, ok := s.Conversation("alice", "bob")
	if !ok {
		t.Fatalf("conversation must exist after two sends")
	}
	if thread.Peer != "bob" || thread.LastSeq != 2 || thread.Messages != 2 || thread.Cursor != 0 {
		t.Fatalf("thread = %+v, want peer bob last 2 messages 2 cursor 0", thread)
	}
	reverse, ok := s.Conversation("bob", "alice")
	if !ok || reverse.Peer != "alice" || reverse.LastSeq != 2 {
		t.Fatalf("reverse thread = %+v ok=%v, want peer alice last 2", reverse, ok)
	}
}

func TestSendValidation(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	cases := []struct {
		from, to, body string
		at             int64
	}{
		{"", "bob", "hello", 100},
		{"alice", "", "hello", 100},
		{"alice", "alice", "hello", 100},
		{"alice", "bob", "", 100},
		{"alice", "bob", "   ", 100},
		{"alice", "bob", "hello", 0},
		{"alice", "bob", "hello", -1},
	}
	for i, c := range cases {
		if _, err := s.Send(c.from, c.to, c.body, c.at); !errors.Is(err, impl.ErrBadMessage) {
			t.Fatalf("bad send %d = %v, want ErrBadMessage", i, err)
		}
	}
	if got := len(s.Conversations("alice")); got != 0 {
		t.Fatalf("rejected sends created %d conversations", got)
	}
}

func TestFetchBothDirections(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "one", 100)
	sendDM(t, s, "bob", "alice", "two", 110)
	sendDM(t, s, "alice", "bob", "three", 120)
	fromAlice := s.Fetch("alice", "bob", 0)
	if len(fromAlice) != 3 {
		t.Fatalf("alice fetch = %d messages, want 3", len(fromAlice))
	}
	wantFrom := []string{"alice", "bob", "alice"}
	wantBody := []string{"one", "two", "three"}
	for i := range fromAlice {
		if fromAlice[i].Seq != int64(i+1) {
			t.Fatalf("message %d seq = %d, want %d", i, fromAlice[i].Seq, i+1)
		}
		if fromAlice[i].From != wantFrom[i] || fromAlice[i].Body != wantBody[i] {
			t.Fatalf("message %d = %+v", i, fromAlice[i])
		}
	}
	fromBob := s.Fetch("bob", "alice", 0)
	if len(fromBob) != 3 || fromBob[2].Body != "three" {
		t.Fatalf("bob fetch = %+v, want the same thread", fromBob)
	}
	if got := s.Fetch("alice", "carol", 0); len(got) != 0 {
		t.Fatalf("unknown peer fetch = %d messages", len(got))
	}
}

func TestFetchAfterSeq(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "one", 100)
	sendDM(t, s, "alice", "bob", "two", 110)
	sendDM(t, s, "alice", "bob", "three", 120)
	tail := s.Fetch("alice", "bob", 1)
	if len(tail) != 2 || tail[0].Seq != 2 || tail[1].Seq != 3 {
		t.Fatalf("fetch after 1 = %+v, want seq 2 and 3", tail)
	}
	if got := s.Fetch("alice", "bob", 3); len(got) != 0 {
		t.Fatalf("fetch after last = %d messages", len(got))
	}
	if got := s.Fetch("alice", "bob", 99); len(got) != 0 {
		t.Fatalf("fetch far beyond = %d messages", len(got))
	}
}

func TestFetchIsolationBetweenConversations(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "for-bob-1", 100)
	sendDM(t, s, "alice", "bob", "for-bob-2", 110)
	sendDM(t, s, "alice", "carol", "for-carol", 120)
	bob := s.Fetch("alice", "bob", 0)
	carol := s.Fetch("alice", "carol", 0)
	if len(bob) != 2 || len(carol) != 1 {
		t.Fatalf("fetch lengths bob=%d carol=%d, want 2 and 1", len(bob), len(carol))
	}
	if carol[0].Body != "for-carol" || carol[0].Seq != 1 {
		t.Fatalf("carol message = %+v, want first seq of its own thread", carol[0])
	}
}

func TestAckAdvancesCursor(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	for i := 1; i <= 5; i++ {
		sendDM(t, s, "alice", "bob", fmt.Sprintf("m%d", i), int64(100+i))
	}
	if err := s.Ack("alice", "phone", "bob", 3); err != nil {
		t.Fatalf("ack 3: %v", err)
	}
	if got := s.Cursor("alice", "bob"); got != 3 {
		t.Fatalf("cursor = %d, want 3", got)
	}
	if err := s.Ack("alice", "phone", "bob", 5); err != nil {
		t.Fatalf("ack 5: %v", err)
	}
	if got := s.Cursor("alice", "bob"); got != 5 {
		t.Fatalf("cursor = %d, want 5", got)
	}
	devices := s.Devices("alice", "bob")
	if len(devices) != 1 || devices[0].Device != "phone" || devices[0].Seq != 5 {
		t.Fatalf("devices = %+v, want phone at 5", devices)
	}
}

func TestCursorMinAcrossDevices(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	for i := 1; i <= 5; i++ {
		sendDM(t, s, "alice", "bob", fmt.Sprintf("m%d", i), int64(100+i))
	}
	if err := s.Ack("alice", "phone", "bob", 5); err != nil {
		t.Fatalf("phone ack: %v", err)
	}
	if err := s.Ack("alice", "tablet", "bob", 2); err != nil {
		t.Fatalf("tablet ack: %v", err)
	}
	if got := s.Cursor("alice", "bob"); got != 2 {
		t.Fatalf("merged cursor = %d, want the slowest device 2", got)
	}
	devices := s.Devices("alice", "bob")
	if len(devices) != 2 || devices[0].Device != "phone" || devices[1].Device != "tablet" {
		t.Fatalf("devices = %+v, want phone then tablet", devices)
	}
	if err := s.Ack("alice", "tablet", "bob", 4); err != nil {
		t.Fatalf("tablet ack 4: %v", err)
	}
	if got := s.Cursor("alice", "bob"); got != 4 {
		t.Fatalf("merged cursor = %d, want 4", got)
	}
}

func TestAckValidation(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "hello", 100)
	if err := s.Ack("", "phone", "bob", 1); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank account = %v, want ErrBadAccount", err)
	}
	if err := s.Ack("alice", "", "bob", 1); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank device = %v, want ErrBadAccount", err)
	}
	if err := s.Ack("alice", "phone", "", 1); !errors.Is(err, impl.ErrBadAccount) {
		t.Fatalf("blank peer = %v, want ErrBadAccount", err)
	}
	if err := s.Ack("alice", "phone", "bob", -1); !errors.Is(err, impl.ErrBadCursor) {
		t.Fatalf("negative seq = %v, want ErrBadCursor", err)
	}
	if err := s.Ack("alice", "phone", "carol", 1); !errors.Is(err, impl.ErrNoConversation) {
		t.Fatalf("unknown conversation = %v, want ErrNoConversation", err)
	}
	if got := s.Cursor("alice", "bob"); got != 0 {
		t.Fatalf("rejected acks moved cursor to %d", got)
	}
}

func TestConversationLookupAndList(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "b1", 100)
	sendDM(t, s, "alice", "carol", "c1", 110)
	sendDM(t, s, "alice", "carol", "c2", 120)
	thread, ok := s.Conversation("alice", "bob")
	if !ok || thread.LastSeq != 1 || thread.Messages != 1 {
		t.Fatalf("bob thread = %+v ok=%v", thread, ok)
	}
	if _, ok := s.Conversation("alice", "dave"); ok {
		t.Fatalf("unknown thread must not be found")
	}
	threads := s.Conversations("alice")
	if len(threads) != 2 || threads[0].Peer != "bob" || threads[1].Peer != "carol" {
		t.Fatalf("conversations = %+v, want bob then carol", threads)
	}
	if threads[1].LastSeq != 2 || threads[1].Messages != 2 {
		t.Fatalf("carol thread = %+v, want last 2 messages 2", threads[1])
	}
	if got := len(s.Conversations("nobody")); got != 0 {
		t.Fatalf("unknown account conversations = %d", got)
	}
}

func TestPruneRemovesOldMessages(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "old", 100)
	sendDM(t, s, "alice", "bob", "mid", 200)
	sendDM(t, s, "alice", "bob", "new", 300)
	dropped, err := s.Prune(400)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("prune dropped %d messages, want 1", dropped)
	}
	left := s.Fetch("alice", "bob", 0)
	if len(left) != 2 || left[0].Seq != 2 || left[1].Seq != 3 {
		t.Fatalf("messages after prune = %+v, want seq 2 and 3", left)
	}
	if _, err := s.Prune(0); !errors.Is(err, impl.ErrBadTime) {
		t.Fatalf("prune at 0 = %v, want ErrBadTime", err)
	}
}

func TestMessageCapDropsOldest(t *testing.T) {
	opts := dmOptions()
	opts.MaxMessagesPerConversation = 3
	s := impl.NewDmCursorStore(opts)
	for i := 1; i <= 5; i++ {
		sendDM(t, s, "alice", "bob", fmt.Sprintf("m%d", i), int64(100+i))
	}
	left := s.Fetch("alice", "bob", 0)
	if len(left) != 3 {
		t.Fatalf("retained %d messages, want 3", len(left))
	}
	for i, want := range []int64{3, 4, 5} {
		if left[i].Seq != want {
			t.Fatalf("retained[%d] seq = %d, want %d", i, left[i].Seq, want)
		}
	}
	thread, ok := s.Conversation("alice", "bob")
	if !ok || thread.LastSeq != 5 || thread.Messages != 3 {
		t.Fatalf("thread after cap = %+v ok=%v, want last 5 messages 3", thread, ok)
	}
}

func TestDevicesListingCopy(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	for i := 1; i <= 4; i++ {
		sendDM(t, s, "alice", "bob", fmt.Sprintf("m%d", i), int64(100+i))
	}
	if err := s.Ack("alice", "tablet", "bob", 2); err != nil {
		t.Fatalf("tablet ack: %v", err)
	}
	if err := s.Ack("alice", "phone", "bob", 4); err != nil {
		t.Fatalf("phone ack: %v", err)
	}
	devices := s.Devices("alice", "bob")
	if len(devices) != 2 || devices[0].Device != "phone" || devices[0].Seq != 4 ||
		devices[1].Device != "tablet" || devices[1].Seq != 2 {
		t.Fatalf("devices = %+v, want phone:4 tablet:2", devices)
	}
	devices[0].Seq = 99
	again := s.Devices("alice", "bob")
	if again[0].Seq != 4 {
		t.Fatalf("Devices must return a copy, got seq %d", again[0].Seq)
	}
	if got := s.Devices("alice", "carol"); len(got) != 0 {
		t.Fatalf("unknown thread devices = %d", len(got))
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	sendDM(t, s, "alice", "bob", "one", 100)
	sendDM(t, s, "bob", "alice", "two", 110)
	if err := s.Ack("alice", "phone", "bob", 1); err != nil {
		t.Fatalf("ack: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewDmCursorStore(dmOptions())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Fetch("alice", "bob", 0); len(got) != 2 {
		t.Fatalf("restored messages = %d, want 2", len(got))
	}
	if got := restored.Cursor("alice", "bob"); got != 1 {
		t.Fatalf("restored cursor = %d, want 1", got)
	}
	next := sendDM(t, restored, "alice", "bob", "three", 120)
	if next.Seq != 3 {
		t.Fatalf("seq after restore = %d, want 3", next.Seq)
	}
	if err := restored.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("bad payload = %v, want ErrBadSnapshot", err)
	}
	if got := len(restored.Fetch("alice", "bob", 0)); got != 3 {
		t.Fatalf("failed restore changed messages to %d", got)
	}
	if err := restored.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("nil payload = %v, want ErrBadSnapshot", err)
	}
}

func TestConcurrentSendUniqueSeq(t *testing.T) {
	s := impl.NewDmCursorStore(impl.Options{MaxMessagesPerConversation: 100, MaxRetentionSeconds: 150})
	var wg sync.WaitGroup
	seqs := make(chan int64, 12)
	errs := make(chan error, 12)
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			m, err := s.Send("alice", "bob", fmt.Sprintf("body-%d", worker), 100)
			if err != nil {
				errs <- err
				return
			}
			seqs <- m.Seq
		}(w)
	}
	wg.Wait()
	close(seqs)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent send failed: %v", err)
	}
	seen := map[int64]bool{}
	for seq := range seqs {
		if seen[seq] {
			t.Fatalf("duplicate sequence %d", seq)
		}
		seen[seq] = true
	}
	for want := int64(1); want <= 12; want++ {
		if !seen[want] {
			t.Fatalf("missing sequence %d, got %v", want, seen)
		}
	}
	thread, ok := s.Conversation("alice", "bob")
	if !ok || thread.LastSeq != 12 || thread.Messages != 12 {
		t.Fatalf("thread after concurrent sends = %+v ok=%v", thread, ok)
	}
	if got := len(s.Fetch("alice", "bob", 0)); got != 12 {
		t.Fatalf("fetch after concurrent sends = %d, want 12", got)
	}
}

func TestConcurrentAckMergesCursor(t *testing.T) {
	s := impl.NewDmCursorStore(dmOptions())
	for i := 1; i <= 4; i++ {
		sendDM(t, s, "alice", "bob", fmt.Sprintf("m%d", i), int64(100+i))
	}
	devices := []string{"d1", "d2", "d3", "d4"}
	var wg sync.WaitGroup
	errs := make(chan error, len(devices))
	for i, device := range devices {
		wg.Add(1)
		go func(name string, seq int64) {
			defer wg.Done()
			if err := s.Ack("alice", name, "bob", seq); err != nil {
				errs <- err
			}
		}(device, int64(i+1))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent ack failed: %v", err)
	}
	if got := s.Cursor("alice", "bob"); got != 1 {
		t.Fatalf("merged cursor = %d, want 1", got)
	}
	if got := s.Devices("alice", "bob"); len(got) != 4 {
		t.Fatalf("devices = %d, want 4", len(got))
	}
}
