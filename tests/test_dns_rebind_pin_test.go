package task

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	impl "gsb/batch49-go-safety-b-02"
)

type fakeResolver struct {
	mu    sync.Mutex
	calls int
	table map[string][]impl.Addr
	ttl   int64
	fail  error
	empty bool
}

func (r *fakeResolver) lookup(host string) ([]impl.Addr, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail != nil {
		return nil, 0, r.fail
	}
	if r.empty {
		return nil, r.ttl, nil
	}
	addrs, ok := r.table[host]
	if !ok {
		return nil, r.ttl, errors.New("no such host")
	}
	out := make([]impl.Addr, len(addrs))
	copy(out, addrs)
	return out, r.ttl, nil
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newPin(t *testing.T, r *fakeResolver, now *int64) *impl.RebindPin {
	t.Helper()
	return impl.NewRebindPin(impl.PinConfig{
		MinTTLMillis:      5000,
		MaxPinsPerSession: 4,
		Lookup:            r.lookup,
		Now:               func() int64 { return *now },
	})
}

func publicResolver() *fakeResolver {
	return &fakeResolver{
		table: map[string][]impl.Addr{
			"a.example": {{IP: "93.184.216.34"}},
			"b.example": {{IP: "203.0.113.9"}},
			"c.example": {{IP: "198.51.100.7"}},
		},
		ttl: 30000,
	}
}

func TestResolvePinsFirstLookupSet(t *testing.T) {
	now := int64(1000)
	r := publicResolver()
	p := newPin(t, r, &now)
	pin, err := p.Resolve("sess-1", "a.example")
	if err != nil {
		t.Fatalf("first resolve must pin the host: %v", err)
	}
	if pin.SessionID != "sess-1" || pin.Host != "a.example" {
		t.Fatalf("pin identity = %+v, want sess-1 on a.example", pin)
	}
	if len(pin.Addrs) != 1 || pin.Addrs[0] != "93.184.216.34" {
		t.Fatalf("pinned addrs = %v, want the single first answer", pin.Addrs)
	}
	if pin.Gen != 1 {
		t.Fatalf("pin generation = %d, want 1 for the first resolution", pin.Gen)
	}
	if pin.FirstAt != 1000 {
		t.Fatalf("first at = %d, want the injected clock 1000", pin.FirstAt)
	}
	if pin.TTLMillis != 30000 {
		t.Fatalf("ttl = %d, want the resolver ttl 30000 above the configured floor", pin.TTLMillis)
	}
	if pin.ExpiresAt != 31000 {
		t.Fatalf("expires at = %d, want first at plus ttl = 31000", pin.ExpiresAt)
	}
	if got := p.PinCount("sess-1"); got != 1 {
		t.Fatalf("pin count = %d, want 1", got)
	}
	if got := p.Generations("sess-1", "a.example"); got != 1 {
		t.Fatalf("generation reader = %d, want 1", got)
	}
	stored, ok := p.PinnedFor("sess-1", "a.example")
	if !ok {
		t.Fatalf("expected a readable pin record")
	}
	if stored.ExpiresAt != 31000 || stored.Gen != 1 {
		t.Fatalf("stored pin = %+v, want expires 31000 at generation 1", stored)
	}
	sessions := p.Sessions()
	if len(sessions) != 1 || sessions[0] != "sess-1" {
		t.Fatalf("sessions = %v, want exactly sess-1", sessions)
	}
	if _, ok := p.PinnedFor("sess-1", "b.example"); ok {
		t.Fatalf("an unpinned host must not be readable")
	}
}

func TestResolveRejectsBlankInput(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("  ", "a.example"); !errors.Is(err, impl.ErrBlankSession) {
		t.Fatalf("blank session must fail with ErrBlankSession, got %v", err)
	}
	if _, err := p.Resolve("sess-1", "   "); !errors.Is(err, impl.ErrBlankHost) {
		t.Fatalf("blank host must fail with ErrBlankHost, got %v", err)
	}
	if got := p.PinCount("sess-1"); got != 0 {
		t.Fatalf("rejected resolves must not create pins, got %d", got)
	}
	if got := len(p.Sessions()); got != 0 {
		t.Fatalf("rejected resolves must not register a session, got %d", got)
	}
}

func TestResolveEnforcesPinCap(t *testing.T) {
	now := int64(1000)
	r := publicResolver()
	p := impl.NewRebindPin(impl.PinConfig{
		MinTTLMillis:      5000,
		MaxPinsPerSession: 2,
		Lookup:            r.lookup,
		Now:               func() int64 { return now },
	})
	for _, host := range []string{"a.example", "b.example"} {
		if _, err := p.Resolve("sess-1", host); err != nil {
			t.Fatalf("resolve %s under the cap: %v", host, err)
		}
	}
	if _, err := p.Resolve("sess-1", "c.example"); !errors.Is(err, impl.ErrPinLimit) {
		t.Fatalf("the third pinned host must fail with ErrPinLimit, got %v", err)
	}
	if got := p.PinCount("sess-1"); got != 2 {
		t.Fatalf("pin count = %d, want the configured cap 2", got)
	}
	if _, ok := p.PinnedFor("sess-1", "c.example"); ok {
		t.Fatalf("a refused host must not be pinned")
	}
	if _, err := p.Resolve("sess-2", "c.example"); err != nil {
		t.Fatalf("another session must not be affected by the cap, got %v", err)
	}
}

func TestResolveReusesPinWithinTTL(t *testing.T) {
	now := int64(1000)
	r := publicResolver()
	p := newPin(t, r, &now)
	first, err := p.Resolve("sess-1", "a.example")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	now += 900
	second, err := p.Resolve("sess-1", "a.example")
	if err != nil {
		t.Fatalf("second resolve inside the ttl: %v", err)
	}
	if second.Gen != first.Gen {
		t.Fatalf("generation moved from %d to %d inside the ttl, want it stable", first.Gen, second.Gen)
	}
	if second.ExpiresAt != first.ExpiresAt || second.FirstAt != first.FirstAt {
		t.Fatalf("pin window moved inside the ttl: first=%+v second=%+v", first, second)
	}
	if got := r.callCount(); got != 1 {
		t.Fatalf("resolver was called %d times inside the ttl, want 1", got)
	}
	if _, ok := p.PinnedFor("sess-1", "a.example"); !ok {
		t.Fatalf("expected the pin to stay readable")
	}
}

func TestVerifyAcceptsPinnedAddress(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	v, err := p.Verify("sess-1", []impl.Addr{{IP: "93.184.216.34"}})
	if err != nil {
		t.Fatalf("verifying the pinned address must pass: %v", err)
	}
	if v.Rebound {
		t.Fatalf("verdict = %+v, want no rebinding for the pinned address", v)
	}
	if v.Reason != impl.ReasonPinOK {
		t.Fatalf("reason = %q, want %q", v.Reason, impl.ReasonPinOK)
	}
	if len(v.Allowed) != 1 || v.Allowed[0] != "93.184.216.34" {
		t.Fatalf("allowed = %v, want exactly the pinned address", v.Allowed)
	}
	if v.Gen != 1 || v.SessionID != "sess-1" || v.Host != "a.example" {
		t.Fatalf("verdict header = %+v, want sess-1 on a.example at generation 1", v)
	}
	if len(v.Rejected) != 0 {
		t.Fatalf("rejected = %v, want nothing rejected on a clean verification", v.Rejected)
	}
}

func TestVerifyRejectsRebindingWhenDisjoint(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	v, err := p.Verify("sess-1", []impl.Addr{{IP: "81.2.69.142"}})
	if !errors.Is(err, impl.ErrRebindDetected) {
		t.Fatalf("a disjoint resolution must fail with ErrRebindDetected, got %v", err)
	}
	if !v.Rebound {
		t.Fatalf("verdict = %+v, want the rebound flag", v)
	}
	if v.Reason != impl.ReasonRebinding {
		t.Fatalf("reason = %q, want %q", v.Reason, impl.ReasonRebinding)
	}
	if len(v.Allowed) != 0 {
		t.Fatalf("allowed = %v, want no allowed address for a rebinding", v.Allowed)
	}
	if got := p.Generations("sess-1", "a.example"); got != 1 {
		t.Fatalf("generation = %d, a rebinding verdict must not rewrite the pin", got)
	}
}

func TestVerifyRejectsLoopbackAndLinkLocal(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cases := []struct {
		ip     string
		reason string
	}{
		{"127.0.0.1", impl.ReasonLoopback},
		{"::1", impl.ReasonLoopback},
		{"169.254.10.7", impl.ReasonLinkLocal},
		{"fe80::1", impl.ReasonLinkLocal},
	}
	for _, c := range cases {
		v, err := p.Verify("sess-1", []impl.Addr{{IP: c.ip}})
		if !errors.Is(err, impl.ErrBlockedAddr) {
			t.Fatalf("verifying %s must fail with ErrBlockedAddr, got %v", c.ip, err)
		}
		if v.Reason != c.reason {
			t.Fatalf("verifying %s gave reason %q, want %q", c.ip, v.Reason, c.reason)
		}
		if v.Rebound {
			t.Fatalf("verifying %s must be reported as a blocked family, not a rebinding", c.ip)
		}
	}
}

func TestVerifyRejectsPrivateAndCGNAT(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cases := []struct {
		ip     string
		reason string
	}{
		{"10.5.5.5", impl.ReasonPrivate},
		{"172.20.3.4", impl.ReasonPrivate},
		{"192.168.9.9", impl.ReasonPrivate},
		{"fd00::7", impl.ReasonPrivate},
		{"100.64.9.9", impl.ReasonCGNAT},
		{"100.127.255.254", impl.ReasonCGNAT},
	}
	for _, c := range cases {
		v, err := p.Verify("sess-1", []impl.Addr{{IP: c.ip}})
		if !errors.Is(err, impl.ErrBlockedAddr) {
			t.Fatalf("verifying %s must fail with ErrBlockedAddr, got %v", c.ip, err)
		}
		if v.Reason != c.reason {
			t.Fatalf("verifying %s gave reason %q, want %q", c.ip, v.Reason, c.reason)
		}
	}
}

func TestVerifyUnknownSession(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Verify("sess-404", []impl.Addr{{IP: "93.184.216.34"}}); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("verifying a session without pins must fail with ErrUnknownSession, got %v", err)
	}
	if _, err := p.Verify("  ", []impl.Addr{{IP: "93.184.216.34"}}); !errors.Is(err, impl.ErrBlankSession) {
		t.Fatalf("verifying a blank session must fail with ErrBlankSession, got %v", err)
	}
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := p.Verify("sess-1", nil); !errors.Is(err, impl.ErrBadAddress) {
		t.Fatalf("verifying an empty address set must fail with ErrBadAddress, got %v", err)
	}
	if _, err := p.Verify("sess-1", []impl.Addr{{IP: "not-an-ip"}}); !errors.Is(err, impl.ErrBadAddress) {
		t.Fatalf("verifying an unparsable address must fail with ErrBadAddress, got %v", err)
	}
}

func TestReleaseRemovesSessionPins(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	for _, host := range []string{"a.example", "b.example"} {
		if _, err := p.Resolve("sess-1", host); err != nil {
			t.Fatalf("resolve %s: %v", host, err)
		}
	}
	if _, err := p.Resolve("sess-2", "a.example"); err != nil {
		t.Fatalf("resolve for the second session: %v", err)
	}
	p.Release("sess-1")
	if got := p.PinCount("sess-1"); got != 0 {
		t.Fatalf("pin count after release = %d, want 0", got)
	}
	if _, ok := p.PinnedFor("sess-1", "a.example"); ok {
		t.Fatalf("a released session must not keep a readable pin")
	}
	if got := p.PinCount("sess-2"); got != 1 {
		t.Fatalf("releasing one session must not touch another, got %d pins", got)
	}
	sessions := p.Sessions()
	if len(sessions) != 1 || sessions[0] != "sess-2" {
		t.Fatalf("sessions after release = %v, want only sess-2", sessions)
	}
	p.Release("sess-404")
	if got := p.PinCount("sess-2"); got != 1 {
		t.Fatalf("releasing an unknown session must be a no-op, got %d pins", got)
	}
}

func TestConcurrentVerifyIsSerialised(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var ok, failed int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				v, err := p.Verify("sess-1", []impl.Addr{{IP: "93.184.216.34"}})
				if err != nil || len(v.Allowed) != 1 {
					atomic.AddInt64(&failed, 1)
					continue
				}
				atomic.AddInt64(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent verifications failed, want none", failed)
	}
	if ok != 160 {
		t.Fatalf("concurrent verifications succeeded %d times, want 160", ok)
	}
	if got := p.PinCount("sess-1"); got != 1 {
		t.Fatalf("concurrent verification must not churn the pin table, got %d pins", got)
	}
}

func TestSnapshotRestoreKeepsPinsAndGeneration(t *testing.T) {
	now := int64(1000)
	r := publicResolver()
	p := newPin(t, r, &now)
	for _, host := range []string{"a.example", "b.example"} {
		if _, err := p.Resolve("sess-1", host); err != nil {
			t.Fatalf("resolve %s: %v", host, err)
		}
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewRebindPin(impl.PinConfig{
		MinTTLMillis:      5000,
		MaxPinsPerSession: 4,
		Lookup:            r.lookup,
		Now:               func() int64 { return now },
	})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.PinCount("sess-1"); got != 2 {
		t.Fatalf("restored pin count = %d, want 2", got)
	}
	pin, ok := restored.PinnedFor("sess-1", "b.example")
	if !ok {
		t.Fatalf("restored table lost b.example")
	}
	if len(pin.Addrs) != 1 || pin.Addrs[0] != "203.0.113.9" || pin.ExpiresAt != 31000 || pin.Gen != 1 {
		t.Fatalf("restored pin = %+v, want the same address, deadline and generation", pin)
	}
	v, err := restored.Verify("sess-1", []impl.Addr{{IP: "203.0.113.9"}})
	if err != nil {
		t.Fatalf("verify after restore: %v", err)
	}
	if v.Gen != 1 || len(v.Allowed) != 1 {
		t.Fatalf("verdict after restore = %+v, want the restored pin to be usable", v)
	}
	if _, err := restored.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve after restore: %v", err)
	}
	if got := r.callCount(); got != 2 {
		t.Fatalf("resolver was called %d times, want the restored pins to be reused without a new lookup", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, payload := range [][]byte{nil, []byte(""), []byte("{not-a-snapshot"), []byte(`{"pins":`)} {
		if err := p.Restore(payload); !errors.Is(err, impl.ErrBadSnapshot) {
			t.Fatalf("restoring %q must fail with ErrBadSnapshot, got %v", string(payload), err)
		}
	}
	if got := p.PinCount("sess-1"); got != 1 {
		t.Fatalf("a rejected restore must not touch the live table, got %d pins", got)
	}
	if _, err := p.Verify("sess-1", []impl.Addr{{IP: "93.184.216.34"}}); err != nil {
		t.Fatalf("the live pin must keep working after a rejected restore, got %v", err)
	}
	if _, err := p.Resolve("sess-1", "a.example"); err != nil {
		t.Fatalf("resolve after a rejected restore: %v", err)
	}
}

func TestResolvePropagatesLookupFailure(t *testing.T) {
	now := int64(1000)
	r := publicResolver()
	r.fail = errors.New("servfail")
	p := newPin(t, r, &now)
	if _, err := p.Resolve("sess-1", "a.example"); !errors.Is(err, impl.ErrLookupFailed) {
		t.Fatalf("a failing resolver must surface ErrLookupFailed, got %v", err)
	}
	if got := p.PinCount("sess-1"); got != 0 {
		t.Fatalf("a failed lookup must not pin anything, got %d", got)
	}
	empty := publicResolver()
	empty.empty = true
	p2 := newPin(t, empty, &now)
	if _, err := p2.Resolve("sess-1", "a.example"); !errors.Is(err, impl.ErrBadAddress) {
		t.Fatalf("an empty answer must fail with ErrBadAddress, got %v", err)
	}
	private := &fakeResolver{
		table: map[string][]impl.Addr{"intranet.example": {{IP: "10.9.9.9"}}},
		ttl:   30000,
	}
	p3 := newPin(t, private, &now)
	if _, err := p3.Resolve("sess-1", "intranet.example"); !errors.Is(err, impl.ErrBlockedAddr) {
		t.Fatalf("resolving to a private address must fail with ErrBlockedAddr, got %v", err)
	}
	if got := p3.PinCount("sess-1"); got != 0 {
		t.Fatalf("a refused address family must not be pinned, got %d", got)
	}
}

func TestClassifyAddressFamilies(t *testing.T) {
	now := int64(1000)
	p := newPin(t, publicResolver(), &now)
	cases := []struct {
		ip   string
		want string
	}{
		{"127.0.0.1", impl.ReasonLoopback},
		{"127.9.9.9", impl.ReasonLoopback},
		{"::1", impl.ReasonLoopback},
		{"10.1.2.3", impl.ReasonPrivate},
		{"172.16.0.1", impl.ReasonPrivate},
		{"172.31.255.254", impl.ReasonPrivate},
		{"192.168.1.1", impl.ReasonPrivate},
		{"fc00::1", impl.ReasonPrivate},
		{"169.254.1.1", impl.ReasonLinkLocal},
		{"fe80::1", impl.ReasonLinkLocal},
		{"100.64.0.1", impl.ReasonCGNAT},
		{"100.100.100.100", impl.ReasonCGNAT},
		{"8.8.8.8", impl.ReasonPublic},
		{"172.32.0.1", impl.ReasonPublic},
		{"2001:4860:4860::8888", impl.ReasonPublic},
		{"not-an-ip", impl.ReasonInvalid},
		{"", impl.ReasonInvalid},
	}
	for _, c := range cases {
		if got := p.Classify(c.ip); got != c.want {
			t.Fatalf("classify %q = %q, want %q", c.ip, got, c.want)
		}
	}
}
