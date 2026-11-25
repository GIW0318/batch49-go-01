// Package task propagates upstream VEX statements to downstream consumers,
// orders revisions, and keeps retractions and acknowledgements apart.
package task

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VEX statuses understood by the propagator.
const (
	StatusAffected           = "affected"
	StatusNotAffected        = "not_affected"
	StatusUnderInvestigation = "under_investigation"
	StatusRetracted          = "retracted"
)

// Basis values recorded on a consumer facing state.
const (
	BasisUpstream          = "upstream"
	BasisOutsideRange      = "outside_range"
	BasisRetracted         = "retracted"
	BasisExpired           = "expired"
	BasisUpstreamWithLocal = "upstream_with_local"
)

// Record kinds kept in the per consumer history.
const (
	RecordStatement  = "statement"
	RecordRetraction = "retraction"
)

// Errors surfaced by the propagator.
var (
	ErrBlankField         = errors.New("upstream, component, cve and status are required")
	ErrBadStatus          = errors.New("unknown vex status")
	ErrBadRevision        = errors.New("revision must be at least one")
	ErrBadRange           = errors.New("affected version range is invalid")
	ErrDuplicateStatement = errors.New("statement id already published")
	ErrUnknownConsumer    = errors.New("consumer is not registered")
	ErrUnknownComponent   = errors.New("component is not registered")
	ErrUnknownStatement   = errors.New("no statement for this component and cve")
)

// Statement is one upstream VEX declaration.
type Statement struct {
	ID          string    `json:"id"`
	Upstream    string    `json:"upstream"`
	Component   string    `json:"component"`
	CVE         string    `json:"cve"`
	Status      string    `json:"status"`
	Revision    int       `json:"revision"`
	AffectsFrom string    `json:"affects_from"`
	AffectsTo   string    `json:"affects_to"`
	DeclaredAt  time.Time `json:"declared_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// LocalFinding is a consumer side analysis kept next to the upstream one.
type LocalFinding struct {
	Consumer  string    `json:"consumer"`
	Component string    `json:"component"`
	CVE       string    `json:"cve"`
	Status    string    `json:"status"`
	Revision  int       `json:"revision"`
	At        time.Time `json:"at"`
}

// State is the effective conclusion for one consumer.
type State struct {
	Consumer       string `json:"consumer"`
	Component      string `json:"component"`
	CVE            string `json:"cve"`
	Status         string `json:"status"`
	UpstreamStatus string `json:"upstream_status"`
	LocalStatus    string `json:"local_status"`
	Basis          string `json:"basis"`
	Revision       int    `json:"revision"`
	Acked          bool   `json:"acked"`
}

// Record is one entry of the per consumer conclusion history.
type Record struct {
	Kind     string    `json:"kind"`
	Status   string    `json:"status"`
	Revision int       `json:"revision"`
	At       time.Time `json:"at"`
	Upstream string    `json:"upstream"`
	Valid    bool      `json:"valid"`
	Acked    bool      `json:"acked"`
}

// conclusionKey identifies one component and CVE conclusion stream.
type conclusionKey struct {
	component string
	cve       string
}

// storedStatement keeps a published statement together with its ordering data.
type storedStatement struct {
	statement Statement
	at        time.Time
	seq       int64
}

// storedRetraction is the single idempotent withdrawal for one key.
type storedRetraction struct {
	statement Statement
	at        time.Time
	seq       int64
}

// storedLocal keeps the latest consumer owned analysis.
type storedLocal struct {
	finding LocalFinding
	at      time.Time
}

// conclusion is the full append only trail for one component and CVE.
type conclusion struct {
	statements []*storedStatement
	retraction *storedRetraction
}

// consumerState holds the monotonic acknowledgement watermark and local work.
type consumerState struct {
	watermark time.Time
	locals    map[conclusionKey]*storedLocal
}

// VexStatePropagator holds consumers, component versions and conclusions.
type VexStatePropagator struct {
	mu         sync.RWMutex
	consumers  map[string]*consumerState
	components map[string]string
	keys       map[conclusionKey]*conclusion
	ids        map[string]struct{}
	seq        int64
}

// NewVexStatePropagator builds an empty propagator.
func NewVexStatePropagator() *VexStatePropagator {
	return &VexStatePropagator{
		consumers:  make(map[string]*consumerState),
		components: make(map[string]string),
		keys:       make(map[conclusionKey]*conclusion),
		ids:        make(map[string]struct{}),
	}
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}

// eventTime prefers the upstream declared time and falls back to the time the
// event entered the propagator, so callers that only set one still order sanely.
func eventTime(declaredAt, at time.Time) time.Time {
	if !declaredAt.IsZero() {
		return declaredAt
	}
	return at
}

func knownStatus(status string) bool {
	switch status {
	case StatusAffected, StatusNotAffected, StatusUnderInvestigation:
		return true
	default:
		return false
	}
}

func validateRange(from, to string) error {
	if !blank(from) {
		if _, err := parseVersion(from); err != nil {
			return ErrBadRange
		}
	}
	if !blank(to) {
		if _, err := parseVersion(to); err != nil {
			return ErrBadRange
		}
	}
	return nil
}

func validatePublish(st Statement) error {
	if blank(st.Upstream) || blank(st.Component) || blank(st.CVE) || blank(st.Status) {
		return ErrBlankField
	}
	if !knownStatus(st.Status) {
		return ErrBadStatus
	}
	if st.Revision < 1 {
		return ErrBadRevision
	}
	return validateRange(st.AffectsFrom, st.AffectsTo)
}

func validateRetraction(st Statement) error {
	if blank(st.Upstream) || blank(st.Component) || blank(st.CVE) {
		return ErrBlankField
	}
	if st.Revision < 1 {
		return ErrBadRevision
	}
	return validateRange(st.AffectsFrom, st.AffectsTo)
}

func validateLocal(f LocalFinding) error {
	if blank(f.Consumer) || blank(f.Component) || blank(f.CVE) || blank(f.Status) {
		return ErrBlankField
	}
	if !knownStatus(f.Status) {
		return ErrBadStatus
	}
	if f.Revision < 1 {
		return ErrBadRevision
	}
	return nil
}

// parseVersion parses dotted numeric versions such as 1.2.3. A leading v and a
// pre-release/build suffix are tolerated; non numeric cores are rejected.
func parseVersion(raw string) ([]int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, ErrBadRange
	}
	if len(value) > 0 && (value[0] == 'v' || value[0] == 'V') {
		value = value[1:]
	}
	if cut := strings.IndexAny(value, "-+"); cut >= 0 {
		value = value[:cut]
	}
	parts := strings.Split(value, ".")
	parsed := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, ErrBadRange
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return nil, ErrBadRange
		}
		parsed = append(parsed, number)
	}
	if len(parsed) == 0 {
		return nil, ErrBadRange
	}
	return parsed, nil
}

// compareVersions returns -1 when a < b, 0 when equal and 1 when a > b.
func compareVersions(a, b string) (int, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	length := len(pa)
	if len(pb) > length {
		length = len(pb)
	}
	for i := 0; i < length; i++ {
		var va, vb int
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va < vb {
			return -1, nil
		}
		if va > vb {
			return 1, nil
		}
	}
	return 0, nil
}

// versionInsideRange reports whether installed satisfies [from, to). Blank
// endpoints are unbounded. An unparsable installed version cannot be excluded.
func versionInsideRange(installed, from, to string) bool {
	if !blank(from) {
		if cmp, err := compareVersions(installed, from); err == nil && cmp < 0 {
			return false
		}
	}
	if !blank(to) {
		if cmp, err := compareVersions(installed, to); err == nil && cmp >= 0 {
			return false
		}
	}
	return true
}

func expired(st Statement, now time.Time) bool {
	return !st.ExpiresAt.IsZero() && now.After(st.ExpiresAt)
}

// statementBeats reports whether candidate should win over current. Higher
// revision wins; on equal revision the later declared time wins.
func statementBeats(candidate, current *storedStatement) bool {
	if candidate.statement.Revision != current.statement.Revision {
		return candidate.statement.Revision > current.statement.Revision
	}
	return candidate.at.After(current.at)
}

func localBeats(candidate, current *storedLocal) bool {
	if candidate.finding.Revision != current.finding.Revision {
		return candidate.finding.Revision > current.finding.Revision
	}
	return candidate.at.After(current.at)
}

// conclusionPending reports whether the key has any event after the watermark.
func conclusionPending(c *conclusion, watermark time.Time) bool {
	for _, st := range c.statements {
		if st.at.After(watermark) {
			return true
		}
	}
	return c.retraction != nil && c.retraction.at.After(watermark)
}

// RegisterConsumer adds a downstream consumer.
func (p *VexStatePropagator) RegisterConsumer(consumer string) error {
	if blank(consumer) {
		return ErrBlankField
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.consumers[consumer]; !exists {
		p.consumers[consumer] = &consumerState{locals: make(map[conclusionKey]*storedLocal)}
	}
	return nil
}

// RegisterComponent records the version a component currently ships.
func (p *VexStatePropagator) RegisterComponent(component, version string) error {
	if blank(component) || blank(version) {
		return ErrBlankField
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.components[component] = version
	return nil
}

// Publish stores one upstream statement.
func (p *VexStatePropagator) Publish(st Statement, at time.Time) error {
	if err := validatePublish(st); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if st.ID != "" {
		if _, duplicated := p.ids[st.ID]; duplicated {
			return ErrDuplicateStatement
		}
		p.ids[st.ID] = struct{}{}
	}

	p.seq++
	key := conclusionKey{component: st.Component, cve: st.CVE}
	c := p.keys[key]
	if c == nil {
		c = &conclusion{}
		p.keys[key] = c
	}
	c.statements = append(c.statements, &storedStatement{
		statement: st,
		at:        eventTime(st.DeclaredAt, at),
		seq:       p.seq,
	})
	return nil
}

// StateFor resolves the conclusion a consumer should act on.
func (p *VexStatePropagator) StateFor(consumer, component, cve string) (State, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cs, knownConsumer := p.consumers[consumer]
	if !knownConsumer {
		return State{}, ErrUnknownConsumer
	}
	version, knownComponent := p.components[component]
	if !knownComponent {
		return State{}, ErrUnknownComponent
	}

	key := conclusionKey{component: component, cve: cve}
	c := p.keys[key]
	local := cs.locals[key]
	emptyUpstream := c == nil || (len(c.statements) == 0 && c.retraction == nil)
	if emptyUpstream && local == nil {
		return State{}, ErrUnknownStatement
	}

	state := State{
		Consumer:  consumer,
		Component: component,
		CVE:       cve,
	}

	watermark := cs.watermark
	if c != nil {
		state.Acked = !conclusionPending(c, watermark)
	} else {
		state.Acked = true
	}
	if local != nil {
		state.LocalStatus = local.finding.Status
	}

	var winner *storedStatement
	var retraction *storedRetraction
	retracted := false
	if c != nil {
		retraction = c.retraction
		retracted = retraction != nil
		for _, candidate := range c.statements {
			if winner == nil || statementBeats(candidate, winner) {
				winner = candidate
			}
		}
	}
	if winner != nil {
		state.UpstreamStatus = winner.statement.Status
	}

	switch {
	case local != nil:
		// A consumer owned analysis is an equal second source; upstream only
		// informs it and never forcibly overwrites the local conclusion.
		state.Basis = BasisUpstreamWithLocal
		state.Status = local.finding.Status
		switch {
		case retraction != nil:
			state.Revision = retraction.statement.Revision
		case winner != nil:
			state.Revision = winner.statement.Revision
		default:
			state.Revision = local.finding.Revision
		}
	case retracted:
		// A retraction outranks every statement but never deletes history.
		state.Status = StatusRetracted
		state.Basis = BasisRetracted
		state.Revision = retraction.statement.Revision
	default:
		raw := winner.statement
		state.Revision = raw.Revision
		switch {
		case !versionInsideRange(version, raw.AffectsFrom, raw.AffectsTo):
			state.Status = StatusNotAffected
			state.Basis = BasisOutsideRange
		case expired(raw, time.Now()):
			state.Status = StatusUnderInvestigation
			state.Basis = BasisExpired
		default:
			state.Status = raw.Status
			state.Basis = BasisUpstream
		}
	}

	return state, nil
}

// Consumers lists the registered consumers following a component and cve.
func (p *VexStatePropagator) Consumers(component, cve string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	consumers := make([]string, 0, len(p.consumers))
	for name := range p.consumers {
		consumers = append(consumers, name)
	}
	sort.Strings(consumers)
	return consumers
}

// Ack advances the consumer confirmation watermark.
func (p *VexStatePropagator) Ack(consumer string, upto time.Time) error {
	if blank(consumer) {
		return ErrBlankField
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cs, exists := p.consumers[consumer]
	if !exists {
		return ErrUnknownConsumer
	}
	if upto.After(cs.watermark) {
		cs.watermark = upto
	}
	return nil
}

// Retract withdraws the current conclusion for a component and cve.
func (p *VexStatePropagator) Retract(st Statement, at time.Time) error {
	if err := validateRetraction(st); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	key := conclusionKey{component: st.Component, cve: st.CVE}
	c := p.keys[key]
	if c != nil && c.retraction != nil {
		// Withdrawal is a single idempotent tombstone for the key.
		return nil
	}
	if c == nil {
		c = &conclusion{}
		p.keys[key] = c
	}

	p.seq++
	c.retraction = &storedRetraction{
		statement: st,
		at:        eventTime(st.DeclaredAt, at),
		seq:       p.seq,
	}
	return nil
}

// Pending counts the distinct conclusions a consumer has not acknowledged.
func (p *VexStatePropagator) Pending(consumer string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cs, known := p.consumers[consumer]
	if !known {
		return 0
	}
	pending := 0
	for _, c := range p.keys {
		if conclusionPending(c, cs.watermark) {
			pending++
		}
	}
	return pending
}

// History returns the conclusion trail of one component and cve.
func (p *VexStatePropagator) History(consumer, component, cve string) []Record {
	p.mu.RLock()
	defer p.mu.RUnlock()

	records := make([]Record, 0)
	c := p.keys[conclusionKey{component: component, cve: cve}]
	if c == nil {
		return records
	}
	var watermark time.Time
	if cs := p.consumers[consumer]; cs != nil {
		watermark = cs.watermark
	}

	type orderedRecord struct {
		record Record
		seq    int64
	}
	entries := make([]orderedRecord, 0, len(c.statements)+1)
	retracted := c.retraction != nil

	for _, st := range c.statements {
		entries = append(entries, orderedRecord{
			seq: st.seq,
			record: Record{
				Kind:     RecordStatement,
				Status:   st.statement.Status,
				Revision: st.statement.Revision,
				At:       st.at,
				Upstream: st.statement.Upstream,
				Valid:    !retracted,
				Acked:    !st.at.After(watermark),
			},
		})
	}
	if retraction := c.retraction; retraction != nil {
		entries = append(entries, orderedRecord{
			seq: retraction.seq,
			record: Record{
				Kind:     RecordRetraction,
				Status:   StatusRetracted,
				Revision: retraction.statement.Revision,
				At:       retraction.at,
				Upstream: retraction.statement.Upstream,
				Valid:    true,
				Acked:    !retraction.at.After(watermark),
			},
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := entries[i].record, entries[j].record
		if !ri.At.Equal(rj.At) {
			return ri.At.Before(rj.At)
		}
		if ri.Revision != rj.Revision {
			return ri.Revision < rj.Revision
		}
		return entries[i].seq < entries[j].seq
	})

	for _, entry := range entries {
		records = append(records, entry.record)
	}
	return records
}

// SubmitLocal records a consumer side analysis as a second source.
func (p *VexStatePropagator) SubmitLocal(f LocalFinding, at time.Time) error {
	if err := validateLocal(f); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	cs, known := p.consumers[f.Consumer]
	if !known {
		return ErrUnknownConsumer
	}
	key := conclusionKey{component: f.Component, cve: f.CVE}
	record := &storedLocal{finding: f, at: eventTime(f.At, at)}
	if current := cs.locals[key]; current == nil || localBeats(record, current) {
		cs.locals[key] = record
	}
	return nil
}
