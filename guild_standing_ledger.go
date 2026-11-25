// Package task keeps guild contribution ledgers: weighted contribution,
// two threshold promotion and weekly activity decay with demotion.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Rank change reasons.
const (
	ReasonPromoted    = "promoted"
	ReasonNotEligible = "not_eligible"
	ReasonMaxRank     = "max_rank"
	ReasonDemoted     = "demoted"
)

// Defaults used when Options leaves a field unset.
const (
	DefaultWeekSeconds   = 604800
	DefaultActivityFloor = 100
	DefaultDecayPermille = 500
	DefaultMaxMembers    = 50
)

// Errors returned by the ledger.
var (
	ErrBadGuild       = errors.New("guild id is required")
	ErrBadMember      = errors.New("member is required")
	ErrBadTime        = errors.New("timestamp must be positive")
	ErrMemberExists   = errors.New("member already belongs to the guild")
	ErrGuildFull      = errors.New("guild member limit reached")
	ErrMemberNotFound = errors.New("member not found")
	ErrUnknownGuild   = errors.New("guild not found")
	ErrBadEvent       = errors.New("contribution event id is required")
	ErrBadAmount      = errors.New("contribution amount must be positive")
	ErrBadKind        = errors.New("unknown contribution kind")
	ErrBadRankRule    = errors.New("invalid rank ladder")
	ErrBadWeek        = errors.New("week index must be positive")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// RankRule is one step of the guild ladder.
type RankRule struct {
	Name             string `json:"name"`
	MinContribution  int64  `json:"min_contribution"`
	MinTenureSeconds int64  `json:"min_tenure_seconds"`
}

// Options bounds the ledger.
type Options struct {
	Ranks         []RankRule
	KindWeights   map[string]int
	WeekSeconds   int64
	ActivityFloor int64
	DecayPermille int
	MaxMembers    int
}

// Guild is one guild header.
type Guild struct {
	ID      string `json:"id"`
	Master  string `json:"master"`
	Members int    `json:"members"`
}

// Standing is the public view of one member.
type Standing struct {
	GuildID      string `json:"guild_id"`
	Member       string `json:"member"`
	Rank         string `json:"rank"`
	Contribution int64  `json:"contribution"`
	JoinedAt     int64  `json:"joined_at"`
}

// RankChange reports one promotion or demotion.
type RankChange struct {
	GuildID       string `json:"guild_id"`
	Member        string `json:"member"`
	From          string `json:"from"`
	To            string `json:"to"`
	Changed       bool   `json:"changed"`
	Reason        string `json:"reason"`
	Contribution  int64  `json:"contribution"`
	TenureSeconds int64  `json:"tenure_seconds"`
}

// StandingLedger owns guilds, members, contribution buckets and ranks.
type StandingLedger struct {
	opts Options
}

// NewStandingLedger builds a ledger with the given policy.
func NewStandingLedger(opts Options) *StandingLedger {
	return &StandingLedger{opts: opts}
}

// AddMember inserts one member, the first member of a guild becomes master.
func (l *StandingLedger) AddMember(guildID, member string, joinedAt int64) error {
	return errNotImplemented
}

// Leave removes one member from a guild.
func (l *StandingLedger) Leave(guildID, member string) error {
	return errNotImplemented
}

// Contribute records one weighted contribution event.
func (l *StandingLedger) Contribute(guildID, member, kind, eventID string, amount int64, at int64) error {
	return errNotImplemented
}

// Promote moves one member up at most one rank.
func (l *StandingLedger) Promote(guildID, member string, at int64) (RankChange, error) {
	return RankChange{}, errNotImplemented
}

// WeeklyDecay closes one week, decays inactive members and demotes them.
func (l *StandingLedger) WeeklyDecay(guildID string, week int64) ([]RankChange, error) {
	return nil, errNotImplemented
}

// Standing reads one member view.
func (l *StandingLedger) Standing(guildID, member string) Standing {
	return Standing{}
}

// Members lists one guild ordered by member id.
func (l *StandingLedger) Members(guildID string) []Standing {
	return nil
}

// Guild reads one guild header.
func (l *StandingLedger) Guild(guildID string) (Guild, bool) {
	return Guild{}, false
}

// Ranks returns a copy of the rank ladder, lowest first.
func (l *StandingLedger) Ranks() []RankRule {
	return nil
}

// Snapshot serialises guilds, contribution buckets, activity and ranks.
func (l *StandingLedger) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *StandingLedger) Restore(data []byte) error {
	return errNotImplemented
}
