// Package task reserves bag slots for reward delivery, commits or releases the
// reservations and buffers overflowing rewards in a time limited spill area.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Reservation states.
const (
	ReservationPending   = "pending"
	ReservationCommitted = "committed"
	ReservationReleased  = "released"
	ReservationExpired   = "expired"
)

// Errors returned by the slot reserver.
var (
	ErrBlankArg         = errors.New("argument is required")
	ErrBadSlots         = errors.New("slot count must be positive")
	ErrBadTTL           = errors.New("time to live must be positive")
	ErrTokenConflict    = errors.New("token already used with different terms")
	ErrUnknownToken     = errors.New("reservation token is not pending")
	ErrCapacityExceeded = errors.New("slot capacity would be exceeded")
	ErrReservationLimit = errors.New("pending reservation limit reached")
	ErrBadReward        = errors.New("reward is invalid")
	ErrRewardDuplicated = errors.New("reward already sits in the spill buffer")
	ErrSpillUnknown     = errors.New("spill entry not found")
	ErrSpillExpired     = errors.New("spill entry has expired")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Options bounds the slot table.
type Options struct {
	Capacity        int
	SpillTTL        int64
	MaxReservations int
}

// Reward is one delivery unit that needs bag slots.
type Reward struct {
	RewardID string   `json:"reward_id"`
	Slots    int      `json:"slots"`
	Items    []string `json:"items"`
	Amount   int64    `json:"amount"`
}

// Reservation is one slot claim kept until commit, release or expiry.
type Reservation struct {
	Owner    string `json:"owner"`
	Token    string `json:"token"`
	Slots    int    `json:"slots"`
	ExpireAt int64  `json:"expire_at"`
	State    string `json:"state"`
	Seq      int64  `json:"seq"`
}

// Spill is one reward parked outside the bag until capacity frees up.
type Spill struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Reward   Reward `json:"reward"`
	ExpireAt int64  `json:"expire_at"`
	Seq      int64  `json:"seq"`
}

type ownerState struct {
	reservations      map[string]Reservation
	spills            map[string]Spill
	spilledRewardIDs  map[string]struct{}
	used              int
	nextReservationID int64
	nextSpillID       int64
}

func newOwnerState() *ownerState {
	return &ownerState{
		reservations:      make(map[string]Reservation),
		spills:            make(map[string]Spill),
		spilledRewardIDs:  make(map[string]struct{}),
		nextReservationID: 1,
		nextSpillID:       1,
	}
}

// SlotReserver owns the per owner slot ledger and the spill buffer.
type SlotReserver struct {
	mu      sync.Mutex
	options Options
	owners  map[string]*ownerState
}

// NewSlotReserver builds a reserver with the given caps.
func NewSlotReserver(options Options) *SlotReserver {
	return &SlotReserver{
		options: options,
		owners:  make(map[string]*ownerState),
	}
}

// Reserve claims slots for one delivery attempt.
func (s *SlotReserver) Reserve(owner, token string, slots int, ttl int64) (Reservation, error) {
	if isBlank(owner) || isBlank(token) {
		return Reservation{}, ErrBlankArg
	}
	if slots <= 0 {
		return Reservation{}, ErrBadSlots
	}
	if ttl <= 0 {
		return Reservation{}, ErrBadTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state != nil {
		if existing, ok := state.reservations[token]; ok {
			if existing.Slots != slots || existing.ExpireAt != ttl {
				return Reservation{}, ErrTokenConflict
			}
			return existing, nil
		}
	}

	if s.options.MaxReservations > 0 && state.pendingCount() >= s.options.MaxReservations {
		return Reservation{}, ErrReservationLimit
	}
	if int64(slots) > s.availableLocked(state) {
		return Reservation{}, ErrCapacityExceeded
	}

	if state == nil {
		state = newOwnerState()
		s.owners[owner] = state
	}
	seq := state.nextReservationID
	if seq < 1 {
		seq = 1
	}
	reservation := Reservation{
		Owner:    owner,
		Token:    token,
		Slots:    slots,
		ExpireAt: ttl,
		State:    ReservationPending,
		Seq:      seq,
	}
	state.reservations[token] = reservation
	state.nextReservationID = nextSequence(seq)
	return reservation, nil
}

// Commit turns a pending reservation into used slots.
func (s *SlotReserver) Commit(owner, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil {
		return ErrUnknownToken
	}
	reservation, ok := state.reservations[token]
	if !ok || reservation.State != ReservationPending {
		return ErrUnknownToken
	}
	reservation.State = ReservationCommitted
	state.reservations[token] = reservation
	state.used += reservation.Slots
	return nil
}

// Release returns the slots of a pending reservation.
func (s *SlotReserver) Release(owner, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil {
		return ErrUnknownToken
	}
	reservation, ok := state.reservations[token]
	if !ok || reservation.State != ReservationPending {
		return ErrUnknownToken
	}
	reservation.State = ReservationReleased
	state.reservations[token] = reservation
	return nil
}

// Available reports the free slots of one owner.
func (s *SlotReserver) Available(owner string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.availableLocked(s.owners[owner])
	if available < 0 {
		return 0
	}
	if available > int64(math.MaxInt) {
		return math.MaxInt
	}
	return int(available)
}

// Used reports the committed slots of one owner.
func (s *SlotReserver) Used(owner string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil || state.used <= 0 {
		return 0
	}
	return state.used
}

// Tick reclaims the reservations that expired at the given moment.
func (s *SlotReserver) Tick(owner string, at int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil {
		return 0
	}
	reclaimed := 0
	for token, reservation := range state.reservations {
		if reservation.State == ReservationPending && reservation.ExpireAt <= at {
			reservation.State = ReservationExpired
			state.reservations[token] = reservation
			reclaimed++
		}
	}
	return reclaimed
}

// Reservation reads one reservation record.
func (s *SlotReserver) Reservation(owner, token string) (Reservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil {
		return Reservation{}, false
	}
	reservation, ok := state.reservations[token]
	return reservation, ok
}

// Spill parks one reward in the overflow buffer.
func (s *SlotReserver) Spill(owner string, reward Reward, at int64) (string, error) {
	if isBlank(owner) {
		return "", ErrBlankArg
	}
	if isBlank(reward.RewardID) || reward.Slots <= 0 {
		return "", ErrBadReward
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil {
		state = newOwnerState()
		s.owners[owner] = state
	}
	if _, exists := state.spilledRewardIDs[reward.RewardID]; exists {
		return "", ErrRewardDuplicated
	}

	seq := state.nextSpillID
	if seq < 1 {
		seq = 1
	}
	for {
		id := spillID(owner, seq)
		if _, exists := state.spills[id]; !exists {
			break
		}
		seq = nextSequence(seq)
	}

	spill := Spill{
		ID:       spillID(owner, seq),
		Owner:    owner,
		Reward:   cloneReward(reward),
		ExpireAt: addSaturating(at, s.options.SpillTTL),
		Seq:      seq,
	}
	state.spills[spill.ID] = spill
	state.spilledRewardIDs[reward.RewardID] = struct{}{}
	state.nextSpillID = nextSequence(seq)
	return spill.ID, nil
}

// ClaimSpill moves one buffered reward into the bag when slots allow it.
func (s *SlotReserver) ClaimSpill(owner, spillID string, at int64) (Reward, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.owners[owner]
	if state == nil {
		return Reward{}, ErrSpillUnknown
	}
	spill, ok := state.spills[spillID]
	if !ok {
		return Reward{}, ErrSpillUnknown
	}
	if at >= spill.ExpireAt {
		return Reward{}, ErrSpillExpired
	}
	if int64(spill.Reward.Slots) > s.availableLocked(state) {
		return Reward{}, ErrCapacityExceeded
	}

	state.used += spill.Reward.Slots
	delete(state.spills, spillID)
	return cloneReward(spill.Reward), nil
}

// SpillEntries lists the unclaimed spill entries of one owner.
func (s *SlotReserver) SpillEntries(owner string) []Spill {
	s.mu.Lock()
	state := s.owners[owner]
	if state == nil {
		s.mu.Unlock()
		return []Spill{}
	}
	entries := make([]Spill, 0, len(state.spills))
	for _, spill := range state.spills {
		spill.Reward = cloneReward(spill.Reward)
		entries = append(entries, spill)
	}
	s.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Seq == entries[j].Seq {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Seq < entries[j].Seq
	})
	return entries
}

const snapshotVersion = 1

type snapshotOwner struct {
	Used               int           `json:"used"`
	NextReservationSeq int64         `json:"next_reservation_seq"`
	NextSpillSeq       int64         `json:"next_spill_seq"`
	Reservations       []Reservation `json:"reservations"`
	Spills             []Spill       `json:"spills"`
	SpilledRewardIDs   []string      `json:"spilled_reward_ids"`
}

type snapshotState struct {
	Version int                      `json:"version"`
	Owners  map[string]snapshotOwner `json:"owners"`
}

// Snapshot serialises the reservations, the spill buffer and the counters.
func (s *SlotReserver) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := snapshotState{
		Version: snapshotVersion,
		Owners:  make(map[string]snapshotOwner, len(s.owners)),
	}
	for owner, state := range s.owners {
		entry := snapshotOwner{
			Used:               state.used,
			NextReservationSeq: state.nextReservationID,
			NextSpillSeq:       state.nextSpillID,
			Reservations:       make([]Reservation, 0, len(state.reservations)),
			Spills:             make([]Spill, 0, len(state.spills)),
			SpilledRewardIDs:   make([]string, 0, len(state.spilledRewardIDs)),
		}
		for _, reservation := range state.reservations {
			entry.Reservations = append(entry.Reservations, reservation)
		}
		for _, spill := range state.spills {
			spill.Reward = cloneReward(spill.Reward)
			entry.Spills = append(entry.Spills, spill)
		}
		for rewardID := range state.spilledRewardIDs {
			entry.SpilledRewardIDs = append(entry.SpilledRewardIDs, rewardID)
		}
		sort.Slice(entry.Reservations, func(i, j int) bool {
			if entry.Reservations[i].Seq == entry.Reservations[j].Seq {
				return entry.Reservations[i].Token < entry.Reservations[j].Token
			}
			return entry.Reservations[i].Seq < entry.Reservations[j].Seq
		})
		sort.Slice(entry.Spills, func(i, j int) bool {
			if entry.Spills[i].Seq == entry.Spills[j].Seq {
				return entry.Spills[i].ID < entry.Spills[j].ID
			}
			return entry.Spills[i].Seq < entry.Spills[j].Seq
		})
		sort.Strings(entry.SpilledRewardIDs)
		snapshot.Owners[owner] = entry
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *SlotReserver) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var snapshot snapshotState
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if snapshot.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	owners, err := s.restoreOwners(snapshot)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.owners = owners
	s.mu.Unlock()
	return nil
}

func (s *SlotReserver) restoreOwners(snapshot snapshotState) (map[string]*ownerState, error) {
	owners := make(map[string]*ownerState, len(snapshot.Owners))
	for owner, entry := range snapshot.Owners {
		if isBlank(owner) {
			return nil, badSnapshot("blank owner")
		}
		if entry.Used < 0 {
			return nil, badSnapshot("negative used slots")
		}
		if entry.NextReservationSeq < 1 || entry.NextSpillSeq < 1 {
			return nil, badSnapshot("invalid sequence counter")
		}

		state := newOwnerState()
		state.used = entry.Used

		var pendingSlots int64
		var committedSlots int64
		var maxReservationSeq int64
		seenTokens := make(map[string]struct{}, len(entry.Reservations))
		seenReservationSeqs := make(map[int64]struct{}, len(entry.Reservations))
		for _, reservation := range entry.Reservations {
			if reservation.Owner != owner {
				return nil, badSnapshot("reservation owner mismatch")
			}
			if isBlank(reservation.Token) {
				return nil, badSnapshot("blank reservation token")
			}
			if reservation.Slots <= 0 {
				return nil, badSnapshot("non-positive reservation slots")
			}
			switch reservation.State {
			case ReservationPending, ReservationCommitted, ReservationReleased, ReservationExpired:
			default:
				return nil, badSnapshot("unknown reservation state")
			}
			if reservation.Seq <= 0 {
				return nil, badSnapshot("invalid reservation sequence")
			}
			if _, exists := seenTokens[reservation.Token]; exists {
				return nil, badSnapshot("duplicate reservation token")
			}
			if _, exists := seenReservationSeqs[reservation.Seq]; exists {
				return nil, badSnapshot("duplicate reservation sequence")
			}
			seenTokens[reservation.Token] = struct{}{}
			seenReservationSeqs[reservation.Seq] = struct{}{}
			if reservation.Seq > maxReservationSeq {
				maxReservationSeq = reservation.Seq
			}
			switch reservation.State {
			case ReservationPending:
				pendingSlots += int64(reservation.Slots)
			case ReservationCommitted:
				committedSlots += int64(reservation.Slots)
			}
			state.reservations[reservation.Token] = reservation
		}
		if entry.NextReservationSeq <= maxReservationSeq {
			return nil, badSnapshot("reservation sequence would roll back")
		}
		if committedSlots > int64(entry.Used) {
			return nil, badSnapshot("committed slots exceed used ledger")
		}
		if int64(entry.Used)+pendingSlots > int64(s.options.Capacity) {
			return nil, badSnapshot("slot ledger exceeds capacity")
		}

		seenSpillIDs := make(map[string]struct{}, len(entry.Spills))
		seenSpillSeqs := make(map[int64]struct{}, len(entry.Spills))
		var maxSpillSeq int64
		for _, spill := range entry.Spills {
			if spill.Owner != owner {
				return nil, badSnapshot("spill owner mismatch")
			}
			if isBlank(spill.ID) || isBlank(spill.Reward.RewardID) || spill.Reward.Slots <= 0 {
				return nil, badSnapshot("invalid spill entry")
			}
			if spill.Seq <= 0 {
				return nil, badSnapshot("invalid spill sequence")
			}
			if _, exists := seenSpillIDs[spill.ID]; exists {
				return nil, badSnapshot("duplicate spill id")
			}
			if _, exists := seenSpillSeqs[spill.Seq]; exists {
				return nil, badSnapshot("duplicate spill sequence")
			}
			seenSpillIDs[spill.ID] = struct{}{}
			seenSpillSeqs[spill.Seq] = struct{}{}
			if spill.Seq > maxSpillSeq {
				maxSpillSeq = spill.Seq
			}
			state.spills[spill.ID] = spill
		}
		if entry.NextSpillSeq <= maxSpillSeq {
			return nil, badSnapshot("spill sequence would roll back")
		}

		for _, rewardID := range entry.SpilledRewardIDs {
			if isBlank(rewardID) {
				return nil, badSnapshot("blank spilled reward id")
			}
			if _, exists := state.spilledRewardIDs[rewardID]; exists {
				return nil, badSnapshot("duplicate spilled reward id")
			}
			state.spilledRewardIDs[rewardID] = struct{}{}
		}
		for _, spill := range state.spills {
			if _, exists := state.spilledRewardIDs[spill.Reward.RewardID]; !exists {
				return nil, badSnapshot("spill reward is missing from its ledger")
			}
		}

		state.nextReservationID = entry.NextReservationSeq
		state.nextSpillID = entry.NextSpillSeq
		owners[owner] = state
	}
	return owners, nil
}

func (s *SlotReserver) availableLocked(state *ownerState) int64 {
	available := int64(s.options.Capacity)
	if state == nil {
		if available < 0 {
			return 0
		}
		return available
	}
	available -= int64(state.used)
	for _, reservation := range state.reservations {
		if reservation.State == ReservationPending {
			available -= int64(reservation.Slots)
		}
	}
	if available < 0 {
		return 0
	}
	return available
}

func (state *ownerState) pendingCount() int {
	if state == nil {
		return 0
	}
	pending := 0
	for _, reservation := range state.reservations {
		if reservation.State == ReservationPending {
			pending++
		}
	}
	return pending
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func spillID(owner string, seq int64) string {
	return "spill-" + owner + "-" + strconv.FormatInt(seq, 10)
}

func nextSequence(seq int64) int64 {
	if seq == math.MaxInt64 {
		return math.MaxInt64
	}
	return seq + 1
}

func addSaturating(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func cloneReward(reward Reward) Reward {
	cloned := reward
	if reward.Items != nil {
		cloned.Items = append([]string(nil), reward.Items...)
	}
	return cloned
}

func badSnapshot(_ string) error {
	return ErrBadSnapshot
}
