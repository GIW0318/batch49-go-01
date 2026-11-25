// Package task keeps the member process ledger of isolated domains and
// reconciles it with an external process scan.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// ProcessRef identifies one process. PID alone is not unique because the
// kernel reuses it, so StartID always travels with it.
type ProcessRef struct {
	PID     int    `json:"pid"`
	StartID string `json:"start_id"`
	Domain  string `json:"domain"`
	Comm    string `json:"comm"`

	// Confidence is set on reconciliation entries when StartID is not precise
	// enough to distinguish processes and Comm had to be used as a fallback.
	Confidence string `json:"confidence,omitempty"`
}

// Confidence values used by reconciliation entries.
const (
	ConfidenceExact    = "exact"
	ConfidenceFallback = "fallback"
)

// Reconciliation is the difference report between ledger and scan.
type Reconciliation struct {
	Unregistered []ProcessRef `json:"unregistered"`
	Exited       []ProcessRef `json:"exited"`
	Duplicated   []ProcessRef `json:"duplicated"`
	Scanned      int          `json:"scanned"`
	Generation   uint64       `json:"generation"`

	// Err is set when the scan could not be accepted as a whole, for example
	// because it exceeds LedgerLimits.MaxScan.
	Err error `json:"-"`
}

// LedgerLimits caps the ledger size.
type LedgerLimits struct {
	MaxDomains          int `json:"max_domains"`
	MaxMembersPerDomain int `json:"max_members_per_domain"`
	MaxScan             int `json:"max_scan"`
}

// LedgerStats summarises the ledger state.
type LedgerStats struct {
	Domains    int    `json:"domains"`
	Members    int    `json:"members"`
	Generation uint64 `json:"generation"`
}

// Errors reported by the ledger.
var (
	ErrBlankDomain      = errors.New("domain name is required")
	ErrDomainExists     = errors.New("domain already exists")
	ErrDomainLimit      = errors.New("domain limit reached")
	ErrDomainDestroying = errors.New("domain is being destroyed")
	ErrUnknownDomain    = errors.New("unknown domain")
	ErrBlankProcess     = errors.New("process pid and start id are required")
	ErrCrossDomain      = errors.New("process belongs to another domain")
	ErrAlreadyMember    = errors.New("process is already a member of the domain")
	ErrMemberLimit      = errors.New("member limit reached for the domain")
	ErrUnknownProcess   = errors.New("process is not a member of the domain")
	ErrAmbiguousPID     = errors.New("pid matches more than one member")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
	ErrScanLimit        = errors.New("scan limit reached")
)

const snapshotVersion = 1

type processKey struct {
	pid     int
	startID string
}

type ledgerDomain struct {
	members    map[processKey]ProcessRef
	destroying bool
}

func newLedgerDomain() *ledgerDomain {
	return &ledgerDomain{members: make(map[processKey]ProcessRef)}
}

// ProcessDomainLedger records which process belongs to which isolated domain.
type ProcessDomainLedger struct {
	mu         sync.RWMutex
	limits     LedgerLimits
	domains    map[string]*ledgerDomain
	generation uint64
}

// NewProcessDomainLedger builds a ledger with the given limits.
func NewProcessDomainLedger(l LedgerLimits) *ProcessDomainLedger {
	return &ProcessDomainLedger{
		limits:  l,
		domains: make(map[string]*ledgerDomain),
	}
}

// Create adds an empty domain.
func (g *ProcessDomainLedger) Create(domain string) error {
	if strings.TrimSpace(domain) == "" {
		return ErrBlankDomain
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.domains[domain]; ok {
		return ErrDomainExists
	}
	if g.limits.MaxDomains <= 0 || len(g.domains) >= g.limits.MaxDomains {
		return ErrDomainLimit
	}

	g.domains[domain] = newLedgerDomain()
	g.generation++
	return nil
}

// Join registers one process in a domain.
func (g *ProcessDomainLedger) Join(domain string, p ProcessRef) error {
	if !validProcess(p) {
		return ErrBlankProcess
	}
	p.Confidence = ""

	g.mu.Lock()
	defer g.mu.Unlock()

	d, ok := g.domains[domain]
	if !ok {
		return ErrUnknownDomain
	}
	if d.destroying {
		return ErrDomainDestroying
	}
	if p.Domain != "" && p.Domain != domain {
		return ErrCrossDomain
	}

	key := processKey{pid: p.PID, startID: p.StartID}
	if _, exists := d.members[key]; exists {
		return ErrAlreadyMember
	}
	if g.limits.MaxMembersPerDomain <= 0 || len(d.members) >= g.limits.MaxMembersPerDomain {
		return ErrMemberLimit
	}

	p.Domain = domain
	d.members[key] = p
	g.generation++
	return nil
}

// Leave removes one member by pid.
func (g *ProcessDomainLedger) Leave(domain string, pid int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	d, ok := g.domains[domain]
	if !ok {
		return ErrUnknownDomain
	}
	if d.destroying {
		return ErrDomainDestroying
	}

	var (
		matchKey processKey
		matches  int
	)
	for key := range d.members {
		if key.pid == pid {
			matchKey = key
			matches++
		}
	}
	if matches == 0 {
		return ErrUnknownProcess
	}
	if matches > 1 {
		return ErrAmbiguousPID
	}

	delete(d.members, matchKey)
	g.generation++
	return nil
}

// Members returns a sorted copy of one domain's members.
func (g *ProcessDomainLedger) Members(domain string) []ProcessRef {
	g.mu.RLock()
	defer g.mu.RUnlock()

	d, ok := g.domains[domain]
	if !ok {
		return []ProcessRef{}
	}

	members := make([]ProcessRef, 0, len(d.members))
	for _, p := range d.members {
		members = append(members, p)
	}
	sortProcessRefs(members)
	return members
}

// Destroy closes a domain and returns its remaining members.
func (g *ProcessDomainLedger) Destroy(domain string) ([]ProcessRef, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	d, ok := g.domains[domain]
	if !ok {
		return nil, ErrUnknownDomain
	}

	// Close the domain before collecting. Join and Leave observe destroying
	// under the same lock, so no member can slip in between these phases.
	d.destroying = true
	members := make([]ProcessRef, 0, len(d.members))
	for _, p := range d.members {
		members = append(members, p)
	}
	sortProcessRefs(members)

	delete(g.domains, domain)
	g.generation++
	return members, nil
}

// Reconcile compares the ledger with an external scan without mutating it.
func (g *ProcessDomainLedger) Reconcile(scan []ProcessRef) Reconciliation {
	rec := Reconciliation{
		Unregistered: []ProcessRef{},
		Exited:       []ProcessRef{},
		Duplicated:   []ProcessRef{},
		Scanned:      len(scan),
	}

	g.mu.RLock()
	limits := g.limits
	rec.Generation = g.generation

	ledgerMembers := make([]ledgerMember, 0)
	exactIndex := make(map[exactLedgerKey]ledgerMember)
	fallbackIndex := make(map[fallbackLedgerKey]ledgerMember)
	globalExactIndex := make(map[globalExactKey][]ledgerMember)
	globalFallbackIndex := make(map[globalFallbackKey][]ledgerMember)

	startPIDs := make(map[string]map[int]struct{})
	rawComms := make(map[exactLedgerKey]map[string]struct{})

	for domain, d := range g.domains {
		for _, p := range d.members {
			p.Domain = domain
			p.Confidence = ""

			member := ledgerMember{
				exact:    exactLedgerKey{domain: domain, pid: p.PID, startID: p.StartID},
				fallback: fallbackLedgerKey{domain: domain, pid: p.PID, startID: p.StartID, comm: p.Comm},
				ref:      p,
			}
			ledgerMembers = append(ledgerMembers, member)
			exactIndex[member.exact] = member
			fallbackIndex[member.fallback] = member

			ge := globalExactKey{pid: p.PID, startID: p.StartID}
			gf := globalFallbackKey{pid: p.PID, startID: p.StartID, comm: p.Comm}
			globalExactIndex[ge] = append(globalExactIndex[ge], member)
			globalFallbackIndex[gf] = append(globalFallbackIndex[gf], member)

			addStartPID(startPIDs, p)
			addRawComm(rawComms, member.exact, p)
		}
	}
	g.mu.RUnlock()

	if limits.MaxScan < 0 || len(scan) > limits.MaxScan {
		rec.Err = ErrScanLimit
		return rec
	}

	scanRefs := make([]ProcessRef, 0, len(scan))
	for _, p := range scan {
		if !validProcess(p) {
			continue
		}
		p.Confidence = ""
		p.Domain = inferScanDomain(p, globalExactIndex, globalFallbackIndex)
		scanRefs = append(scanRefs, p)

		key := exactLedgerKey{domain: p.Domain, pid: p.PID, startID: p.StartID}
		addStartPID(startPIDs, p)
		addRawComm(rawComms, key, p)
	}

	weakStart := make(map[string]bool)
	for startID, pids := range startPIDs {
		if len(pids) > 1 {
			weakStart[startID] = true
		}
	}

	conflictingIdentity := make(map[exactLedgerKey]bool)
	for key, comms := range rawComms {
		if len(comms) > 1 {
			conflictingIdentity[key] = true
		}
	}

	groups := make([]scanGroup, 0)
	groupIndex := make(map[scanIdentity]int)
	for _, p := range scanRefs {
		rawKey := exactLedgerKey{domain: p.Domain, pid: p.PID, startID: p.StartID}
		fallback := conflictingIdentity[rawKey]

		identity := scanIdentity{domain: p.Domain, pid: p.PID, startID: p.StartID}
		if fallback {
			identity.comm = p.Comm
		}

		confidence := ""
		if fallback || weakStart[p.StartID] {
			confidence = ConfidenceFallback
		}
		p.Confidence = confidence

		if idx, ok := groupIndex[identity]; ok {
			groups[idx].count++
			if lessProcessRef(p, groups[idx].ref) {
				groups[idx].ref = p
			}
			if confidence != "" {
				groups[idx].confidence = confidence
			}
			continue
		}

		groupIndex[identity] = len(groups)
		groups = append(groups, scanGroup{
			identity:   identity,
			ref:        p,
			count:      1,
			fallback:   fallback,
			confidence: confidence,
		})
	}

	matched := make(map[exactLedgerKey]bool)
	for _, group := range groups {
		p := group.ref
		p.Confidence = group.confidence

		matchedMember := matchLedger(
			p,
			group.fallback,
			exactIndex,
			fallbackIndex,
			globalExactIndex,
			globalFallbackIndex,
			matched,
		)
		if group.count > 1 {
			rec.Duplicated = append(rec.Duplicated, p)
			continue
		}
		if !matchedMember {
			rec.Unregistered = append(rec.Unregistered, p)
		}
	}

	for _, member := range ledgerMembers {
		if matched[member.exact] {
			continue
		}
		p := member.ref
		if conflictingIdentity[member.exact] || weakStart[p.StartID] {
			p.Confidence = ConfidenceFallback
		}
		rec.Exited = append(rec.Exited, p)
	}

	sortProcessRefs(rec.Unregistered)
	sortProcessRefs(rec.Exited)
	sortProcessRefs(rec.Duplicated)
	return rec
}

type exactLedgerKey struct {
	domain  string
	pid     int
	startID string
}

type fallbackLedgerKey struct {
	domain  string
	pid     int
	startID string
	comm    string
}

type globalExactKey struct {
	pid     int
	startID string
}

type globalFallbackKey struct {
	pid     int
	startID string
	comm    string
}

type ledgerMember struct {
	exact    exactLedgerKey
	fallback fallbackLedgerKey
	ref      ProcessRef
}

type scanIdentity struct {
	domain  string
	pid     int
	startID string
	comm    string
}

type scanGroup struct {
	identity   scanIdentity
	ref        ProcessRef
	count      int
	fallback   bool
	confidence string
}

func addStartPID(index map[string]map[int]struct{}, p ProcessRef) {
	if p.StartID == "" {
		return
	}
	pids := index[p.StartID]
	if pids == nil {
		pids = make(map[int]struct{})
		index[p.StartID] = pids
	}
	pids[p.PID] = struct{}{}
}

func addRawComm(index map[exactLedgerKey]map[string]struct{}, key exactLedgerKey, p ProcessRef) {
	if p.Comm == "" {
		return
	}
	comms := index[key]
	if comms == nil {
		comms = make(map[string]struct{})
		index[key] = comms
	}
	comms[p.Comm] = struct{}{}
}

func inferScanDomain(
	p ProcessRef,
	globalExact map[globalExactKey][]ledgerMember,
	globalFallback map[globalFallbackKey][]ledgerMember,
) string {
	if p.Domain != "" {
		return p.Domain
	}

	exactMatches := globalExact[globalExactKey{pid: p.PID, startID: p.StartID}]
	if len(exactMatches) == 1 {
		return exactMatches[0].ref.Domain
	}
	if len(exactMatches) > 1 && p.Comm != "" {
		fallbackMatches := globalFallback[globalFallbackKey{pid: p.PID, startID: p.StartID, comm: p.Comm}]
		domain := ""
		for _, match := range fallbackMatches {
			if domain == "" {
				domain = match.ref.Domain
				continue
			}
			if match.ref.Domain != domain {
				return ""
			}
		}
		return domain
	}
	return ""
}

func matchLedger(
	p ProcessRef,
	fallback bool,
	exactIndex map[exactLedgerKey]ledgerMember,
	fallbackIndex map[fallbackLedgerKey]ledgerMember,
	globalExactIndex map[globalExactKey][]ledgerMember,
	globalFallbackIndex map[globalFallbackKey][]ledgerMember,
	matched map[exactLedgerKey]bool,
) bool {
	if p.Domain != "" {
		if fallback {
			member, ok := fallbackIndex[fallbackLedgerKey{
				domain: p.Domain, pid: p.PID, startID: p.StartID, comm: p.Comm,
			}]
			if !ok {
				return false
			}
			matched[member.exact] = true
			return true
		}

		member, ok := exactIndex[exactLedgerKey{domain: p.Domain, pid: p.PID, startID: p.StartID}]
		if !ok {
			return false
		}
		matched[member.exact] = true
		return true
	}

	var members []ledgerMember
	if fallback {
		members = globalFallbackIndex[globalFallbackKey{pid: p.PID, startID: p.StartID, comm: p.Comm}]
	} else {
		members = globalExactIndex[globalExactKey{pid: p.PID, startID: p.StartID}]
	}
	if len(members) == 0 {
		return false
	}
	for _, member := range members {
		matched[member.exact] = true
	}
	return true
}

// Snapshot serialises the whole ledger.
func (g *ProcessDomainLedger) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	doc := snapshotDocument{
		Version:    snapshotVersion,
		Generation: g.generation,
		Limits:     g.limits,
		Domains:    make([]snapshotDomain, 0, len(g.domains)),
	}

	names := make([]string, 0, len(g.domains))
	for name := range g.domains {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		d := g.domains[name]
		members := make([]ProcessRef, 0, len(d.members))
		for _, p := range d.members {
			p.Domain = name
			p.Confidence = ""
			members = append(members, p)
		}
		sortProcessRefs(members)
		doc.Domains = append(doc.Domains, snapshotDomain{Name: name, Members: members})
	}

	return json.Marshal(doc)
}

type snapshotDocument struct {
	Version    int              `json:"version"`
	Generation uint64           `json:"generation"`
	Limits     LedgerLimits     `json:"limits"`
	Domains    []snapshotDomain `json:"domains"`
}

type snapshotDomain struct {
	Name    string       `json:"name"`
	Members []ProcessRef `json:"members"`
}

// Restore replaces the ledger with a previous snapshot.
func (g *ProcessDomainLedger) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var doc snapshotDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return ErrBadSnapshot
	}
	if doc.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if doc.Limits.MaxDomains < 0 ||
		doc.Limits.MaxMembersPerDomain < 0 ||
		doc.Limits.MaxScan < 0 {
		return ErrBadSnapshot
	}

	g.mu.RLock()
	currentLimits := g.limits
	g.mu.RUnlock()

	// The constructor's non-zero limits stay authoritative. A ledger created
	// without limits can still be restored from a snapshot that carries them.
	limits := currentLimits
	if limits.MaxDomains == 0 {
		limits.MaxDomains = doc.Limits.MaxDomains
	}
	if limits.MaxMembersPerDomain == 0 {
		limits.MaxMembersPerDomain = doc.Limits.MaxMembersPerDomain
	}
	if limits.MaxScan == 0 {
		limits.MaxScan = doc.Limits.MaxScan
	}
	if limits.MaxDomains < 0 ||
		limits.MaxMembersPerDomain < 0 ||
		limits.MaxScan < 0 {
		return ErrBadSnapshot
	}
	if len(doc.Domains) > limits.MaxDomains {
		return ErrBadSnapshot
	}

	domains := make(map[string]*ledgerDomain, len(doc.Domains))
	for _, sd := range doc.Domains {
		if strings.TrimSpace(sd.Name) == "" {
			return ErrBadSnapshot
		}
		if _, exists := domains[sd.Name]; exists {
			return ErrBadSnapshot
		}
		if len(sd.Members) > limits.MaxMembersPerDomain {
			return ErrBadSnapshot
		}

		d := newLedgerDomain()
		for _, p := range sd.Members {
			if !validProcess(p) {
				return ErrBadSnapshot
			}
			if p.Domain != "" && p.Domain != sd.Name {
				return ErrBadSnapshot
			}
			p.Domain = sd.Name
			p.Confidence = ""

			key := processKey{pid: p.PID, startID: p.StartID}
			if _, exists := d.members[key]; exists {
				return ErrBadSnapshot
			}
			d.members[key] = p
		}
		domains[sd.Name] = d
	}

	g.mu.Lock()
	g.limits = limits
	g.domains = domains
	g.generation = doc.Generation
	g.mu.Unlock()
	return nil
}

// Domains returns the domain names in ascending order.
func (g *ProcessDomainLedger) Domains() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	names := make([]string, 0, len(g.domains))
	for name := range g.domains {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Stats summarises the ledger.
func (g *ProcessDomainLedger) Stats() LedgerStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	members := 0
	for _, d := range g.domains {
		members += len(d.members)
	}
	return LedgerStats{
		Domains:    len(g.domains),
		Members:    members,
		Generation: g.generation,
	}
}

// Generation returns the monotonic change counter of the ledger.
func (g *ProcessDomainLedger) Generation() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.generation
}

func validProcess(p ProcessRef) bool {
	return p.PID > 0 && strings.TrimSpace(p.StartID) != ""
}

func sortProcessRefs(refs []ProcessRef) {
	sort.Slice(refs, func(i, j int) bool {
		return lessProcessRef(refs[i], refs[j])
	})
}

func lessProcessRef(a, b ProcessRef) bool {
	if a.Domain != b.Domain {
		return a.Domain < b.Domain
	}
	if a.PID != b.PID {
		return a.PID < b.PID
	}
	if a.StartID != b.StartID {
		return a.StartID < b.StartID
	}
	if a.Comm != b.Comm {
		return a.Comm < b.Comm
	}
	return a.Confidence < b.Confidence
}
