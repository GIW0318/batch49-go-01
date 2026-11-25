package task

import (
	"errors"
	"reflect"
	"testing"
	"time"

	impl "gsb/batch49-go-fix-a-04"
)

var dnsBase = time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)

func dnsConfig() impl.ResolverConfig {
	return impl.ResolverConfig{
		MaxCacheEntries: 8,
		MinTTL:          30,
		MaxTTL:          300,
		MaxCNAME:        4,
	}
}

func newGuard() *impl.DnssecCacheGuard {
	g := impl.NewDnssecCacheGuard(dnsConfig())
	g.SetNow(dnsBase)
	_ = g.SetTrustAnchor("example.", impl.DsRecord{Zone: "example.", KeyTag: 1001, Algorithm: "RSASHA256", Digest: "ab12"})
	_ = g.AddZoneKey("example.", impl.DnskeyRecord{Zone: "example.", KeyTag: 1001, Flags: 257, Algorithm: "RSASHA256", PublicKey: "pub-1001"})
	return g
}

func queryA() impl.Query {
	return impl.Query{
		Name:     "www.example.",
		Type:     "A",
		Class:    "IN",
		DO:       true,
		ID:       0x4242,
		CaseName: "wWw.ExAmPlE.",
	}
}

func sigFor(covered string) impl.Rrsig {
	return impl.Rrsig{
		TypeCovered: covered,
		SignerName:  "example.",
		KeyTag:      1001,
		Inception:   dnsBase.Add(-time.Hour),
		Expiration:  dnsBase.Add(time.Hour),
		Labels:      2,
		Valid:       true,
	}
}

func aRRset() impl.RRset {
	return impl.RRset{
		Name:  "www.example.",
		Type:  "A",
		Class: "IN",
		TTL:   60,
		Records: []impl.RR{
			{Name: "www.example.", Type: "A", TTL: 60, RData: "192.0.2.10"},
		},
		Sigs: []impl.Rrsig{sigFor("A")},
	}
}

func responseA() impl.Response {
	return impl.Response{
		ID:           0x4242,
		QuestionName: "wWw.ExAmPlE.",
		Zone:         "example.",
		Answer:       []impl.RRset{aRRset()},
	}
}

func TestResolveAcceptsSignedAnswer(t *testing.T) {
	g := newGuard()
	ans, err := g.Resolve(queryA(), responseA())
	if err != nil {
		t.Fatalf("signed answer must be accepted, got %v", err)
	}
	if !ans.Secure || ans.Denial {
		t.Fatalf("answer flags = secure:%v denial:%v, want secure and not a denial", ans.Secure, ans.Denial)
	}
	if !reflect.DeepEqual(ans.RData, []string{"192.0.2.10"}) {
		t.Fatalf("answer rdata = %v", ans.RData)
	}
	if ans.TTL != 60 {
		t.Fatalf("answer ttl = %d, want 60", ans.TTL)
	}
	if !ans.ExpiresAt.Equal(dnsBase.Add(60 * time.Second)) {
		t.Fatalf("expires at = %v, want %v", ans.ExpiresAt, dnsBase.Add(60*time.Second))
	}
	if got := g.Stats().Entries; got != 1 {
		t.Fatalf("cache entries = %d, want 1", got)
	}
}

func TestResolveRejectsBadQueries(t *testing.T) {
	g := newGuard()
	resp := responseA()
	blankName := queryA()
	blankName.Name = ""
	if _, err := g.Resolve(blankName, resp); !errors.Is(err, impl.ErrBlankQuery) {
		t.Fatalf("blank name must be rejected, got %v", err)
	}
	blankType := queryA()
	blankType.Type = ""
	if _, err := g.Resolve(blankType, resp); !errors.Is(err, impl.ErrBlankQuery) {
		t.Fatalf("blank type must be rejected, got %v", err)
	}
	relative := queryA()
	relative.Name = "www.example"
	if _, err := g.Resolve(relative, resp); !errors.Is(err, impl.ErrNotAbsolute) {
		t.Fatalf("relative name must be rejected, got %v", err)
	}
	chaos := queryA()
	chaos.Class = "CH"
	if _, err := g.Resolve(chaos, resp); !errors.Is(err, impl.ErrUnsupportedClass) {
		t.Fatalf("non IN class must be rejected, got %v", err)
	}
}

func TestResolveRejectsTransactionIDMismatch(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.ID = 0x1111
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrIDMismatch) {
		t.Fatalf("mismatched transaction id must be rejected, got %v", err)
	}
}

func TestResolveRejectsCaseMismatch(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.QuestionName = "www.example."
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrCaseMismatch) {
		t.Fatalf("question name that ignores the 0x20 case must be rejected, got %v", err)
	}
}

func TestResolveRejectsOutOfBailiwickData(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.Answer[0].Name = "www.example.evil."
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrBailiwickViolation) {
		t.Fatalf("answer outside the answering zone must be rejected, got %v", err)
	}
	g2 := newGuard()
	resp2 := responseA()
	resp2.Authority = []impl.RRset{{
		Name:  "evil.test.",
		Type:  "NS",
		Class: "IN",
		TTL:   60,
		Records: []impl.RR{
			{Name: "evil.test.", Type: "NS", TTL: 60, RData: "ns.evil.test."},
		},
	}}
	ans, err := g2.Resolve(queryA(), resp2)
	if err != nil {
		t.Fatalf("answer section itself is valid, got %v", err)
	}
	if !ans.Secure {
		t.Fatalf("answer must stay secure after dropping out of bailiwick glue")
	}
	if got := g2.Stats().Poisoned; got != 1 {
		t.Fatalf("poisoned counter = %d, want 1 dropped authority record", got)
	}
}

func TestResolveRequiresSignature(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.Answer[0].Sigs = nil
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrSignatureMissing) {
		t.Fatalf("unsigned rrset must be rejected, got %v", err)
	}
}

func TestResolveRejectsInvalidSignature(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.Answer[0].Sigs[0].Valid = false
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrSignatureInvalid) {
		t.Fatalf("invalid signature must be rejected, got %v", err)
	}
}

func TestResolveRejectsExpiredSignature(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.Answer[0].Sigs[0].Expiration = dnsBase.Add(-time.Minute)
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrSignatureExpired) {
		t.Fatalf("signature outside its window must be rejected, got %v", err)
	}
}

func TestResolveFollowsCnameChain(t *testing.T) {
	g := newGuard()
	cname := impl.RRset{
		Name:  "www.example.",
		Type:  "CNAME",
		Class: "IN",
		TTL:   120,
		Records: []impl.RR{
			{Name: "www.example.", Type: "CNAME", TTL: 120, RData: "cdn.example."},
		},
		Sigs: []impl.Rrsig{sigFor("CNAME")},
	}
	final := impl.RRset{
		Name:  "cdn.example.",
		Type:  "A",
		Class: "IN",
		TTL:   45,
		Records: []impl.RR{
			{Name: "cdn.example.", Type: "A", TTL: 45, RData: "192.0.2.20"},
		},
		Sigs: []impl.Rrsig{sigFor("A")},
	}
	resp := impl.Response{
		ID:           0x4242,
		QuestionName: "wWw.ExAmPlE.",
		Zone:         "example.",
		Answer:       []impl.RRset{cname, final},
	}
	ans, err := g.Resolve(queryA(), resp)
	if err != nil {
		t.Fatalf("cname chain must be followed, got %v", err)
	}
	if !reflect.DeepEqual(ans.RData, []string{"192.0.2.20"}) {
		t.Fatalf("final rdata = %v, want the cname target address", ans.RData)
	}
	if ans.TTL != 45 {
		t.Fatalf("chain ttl = %d, want the minimum 45", ans.TTL)
	}
	if !ans.Secure {
		t.Fatalf("validated chain must be secure")
	}
}

func TestResolveRejectsCnameLoop(t *testing.T) {
	g := newGuard()
	first := impl.RRset{
		Name:    "www.example.",
		Type:    "CNAME",
		Class:   "IN",
		TTL:     60,
		Records: []impl.RR{{Name: "www.example.", Type: "CNAME", TTL: 60, RData: "a.example."}},
		Sigs:    []impl.Rrsig{sigFor("CNAME")},
	}
	second := impl.RRset{
		Name:    "a.example.",
		Type:    "CNAME",
		Class:   "IN",
		TTL:     60,
		Records: []impl.RR{{Name: "a.example.", Type: "CNAME", TTL: 60, RData: "www.example."}},
		Sigs:    []impl.Rrsig{sigFor("CNAME")},
	}
	resp := impl.Response{
		ID:           0x4242,
		QuestionName: "wWw.ExAmPlE.",
		Zone:         "example.",
		Answer:       []impl.RRset{first, second},
	}
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrCnameLoop) {
		t.Fatalf("cname loop must be rejected, got %v", err)
	}
}

func TestResolveClampsTTLToConfiguredBounds(t *testing.T) {
	g := newGuard()
	long := responseA()
	long.Answer[0].TTL = 9999
	long.Answer[0].Records[0].TTL = 9999
	ans, err := g.Resolve(queryA(), long)
	if err != nil {
		t.Fatalf("resolve long ttl: %v", err)
	}
	if ans.TTL != 300 {
		t.Fatalf("ttl above the ceiling = %d, want 300", ans.TTL)
	}
	g2 := newGuard()
	short := responseA()
	short.Answer[0].TTL = 1
	short.Answer[0].Records[0].TTL = 1
	ans, err = g2.Resolve(queryA(), short)
	if err != nil {
		t.Fatalf("resolve short ttl: %v", err)
	}
	if ans.TTL != 30 {
		t.Fatalf("ttl below the floor = %d, want 30", ans.TTL)
	}
}

func TestCacheGetHonoursExpiryAndDofKey(t *testing.T) {
	g := newGuard()
	if _, err := g.Resolve(queryA(), responseA()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cached, ok := g.CacheGet(queryA())
	if !ok {
		t.Fatalf("resolved answer must be cacheable")
	}
	if !reflect.DeepEqual(cached.RData, []string{"192.0.2.10"}) {
		t.Fatalf("cached rdata = %v", cached.RData)
	}
	plain := queryA()
	plain.DO = false
	if _, ok := g.CacheGet(plain); ok {
		t.Fatalf("cache key must include the DO bit")
	}
	g.SetNow(dnsBase.Add(61 * time.Second))
	if _, ok := g.CacheGet(queryA()); ok {
		t.Fatalf("expired entry must not be served")
	}
}

func TestResolveValidatesDenialOfExistence(t *testing.T) {
	g := newGuard()
	soa := impl.RRset{
		Name:  "example.",
		Type:  "SOA",
		Class: "IN",
		TTL:   60,
		Records: []impl.RR{
			{Name: "example.", Type: "SOA", TTL: 60, RData: "ns.example. hostmaster.example. 1 60 60 60 60"},
		},
	}
	nsec := impl.RRset{
		Name:  "www.example.",
		Type:  "NSEC",
		Class: "IN",
		TTL:   60,
		Records: []impl.RR{
			{Name: "www.example.", Type: "NSEC", TTL: 60, RData: "next.example. A RRSIG NSEC"},
		},
		Sigs: []impl.Rrsig{sigFor("NSEC")},
	}
	resp := impl.Response{
		ID:           0x4242,
		QuestionName: "wWw.ExAmPlE.",
		Zone:         "example.",
		Authority:    []impl.RRset{soa, nsec},
	}
	ans, err := g.Resolve(queryA(), resp)
	if err != nil {
		t.Fatalf("signed denial must be accepted, got %v", err)
	}
	if !ans.Denial || !ans.Secure {
		t.Fatalf("denial answer flags = denial:%v secure:%v", ans.Denial, ans.Secure)
	}
	if ans.TTL != 60 {
		t.Fatalf("denial ttl = %d, want the soa ttl 60", ans.TTL)
	}
	if got := g.Stats().Denials; got != 1 {
		t.Fatalf("denial counter = %d, want 1", got)
	}
	g2 := newGuard()
	missing := impl.Response{
		ID:           0x4242,
		QuestionName: "wWw.ExAmPlE.",
		Zone:         "example.",
		Authority:    []impl.RRset{soa},
	}
	if _, err := g2.Resolve(queryA(), missing); !errors.Is(err, impl.ErrDenialInvalid) {
		t.Fatalf("denial without a proof must be rejected, got %v", err)
	}
}

func TestResolveRejectsUntrustedSigner(t *testing.T) {
	g := newGuard()
	resp := responseA()
	resp.Answer[0].Sigs[0].SignerName = "other."
	if _, err := g.Resolve(queryA(), resp); !errors.Is(err, impl.ErrNoTrustAnchor) {
		t.Fatalf("signer outside the anchors must be rejected, got %v", err)
	}
	g2 := newGuard()
	unknownKey := responseA()
	unknownKey.Answer[0].Sigs[0].KeyTag = 9999
	if _, err := g2.Resolve(queryA(), unknownKey); !errors.Is(err, impl.ErrKeyUnknown) {
		t.Fatalf("unknown signing key must be rejected, got %v", err)
	}
}

func TestSnapshotRestoreKeepsAnchorsAndCache(t *testing.T) {
	g := newGuard()
	if _, err := g.Resolve(queryA(), responseA()); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := impl.NewDnssecCacheGuard(dnsConfig())
	restored.SetNow(dnsBase)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Stats() != g.Stats() {
		t.Fatalf("restored stats = %+v, want %+v", restored.Stats(), g.Stats())
	}
	ans, ok := restored.CacheGet(queryA())
	if !ok || !reflect.DeepEqual(ans.RData, []string{"192.0.2.10"}) {
		t.Fatalf("restored cache must serve the validated answer, got %+v (found=%v)", ans, ok)
	}
	more := impl.Response{
		ID:           0x4242,
		QuestionName: "wWw.ExAmPlE.",
		Zone:         "example.",
		Answer:       []impl.RRset{aRRset()},
	}
	if _, err := restored.Resolve(queryA(), more); err != nil {
		t.Fatalf("restored anchors and keys must still validate, got %v", err)
	}
	before := restored.Stats()
	if err := restored.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if restored.Stats() != before {
		t.Fatalf("failed restore mutated the cache: %+v -> %+v", before, restored.Stats())
	}
}
