// Package task ranks players on score boards with competition and dense
// ranking, percentile buckets and composite cursor paging.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the resolver.
var (
	ErrBlankKey       = errors.New("board and player are required")
	ErrPlayerNotFound = errors.New("player is not ranked on this board")
	ErrBadLimit       = errors.New("page limit must be positive")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// RankInfo is the full standing of one player.
type RankInfo struct {
	Board  string
	Player string
	Score  int64
	Rank   int
	Dense  int
	Total  int
}

// RankRow is one row of an ordered board listing.
type RankRow struct {
	Player string
	Score  int64
	Rank   int
	Dense  int
}

// Cursor addresses one position in the board order.
type Cursor struct {
	Score  int64
	Player string
}

// Page is one slice of a board listing.
type Page struct {
	Rows    []RankRow
	Next    Cursor
	HasMore bool
	Total   int
}

// Options tunes the resolver.
type Options struct {
	// MaxEntries caps how many entries a board keeps. Values below one
	// mean the board is unbounded.
	MaxEntries int
}

// TieRankResolver owns every board table and the eviction watermarks.
type TieRankResolver struct {
	opts Options
}

// NewTieRankResolver builds an empty resolver.
func NewTieRankResolver(opts Options) *TieRankResolver {
	return &TieRankResolver{opts: opts}
}

// Put inserts or updates one player score on a board.
func (r *TieRankResolver) Put(board, player string, score int64) error {
	return errNotImplemented
}

// Rank reports the competition rank, dense rank and total of one player.
func (r *TieRankResolver) Rank(board, player string) (RankInfo, error) {
	return RankInfo{}, errNotImplemented
}

// Top returns the first n rows of a board.
func (r *TieRankResolver) Top(board string, n int) []RankRow {
	return nil
}

// Page returns the rows that follow a cursor.
func (r *TieRankResolver) Page(board string, after Cursor, limit int) (Page, error) {
	return Page{}, errNotImplemented
}

// Percentile reports the percentage of ranked players strictly below score.
func (r *TieRankResolver) Percentile(board string, score int64) int {
	return 0
}

// Watermark reports the most recent evicted score of a board.
func (r *TieRankResolver) Watermark(board string) (int64, bool) {
	return 0, false
}

// Len reports how many players a board currently holds.
func (r *TieRankResolver) Len(board string) int {
	return 0
}

// Snapshot serialises every board table and eviction watermark.
func (r *TieRankResolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole resolver state with a snapshot.
func (r *TieRankResolver) Restore(data []byte) error {
	return errNotImplemented
}
