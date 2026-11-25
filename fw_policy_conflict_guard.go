// Package task evaluates an ordered firewall policy set: selector matching,
// conflict reporting, first match evaluation with a default deny tail and
// restart safe snapshots.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
)

// Rule actions.
const (
	ActionAllow = "allow"
	ActionDeny  = "deny"
)

// Rule protocols.
const (
	ProtoAny  = "any"
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
	ProtoICMP = "icmp"
)

// Decision reasons.
const (
	ReasonRuleMatch   = "rule_match"
	ReasonDefaultDeny = "default_deny"
)

const (
	snapshotMagic   = "gsb-fw-policy-conflict-guard-v1"
	snapshotVersion = 1
	portWildcard    = -1
)

// Errors returned by the guard.
var (
	ErrBadConfig       = errors.New("guard config is invalid")
	ErrInvalidRule     = errors.New("rule is invalid")
	ErrDuplicateRuleID = errors.New("rule id already exists")
	ErrRuleConflict    = errors.New("rule contradicts an existing rule")
	ErrRuleLimit       = errors.New("rule limit reached")
	ErrRuleNotFound    = errors.New("rule not found")
	ErrBadOrder        = errors.New("reorder is not a full permutation")
	ErrBadPacket       = errors.New("packet is invalid")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// GuardConfig bounds one policy guard. DefaultAction must be deny; MaxRules is
// the hard cap on the number of stored rules.
type GuardConfig struct {
	MaxRules      int
	DefaultAction string
}

// Rule is one firewall policy entry. DstPort zero means every port and is the
// only legal value for icmp rules. Empty CIDR fields mean every address.
type Rule struct {
	ID       int
	Priority int
	Action   string
	Proto    string
	SrcCIDR  string
	DstCIDR  string
	DstPort  int
}

// Packet is one packet header to evaluate.
type Packet struct {
	SrcIP   string
	DstIP   string
	Proto   string
	DstPort int
}

// Decision is the evaluation result of one packet.
type Decision struct {
	Allow  bool
	Action string
	RuleID int
	Reason string
}

// ConflictReport describes how a candidate rule relates to the stored rules.
type ConflictReport struct {
	Shadowed         bool
	ShadowedBy       int
	Contradiction    bool
	SameSelectorWith int
	Redundant        bool
}

type selector struct {
	proto  string
	srcNet *net.IPNet
	dstNet *net.IPNet
	port   int // portWildcard represents DstPort == 0.
}

type ruleEntry struct {
	rule Rule
	sel  selector
	hits uint64
}

// PolicyConflictGuard owns the ordered rule set and the hit statistics.
type PolicyConflictGuard struct {
	cfg GuardConfig

	mu             sync.RWMutex
	rules          map[int]*ruleEntry
	order          []int
	defaultDenied  uint64
	shadowCount    int
	redundantCount int
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotPayload struct {
	Rules          []Rule         `json:"rules"`
	Order          []int          `json:"order"`
	Hits           map[int]uint64 `json:"hits"`
	DefaultDenied  uint64         `json:"default_denied"`
	ShadowCount    int            `json:"shadow_count"`
	RedundantCount int            `json:"redundant_count"`
}

type restoredState struct {
	rules          map[int]*ruleEntry
	order          []int
	defaultDenied  uint64
	shadowCount    int
	redundantCount int
}

// NewPolicyConflictGuard validates the configuration and builds an empty rule
// set.
func NewPolicyConflictGuard(cfg GuardConfig) (*PolicyConflictGuard, error) {
	if cfg.DefaultAction != ActionDeny || cfg.MaxRules <= 0 {
		return nil, ErrBadConfig
	}

	return &PolicyConflictGuard{
		cfg:   cfg,
		rules: make(map[int]*ruleEntry),
		order: make([]int, 0),
	}, nil
}

// AddRule validates one rule, checks it against the stored rules and appends it
// in evaluation order.
func (g *PolicyConflictGuard) AddRule(rule Rule) (int, error) {
	candidateSel, err := validatedSelector(rule)
	if err != nil {
		return 0, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if rule.ID == 0 {
		maxID := 0
		for id := range g.rules {
			if id > maxID {
				maxID = id
			}
		}
		if maxID == math.MaxInt {
			return 0, ErrRuleLimit
		}
		rule.ID = maxID + 1
	} else if _, exists := g.rules[rule.ID]; exists {
		return 0, ErrDuplicateRuleID
	}

	if len(g.rules) >= g.cfg.MaxRules {
		return 0, ErrRuleLimit
	}

	report := g.analyzeLocked(rule, candidateSel)
	if report.Contradiction {
		return 0, ErrRuleConflict
	}

	g.rules[rule.ID] = &ruleEntry{
		rule: rule,
		sel:  candidateSel,
	}
	g.order = append(g.order, rule.ID)

	if report.Shadowed && g.shadowCount < math.MaxInt {
		g.shadowCount++
	}
	if report.Redundant && g.redundantCount < math.MaxInt {
		g.redundantCount++
	}

	return rule.ID, nil
}

// RemoveRule deletes one rule and its hit counter.
func (g *PolicyConflictGuard) RemoveRule(id int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.rules[id]; !exists {
		return ErrRuleNotFound
	}

	delete(g.rules, id)
	for i, existingID := range g.order {
		if existingID == id {
			g.order = append(g.order[:i], g.order[i+1:]...)
			break
		}
	}
	return nil
}

// Check reports shadowing, contradiction and redundancy of a candidate rule.
func (g *PolicyConflictGuard) Check(rule Rule) (ConflictReport, error) {
	candidateSel, err := validatedSelector(rule)
	if err != nil {
		return ConflictReport{}, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.analyzeLocked(rule, candidateSel), nil
}

// Evaluate returns the verdict of the first matching rule, or the default deny
// verdict when nothing matches.
func (g *PolicyConflictGuard) Evaluate(pkt Packet) (Decision, error) {
	srcIP, dstIP, err := validatePacket(pkt)
	if err != nil {
		return Decision{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	for _, id := range g.orderedIDsLocked() {
		entry := g.rules[id]
		if !ruleMatches(entry.sel, pkt.Proto, pkt.DstPort, srcIP, dstIP) {
			continue
		}

		if entry.hits < math.MaxUint64 {
			entry.hits++
		}
		return Decision{
			Allow:  entry.rule.Action == ActionAllow,
			Action: entry.rule.Action,
			RuleID: id,
			Reason: ReasonRuleMatch,
		}, nil
	}

	if g.defaultDenied < math.MaxUint64 {
		g.defaultDenied++
	}
	return Decision{
		Allow:  false,
		Action: ActionDeny,
		RuleID: 0,
		Reason: ReasonDefaultDeny,
	}, nil
}

// Candidates lists the ids of every rule that matches the packet, in
// evaluation order.
func (g *PolicyConflictGuard) Candidates(pkt Packet) ([]int, error) {
	srcIP, dstIP, err := validatePacket(pkt)
	if err != nil {
		return nil, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := make([]int, 0)
	for _, id := range g.orderedIDsLocked() {
		entry := g.rules[id]
		if ruleMatches(entry.sel, pkt.Proto, pkt.DstPort, srcIP, dstIP) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Rules lists the stored rules in evaluation order.
func (g *PolicyConflictGuard) Rules() []Rule {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := g.orderedIDsLocked()
	rules := make([]Rule, 0, len(ids))
	for _, id := range ids {
		rules = append(rules, g.rules[id].rule)
	}
	return rules
}

// Reorder replaces the insertion order with the given full permutation.
func (g *PolicyConflictGuard) Reorder(ids []int) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.isFullPermutationLocked(ids) {
		return ErrBadOrder
	}

	g.order = append([]int(nil), ids...)
	return nil
}

// Hits reports how many packets one rule matched.
func (g *PolicyConflictGuard) Hits(ruleID int) uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if entry, ok := g.rules[ruleID]; ok {
		return entry.hits
	}
	return 0
}

// DefaultDenied reports how many packets fell through to the default deny.
func (g *PolicyConflictGuard) DefaultDenied() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.defaultDenied
}

// ShadowCount reports how many accepted rules were shadowed when added.
func (g *PolicyConflictGuard) ShadowCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.shadowCount
}

// Snapshot serialises the rules, the order, the hit counters and the
// statistics.
func (g *PolicyConflictGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	evaluationIDs := g.orderedIDsLocked()
	payload := snapshotPayload{
		Rules:          make([]Rule, 0, len(evaluationIDs)),
		Order:          append([]int(nil), g.order...),
		Hits:           make(map[int]uint64, len(g.order)),
		DefaultDenied:  g.defaultDenied,
		ShadowCount:    g.shadowCount,
		RedundantCount: g.redundantCount,
	}
	for _, id := range evaluationIDs {
		entry := g.rules[id]
		payload.Rules = append(payload.Rules, entry.rule)
		payload.Hits[id] = entry.hits
	}
	g.mu.RUnlock()

	rawPayload, err := json.Marshal(&payload)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot payload: %w", err)
	}

	checksum := sha256.Sum256(rawPayload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(checksum[:]),
		Payload:  rawPayload,
	}
	data, err := json.Marshal(&envelope)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot envelope: %w", err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *PolicyConflictGuard) Restore(data []byte) error {
	state, err := decodeSnapshot(data, g.cfg.MaxRules)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.rules = state.rules
	g.order = state.order
	g.defaultDenied = state.defaultDenied
	g.shadowCount = state.shadowCount
	g.redundantCount = state.redundantCount
	return nil
}

func (g *PolicyConflictGuard) orderedIDsLocked() []int {
	ids := append([]int(nil), g.order...)
	sort.SliceStable(ids, func(i, j int) bool {
		return g.rules[ids[i]].rule.Priority > g.rules[ids[j]].rule.Priority
	})
	return ids
}

func (g *PolicyConflictGuard) isFullPermutationLocked(ids []int) bool {
	if len(ids) != len(g.rules) {
		return false
	}

	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := g.rules[id]; !exists {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return len(seen) == len(g.rules)
}

func (g *PolicyConflictGuard) analyzeLocked(candidate Rule, candidateSel selector) ConflictReport {
	report := ConflictReport{}
	sameSelectorID := 0
	contradictionID := 0
	shadowedBy := 0
	sameActionHigher := make([]selector, 0)

	for id, entry := range g.rules {
		existing := entry.rule
		existingSel := entry.sel

		if selectorsEqual(existingSel, candidateSel) {
			if sameSelectorID == 0 || id < sameSelectorID {
				sameSelectorID = id
			}
			if existing.Priority == candidate.Priority && existing.Action != candidate.Action {
				report.Contradiction = true
				if contradictionID == 0 || id < contradictionID {
					contradictionID = id
				}
			}
		}

		if existing.Priority > candidate.Priority && selectorCovers(existingSel, candidateSel) {
			report.Shadowed = true
			if shadowedBy == 0 || id < shadowedBy {
				shadowedBy = id
			}
		}

		if existing.Priority > candidate.Priority && existing.Action == candidate.Action {
			sameActionHigher = append(sameActionHigher, existingSel)
		}
	}

	if sameSelectorID != 0 {
		report.SameSelectorWith = sameSelectorID
	}
	if report.Contradiction && contradictionID != 0 {
		report.SameSelectorWith = contradictionID
	}
	if report.Shadowed {
		report.ShadowedBy = shadowedBy
	}
	if unionCovers(candidateSel, sameActionHigher) {
		report.Redundant = true
	}

	return report
}

func validatedSelector(rule Rule) (selector, error) {
	if rule.ID < 0 {
		return selector{}, fmt.Errorf("%w: id must not be negative", ErrInvalidRule)
	}
	if rule.Action != ActionAllow && rule.Action != ActionDeny {
		return selector{}, fmt.Errorf("%w: unsupported action %q", ErrInvalidRule, rule.Action)
	}
	if !validProtocol(rule.Proto) {
		return selector{}, fmt.Errorf("%w: unsupported protocol %q", ErrInvalidRule, rule.Proto)
	}
	if rule.DstPort < 0 || rule.DstPort > 65535 {
		return selector{}, fmt.Errorf("%w: destination port %d is out of range", ErrInvalidRule, rule.DstPort)
	}
	if rule.Proto == ProtoICMP && rule.DstPort != 0 {
		return selector{}, fmt.Errorf("%w: icmp rules must use port 0", ErrInvalidRule)
	}

	srcNet, err := parseStrictCIDR(rule.SrcCIDR)
	if err != nil {
		return selector{}, fmt.Errorf("%w: source cidr %q: %v", ErrInvalidRule, rule.SrcCIDR, err)
	}
	dstNet, err := parseStrictCIDR(rule.DstCIDR)
	if err != nil {
		return selector{}, fmt.Errorf("%w: destination cidr %q: %v", ErrInvalidRule, rule.DstCIDR, err)
	}

	port := rule.DstPort
	if port == 0 {
		port = portWildcard
	}
	return selector{
		proto:  rule.Proto,
		srcNet: srcNet,
		dstNet: dstNet,
		port:   port,
	}, nil
}

func validProtocol(proto string) bool {
	switch proto {
	case ProtoTCP, ProtoUDP, ProtoICMP, ProtoAny:
		return true
	default:
		return false
	}
}

func validatePacket(pkt Packet) (net.IP, net.IP, error) {
	if !validProtocol(pkt.Proto) {
		return nil, nil, fmt.Errorf("%w: unsupported protocol %q", ErrBadPacket, pkt.Proto)
	}
	if pkt.DstPort < 0 || pkt.DstPort > 65535 {
		return nil, nil, fmt.Errorf("%w: destination port %d is out of range", ErrBadPacket, pkt.DstPort)
	}

	srcIP := net.ParseIP(pkt.SrcIP)
	if srcIP == nil {
		return nil, nil, fmt.Errorf("%w: source address %q", ErrBadPacket, pkt.SrcIP)
	}
	dstIP := net.ParseIP(pkt.DstIP)
	if dstIP == nil {
		return nil, nil, fmt.Errorf("%w: destination address %q", ErrBadPacket, pkt.DstIP)
	}
	return srcIP, dstIP, nil
}

func parseStrictCIDR(value string) (*net.IPNet, error) {
	if value == "" {
		return nil, nil
	}

	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		return nil, err
	}
	if !ip.Equal(network.IP) {
		return nil, errors.New("address contains host bits")
	}
	return network, nil
}

func ruleMatches(sel selector, packetProto string, packetPort int, srcIP, dstIP net.IP) bool {
	if sel.proto != ProtoAny && sel.proto != packetProto {
		return false
	}
	if sel.port != portWildcard && sel.port != packetPort {
		return false
	}
	if sel.srcNet != nil && !sel.srcNet.Contains(srcIP) {
		return false
	}
	if sel.dstNet != nil && !sel.dstNet.Contains(dstIP) {
		return false
	}
	return true
}

func selectorsEqual(a, b selector) bool {
	return selectorCovers(a, b) && selectorCovers(b, a)
}

func selectorCovers(outer, inner selector) bool {
	return protocolCovers(outer.proto, inner.proto) &&
		portCovers(outer.port, inner.port) &&
		ipNetCovers(outer.srcNet, inner.srcNet) &&
		ipNetCovers(outer.dstNet, inner.dstNet)
}

func protocolCovers(outer, inner string) bool {
	return outer == ProtoAny || outer == inner
}

func portCovers(outer, inner int) bool {
	return outer == portWildcard || outer == inner
}

func ipNetCovers(outer, inner *net.IPNet) bool {
	if outer == nil {
		return true
	}
	if inner == nil {
		return false
	}

	outerOnes, outerBits := outer.Mask.Size()
	innerOnes, innerBits := inner.Mask.Size()
	if outerBits != innerBits || outerOnes > innerOnes {
		return false
	}
	return outer.Contains(inner.IP)
}

func selectorIntersects(a, b selector) bool {
	return protocolsIntersect(a.proto, b.proto) &&
		portsIntersect(a.port, b.port) &&
		ipNetsIntersect(a.srcNet, b.srcNet) &&
		ipNetsIntersect(a.dstNet, b.dstNet)
}

func protocolsIntersect(a, b string) bool {
	return a == ProtoAny || b == ProtoAny || a == b
}

func portsIntersect(a, b int) bool {
	return a == portWildcard || b == portWildcard || a == b
}

func ipNetsIntersect(a, b *net.IPNet) bool {
	if a == nil || b == nil {
		return true
	}
	return ipNetCovers(a, b) || ipNetCovers(b, a)
}

// unionCovers reports whether the union of rules covers the complete candidate
// selector. CIDRs and protocols are split into disjoint children. Port zero is
// a wildcard selector, so only wildcard ports cover another wildcard scope.
func unionCovers(box selector, rules []selector) bool {
	for _, candidate := range rules {
		if selectorCovers(candidate, box) {
			return true
		}
	}

	active := make([]selector, 0, len(rules))
	for _, candidate := range rules {
		if selectorIntersects(candidate, box) {
			active = append(active, candidate)
		}
	}
	if len(active) == 0 {
		return false
	}

	if box.proto == ProtoAny {
		for _, proto := range []string{ProtoTCP, ProtoUDP, ProtoICMP} {
			child := box
			child.proto = proto
			if !unionCovers(child, active) {
				return false
			}
		}
		return true
	}

	srcDistance := ipSplitDistance(box.srcNet, active, true)
	dstDistance := ipSplitDistance(box.dstNet, active, false)
	if srcDistance == math.MaxInt && dstDistance == math.MaxInt {
		return false
	}

	if srcDistance <= dstDistance {
		left, right, ok := splitIPNet(box.srcNet)
		if !ok {
			return false
		}
		leftBox := box
		leftBox.srcNet = left
		rightBox := box
		rightBox.srcNet = right
		return unionCovers(leftBox, active) && unionCovers(rightBox, active)
	}

	left, right, ok := splitIPNet(box.dstNet)
	if !ok {
		return false
	}
	leftBox := box
	leftBox.dstNet = left
	rightBox := box
	rightBox.dstNet = right
	return unionCovers(leftBox, active) && unionCovers(rightBox, active)
}

func ipSplitDistance(box *net.IPNet, rules []selector, useSource bool) int {
	if box == nil {
		for _, candidate := range rules {
			network := candidate.dstNet
			if useSource {
				network = candidate.srcNet
			}
			if network != nil {
				return 1
			}
		}
		return math.MaxInt
	}

	ones, bits := box.Mask.Size()
	best := math.MaxInt
	for _, candidate := range rules {
		network := candidate.dstNet
		if useSource {
			network = candidate.srcNet
		}
		if network == nil {
			continue
		}
		candidateOnes, candidateBits := network.Mask.Size()
		if candidateBits != bits || candidateOnes <= ones {
			continue
		}
		if distance := candidateOnes - ones; distance < best {
			best = distance
		}
	}
	return best
}

func splitIPNet(network *net.IPNet) (*net.IPNet, *net.IPNet, bool) {
	if network == nil {
		v4 := &net.IPNet{
			IP:   append(net.IP(nil), net.IPv4zero...).To4(),
			Mask: net.CIDRMask(0, 32),
		}
		v6 := &net.IPNet{
			IP:   append(net.IP(nil), net.IPv6unspecified...),
			Mask: net.CIDRMask(0, 128),
		}
		return v4, v6, true
	}

	ones, bits := network.Mask.Size()
	if ones >= bits {
		return nil, nil, false
	}

	baseIP := append(net.IP(nil), network.IP...)
	if v4 := network.IP.To4(); v4 != nil {
		baseIP = append(net.IP(nil), v4...)
		bits = 32
	} else {
		baseIP = append(net.IP(nil), network.IP.To16()...)
		bits = 128
	}

	childMask := net.CIDRMask(ones+1, bits)
	leftIP := append(net.IP(nil), baseIP...).Mask(childMask)

	rightIP := append(net.IP(nil), baseIP...)
	byteIndex := ones / 8
	bitShift := uint(7 - ones%8)
	rightIP[byteIndex] |= 1 << bitShift
	rightIP = rightIP.Mask(childMask)

	return &net.IPNet{IP: leftIP, Mask: childMask},
		&net.IPNet{IP: rightIP, Mask: childMask},
		true
}

func decodeSnapshot(data []byte, maxRules int) (restoredState, error) {
	if len(data) == 0 {
		return restoredState{}, fmt.Errorf("%w: empty payload", ErrBadSnapshot)
	}

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return restoredState{}, fmt.Errorf("%w: invalid envelope: %v", ErrBadSnapshot, err)
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion {
		return restoredState{}, fmt.Errorf("%w: unsupported snapshot", ErrBadSnapshot)
	}

	checksum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(checksum[:]) {
		return restoredState{}, fmt.Errorf("%w: checksum mismatch", ErrBadSnapshot)
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return restoredState{}, fmt.Errorf("%w: invalid payload: %v", ErrBadSnapshot, err)
	}
	if payload.ShadowCount < 0 || payload.RedundantCount < 0 {
		return restoredState{}, fmt.Errorf("%w: negative statistic", ErrBadSnapshot)
	}
	if len(payload.Rules) > maxRules {
		return restoredState{}, fmt.Errorf("%w: too many rules", ErrBadSnapshot)
	}
	if len(payload.Hits) != len(payload.Rules) {
		return restoredState{}, fmt.Errorf("%w: hit counters do not match rules", ErrBadSnapshot)
	}

	state := restoredState{
		rules:          make(map[int]*ruleEntry, len(payload.Rules)),
		order:          append([]int(nil), payload.Order...),
		defaultDenied:  payload.DefaultDenied,
		shadowCount:    payload.ShadowCount,
		redundantCount: payload.RedundantCount,
	}

	for _, rule := range payload.Rules {
		sel, err := validatedSelector(rule)
		if err != nil {
			return restoredState{}, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
		}
		if rule.ID <= 0 {
			return restoredState{}, fmt.Errorf("%w: restored rule has invalid id", ErrBadSnapshot)
		}
		if _, duplicate := state.rules[rule.ID]; duplicate {
			return restoredState{}, fmt.Errorf("%w: duplicate rule id %d", ErrBadSnapshot, rule.ID)
		}
		if _, hitExists := payload.Hits[rule.ID]; !hitExists {
			return restoredState{}, fmt.Errorf("%w: missing hit counter for rule %d", ErrBadSnapshot, rule.ID)
		}

		state.rules[rule.ID] = &ruleEntry{
			rule: rule,
			sel:  sel,
			hits: payload.Hits[rule.ID],
		}
	}

	if len(state.order) != len(state.rules) {
		return restoredState{}, fmt.Errorf("%w: order length does not match rules", ErrBadSnapshot)
	}
	seen := make(map[int]struct{}, len(state.order))
	for _, id := range state.order {
		if _, exists := state.rules[id]; !exists {
			return restoredState{}, fmt.Errorf("%w: order references missing rule %d", ErrBadSnapshot, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return restoredState{}, fmt.Errorf("%w: order contains duplicate id %d", ErrBadSnapshot, id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != len(state.rules) {
		return restoredState{}, fmt.Errorf("%w: order is missing rules", ErrBadSnapshot)
	}

	return state, nil
}
