// Package task validates DNSSEC signed answers, rejects out of bailiwick
// data and keeps a bounded cache of validated lookups.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Errors returned by the validating cache.
var (
	ErrBlankQuery          = errors.New("query name and type are required")
	ErrNotAbsolute         = errors.New("query name must be fully qualified")
	ErrUnsupportedClass    = errors.New("only the IN class is supported")
	ErrIDMismatch          = errors.New("response transaction id does not match the query")
	ErrCaseMismatch        = errors.New("response question name does not echo the query case")
	ErrBailiwickViolation  = errors.New("record owner is outside the answering zone")
	ErrSignatureMissing    = errors.New("rrset has no signature")
	ErrSignatureInvalid    = errors.New("rrset signature is not valid")
	ErrSignatureExpired    = errors.New("rrset signature is outside its validity window")
	ErrKeyUnknown          = errors.New("signing key is not known for the signer zone")
	ErrNoTrustAnchor       = errors.New("signer zone has no trust anchor")
	ErrDenialInvalid       = errors.New("denial of existence has no valid proof")
	ErrCnameLoop           = errors.New("cname chain loops or exceeds the depth limit")
	ErrBadDelegation       = errors.New("delegation cannot be learned from this zone")
	ErrBadSnapshot         = errors.New("snapshot payload is invalid")
)

// ResolverConfig bounds the cache and the accepted TTLs.
type ResolverConfig struct {
	MaxCacheEntries int
	MinTTL          uint32
	MaxTTL          uint32
	MaxCNAME        int
}

// Query is one recursive lookup request.
type Query struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Class    string `json:"class"`
	DO       bool   `json:"do"`
	ID       uint16 `json:"id"`
	CaseName string `json:"case_name"`
}

// RR is one resource record inside an rrset.
type RR struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	RData string `json:"rdata"`
}

// Rrsig is the signature that covers one rrset.
type Rrsig struct {
	TypeCovered string    `json:"type_covered"`
	SignerName  string    `json:"signer_name"`
	KeyTag      int       `json:"key_tag"`
	Inception   time.Time `json:"inception"`
	Expiration  time.Time `json:"expiration"`
	Labels      int       `json:"labels"`
	Valid       bool      `json:"valid"`
}

// RRset is one owner and type with its records and signatures.
type RRset struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Class   string  `json:"class"`
	TTL     uint32  `json:"ttl"`
	Records []RR    `json:"records"`
	Sigs    []Rrsig `json:"sigs"`
}

// DnskeyRecord is one zone signing key registered locally.
type DnskeyRecord struct {
	Zone      string `json:"zone"`
	KeyTag    int    `json:"key_tag"`
	Flags     int    `json:"flags"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

// DsRecord is one delegation signer record from the parent zone.
type DsRecord struct {
	Zone      string `json:"zone"`
	KeyTag    int    `json:"key_tag"`
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

// Response is the upstream answer handed to the validator.
type Response struct {
	ID           uint16  `json:"id"`
	QuestionName string  `json:"question_name"`
	Zone         string  `json:"zone"`
	Answer       []RRset `json:"answer"`
	Authority    []RRset `json:"authority"`
	Additional   []RRset `json:"additional"`
}

// Answer is one validated lookup result.
type Answer struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Class     string   `json:"class"`
	RData     []string `json:"rdata"`
	TTL       uint32   `json:"ttl"`
	Secure    bool     `json:"secure"`
	Denial    bool     `json:"denial"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CacheStats reports lookup and poisoning counters.
type CacheStats struct {
	Entries  int `json:"entries"`
	Hits     int `json:"hits"`
	Misses   int `json:"misses"`
	Poisoned int `json:"poisoned"`
	Denials  int `json:"denials"`
}

// DnssecCacheGuard owns the anchors, the zone keys and the answer cache.
type DnssecCacheGuard struct {
	cfg ResolverConfig
	now time.Time
}

// NewDnssecCacheGuard builds an empty validating cache.
func NewDnssecCacheGuard(cfg ResolverConfig) *DnssecCacheGuard {
	return &DnssecCacheGuard{cfg: cfg}
}

// SetNow fixes the clock used for signatures, TTLs and cache expiry.
func (g *DnssecCacheGuard) SetNow(now time.Time) {
}

// SetTrustAnchor pins the DS record of one zone.
func (g *DnssecCacheGuard) SetTrustAnchor(zone string, ds DsRecord) error {
	return errNotImplemented
}

// AddZoneKey registers one DNSKEY for a zone.
func (g *DnssecCacheGuard) AddZoneKey(zone string, key DnskeyRecord) error {
	return errNotImplemented
}

// LearnDelegation stores DS records for a child zone learned from its parent.
func (g *DnssecCacheGuard) LearnDelegation(zone string, ds []DsRecord) error {
	return errNotImplemented
}

// Resolve validates one upstream response and caches the result.
func (g *DnssecCacheGuard) Resolve(q Query, resp Response) (Answer, error) {
	return Answer{}, errNotImplemented
}

// CacheGet returns a live cached answer for one query.
func (g *DnssecCacheGuard) CacheGet(q Query) (Answer, bool) {
	return Answer{}, false
}

// Purge drops cached answers under one zone.
func (g *DnssecCacheGuard) Purge(zone string) int {
	return 0
}

// Stats returns the current cache counters.
func (g *DnssecCacheGuard) Stats() CacheStats {
	return CacheStats{}
}

// Snapshot serialises anchors, zone keys and cached answers.
func (g *DnssecCacheGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole guard state from a snapshot.
func (g *DnssecCacheGuard) Restore(data []byte) error {
	return errNotImplemented
}
