// Package task reserves bag slots for reward delivery, commits or releases the
// reservations and buffers overflowing rewards in a time limited spill area.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// SlotReserver owns the per owner slot ledger and the spill buffer.
type SlotReserver struct {
	options Options
}

// NewSlotReserver builds a reserver with the given caps.
func NewSlotReserver(options Options) *SlotReserver {
	return &SlotReserver{options: options}
}

// Reserve claims slots for one delivery attempt.
func (s *SlotReserver) Reserve(owner, token string, slots int, ttl int64) (Reservation, error) {
	return Reservation{}, errNotImplemented
}

// Commit turns a pending reservation into used slots.
func (s *SlotReserver) Commit(owner, token string) error {
	return errNotImplemented
}

// Release returns the slots of a pending reservation.
func (s *SlotReserver) Release(owner, token string) error {
	return errNotImplemented
}

// Available reports the free slots of one owner.
func (s *SlotReserver) Available(owner string) int {
	return 0
}

// Used reports the committed slots of one owner.
func (s *SlotReserver) Used(owner string) int {
	return 0
}

// Tick reclaims the reservations that expired at the given moment.
func (s *SlotReserver) Tick(owner string, at int64) int {
	return 0
}

// Reservation reads one reservation record.
func (s *SlotReserver) Reservation(owner, token string) (Reservation, bool) {
	return Reservation{}, false
}

// Spill parks one reward in the overflow buffer.
func (s *SlotReserver) Spill(owner string, reward Reward, at int64) (string, error) {
	return "", errNotImplemented
}

// ClaimSpill moves one buffered reward into the bag when slots allow it.
func (s *SlotReserver) ClaimSpill(owner, spillID string, at int64) (Reward, error) {
	return Reward{}, errNotImplemented
}

// SpillEntries lists the unclaimed spill entries of one owner.
func (s *SlotReserver) SpillEntries(owner string) []Spill {
	return nil
}

// Snapshot serialises the reservations, the spill buffer and the counters.
func (s *SlotReserver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *SlotReserver) Restore(data []byte) error {
	return errNotImplemented
}
