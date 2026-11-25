// Package task keeps a fuzz corpus under a byte quota: entries are admitted
// only when they extend coverage, repeated content is deduplicated, and
// eviction is driven by contribution.
package task

import "errors"

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Verdict reasons reported for an Add call.
const (
	ReasonAccepted      = "accepted"
	ReasonDuplicate     = "duplicate_content"
	ReasonNoNewCoverage = "no_new_coverage"
	ReasonNoCoverage    = "no_coverage"
	ReasonQuotaFull     = "quota_full"
)

// Errors returned by the vault.
var (
	ErrBlankOrigin = errors.New("origin is required")
	ErrNilReducer  = errors.New("cover reducer is required")
	ErrNotFound    = errors.New("corpus entry not found")
	ErrNotCrash    = errors.New("entry is not a crash case")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Reducer derives the coverage of a corpus input. The vault calls it while
// minimising an entry.
type Reducer func(input []byte) CoverSet

// CoverSet is the coverage bitmap of one input: an opaque bitmap hash plus
// the sorted edge identifiers it reached.
type CoverSet struct {
	Hash  string
	Edges []uint64
}

// Limits bounds the corpus.
type Limits struct {
	MaxBytes   int
	MaxEntries int
}

// Entry is one stored corpus input.
type Entry struct {
	ID        string
	Hash      string
	CoverHash string
	Bytes     int
	NewEdges  int
	Origins   []string
	Crash     bool
	Confirmed bool
	Pinned    bool
	Seq       int64
}

// Verdict reports what happened to one Add call.
type Verdict struct {
	ID        string
	Accepted  bool
	Duplicate bool
	NewEdges  int
	Reason    string
	Bytes     int
	Evicted   []string
}

// VaultStats is the running counter block of the vault.
type VaultStats struct {
	Entries    int
	Bytes      int
	Invalid    int
	Duplicates int
	Redundant  int
	Evicted    int
	Crash      int
}

// CorpusVault owns the corpus entries, the coverage watermark and counters.
type CorpusVault struct {
	limits Limits
	reduce Reducer
}

// NewCorpusVault builds an empty vault with the given caps and reducer.
func NewCorpusVault(limits Limits, reduce Reducer) *CorpusVault {
	return &CorpusVault{limits: limits, reduce: reduce}
}

// Add admits one corpus input when it brings unseen coverage.
func (v *CorpusVault) Add(input []byte, cov CoverSet, origin string) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Get reads one entry.
func (v *CorpusVault) Get(id string) (Entry, bool) {
	return Entry{}, false
}

// Entries lists the live entries in admission order.
func (v *CorpusVault) Entries() []Entry {
	return nil
}

// Minimize shrinks one entry to the shortest prefix that reproduces its
// coverage.
func (v *CorpusVault) Minimize(id string) ([]byte, error) {
	return nil, errNotImplemented
}

// Promote pins an entry so eviction skips it.
func (v *CorpusVault) Promote(id string) error {
	return errNotImplemented
}

// MarkCrash flags an entry as a crash case and keeps its origins.
func (v *CorpusVault) MarkCrash(id string) error {
	return errNotImplemented
}

// ConfirmCrash records the manual confirmation of a crash case.
func (v *CorpusVault) ConfirmCrash(id string) error {
	return errNotImplemented
}

// Reap evicts entries until the byte quota is met and returns their ids.
func (v *CorpusVault) Reap() []string {
	return nil
}

// Stats reads the counter block.
func (v *CorpusVault) Stats() VaultStats {
	return VaultStats{}
}

// Snapshot serialises entries, origins, the coverage watermark and counters.
func (v *CorpusVault) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole vault state with a snapshot, all or nothing.
func (v *CorpusVault) Restore(data []byte) error {
	return errNotImplemented
}
