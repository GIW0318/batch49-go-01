package task

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch49-go-safety-b-09"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(at time.Time) *manualClock {
	return &manualClock{now: at}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func cacheConfig(clock impl.Clock) impl.Config {
	return impl.Config{MaxSessions: 32, MaxPerSNI: 8, Clock: clock}
}

func sessionAt(id, sni string, at time.Time) impl.Session {
	return impl.Session{
		ID:                id,
		SNI:               sni,
		ALPN:              "h2",
		CipherFamily:      "tls13_aesgcm",
		ClientCertSubject: "CN=client-7",
		Secret:            []byte("secret-" + id),
		IssuedAt:          at,
		ExpiresAt:         at.Add(time.Hour),
	}
}

func helloFor(sess impl.Session) impl.ClientHello {
	return impl.ClientHello{
		SNI:               sess.SNI,
		ALPN:              sess.ALPN,
		CipherFamily:      sess.CipherFamily,
		ClientCertSubject: sess.ClientCertSubject,
	}
}

func TestStoreMintsTicketAndIndexesSession(t *testing.T) {
	clock := newManualClock(time.Unix(20000, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	sess := sessionAt("sid-1", "api.example", clock.Now())
	tid, err := c.Store(sess)
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if tid == "" {
		t.Fatalf("store must return a non empty ticket id")
	}
	got, ok := c.Lookup(tid)
	if !ok {
		t.Fatalf("stored ticket must be found")
	}
	if got.ID != "sid-1" || got.SNI != "api.example" || got.ALPN != "h2" || got.CipherFamily != "tls13_aesgcm" {
		t.Fatalf("stored session = %+v", got)
	}
	if got.ClientCertSubject != "CN=client-7" {
		t.Fatalf("stored client subject = %q", got.ClientCertSubject)
	}
	if !bytes.Equal(got.Secret, sess.Secret) {
		t.Fatalf("stored secret = %q, want %q", got.Secret, sess.Secret)
	}
	if got.KeyGeneration != 1 {
		t.Fatalf("ticket generation = %d, want 1", got.KeyGeneration)
	}
	st := c.Stats()
	if st.Sessions != 1 || st.Hits != 1 || st.KeyGeneration != 1 {
		t.Fatalf("stats after store and lookup = %+v", st)
	}
}

func TestStoreRejectsIncompleteBinding(t *testing.T) {
	clock := newManualClock(time.Unix(20100, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	base := sessionAt("sid-1", "api.example", clock.Now())

	noSNI := base
	noSNI.SNI = "  "
	if _, err := c.Store(noSNI); !errors.Is(err, impl.ErrBadSession) {
		t.Fatalf("blank SNI must fail with ErrBadSession, got %v", err)
	}
	noALPN := base
	noALPN.ALPN = ""
	if _, err := c.Store(noALPN); !errors.Is(err, impl.ErrBadSession) {
		t.Fatalf("blank ALPN must fail with ErrBadSession, got %v", err)
	}
	noCipher := base
	noCipher.CipherFamily = ""
	if _, err := c.Store(noCipher); !errors.Is(err, impl.ErrBadSession) {
		t.Fatalf("blank cipher family must fail with ErrBadSession, got %v", err)
	}
	if got := c.Stats().Sessions; got != 0 {
		t.Fatalf("rejected sessions must not be stored, got %d", got)
	}
}

func TestLookupThroughBothTracks(t *testing.T) {
	clock := newManualClock(time.Unix(20200, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	tid, err := c.Store(sessionAt("sid-2", "api.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if _, ok := c.Lookup(tid); !ok {
		t.Fatalf("ticket track must find the session")
	}
	byID, ok := c.LookupBySessionID("sid-2")
	if !ok || byID.ID != "sid-2" {
		t.Fatalf("session id track returned %+v ok=%v", byID, ok)
	}
	if got := c.Stats().Hits; got != 2 {
		t.Fatalf("hit counter across both tracks = %d, want 2", got)
	}
}

func TestLookupUnknownTicket(t *testing.T) {
	clock := newManualClock(time.Unix(20300, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	if _, ok := c.Lookup(impl.TicketID("ticket-missing")); ok {
		t.Fatalf("unknown ticket must not resolve")
	}
	if got := c.Stats().Misses; got != 1 {
		t.Fatalf("miss counter = %d, want 1", got)
	}
}

func TestResumeWithMatchingHello(t *testing.T) {
	clock := newManualClock(time.Unix(20400, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	sess := sessionAt("sid-3", "api.example", clock.Now())
	tid, err := c.Store(sess)
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	got, ok := c.Resume(tid, helloFor(sess))
	if !ok || got.ID != "sid-3" {
		t.Fatalf("matching resume returned %+v ok=%v", got, ok)
	}
	if got := c.Stats().Resumes; got != 1 {
		t.Fatalf("resume counter = %d, want 1", got)
	}
}

func TestConcurrentStoreRespectsCapacity(t *testing.T) {
	cfg := impl.Config{MaxSessions: 5, MaxPerSNI: 100, Clock: newManualClock(time.Unix(20500, 0))}
	c := impl.NewResumeCache(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sess := sessionAt(fmt.Sprintf("sid-%02d", n), fmt.Sprintf("svc-%02d.example", n), cfg.Clock.Now())
			if _, err := c.Store(sess); err != nil {
				t.Errorf("concurrent store %d failed: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	st := c.Stats()
	if st.Sessions != 5 {
		t.Fatalf("concurrent stores left %d sessions, want exactly the cap 5", st.Sessions)
	}
	if st.Evicted != 55 {
		t.Fatalf("eviction counter = %d, want 55", st.Evicted)
	}
}

func TestRotateKeyKeepsTwoGenerations(t *testing.T) {
	clock := newManualClock(time.Unix(20600, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	if _, err := c.Store(sessionAt("sid-4", "api.example", clock.Now())); err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if err := c.RotateKey(); err != nil {
		t.Fatalf("first rotation must succeed, got %v", err)
	}
	if err := c.RotateKey(); err != nil {
		t.Fatalf("second rotation must succeed, got %v", err)
	}
	gens := c.KeyGenerations()
	if len(gens) != 2 || gens[0] != 2 || gens[1] != 3 {
		t.Fatalf("key generations = %v, want [2 3]", gens)
	}
	st := c.Stats()
	if st.KeyGeneration != 3 || st.Rotations != 2 {
		t.Fatalf("stats after rotations = %+v", st)
	}
}

func TestTicketsOfRetiredGenerationRejected(t *testing.T) {
	clock := newManualClock(time.Unix(20700, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	tid, err := c.Store(sessionAt("sid-5", "api.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if err := c.RotateKey(); err != nil {
		t.Fatalf("rotation must succeed, got %v", err)
	}
	if err := c.RotateKey(); err != nil {
		t.Fatalf("rotation must succeed, got %v", err)
	}
	if _, ok := c.Lookup(tid); ok {
		t.Fatalf("ticket of a retired generation must not resolve")
	}
	if got := c.Stats().Sessions; got != 0 {
		t.Fatalf("retired generation left %d sessions behind", got)
	}
}

func TestPerSNIQuotaEvictsOldestInThatSNI(t *testing.T) {
	cfg := impl.Config{MaxSessions: 10, MaxPerSNI: 2, Clock: newManualClock(time.Unix(20800, 0))}
	c := impl.NewResumeCache(cfg)
	at := cfg.Clock.Now()
	t1, err := c.Store(sessionAt("sid-a1", "a.example", at))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	t2, err := c.Store(sessionAt("sid-a2", "a.example", at))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	t3, err := c.Store(sessionAt("sid-a3", "a.example", at))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if _, ok := c.Lookup(t1); ok {
		t.Fatalf("oldest session of the SNI must be evicted by the quota")
	}
	if _, ok := c.Lookup(t2); !ok {
		t.Fatalf("second session of the SNI must survive")
	}
	if _, ok := c.Lookup(t3); !ok {
		t.Fatalf("newest session of the SNI must survive")
	}
	st := c.Stats()
	if st.Sessions != 2 || st.Evicted != 1 {
		t.Fatalf("stats after SNI quota eviction = %+v", st)
	}
}

func TestGlobalCapacityEvictsOldest(t *testing.T) {
	cfg := impl.Config{MaxSessions: 3, MaxPerSNI: 10, Clock: newManualClock(time.Unix(20900, 0))}
	c := impl.NewResumeCache(cfg)
	at := cfg.Clock.Now()
	var tickets []impl.TicketID
	for i := 0; i < 4; i++ {
		tid, err := c.Store(sessionAt(fmt.Sprintf("sid-g%d", i), fmt.Sprintf("g%d.example", i), at))
		if err != nil {
			t.Fatalf("store %d must succeed, got %v", i, err)
		}
		tickets = append(tickets, tid)
	}
	if _, ok := c.Lookup(tickets[0]); ok {
		t.Fatalf("oldest session must be evicted by the global capacity")
	}
	for i := 1; i < 4; i++ {
		if _, ok := c.Lookup(tickets[i]); !ok {
			t.Fatalf("session %d must survive the global eviction", i)
		}
	}
	st := c.Stats()
	if st.Sessions != 3 || st.Evicted != 1 {
		t.Fatalf("stats after global eviction = %+v", st)
	}
}

func TestInvalidateSNIRemovesMatchingSessions(t *testing.T) {
	clock := newManualClock(time.Unix(21000, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	t1, err := c.Store(sessionAt("sid-x1", "x.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	t2, err := c.Store(sessionAt("sid-x2", "x.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	t3, err := c.Store(sessionAt("sid-y1", "y.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if got := c.InvalidateSNI("x.example"); got != 2 {
		t.Fatalf("invalidate returned %d, want 2", got)
	}
	if _, ok := c.Lookup(t1); ok {
		t.Fatalf("invalidated session x1 must be gone")
	}
	if _, ok := c.Lookup(t2); ok {
		t.Fatalf("invalidated session x2 must be gone")
	}
	if _, ok := c.Lookup(t3); !ok {
		t.Fatalf("session of another SNI must survive")
	}
	if got := c.Stats().Sessions; got != 1 {
		t.Fatalf("sessions after invalidation = %d, want 1", got)
	}
}

func TestExpiredTicketRejected(t *testing.T) {
	clock := newManualClock(time.Unix(21100, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	sess := sessionAt("sid-e1", "e.example", clock.Now())
	sess.ExpiresAt = clock.Now().Add(time.Minute)
	tid, err := c.Store(sess)
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, ok := c.Lookup(tid); ok {
		t.Fatalf("expired ticket must not resolve")
	}
	st := c.Stats()
	if st.Expired != 1 || st.Sessions != 0 {
		t.Fatalf("stats after expiry = %+v", st)
	}
}

func TestPurgeExpiredCountsRemovals(t *testing.T) {
	clock := newManualClock(time.Unix(21200, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	expiring := sessionAt("sid-e2", "e.example", clock.Now())
	expiring.ExpiresAt = clock.Now().Add(time.Minute)
	expiring2 := sessionAt("sid-e3", "e.example", clock.Now())
	expiring2.ExpiresAt = clock.Now().Add(time.Minute)
	good := sessionAt("sid-ok", "ok.example", clock.Now())
	for _, sess := range []impl.Session{expiring, expiring2, good} {
		if _, err := c.Store(sess); err != nil {
			t.Fatalf("store must succeed, got %v", err)
		}
	}
	clock.Advance(2 * time.Minute)
	if got := c.PurgeExpired(); got != 2 {
		t.Fatalf("purge removed %d sessions, want 2", got)
	}
	if got := c.Stats().Sessions; got != 1 {
		t.Fatalf("sessions after purge = %d, want 1", got)
	}
	if _, ok := c.LookupBySessionID("sid-ok"); !ok {
		t.Fatalf("live session must survive the purge")
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	clock := newManualClock(time.Unix(21300, 0))
	cfg := cacheConfig(clock)
	c := impl.NewResumeCache(cfg)
	t1, err := c.Store(sessionAt("sid-s1", "s1.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	t2, err := c.Store(sessionAt("sid-s2", "s2.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	blob, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewResumeCache(cfg)
	if err := restored.Restore(blob); err != nil {
		t.Fatalf("restore must accept a snapshot written by Snapshot, got %v", err)
	}
	for _, tid := range []impl.TicketID{t1, t2} {
		if _, ok := restored.Lookup(tid); !ok {
			t.Fatalf("restored cache must still resolve ticket %q", tid)
		}
	}
	st := restored.Stats()
	if st.Sessions != 2 || st.KeyGeneration != 1 {
		t.Fatalf("restored stats = %+v", st)
	}
	gens := restored.KeyGenerations()
	if len(gens) != 1 || gens[0] != 1 {
		t.Fatalf("restored key generations = %v, want [1]", gens)
	}
}

func TestRestoreRejectsBadSnapshot(t *testing.T) {
	clock := newManualClock(time.Unix(21400, 0))
	c := impl.NewResumeCache(cacheConfig(clock))
	tid, err := c.Store(sessionAt("sid-r1", "r.example", clock.Now()))
	if err != nil {
		t.Fatalf("store must succeed, got %v", err)
	}
	if err := c.Restore([]byte("---broken---")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := c.Lookup(tid); !ok {
		t.Fatalf("failed restore must leave the live cache intact")
	}
}
