// Package task keeps guild contribution ledgers: weighted contribution,
// two threshold promotion and weekly activity decay with demotion.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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
	mu sync.RWMutex

	opts      Options
	ranks     []RankRule
	rankIndex map[string]int
	badRanks  bool
	guilds    map[string]*guildState
}

// NewStandingLedger builds a ledger with the given policy.
func NewStandingLedger(opts Options) *StandingLedger {
	opts = normaliseOptions(opts)
	l := &StandingLedger{
		opts:      opts,
		rankIndex: make(map[string]int),
		guilds:    make(map[string]*guildState),
	}
	if err := validateRanks(opts.Ranks); err != nil {
		l.badRanks = true
		return l
	}
	l.ranks = append([]RankRule(nil), opts.Ranks...)
	for i, rank := range l.ranks {
		l.rankIndex[rank.Name] = i
	}
	return l
}

// AddMember inserts one member, the first member of a guild becomes master.
func (l *StandingLedger) AddMember(guildID, member string, joinedAt int64) error {
	if strings.TrimSpace(guildID) == "" {
		return ErrBadGuild
	}
	if strings.TrimSpace(member) == "" {
		return ErrBadMember
	}
	if joinedAt <= 0 {
		return ErrBadTime
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.badRanks {
		return ErrBadRankRule
	}
	if l.guilds == nil {
		l.guilds = make(map[string]*guildState)
	}

	g, ok := l.guilds[guildID]
	if !ok {
		g = &guildState{
			id:      guildID,
			members: make(map[string]*memberState),
			events:  make(map[string]string),
		}
		l.guilds[guildID] = g
	}
	if _, exists := g.members[member]; exists {
		return ErrMemberExists
	}
	if len(g.members) >= l.opts.MaxMembers {
		return ErrGuildFull
	}

	rankIdx := 0
	if len(g.members) == 0 {
		rankIdx = len(l.ranks) - 1
		g.master = member
	}
	g.members[member] = &memberState{
		id:       member,
		joinedAt: joinedAt,
		rankIdx:  rankIdx,
		weeks:    make(map[int64]int64),
	}
	return nil
}

// Leave removes one member from a guild.
func (l *StandingLedger) Leave(guildID, member string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	g, ok := l.guilds[guildID]
	if !ok {
		return ErrUnknownGuild
	}
	if _, ok := g.members[member]; !ok {
		return ErrMemberNotFound
	}

	delete(g.members, member)
	for eventID, owner := range g.events {
		if owner == member {
			delete(g.events, eventID)
		}
	}
	if len(g.members) == 0 {
		g.master = ""
	} else if g.master == member {
		g.master = earliestMember(g.members)
	}
	return nil
}

// Contribute records one weighted contribution event.
func (l *StandingLedger) Contribute(guildID, member, kind, eventID string, amount int64, at int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.badRanks {
		return ErrBadRankRule
	}
	g, ok := l.guilds[guildID]
	if !ok {
		return ErrUnknownGuild
	}
	m, ok := g.members[member]
	if !ok {
		return ErrMemberNotFound
	}
	if strings.TrimSpace(eventID) == "" {
		return ErrBadEvent
	}
	if _, exists := g.events[eventID]; exists {
		return nil
	}
	weight, ok := l.opts.KindWeights[kind]
	if !ok {
		return ErrBadKind
	}
	if amount <= 0 {
		return ErrBadAmount
	}
	if at <= 0 {
		return ErrBadTime
	}

	weighted := floorDiv(amount*int64(weight), 100)
	week := at / l.opts.WeekSeconds
	m.weeks[week] += weighted
	g.events[eventID] = member
	return nil
}

// Promote moves one member up at most one rank.
func (l *StandingLedger) Promote(guildID, member string, at int64) (RankChange, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.badRanks {
		return RankChange{}, ErrBadRankRule
	}
	g, ok := l.guilds[guildID]
	if !ok {
		return RankChange{}, ErrUnknownGuild
	}
	m, ok := g.members[member]
	if !ok {
		return RankChange{}, ErrMemberNotFound
	}
	if at <= 0 {
		return RankChange{}, ErrBadTime
	}

	contribution := memberContribution(m)
	tenure := at - m.joinedAt
	change := RankChange{
		GuildID:       guildID,
		Member:        member,
		From:          l.ranks[m.rankIdx].Name,
		To:            l.ranks[m.rankIdx].Name,
		Contribution:  contribution,
		TenureSeconds: tenure,
	}
	if m.rankIdx >= len(l.ranks)-1 {
		change.Reason = ReasonMaxRank
		return change, nil
	}

	next := l.ranks[m.rankIdx+1]
	if contribution < next.MinContribution || tenure < next.MinTenureSeconds {
		change.Reason = ReasonNotEligible
		return change, nil
	}

	m.rankIdx++
	change.To = l.ranks[m.rankIdx].Name
	change.Changed = true
	change.Reason = ReasonPromoted
	return change, nil
}

// WeeklyDecay closes one week, decays inactive members and demotes them.
func (l *StandingLedger) WeeklyDecay(guildID string, week int64) ([]RankChange, error) {
	if week <= 0 {
		return nil, ErrBadWeek
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.badRanks {
		return nil, ErrBadRankRule
	}
	g, ok := l.guilds[guildID]
	if !ok {
		return nil, ErrUnknownGuild
	}
	if week <= g.lastDecayWeek {
		return nil, nil
	}

	weekStart := week * l.opts.WeekSeconds
	weekEnd := weekStart + l.opts.WeekSeconds
	weekLength := l.opts.WeekSeconds
	decay := int64(l.opts.DecayPermille)
	ids := sortedMemberIDs(g)
	changes := make([]RankChange, 0)

	for _, id := range ids {
		m := g.members[id]
		if m.rankIdx >= len(l.ranks)-1 {
			continue
		}
		if m.joinedAt >= weekEnd {
			continue
		}

		activeSeconds := weekEnd - maxInt64(m.joinedAt, weekStart)
		if activeSeconds < 0 {
			activeSeconds = 0
		}
		if activeSeconds > weekLength {
			activeSeconds = weekLength
		}
		activityFloor := l.opts.ActivityFloor * activeSeconds / weekLength

		if m.weeks[week] < activityFloor {
			for bucket, amount := range m.weeks {
				if bucket < week {
					m.weeks[bucket] = amount * decay / 1000
				}
			}
		}

		contribution := memberContribution(m)
		if m.rankIdx == 0 || contribution >= l.ranks[m.rankIdx].MinContribution {
			continue
		}

		from := l.ranks[m.rankIdx].Name
		m.rankIdx--
		changes = append(changes, RankChange{
			GuildID:       guildID,
			Member:        id,
			From:          from,
			To:            l.ranks[m.rankIdx].Name,
			Changed:       true,
			Reason:        ReasonDemoted,
			Contribution:  contribution,
			TenureSeconds: weekStart - m.joinedAt,
		})
	}
	g.lastDecayWeek = week
	return changes, nil
}

// Standing reads one member view.
func (l *StandingLedger) Standing(guildID, member string) Standing {
	l.mu.RLock()
	defer l.mu.RUnlock()

	g, ok := l.guilds[guildID]
	if !ok {
		return Standing{}
	}
	m, ok := g.members[member]
	if !ok {
		return Standing{}
	}
	return Standing{
		GuildID:      guildID,
		Member:       member,
		Rank:         l.ranks[m.rankIdx].Name,
		Contribution: memberContribution(m),
		JoinedAt:     m.joinedAt,
	}
}

// Members lists one guild ordered by member id.
func (l *StandingLedger) Members(guildID string) []Standing {
	l.mu.RLock()
	defer l.mu.RUnlock()

	g, ok := l.guilds[guildID]
	if !ok {
		return nil
	}
	ids := sortedMemberIDs(g)
	members := make([]Standing, 0, len(ids))
	for _, id := range ids {
		m := g.members[id]
		members = append(members, Standing{
			GuildID:      guildID,
			Member:       id,
			Rank:         l.ranks[m.rankIdx].Name,
			Contribution: memberContribution(m),
			JoinedAt:     m.joinedAt,
		})
	}
	return members
}

// Guild reads one guild header.
func (l *StandingLedger) Guild(guildID string) (Guild, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	g, ok := l.guilds[guildID]
	if !ok {
		return Guild{}, false
	}
	return Guild{ID: g.id, Master: g.master, Members: len(g.members)}, true
}

// Ranks returns a copy of the rank ladder, lowest first.
func (l *StandingLedger) Ranks() []RankRule {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.badRanks {
		return nil
	}
	ranks := make([]RankRule, len(l.ranks))
	copy(ranks, l.ranks)
	return ranks
}

// Snapshot serialises guilds, contribution buckets, activity and ranks.
func (l *StandingLedger) Snapshot() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.badRanks {
		return nil, ErrBadRankRule
	}

	snap := snapshot{
		Version:       snapshotVersion,
		Ranks:         append([]RankRule(nil), l.ranks...),
		KindWeights:   cloneWeights(l.opts.KindWeights),
		WeekSeconds:   l.opts.WeekSeconds,
		ActivityFloor: l.opts.ActivityFloor,
		DecayPermille: l.opts.DecayPermille,
		MaxMembers:    l.opts.MaxMembers,
		Guilds:        make([]snapshotGuild, 0, len(l.guilds)),
	}

	guildIDs := make([]string, 0, len(l.guilds))
	for id := range l.guilds {
		guildIDs = append(guildIDs, id)
	}
	sort.Strings(guildIDs)
	for _, id := range guildIDs {
		g := l.guilds[id]
		sg := snapshotGuild{
			ID:            g.id,
			Master:        g.master,
			LastDecayWeek: g.lastDecayWeek,
			Members:       make([]snapshotMember, 0, len(g.members)),
			Events:        make([]snapshotEvent, 0, len(g.events)),
		}
		for _, memberID := range sortedMemberIDs(g) {
			m := g.members[memberID]
			sm := snapshotMember{
				Member:   m.id,
				JoinedAt: m.joinedAt,
				Rank:     m.rankIdx,
				Weeks:    make([]snapshotWeek, 0, len(m.weeks)),
			}
			weekIDs := make([]int64, 0, len(m.weeks))
			for week := range m.weeks {
				weekIDs = append(weekIDs, week)
			}
			sort.Slice(weekIDs, func(i, j int) bool { return weekIDs[i] < weekIDs[j] })
			for _, week := range weekIDs {
				sm.Weeks = append(sm.Weeks, snapshotWeek{Week: week, Contribution: m.weeks[week]})
			}
			sg.Members = append(sg.Members, sm)
		}
		eventIDs := make([]string, 0, len(g.events))
		for eventID := range g.events {
			eventIDs = append(eventIDs, eventID)
		}
		sort.Strings(eventIDs)
		for _, eventID := range eventIDs {
			sg.Events = append(sg.Events, snapshotEvent{Event: eventID, Member: g.events[eventID]})
		}
		snap.Guilds = append(snap.Guilds, sg)
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *StandingLedger) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	opts := normaliseOptions(Options{
		Ranks:         append([]RankRule(nil), snap.Ranks...),
		KindWeights:   cloneWeights(snap.KindWeights),
		WeekSeconds:   snap.WeekSeconds,
		ActivityFloor: snap.ActivityFloor,
		DecayPermille: snap.DecayPermille,
		MaxMembers:    snap.MaxMembers,
	})
	if err := validateRanks(opts.Ranks); err != nil {
		return ErrBadSnapshot
	}
	ranks := append([]RankRule(nil), opts.Ranks...)
	rankIndex := make(map[string]int, len(ranks))
	for i, rank := range ranks {
		rankIndex[rank.Name] = i
	}

	guilds := make(map[string]*guildState, len(snap.Guilds))
	for _, sg := range snap.Guilds {
		if strings.TrimSpace(sg.ID) == "" || sg.LastDecayWeek < 0 {
			return ErrBadSnapshot
		}
		if _, exists := guilds[sg.ID]; exists {
			return ErrBadSnapshot
		}
		g := &guildState{
			id:            sg.ID,
			master:        sg.Master,
			events:        make(map[string]string, len(sg.Events)),
			members:       make(map[string]*memberState, len(sg.Members)),
			lastDecayWeek: sg.LastDecayWeek,
		}
		if len(sg.Members) > opts.MaxMembers {
			return ErrBadSnapshot
		}
		for _, sm := range sg.Members {
			if strings.TrimSpace(sm.Member) == "" || sm.JoinedAt <= 0 {
				return ErrBadSnapshot
			}
			if sm.Rank < 0 || sm.Rank >= len(ranks) {
				return ErrBadSnapshot
			}
			if _, exists := g.members[sm.Member]; exists {
				return ErrBadSnapshot
			}
			weeks := make(map[int64]int64, len(sm.Weeks))
			for _, sw := range sm.Weeks {
				if sw.Week < 0 || sw.Contribution < 0 {
					return ErrBadSnapshot
				}
				if _, exists := weeks[sw.Week]; exists {
					return ErrBadSnapshot
				}
				weeks[sw.Week] = sw.Contribution
			}
			g.members[sm.Member] = &memberState{
				id:       sm.Member,
				joinedAt: sm.JoinedAt,
				rankIdx:  sm.Rank,
				weeks:    weeks,
			}
		}
		if len(g.members) == 0 {
			if sg.Master != "" {
				return ErrBadSnapshot
			}
		} else if sg.Master == "" || g.members[sg.Master] == nil {
			return ErrBadSnapshot
		}
		for _, se := range sg.Events {
			if strings.TrimSpace(se.Event) == "" {
				return ErrBadSnapshot
			}
			if _, exists := g.events[se.Event]; exists {
				return ErrBadSnapshot
			}
			if g.members[se.Member] == nil {
				return ErrBadSnapshot
			}
			g.events[se.Event] = se.Member
		}
		guilds[g.id] = g
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.opts = opts
	l.ranks = ranks
	l.rankIndex = rankIndex
	l.badRanks = false
	l.guilds = guilds
	return nil
}

type guildState struct {
	id            string
	master        string
	members       map[string]*memberState
	events        map[string]string
	lastDecayWeek int64
}

type memberState struct {
	id       string
	joinedAt int64
	rankIdx  int
	weeks    map[int64]int64
}

const snapshotVersion = 1

type snapshot struct {
	Version       int             `json:"version"`
	Ranks         []RankRule      `json:"ranks"`
	KindWeights   map[string]int  `json:"kind_weights"`
	WeekSeconds   int64           `json:"week_seconds"`
	ActivityFloor int64           `json:"activity_floor"`
	DecayPermille int             `json:"decay_permille"`
	MaxMembers    int             `json:"max_members"`
	Guilds        []snapshotGuild `json:"guilds"`
}

type snapshotGuild struct {
	ID            string           `json:"id"`
	Master        string           `json:"master"`
	LastDecayWeek int64            `json:"last_decay_week"`
	Members       []snapshotMember `json:"members"`
	Events        []snapshotEvent  `json:"events"`
}

type snapshotMember struct {
	Member   string         `json:"member"`
	JoinedAt int64          `json:"joined_at"`
	Rank     int            `json:"rank"`
	Weeks    []snapshotWeek `json:"weeks"`
}

type snapshotWeek struct {
	Week         int64 `json:"week"`
	Contribution int64 `json:"contribution"`
}

type snapshotEvent struct {
	Event  string `json:"event"`
	Member string `json:"member"`
}

func normaliseOptions(opts Options) Options {
	if opts.WeekSeconds <= 0 {
		opts.WeekSeconds = DefaultWeekSeconds
	}
	if opts.ActivityFloor <= 0 {
		opts.ActivityFloor = DefaultActivityFloor
	}
	if opts.DecayPermille <= 0 {
		opts.DecayPermille = DefaultDecayPermille
	}
	if opts.MaxMembers <= 0 {
		opts.MaxMembers = DefaultMaxMembers
	}
	opts.Ranks = append([]RankRule(nil), opts.Ranks...)
	opts.KindWeights = cloneWeights(opts.KindWeights)
	return opts
}

func validateRanks(ranks []RankRule) error {
	if len(ranks) < 2 {
		return ErrBadRankRule
	}
	names := make(map[string]struct{}, len(ranks))
	for i, rank := range ranks {
		name := strings.TrimSpace(rank.Name)
		if name == "" {
			return ErrBadRankRule
		}
		if _, exists := names[name]; exists {
			return ErrBadRankRule
		}
		names[name] = struct{}{}
		if rank.MinContribution < 0 || rank.MinTenureSeconds < 0 {
			return ErrBadRankRule
		}
		if i == 0 {
			if rank.MinContribution != 0 {
				return ErrBadRankRule
			}
			continue
		}
		previous := ranks[i-1]
		if rank.MinContribution <= previous.MinContribution ||
			rank.MinTenureSeconds <= previous.MinTenureSeconds {
			return ErrBadRankRule
		}
	}
	return nil
}

func cloneWeights(weights map[string]int) map[string]int {
	if weights == nil {
		return nil
	}
	cloned := make(map[string]int, len(weights))
	for kind, weight := range weights {
		cloned[kind] = weight
	}
	return cloned
}

func memberContribution(m *memberState) int64 {
	var total int64
	for _, amount := range m.weeks {
		total += amount
	}
	return total
}

func sortedMemberIDs(g *guildState) []string {
	ids := make([]string, 0, len(g.members))
	for id := range g.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func earliestMember(members map[string]*memberState) string {
	var best *memberState
	for _, m := range members {
		if best == nil || m.joinedAt < best.joinedAt ||
			(m.joinedAt == best.joinedAt && m.id < best.id) {
			best = m
		}
	}
	if best == nil {
		return ""
	}
	return best.id
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
