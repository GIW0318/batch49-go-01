// Package task ranks players on score boards with competition and dense
// ranking, percentile buckets and composite cursor paging.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

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
	mu     sync.RWMutex
	opts   Options
	boards map[string]*rankBoard
}

type rankBoard struct {
	players         map[string]int64
	entries         []rankEntry
	scoreCounts     map[int64]int
	ascendingScores []int64
	watermark       int64
	hasWatermark    bool
}

type rankEntry struct {
	Player string
	Score  int64
}

// NewTieRankResolver builds an empty resolver.
func NewTieRankResolver(opts Options) *TieRankResolver {
	return &TieRankResolver{
		opts:   opts,
		boards: make(map[string]*rankBoard),
	}
}

// Put inserts or updates one player score on a board.
func (r *TieRankResolver) Put(board, player string, score int64) error {
	if strings.TrimSpace(board) == "" || strings.TrimSpace(player) == "" {
		return ErrBlankKey
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.boards == nil {
		r.boards = make(map[string]*rankBoard)
	}
	b := r.boards[board]
	if b == nil {
		b = newRankBoard()
		r.boards[board] = b
	}

	oldScore, exists := b.players[player]
	if exists {
		if oldScore == score {
			return nil
		}
		if index := b.findIndex(oldScore, player); index >= 0 {
			b.removeEntryAt(index)
		}
	}

	b.insertEntry(rankEntry{Player: player, Score: score})
	b.players[player] = score
	b.addScore(score)

	if r.opts.MaxEntries > 0 {
		for len(b.players) > r.opts.MaxEntries {
			evicted := b.removeEntryAt(len(b.entries) - 1)
			b.watermark = evicted.Score
			b.hasWatermark = true
		}
	}
	return nil
}

// Rank reports the competition rank, dense rank and total of one player.
func (r *TieRankResolver) Rank(board, player string) (RankInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b := r.boards[board]
	if b == nil {
		return RankInfo{}, ErrPlayerNotFound
	}
	score, ok := b.players[player]
	if !ok {
		return RankInfo{}, ErrPlayerNotFound
	}

	return RankInfo{
		Board:  board,
		Player: player,
		Score:  score,
		Rank:   b.competitionRank(score),
		Dense:  b.denseRank(score),
		Total:  len(b.players),
	}, nil
}

// Top returns the first n rows of a board.
func (r *TieRankResolver) Top(board string, n int) []RankRow {
	if n <= 0 {
		return []RankRow{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	b := r.boards[board]
	if b == nil {
		return []RankRow{}
	}
	return b.page(Cursor{}, n).Rows
}

// Page returns the rows that follow a cursor.
func (r *TieRankResolver) Page(board string, after Cursor, limit int) (Page, error) {
	if limit <= 0 {
		return Page{}, ErrBadLimit
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	b := r.boards[board]
	if b == nil {
		return Page{Rows: []RankRow{}, Total: 0}, nil
	}
	return b.page(after, limit), nil
}

// Percentile reports the percentage of ranked players strictly below score.
func (r *TieRankResolver) Percentile(board string, score int64) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b := r.boards[board]
	if b == nil || len(b.entries) == 0 {
		return 0
	}

	firstBelow := sort.Search(len(b.entries), func(i int) bool {
		return b.entries[i].Score < score
	})
	below := len(b.entries) - firstBelow
	return below * 100 / len(b.entries)
}

// Watermark reports the most recent evicted score of a board.
func (r *TieRankResolver) Watermark(board string) (int64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b := r.boards[board]
	if b == nil || !b.hasWatermark {
		return 0, false
	}
	return b.watermark, true
}

// Len reports how many players a board currently holds.
func (r *TieRankResolver) Len(board string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if b := r.boards[board]; b != nil {
		return len(b.players)
	}
	return 0
}

// Snapshot serialises every board table and eviction watermark.
func (r *TieRankResolver) Snapshot() ([]byte, error) {
	r.mu.RLock()
	payload := snapshotData{
		Version: snapshotVersion,
		Boards:  make(map[string]snapshotBoard, len(r.boards)),
	}
	for boardName, b := range r.boards {
		boardSnapshot := snapshotBoard{
			Scores: make(map[string]int64, len(b.players)),
		}
		for player, score := range b.players {
			boardSnapshot.Scores[player] = score
		}
		if b.hasWatermark {
			watermark := b.watermark
			boardSnapshot.Watermark = &watermark
		}
		payload.Boards[boardName] = boardSnapshot
	}
	r.mu.RUnlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal rank snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole resolver state with a snapshot.
func (r *TieRankResolver) Restore(data []byte) error {
	var payload snapshotData
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	boards := make(map[string]*rankBoard, len(payload.Boards))
	for boardName, boardData := range payload.Boards {
		if strings.TrimSpace(boardName) == "" {
			return ErrBadSnapshot
		}

		b := newRankBoard()
		for player, score := range boardData.Scores {
			if strings.TrimSpace(player) == "" {
				return ErrBadSnapshot
			}
			b.players[player] = score
			b.entries = append(b.entries, rankEntry{Player: player, Score: score})
			b.addScore(score)
		}
		sort.Slice(b.entries, func(i, j int) bool {
			return comparePositions(
				b.entries[i].Score, b.entries[i].Player,
				b.entries[j].Score, b.entries[j].Player,
			) < 0
		})
		if boardData.Watermark != nil {
			b.watermark = *boardData.Watermark
			b.hasWatermark = true
		}
		boards[boardName] = b
	}

	r.mu.Lock()
	r.boards = boards
	r.mu.Unlock()
	return nil
}

const snapshotVersion = 1

type snapshotData struct {
	Version int                      `json:"version"`
	Boards  map[string]snapshotBoard `json:"boards"`
}

type snapshotBoard struct {
	Scores    map[string]int64 `json:"scores"`
	Watermark *int64           `json:"watermark,omitempty"`
}

func newRankBoard() *rankBoard {
	return &rankBoard{
		players:     make(map[string]int64),
		scoreCounts: make(map[int64]int),
	}
}

// comparePositions returns -1 when a comes first, 0 for equal positions and 1
// when b comes first. Ordering is score descending then player ascending.
func comparePositions(scoreA int64, playerA string, scoreB int64, playerB string) int {
	switch {
	case scoreA > scoreB:
		return -1
	case scoreA < scoreB:
		return 1
	case playerA < playerB:
		return -1
	case playerA > playerB:
		return 1
	default:
		return 0
	}
}

func (b *rankBoard) findIndex(score int64, player string) int {
	index := sort.Search(len(b.entries), func(i int) bool {
		return comparePositions(
			b.entries[i].Score, b.entries[i].Player,
			score, player,
		) >= 0
	})
	if index < len(b.entries) &&
		b.entries[index].Score == score &&
		b.entries[index].Player == player {
		return index
	}
	return -1
}

func (b *rankBoard) insertEntry(entry rankEntry) {
	index := sort.Search(len(b.entries), func(i int) bool {
		return comparePositions(
			b.entries[i].Score, b.entries[i].Player,
			entry.Score, entry.Player,
		) >= 0
	})

	b.entries = append(b.entries, rankEntry{})
	copy(b.entries[index+1:], b.entries[index:])
	b.entries[index] = entry
}

func (b *rankBoard) removeEntryAt(index int) rankEntry {
	entry := b.entries[index]
	delete(b.players, entry.Player)
	b.removeScore(entry.Score)

	copy(b.entries[index:], b.entries[index+1:])
	last := len(b.entries) - 1
	b.entries[last] = rankEntry{}
	b.entries = b.entries[:last]
	return entry
}

func (b *rankBoard) addScore(score int64) {
	if b.scoreCounts[score] == 0 {
		index := sort.Search(len(b.ascendingScores), func(i int) bool {
			return b.ascendingScores[i] >= score
		})
		b.ascendingScores = append(b.ascendingScores, 0)
		copy(b.ascendingScores[index+1:], b.ascendingScores[index:])
		b.ascendingScores[index] = score
	}
	b.scoreCounts[score]++
}

func (b *rankBoard) removeScore(score int64) {
	count := b.scoreCounts[score]
	if count <= 1 {
		delete(b.scoreCounts, score)
		index := sort.Search(len(b.ascendingScores), func(i int) bool {
			return b.ascendingScores[i] >= score
		})
		if index < len(b.ascendingScores) && b.ascendingScores[index] == score {
			b.ascendingScores = append(
				b.ascendingScores[:index],
				b.ascendingScores[index+1:]...,
			)
		}
		return
	}
	b.scoreCounts[score] = count - 1
}

func (b *rankBoard) competitionRank(score int64) int {
	higher := sort.Search(len(b.entries), func(i int) bool {
		return b.entries[i].Score <= score
	})
	return higher + 1
}

func (b *rankBoard) denseRank(score int64) int {
	firstHigherScore := sort.Search(len(b.ascendingScores), func(i int) bool {
		return b.ascendingScores[i] > score
	})
	higherDistinct := len(b.ascendingScores) - firstHigherScore
	return higherDistinct + 1
}

func (b *rankBoard) page(after Cursor, limit int) Page {
	total := len(b.entries)
	start := 0

	// An empty player cannot be a persisted cursor because blank keys are
	// rejected, so it denotes the first page.
	if after.Player != "" {
		start = sort.Search(total, func(i int) bool {
			return comparePositions(
				b.entries[i].Score, b.entries[i].Player,
				after.Score, after.Player,
			) > 0
		})
	}

	end := total
	if available := total - start; limit < available {
		end = start + limit
	}

	rows := make([]RankRow, 0, end-start)
	for i := start; i < end; i++ {
		entry := b.entries[i]
		rows = append(rows, RankRow{
			Player: entry.Player,
			Score:  entry.Score,
			Rank:   b.competitionRank(entry.Score),
			Dense:  b.denseRank(entry.Score),
		})
	}

	next := Cursor{}
	if len(rows) > 0 {
		last := b.entries[end-1]
		next = Cursor{Score: last.Score, Player: last.Player}
	}

	return Page{
		Rows:    rows,
		Next:    next,
		HasMore: end < total,
		Total:   total,
	}
}
