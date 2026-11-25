// Package task evaluates an ordered firewall policy set: selector matching,
// conflict reporting, first match evaluation with a default deny tail and
// restart safe snapshots.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// PolicyConflictGuard owns the ordered rule set and the hit statistics.
type PolicyConflictGuard struct {
	cfg GuardConfig
}

// NewPolicyConflictGuard validates the configuration and builds an empty rule
// set.
func NewPolicyConflictGuard(cfg GuardConfig) (*PolicyConflictGuard, error) {
	return nil, errNotImplemented
}

// AddRule validates one rule, checks it against the stored rules and appends it
// in evaluation order.
func (g *PolicyConflictGuard) AddRule(rule Rule) (int, error) {
	return 0, errNotImplemented
}

// RemoveRule deletes one rule and its hit counter.
func (g *PolicyConflictGuard) RemoveRule(id int) error {
	return errNotImplemented
}

// Check reports shadowing, contradiction and redundancy of a candidate rule.
func (g *PolicyConflictGuard) Check(rule Rule) (ConflictReport, error) {
	return ConflictReport{}, errNotImplemented
}

// Evaluate returns the verdict of the first matching rule, or the default deny
// verdict when nothing matches.
func (g *PolicyConflictGuard) Evaluate(pkt Packet) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Candidates lists the ids of every rule that matches the packet, in
// evaluation order.
func (g *PolicyConflictGuard) Candidates(pkt Packet) ([]int, error) {
	return nil, errNotImplemented
}

// Rules lists the stored rules in evaluation order.
func (g *PolicyConflictGuard) Rules() []Rule {
	return nil
}

// Reorder replaces the insertion order with the given full permutation.
func (g *PolicyConflictGuard) Reorder(ids []int) error {
	return errNotImplemented
}

// Hits reports how many packets one rule matched.
func (g *PolicyConflictGuard) Hits(ruleID int) uint64 {
	return 0
}

// DefaultDenied reports how many packets fell through to the default deny.
func (g *PolicyConflictGuard) DefaultDenied() uint64 {
	return 0
}

// ShadowCount reports how many accepted rules were shadowed when added.
func (g *PolicyConflictGuard) ShadowCount() int {
	return 0
}

// Snapshot serialises the rules, the order, the hit counters and the
// statistics.
func (g *PolicyConflictGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *PolicyConflictGuard) Restore(data []byte) error {
	return errNotImplemented
}
