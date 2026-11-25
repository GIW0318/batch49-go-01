// Package task keeps the external exposure inventory: item identity,
// evidence ageing, owner inheritance and merge bookkeeping.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Observation states.
const (
	StateOpen       = "open"
	StateClosed     = "closed"
	StateHistorical = "historical"
)

// Transport protocols.
const (
	ProtoTCP = "tcp"
	ProtoUDP = "udp"
)

// Owner provenance values.
const (
	OwnerInherited = "inherited"
	OwnerManual    = "manual"
)

// Change kinds appended to the audit log.
const (
	ChangeNew        = "new"
	ChangeOwner      = "owner_changed"
	ChangeDowngraded = "downgraded"
	ChangeMerged     = "merged"
	ChangeClosed     = "closed"
)

// Errors returned by the ledger.
var (
	ErrBadObservation = errors.New("observation is invalid")
	ErrBlankOwner     = errors.New("owner is required")
	ErrBadNetblock    = errors.New("netblock must be a valid CIDR")
	ErrNotFound       = errors.New("exposure item not found")
	ErrBadSweep       = errors.New("sweep instant is required")
	ErrBadMerge       = errors.New("merge needs at least two item ids")
	ErrMergeMismatch  = errors.New("items do not share address and protocol")
	ErrItemLimit      = errors.New("exposure item limit reached")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Observation is one probe result for one exposed endpoint.
type Observation struct {
	Addr        string
	Port        int
	Proto       string
	CertSubject string
	Owner       string
	State       string
	Seen        time.Time
}

// Item is one exposure inventory entry.
type Item struct {
	ID          string
	Addr        string
	Port        int
	Proto       string
	CertSubject string
	Owner       string
	OwnerSource string
	State       string
	FirstSeen   time.Time
	LastSeen    time.Time
	Evidence    int
	Misses      int
	Seq         int64
	MergedFrom  []string
}

// Change is one lifecycle event of an item.
type Change struct {
	ItemID string
	Kind   string
	From   string
	To     string
	Seq    int64
	At     time.Time
}

// Limits bounds the inventory.
type Limits struct {
	TTL      time.Duration
	MaxItems int
}

// Stats is the counter block of the ledger.
type Stats struct {
	Live       int
	Historical int
	Evidence   int
	Merged     int
}

type inheritedBlock struct {
	prefix netip.Prefix
	owner  string
}

// ExposureLedger owns the live items, the historical items, the inheritance
// table and the change log.
type ExposureLedger struct {
	mu sync.Mutex

	limits Limits

	items    map[string]*Item
	identity map[string]string
	inherit  map[string]inheritedBlock

	changes []Change
	nextID  int
	nextSeq int64
	merged  int
}

// NewExposureLedger builds an empty ledger.
func NewExposureLedger(limits Limits) *ExposureLedger {
	return &ExposureLedger{
		limits:   limits,
		items:    make(map[string]*Item),
		identity: make(map[string]string),
		inherit:  make(map[string]inheritedBlock),
		nextID:   1,
		nextSeq:  1,
	}
}

// Observe records one probe result and returns the item identity.
func (l *ExposureLedger) Observe(obs Observation) (string, error) {
	addr, proto, state, key, err := normalizeObservation(obs)
	if err != nil {
		return "", err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if id, ok := l.identity[key]; ok {
		item, exists := l.items[id]
		if exists {
			l.applyObservation(item, obs, state)
			return id, nil
		}
		delete(l.identity, key)
	}

	if l.limits.MaxItems > 0 && len(l.items) >= l.limits.MaxItems {
		return "", ErrItemLimit
	}

	id := fmt.Sprintf("exp-%04d", l.nextID)
	item := &Item{
		ID:          id,
		Addr:        addr,
		Port:        obs.Port,
		Proto:       proto,
		CertSubject: strings.TrimSpace(obs.CertSubject),
		State:       StateOpen,
		FirstSeen:   obs.Seen,
		LastSeen:    obs.Seen,
		Evidence:    1,
		Seq:         int64(l.nextID),
	}
	l.nextID++

	if owner := strings.TrimSpace(obs.Owner); owner != "" {
		item.Owner = owner
		item.OwnerSource = OwnerManual
	} else {
		item.Owner, item.OwnerSource = l.ownerFor(addr)
	}

	if state == StateClosed {
		item.Misses = 1
	}

	l.items[id] = item
	l.identity[key] = id
	l.appendChange(Change{
		ItemID: id,
		Kind:   ChangeNew,
		To:     item.State,
		At:     obs.Seen,
	})
	return id, nil
}

// Attribute overrides the owner of one item.
func (l *ExposureLedger) Attribute(itemID string, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return ErrBlankOwner
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	item, ok := l.items[itemID]
	if !ok {
		return ErrNotFound
	}
	if item.Owner == owner && item.OwnerSource == OwnerManual {
		return nil
	}

	from := item.Owner
	item.Owner = owner
	item.OwnerSource = OwnerManual
	l.appendChange(Change{
		ItemID: item.ID,
		Kind:   ChangeOwner,
		From:   from,
		To:     owner,
		At:     item.LastSeen,
	})
	return nil
}

// Inherit registers an owner for a netblock.
func (l *ExposureLedger) Inherit(netblock string, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return ErrBlankOwner
	}
	prefix, err := netip.ParsePrefix(netblock)
	if err != nil {
		return ErrBadNetblock
	}
	prefix = prefix.Masked()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.inherit[prefix.String()] = inheritedBlock{prefix: prefix, owner: owner}
	l.refreshInheritedOwners()
	return nil
}

// Sweep downgrades items that outlived their evidence and returns the changes.
func (l *ExposureLedger) Sweep(now time.Time) ([]Change, error) {
	if now.IsZero() {
		return nil, ErrBadSweep
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	candidates := make([]*Item, 0)
	for _, item := range l.items {
		if item.State != StateOpen {
			continue
		}
		if now.Sub(item.LastSeen) > l.limits.TTL {
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return itemLess(candidates[i], candidates[j])
	})

	changes := make([]Change, 0, len(candidates))
	for _, item := range candidates {
		from := item.State
		item.State = StateHistorical
		change := Change{
			ItemID: item.ID,
			Kind:   ChangeDowngraded,
			From:   from,
			To:     StateHistorical,
			At:     now,
		}
		l.appendChange(change)
		changes = append(changes, change)
	}
	return changes, nil
}

// Merge folds duplicate items of one endpoint into the oldest one.
func (l *ExposureLedger) Merge(ids []string) error {
	if len(ids) < 2 {
		return ErrBadMerge
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	seen := make(map[string]struct{}, len(ids))
	selected := make([]*Item, 0, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return ErrBadMerge
		}
		seen[id] = struct{}{}
		item, ok := l.items[id]
		if !ok {
			return ErrNotFound
		}
		selected = append(selected, item)
	}

	addr := selected[0].Addr
	proto := selected[0].Proto
	for _, item := range selected[1:] {
		if item.Addr != addr || item.Proto != proto {
			return ErrMergeMismatch
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		return itemLess(selected[i], selected[j])
	})
	primary := selected[0]
	absorbed := selected[1:]

	merged := cloneItem(*primary)
	for _, item := range absorbed {
		if item.FirstSeen.Before(merged.FirstSeen) {
			merged.FirstSeen = item.FirstSeen
		}
		if item.LastSeen.After(merged.LastSeen) {
			merged.LastSeen = item.LastSeen
		}
		merged.Evidence += item.Evidence
	}

	mergedIDs := make([]string, 0, len(absorbed))
	existing := make(map[string]struct{}, len(merged.MergedFrom)+len(absorbed))
	for _, id := range merged.MergedFrom {
		existing[id] = struct{}{}
		mergedIDs = append(mergedIDs, id)
	}
	for _, item := range absorbed {
		if _, ok := existing[item.ID]; !ok {
			existing[item.ID] = struct{}{}
			mergedIDs = append(mergedIDs, item.ID)
		}
		for _, id := range item.MergedFrom {
			if _, ok := existing[id]; !ok {
				existing[id] = struct{}{}
				mergedIDs = append(mergedIDs, id)
			}
		}
	}
	merged.MergedFrom = mergedIDs

	absorbedIDs := make([]string, 0, len(absorbed))
	for _, item := range absorbed {
		l.removeItem(item)
		absorbedIDs = append(absorbedIDs, item.ID)
	}
	l.items[primary.ID] = &merged
	l.merged += len(absorbed)

	l.appendChange(Change{
		ItemID: primary.ID,
		Kind:   ChangeMerged,
		From:   strings.Join(absorbedIDs, ","),
		To:     primary.ID,
		At:     merged.LastSeen,
	})
	return nil
}

// Get reads one item, including historical ones.
func (l *ExposureLedger) Get(id string) (Item, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	item, ok := l.items[id]
	if !ok {
		return Item{}, false
	}
	return cloneItem(*item), true
}

// Items lists the live items ordered by id.
func (l *ExposureLedger) Items() []Item {
	l.mu.Lock()
	defer l.mu.Unlock()

	items := make([]Item, 0, len(l.items))
	for _, item := range l.items {
		if item.State == StateOpen {
			items = append(items, cloneItem(*item))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Seq < items[j].Seq
	})
	return items
}

// Changes lists the audit log in append order.
func (l *ExposureLedger) Changes() []Change {
	l.mu.Lock()
	defer l.mu.Unlock()

	return cloneChanges(l.changes)
}

// Stats reads the counter block.
func (l *ExposureLedger) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()

	var stats Stats
	for _, item := range l.items {
		stats.Evidence += item.Evidence
		if item.State == StateOpen {
			stats.Live++
		} else {
			stats.Historical++
		}
	}
	stats.Merged = l.merged
	return stats
}

type snapshotInheritance struct {
	Netblock string `json:"netblock"`
	Owner    string `json:"owner"`
}

type ledgerSnapshot struct {
	Version       int                   `json:"version"`
	NextID        int                   `json:"next_id"`
	NextChangeSeq int64                 `json:"next_change_seq"`
	Merged        int                   `json:"merged"`
	Items         []Item                `json:"items"`
	Inheritance   []snapshotInheritance `json:"inheritance"`
	Changes       []Change              `json:"changes"`
}

// Snapshot serialises items, the inheritance table, changes and counters.
func (l *ExposureLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	snap := ledgerSnapshot{
		Version:       1,
		NextID:        l.nextID,
		NextChangeSeq: l.nextSeq,
		Merged:        l.merged,
		Items:         make([]Item, 0, len(l.items)),
		Inheritance:   make([]snapshotInheritance, 0, len(l.inherit)),
		Changes:       cloneChanges(l.changes),
	}
	for _, item := range l.items {
		snap.Items = append(snap.Items, cloneItem(*item))
	}
	sort.Slice(snap.Items, func(i, j int) bool {
		return itemLess(&snap.Items[i], &snap.Items[j])
	})
	for netblock, block := range l.inherit {
		snap.Inheritance = append(snap.Inheritance, snapshotInheritance{
			Netblock: netblock,
			Owner:    block.owner,
		})
	}
	sort.Slice(snap.Inheritance, func(i, j int) bool {
		return snap.Inheritance[i].Netblock < snap.Inheritance[j].Netblock
	})

	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole ledger state with a snapshot, all or nothing.
func (l *ExposureLedger) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}

	var snap ledgerSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != 1 {
		return ErrBadSnapshot
	}
	if snap.NextID < 1 || snap.NextChangeSeq < 1 || snap.Merged < 0 {
		return ErrBadSnapshot
	}

	items := make(map[string]*Item, len(snap.Items))
	identity := make(map[string]string, len(snap.Items))
	seqs := make(map[int64]struct{}, len(snap.Items))
	maxSeq := int64(0)
	for i := range snap.Items {
		item := cloneItem(snap.Items[i])
		if err := validateSnapshotItem(&item); err != nil {
			return ErrBadSnapshot
		}
		if _, duplicate := items[item.ID]; duplicate {
			return ErrBadSnapshot
		}
		if _, duplicate := seqs[item.Seq]; duplicate {
			return ErrBadSnapshot
		}
		seqs[item.Seq] = struct{}{}
		if item.Seq > maxSeq {
			maxSeq = item.Seq
		}
		key, err := identityKey(item.Addr, item.Port, item.Proto, item.CertSubject)
		if err != nil {
			return ErrBadSnapshot
		}
		if _, duplicate := identity[key]; duplicate {
			return ErrBadSnapshot
		}
		items[item.ID] = &item
		identity[key] = item.ID
	}
	if int64(snap.NextID) <= maxSeq {
		return ErrBadSnapshot
	}

	inherit := make(map[string]inheritedBlock, len(snap.Inheritance))
	for _, entry := range snap.Inheritance {
		if strings.TrimSpace(entry.Owner) == "" {
			return ErrBadSnapshot
		}
		prefix, err := netip.ParsePrefix(entry.Netblock)
		if err != nil {
			return ErrBadSnapshot
		}
		prefix = prefix.Masked()
		netblock := prefix.String()
		if _, duplicate := inherit[netblock]; duplicate {
			return ErrBadSnapshot
		}
		inherit[netblock] = inheritedBlock{prefix: prefix, owner: entry.Owner}
	}

	changes := cloneChanges(snap.Changes)
	lastSeq := int64(0)
	for _, change := range changes {
		if change.Seq <= lastSeq || !validChangeKind(change.Kind) || change.ItemID == "" {
			return ErrBadSnapshot
		}
		if change.At.IsZero() {
			return ErrBadSnapshot
		}
		lastSeq = change.Seq
	}
	if snap.NextChangeSeq <= lastSeq {
		return ErrBadSnapshot
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.items = items
	l.identity = identity
	l.inherit = inherit
	l.changes = changes
	l.nextID = snap.NextID
	l.nextSeq = snap.NextChangeSeq
	l.merged = snap.Merged
	return nil
}

func normalizeObservation(obs Observation) (addr string, proto string, state string, key string, err error) {
	if obs.Seen.IsZero() {
		return "", "", "", "", ErrBadObservation
	}
	parsed, parseErr := netip.ParseAddr(obs.Addr)
	if parseErr != nil || parsed.Zone() != "" {
		return "", "", "", "", ErrBadObservation
	}
	if obs.Port < 1 || obs.Port > 65535 {
		return "", "", "", "", ErrBadObservation
	}
	proto = strings.ToLower(obs.Proto)
	if proto != ProtoTCP && proto != ProtoUDP {
		return "", "", "", "", ErrBadObservation
	}
	state = obs.State
	if state != StateOpen && state != StateClosed {
		return "", "", "", "", ErrBadObservation
	}
	addr = parsed.Unmap().String()
	key, keyErr := identityKey(addr, obs.Port, proto, obs.CertSubject)
	if keyErr != nil {
		return "", "", "", "", ErrBadObservation
	}
	return addr, proto, state, key, nil
}

func identityKey(addr string, port int, proto string, cert string) (string, error) {
	parsed, err := netip.ParseAddr(addr)
	if err != nil || parsed.Zone() != "" {
		return "", ErrBadObservation
	}
	addr = parsed.Unmap().String()
	proto = strings.ToLower(proto)
	cert = strings.TrimSpace(cert)
	return addr + "\x00" + strconv.Itoa(port) + "\x00" + proto + "\x00" + cert, nil
}

func (l *ExposureLedger) applyObservation(item *Item, obs Observation, state string) {
	item.Evidence++
	if obs.Seen.Before(item.FirstSeen) {
		item.FirstSeen = obs.Seen
	}
	if obs.Seen.After(item.LastSeen) {
		item.LastSeen = obs.Seen
	}
	if item.State == StateHistorical {
		item.State = StateOpen
		item.Misses = 0
	}

	if state == StateOpen {
		item.State = StateOpen
		item.Misses = 0
		return
	}

	item.Misses++
	if item.Misses < 2 || item.State != StateOpen {
		return
	}
	item.State = StateClosed
	l.appendChange(Change{
		ItemID: item.ID,
		Kind:   ChangeClosed,
		From:   StateOpen,
		To:     StateClosed,
		At:     obs.Seen,
	})
}

func (l *ExposureLedger) ownerFor(addr string) (string, string) {
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		return "", ""
	}
	bestBits := -1
	bestOwner := ""
	for _, block := range l.inherit {
		if !block.prefix.Contains(parsed) {
			continue
		}
		if block.prefix.Bits() > bestBits {
			bestBits = block.prefix.Bits()
			bestOwner = block.owner
		}
	}
	if bestBits < 0 {
		return "", ""
	}
	return bestOwner, OwnerInherited
}

func (l *ExposureLedger) refreshInheritedOwners() {
	items := make([]*Item, 0, len(l.items))
	for _, item := range l.items {
		if item.OwnerSource != OwnerManual {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return itemLess(items[i], items[j])
	})
	for _, item := range items {
		owner, source := l.ownerFor(item.Addr)
		if item.Owner == owner && item.OwnerSource == source {
			continue
		}
		from := item.Owner
		item.Owner = owner
		item.OwnerSource = source
		l.appendChange(Change{
			ItemID: item.ID,
			Kind:   ChangeOwner,
			From:   from,
			To:     owner,
			At:     item.LastSeen,
		})
	}
}

func (l *ExposureLedger) appendChange(change Change) {
	change.Seq = l.nextSeq
	l.nextSeq++
	l.changes = append(l.changes, change)
}

func (l *ExposureLedger) removeItem(item *Item) {
	delete(l.items, item.ID)
	key, err := identityKey(item.Addr, item.Port, item.Proto, item.CertSubject)
	if err == nil && l.identity[key] == item.ID {
		delete(l.identity, key)
	}
}

func validateSnapshotItem(item *Item) error {
	if item.ID == "" || item.Port < 1 || item.Port > 65535 {
		return ErrBadSnapshot
	}
	if item.Proto != ProtoTCP && item.Proto != ProtoUDP {
		return ErrBadSnapshot
	}
	switch item.State {
	case StateOpen, StateClosed, StateHistorical:
	default:
		return ErrBadSnapshot
	}
	if item.FirstSeen.IsZero() || item.LastSeen.IsZero() || item.LastSeen.Before(item.FirstSeen) {
		return ErrBadSnapshot
	}
	if item.Evidence < 1 || item.Misses < 0 || item.Seq < 1 {
		return ErrBadSnapshot
	}
	if item.CertSubject != strings.TrimSpace(item.CertSubject) {
		return ErrBadSnapshot
	}
	switch item.OwnerSource {
	case "":
		if item.Owner != "" {
			return ErrBadSnapshot
		}
	case OwnerInherited, OwnerManual:
		if item.Owner == "" || item.Owner != strings.TrimSpace(item.Owner) {
			return ErrBadSnapshot
		}
	default:
		return ErrBadSnapshot
	}
	if strings.HasPrefix(item.ID, "exp-") {
		seq, err := strconv.Atoi(strings.TrimPrefix(item.ID, "exp-"))
		if err != nil || seq < 1 {
			return ErrBadSnapshot
		}
	} else {
		return ErrBadSnapshot
	}
	return nil
}

func validChangeKind(kind string) bool {
	switch kind {
	case ChangeNew, ChangeOwner, ChangeDowngraded, ChangeMerged, ChangeClosed:
		return true
	default:
		return false
	}
}

func itemLess(a, b *Item) bool {
	if a.Seq == b.Seq {
		return a.ID < b.ID
	}
	return a.Seq < b.Seq
}

func cloneItem(item Item) Item {
	if item.MergedFrom != nil {
		item.MergedFrom = append([]string(nil), item.MergedFrom...)
	}
	return item
}

func cloneChanges(changes []Change) []Change {
	if changes == nil {
		return nil
	}
	return append([]Change(nil), changes...)
}
