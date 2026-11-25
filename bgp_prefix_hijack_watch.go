// Package task watches BGP announcements against a learned baseline of
// legitimate prefix origins: it scores origin, prefix length, path shape and
// policy violations, and keeps per peer counters plus restart safe snapshots.
package task

import (
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Verdict levels.
const (
	LevelClean      = "clean"
	LevelSuspected  = "suspected"
	LevelConfirmed  = "confirmed"
	LevelSuppressed = "suppressed"
)

// Reason codes attached to a verdict.
const (
	ReasonOriginMismatch = "origin_mismatch"
	ReasonMoreSpecific   = "more_specific"
	ReasonUncovered      = "uncovered_prefix"
	ReasonPathSpoofed    = "path_spoofed"
	ReasonPrivateAS      = "private_as"
	ReasonPathLoop       = "path_loop"
)

// Score weights and level thresholds.
const (
	ScoreOriginMismatch = 50
	ScoreMoreSpecific   = 30
	ScoreUncovered      = 20
	ScorePathSpoofed    = 40
	ScorePrivateAS      = 20
	ScorePathLoop       = 25

	SuspectedThreshold = 30
	ConfirmedThreshold = 70
)

// Errors returned by the watch.
var (
	ErrBadConfig        = errors.New("watch config is invalid")
	ErrBadPrefix        = errors.New("prefix is not a canonical cidr")
	ErrBadRoute         = errors.New("route is invalid")
	ErrOriginConflict   = errors.New("prefix already learned with another origin")
	ErrBaselineLimit    = errors.New("baseline limit reached")
	ErrBaselineNotFound = errors.New("baseline not found")
	ErrBadSuppression   = errors.New("suppression window is invalid")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

const (
	snapshotVersion = 1
	maxPeerStats    = 1 << 20
)

// Config bounds one watch instance.
type Config struct {
	MaxBaselines         int
	MaxHistory           int
	AllowMultipleOrigins bool
	Now                  func() int64
}

// Baseline is one learned legitimate prefix.
type Baseline struct {
	Prefix    string
	OriginAS  uint32
	MaxLength int
}

// Route is one observed announcement.
type Route struct {
	Prefix   string
	OriginAS uint32
	ASPath   []uint32
	PeerAS   uint32
	Seq      uint64
	At       int64
}

// Verdict is the hijack assessment of one announcement.
type Verdict struct {
	Prefix     string
	Level      string
	Reasons    []string
	Score      int
	Suppressed bool
}

// PeerStat is the per peer counter record.
type PeerStat struct {
	PeerAS       uint32
	Observations uint64
	Hijacks      uint64
}

type prefixBaselines struct {
	origins map[uint32]Baseline
	order   []uint32
}

func (p *prefixBaselines) primary() Baseline {
	if p == nil || len(p.order) == 0 {
		return Baseline{}
	}
	return p.origins[p.order[0]]
}

func (p *prefixBaselines) allows(origin uint32) bool {
	_, ok := p.origins[origin]
	return ok
}

func (p *prefixBaselines) maxLengthFor(origin uint32) int {
	if b, ok := p.origins[origin]; ok {
		return b.MaxLength
	}
	return p.primary().MaxLength
}

// PrefixHijackWatch owns the baseline, the history, the suppression windows
// and the per peer counters.
type PrefixHijackWatch struct {
	cfg Config

	mu           sync.RWMutex
	baselines    map[string]*prefixBaselines
	histories    map[string][]Verdict
	suppressions map[string]int64
	peers        map[uint32]PeerStat
	peerOrder    []uint32
}

// NewPrefixHijackWatch validates the configuration and builds an empty watch.
func NewPrefixHijackWatch(cfg Config) (*PrefixHijackWatch, error) {
	if cfg.MaxBaselines <= 0 || cfg.MaxHistory <= 0 {
		return nil, ErrBadConfig
	}
	return &PrefixHijackWatch{
		cfg:          cfg,
		baselines:    make(map[string]*prefixBaselines),
		histories:    make(map[string][]Verdict),
		suppressions: make(map[string]int64),
		peers:        make(map[uint32]PeerStat),
	}, nil
}

// Learn registers one legitimate baseline. Learning the same prefix and origin
// again is an idempotent update.
func (w *PrefixHijackWatch) Learn(b Baseline) error {
	prefix, key, ok := canonicalPrefix(b.Prefix)
	if !ok || b.OriginAS == 0 {
		return ErrBadPrefix
	}
	if b.MaxLength < prefix.Bits() || b.MaxLength > prefix.Addr().BitLen() {
		return ErrBadPrefix
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	record, exists := w.baselines[key]
	if !exists {
		if len(w.baselines) >= w.cfg.MaxBaselines {
			return ErrBaselineLimit
		}
		record = &prefixBaselines{
			origins: make(map[uint32]Baseline),
			order:   make([]uint32, 0, 1),
		}
		w.baselines[key] = record
	}

	if _, knownOrigin := record.origins[b.OriginAS]; knownOrigin {
		record.origins[b.OriginAS] = Baseline{
			Prefix:    key,
			OriginAS:  b.OriginAS,
			MaxLength: b.MaxLength,
		}
		return nil
	}
	if exists && !w.cfg.AllowMultipleOrigins {
		return ErrOriginConflict
	}

	record.origins[b.OriginAS] = Baseline{
		Prefix:    key,
		OriginAS:  b.OriginAS,
		MaxLength: b.MaxLength,
	}
	record.order = append(record.order, b.OriginAS)
	return nil
}

// RemoveBaseline withdraws one baseline, keeping its recorded history.
func (w *PrefixHijackWatch) RemoveBaseline(prefix string) error {
	_, key, ok := canonicalPrefix(prefix)
	if !ok {
		return ErrBadPrefix
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.baselines[key]; !exists {
		return ErrBaselineNotFound
	}
	delete(w.baselines, key)
	return nil
}

// BaselineFor reports the primary baseline of a prefix.
func (w *PrefixHijackWatch) BaselineFor(prefix string) (Baseline, bool) {
	_, key, ok := canonicalPrefix(prefix)
	if !ok {
		return Baseline{}, false
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	record, exists := w.baselines[key]
	if !exists {
		return Baseline{}, false
	}
	return record.primary(), true
}

// Observe scores one announcement and updates the history and the counters.
func (w *PrefixHijackWatch) Observe(route Route) (Verdict, error) {
	prefix, key, ok := canonicalPrefix(route.Prefix)
	if !ok {
		return Verdict{}, ErrBadPrefix
	}
	if route.OriginAS == 0 || route.PeerAS == 0 || len(route.ASPath) == 0 {
		return Verdict{}, ErrBadRoute
	}
	for _, as := range route.ASPath {
		if as == 0 {
			return Verdict{}, ErrBadRoute
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	score := 0
	reasons := make([]string, 0, 6)

	record := w.findBaselineLocked(prefix, key)
	if record == nil {
		score += ScoreUncovered
		reasons = append(reasons, ReasonUncovered)
	} else {
		if !record.allows(route.OriginAS) {
			score += ScoreOriginMismatch
			reasons = append(reasons, ReasonOriginMismatch)
		}
		if prefix.Bits() > record.maxLengthFor(route.OriginAS) {
			score += ScoreMoreSpecific
			reasons = append(reasons, ReasonMoreSpecific)
		}
	}

	if route.ASPath[0] != route.PeerAS {
		score += ScorePathSpoofed
		reasons = append(reasons, ReasonPathSpoofed)
	}
	if hasPrivateAS(route.ASPath) {
		score += ScorePrivateAS
		reasons = append(reasons, ReasonPrivateAS)
	}
	if hasPathLoop(route.ASPath) {
		score += ScorePathLoop
		reasons = append(reasons, ReasonPathLoop)
	}

	level := LevelClean
	switch {
	case score >= ConfirmedThreshold:
		level = LevelConfirmed
	case score >= SuspectedThreshold:
		level = LevelSuspected
	}

	suppressed := false
	if until, exists := w.suppressions[key]; exists && w.now() < until {
		suppressed = true
		level = LevelSuppressed
	}

	verdict := Verdict{
		Prefix:     key,
		Level:      level,
		Reasons:    reasons,
		Score:      score,
		Suppressed: suppressed,
	}

	w.recordObservationLocked(route.PeerAS, verdict)
	return verdict, nil
}

// History lists the non suppressed verdicts of one prefix in arrival order.
func (w *PrefixHijackWatch) History(prefix string) []Verdict {
	_, key, ok := canonicalPrefix(prefix)
	if !ok {
		return nil
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	entries := w.histories[key]
	if len(entries) == 0 {
		return nil
	}
	out := make([]Verdict, len(entries))
	for i, entry := range entries {
		out[i] = cloneVerdict(entry)
	}
	return out
}

// Suppress silences one prefix until the given unix time, extending an
// existing window when the new deadline is later.
func (w *PrefixHijackWatch) Suppress(prefix string, until int64) error {
	_, key, ok := canonicalPrefix(prefix)
	if !ok {
		return ErrBadPrefix
	}
	if until <= w.now() {
		return ErrBadSuppression
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if current, exists := w.suppressions[key]; exists && current >= until {
		return nil
	}
	w.suppressions[key] = until
	return nil
}

// Suppressed reports whether one prefix is inside a suppression window now.
func (w *PrefixHijackWatch) Suppressed(prefix string) bool {
	_, key, ok := canonicalPrefix(prefix)
	if !ok {
		return false
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	until, exists := w.suppressions[key]
	return exists && w.now() < until
}

// PeerStats reports the counters of one peer.
func (w *PrefixHijackWatch) PeerStats(peerAS uint32) PeerStat {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.peers[peerAS]
}

// TopOffenders lists the peers with hijack hits, worst first, ties broken by
// the smaller AS number.
func (w *PrefixHijackWatch) TopOffenders(n int) []uint32 {
	if n <= 0 {
		return []uint32{}
	}

	w.mu.RLock()
	stats := make([]PeerStat, 0, len(w.peers))
	for _, stat := range w.peers {
		if stat.Hijacks > 0 {
			stats = append(stats, stat)
		}
	}
	w.mu.RUnlock()

	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Hijacks != stats[j].Hijacks {
			return stats[i].Hijacks > stats[j].Hijacks
		}
		return stats[i].PeerAS < stats[j].PeerAS
	})
	if len(stats) > n {
		stats = stats[:n]
	}
	out := make([]uint32, len(stats))
	for i, stat := range stats {
		out[i] = stat.PeerAS
	}
	return out
}

type snapshotSuppression struct {
	Prefix string `json:"prefix"`
	Until  int64  `json:"until"`
}

type snapshotHistory struct {
	Prefix   string    `json:"prefix"`
	Verdicts []Verdict `json:"verdicts"`
}

type snapshotDocument struct {
	Version      int                   `json:"version"`
	Baselines    []Baseline            `json:"baselines"`
	Suppressions []snapshotSuppression `json:"suppressions"`
	History      []snapshotHistory     `json:"history"`
	Peers        []PeerStat            `json:"peers"`
}

// Snapshot serialises the baseline, the history, the suppression windows and
// the peer counters.
func (w *PrefixHijackWatch) Snapshot() ([]byte, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	doc := snapshotDocument{
		Version:      snapshotVersion,
		Baselines:    make([]Baseline, 0),
		Suppressions: make([]snapshotSuppression, 0, len(w.suppressions)),
		History:      make([]snapshotHistory, 0, len(w.histories)),
		Peers:        make([]PeerStat, 0, len(w.peers)),
	}

	baselineKeys := make([]string, 0, len(w.baselines))
	for key := range w.baselines {
		baselineKeys = append(baselineKeys, key)
	}
	sort.Strings(baselineKeys)
	for _, key := range baselineKeys {
		record := w.baselines[key]
		for _, origin := range record.order {
			doc.Baselines = append(doc.Baselines, record.origins[origin])
		}
	}

	suppressionKeys := make([]string, 0, len(w.suppressions))
	for key := range w.suppressions {
		suppressionKeys = append(suppressionKeys, key)
	}
	sort.Strings(suppressionKeys)
	for _, key := range suppressionKeys {
		doc.Suppressions = append(doc.Suppressions, snapshotSuppression{
			Prefix: key,
			Until:  w.suppressions[key],
		})
	}

	historyKeys := make([]string, 0, len(w.histories))
	for key := range w.histories {
		historyKeys = append(historyKeys, key)
	}
	sort.Strings(historyKeys)
	for _, key := range historyKeys {
		entries := w.histories[key]
		copied := make([]Verdict, len(entries))
		for i, entry := range entries {
			copied[i] = cloneVerdict(entry)
		}
		doc.History = append(doc.History, snapshotHistory{Prefix: key, Verdicts: copied})
	}

	for _, peerAS := range w.peerOrder {
		if stat, exists := w.peers[peerAS]; exists {
			doc.Peers = append(doc.Peers, stat)
		}
	}

	return json.Marshal(doc)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (w *PrefixHijackWatch) Restore(data []byte) error {
	var doc snapshotDocument
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	newBaselines := make(map[string]*prefixBaselines)
	for _, b := range doc.Baselines {
		prefix, key, ok := canonicalPrefix(b.Prefix)
		if !ok || b.OriginAS == 0 {
			return ErrBadSnapshot
		}
		if b.MaxLength < prefix.Bits() || b.MaxLength > prefix.Addr().BitLen() {
			return ErrBadSnapshot
		}

		record, exists := newBaselines[key]
		if !exists {
			if len(newBaselines) >= w.cfg.MaxBaselines {
				return ErrBadSnapshot
			}
			record = &prefixBaselines{
				origins: make(map[uint32]Baseline),
				order:   make([]uint32, 0, 1),
			}
			newBaselines[key] = record
		}
		if _, duplicate := record.origins[b.OriginAS]; duplicate {
			return ErrBadSnapshot
		}
		record.origins[b.OriginAS] = Baseline{
			Prefix:    key,
			OriginAS:  b.OriginAS,
			MaxLength: b.MaxLength,
		}
		record.order = append(record.order, b.OriginAS)
	}

	newSuppressions := make(map[string]int64, len(doc.Suppressions))
	for _, suppression := range doc.Suppressions {
		_, key, ok := canonicalPrefix(suppression.Prefix)
		if !ok {
			return ErrBadSnapshot
		}
		if _, duplicate := newSuppressions[key]; duplicate {
			return ErrBadSnapshot
		}
		newSuppressions[key] = suppression.Until
	}

	newHistories := make(map[string][]Verdict, len(doc.History))
	for _, history := range doc.History {
		_, key, ok := canonicalPrefix(history.Prefix)
		if !ok || len(history.Verdicts) > w.cfg.MaxHistory {
			return ErrBadSnapshot
		}
		if _, duplicate := newHistories[key]; duplicate {
			return ErrBadSnapshot
		}
		entries := make([]Verdict, len(history.Verdicts))
		for i, verdict := range history.Verdicts {
			if !validSnapshotVerdict(key, verdict) {
				return ErrBadSnapshot
			}
			entries[i] = cloneVerdict(verdict)
		}
		if len(entries) > 0 {
			newHistories[key] = entries
		}
	}

	newPeers := make(map[uint32]PeerStat, len(doc.Peers))
	newPeerOrder := make([]uint32, 0, len(doc.Peers))
	for _, stat := range doc.Peers {
		if stat.PeerAS == 0 || len(newPeers) >= maxPeerStats {
			return ErrBadSnapshot
		}
		if _, duplicate := newPeers[stat.PeerAS]; duplicate {
			return ErrBadSnapshot
		}
		newPeers[stat.PeerAS] = stat
		newPeerOrder = append(newPeerOrder, stat.PeerAS)
	}

	w.mu.Lock()
	w.baselines = newBaselines
	w.suppressions = newSuppressions
	w.histories = newHistories
	w.peers = newPeers
	w.peerOrder = newPeerOrder
	w.mu.Unlock()
	return nil
}

func (w *PrefixHijackWatch) now() int64 {
	if w.cfg.Now != nil {
		return w.cfg.Now()
	}
	return time.Now().Unix()
}

func (w *PrefixHijackWatch) findBaselineLocked(prefix netip.Prefix, key string) *prefixBaselines {
	if record, exists := w.baselines[key]; exists {
		return record
	}

	var best *prefixBaselines
	bestBits := -1
	for baseKey, record := range w.baselines {
		basePrefix, _, ok := canonicalPrefix(baseKey)
		if !ok || basePrefix.Bits() > prefix.Bits() {
			continue
		}
		if !basePrefix.Contains(prefix.Addr()) {
			continue
		}
		if basePrefix.Bits() > bestBits {
			best = record
			bestBits = basePrefix.Bits()
		}
	}
	return best
}

func (w *PrefixHijackWatch) recordObservationLocked(peerAS uint32, verdict Verdict) {
	stat, exists := w.peers[peerAS]
	if !exists {
		if len(w.peers) >= maxPeerStats {
			w.evictOldestPeerLocked()
		}
		w.peerOrder = append(w.peerOrder, peerAS)
	}
	stat.PeerAS = peerAS
	stat.Observations++
	if !verdict.Suppressed &&
		(verdict.Level == LevelSuspected || verdict.Level == LevelConfirmed) {
		stat.Hijacks++
	}
	w.peers[peerAS] = stat

	if verdict.Suppressed {
		return
	}

	entries := w.histories[verdict.Prefix]
	if len(entries) >= w.cfg.MaxHistory {
		entries = append(entries[:0], entries[1:]...)
	}
	entries = append(entries, cloneVerdict(verdict))
	w.histories[verdict.Prefix] = entries
}

func (w *PrefixHijackWatch) evictOldestPeerLocked() {
	for len(w.peerOrder) > 0 {
		oldest := w.peerOrder[0]
		w.peerOrder = w.peerOrder[1:]
		if _, exists := w.peers[oldest]; exists {
			delete(w.peers, oldest)
			return
		}
	}
}

func canonicalPrefix(raw string) (netip.Prefix, string, bool) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, "", false
	}
	if prefix.Addr().Zone() != "" {
		return netip.Prefix{}, "", false
	}
	masked := prefix.Masked()
	if prefix != masked {
		return netip.Prefix{}, "", false
	}
	return masked, masked.String(), true
}

func hasPrivateAS(path []uint32) bool {
	for _, as := range path {
		if (as >= 64512 && as <= 65534) ||
			(as >= 4200000000 && as <= 4294967294) {
			return true
		}
	}
	return false
}

func hasPathLoop(path []uint32) bool {
	seen := make(map[uint32]struct{}, len(path))
	for _, as := range path {
		if _, exists := seen[as]; exists {
			return true
		}
		seen[as] = struct{}{}
	}
	return false
}

func cloneVerdict(v Verdict) Verdict {
	clone := v
	if v.Reasons != nil {
		clone.Reasons = append([]string(nil), v.Reasons...)
	}
	return clone
}

func validSnapshotVerdict(prefix string, v Verdict) bool {
	if v.Prefix != prefix || v.Suppressed || v.Score < 0 {
		return false
	}
	switch v.Level {
	case LevelClean, LevelSuspected, LevelConfirmed:
	default:
		return false
	}

	seen := make(map[string]struct{}, len(v.Reasons))
	for _, reason := range v.Reasons {
		switch reason {
		case ReasonOriginMismatch, ReasonMoreSpecific, ReasonUncovered,
			ReasonPathSpoofed, ReasonPrivateAS, ReasonPathLoop:
		default:
			return false
		}
		if _, duplicate := seen[reason]; duplicate {
			return false
		}
		seen[reason] = struct{}{}
	}
	return true
}
