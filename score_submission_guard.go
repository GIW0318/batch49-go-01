// Package task screens score submissions before they enter a board, parks
// suspicious ones in quarantine and admits them again after manual release.
package task

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
)

// Submission and quarantine statuses.
const (
	StatusAdmitted    = "admitted"
	StatusQuarantined = "quarantined"
	StatusReleased    = "released"
	StatusRejected    = "rejected"
	StatusDropped     = "dropped"
	StatusDuplicate   = "duplicate"
)

// Reasons attached to a verdict.
const (
	ReasonOK         = "ok"
	ReasonSingleGain = "single_gain"
	ReasonWindowGain = "window_gain"
	ReasonDuration   = "duration"
)

// Errors reported by the guard.
var (
	ErrBlankKey          = errors.New("board and player are required")
	ErrBadDuration       = errors.New("round duration must be positive")
	ErrBadScore          = errors.New("score must not be negative")
	ErrUnknownSubmission = errors.New("submission id is unknown")
	ErrBadReason         = errors.New("reject reason is required")
	ErrBadState          = errors.New("submission is not pending")
	ErrBadPayload        = errors.New("snapshot payload is invalid")
)

const snapshotVersion = 1

// Options tunes the three screening checks and the quarantine queue.
type Options struct {
	// MaxSingleGain caps the gain over the current best score. Zero disables
	// the check.
	MaxSingleGain int64
	// WindowMs is the sliding window width. Zero disables the window check.
	WindowMs int64
	// WindowGain caps the gain over the best score inside the window.
	WindowGain int64
	// MaxScorePerMinute caps how much score one minute of play may produce.
	// Zero disables the duration check.
	MaxScorePerMinute int64
	// QuarantineLimit caps pending quarantine entries per board. Values
	// below one fall back to 16.
	QuarantineLimit int
}

// Verdict is the outcome of one Submit call.
type Verdict struct {
	SubmissionID string
	Board        string
	Player       string
	Score        int64
	Status       string
	Reason       string
}

// Quarantined is one ledger record kept for a screened submission.
type Quarantined struct {
	ID         string
	Board      string
	Player     string
	Score      int64
	DurationMs int64
	At         int64
	Reasons    []string
	Status     string
}

// EffectiveEntry is one admitted score currently visible on a board.
type EffectiveEntry struct {
	Player string
	Score  int64
	At     int64
}

// Stats is the ledger summary of one board.
type Stats struct {
	Submitted   int
	Admitted    int
	Quarantined int
	Released    int
	Rejected    int
	Dropped     int
	Duplicates  int
}

// ScoreGuard owns the submission ledger, quarantine queues and board state.
type ScoreGuard struct {
	opts   Options
	mu     sync.RWMutex
	boards map[string]*boardState
}

type scorePoint struct {
	Score int64 `json:"score"`
	At    int64 `json:"at"`
}

type record struct {
	id           string
	board        string
	player       string
	score        int64
	durationMs   int64
	at           int64
	reasons      []string
	status       string
	rejectReason string
}

func (r *record) toQuarantined() Quarantined {
	return Quarantined{
		ID:         r.id,
		Board:      r.board,
		Player:     r.player,
		Score:      r.score,
		DurationMs: r.durationMs,
		At:         r.at,
		Reasons:    cloneStrings(r.reasons),
		Status:     r.status,
	}
}

type boardState struct {
	records    map[string]*record
	queue      []string
	effective  map[string]EffectiveEntry
	points     map[string][]scorePoint
	duplicates int
}

func newBoardState() *boardState {
	return &boardState{
		records:   make(map[string]*record),
		queue:     make([]string, 0),
		effective: make(map[string]EffectiveEntry),
		points:    make(map[string][]scorePoint),
	}
}

func (b *boardState) addEffective(player string, score, at int64) {
	b.points[player] = append(b.points[player], scorePoint{Score: score, At: at})
	current, ok := b.effective[player]
	if !ok || score > current.Score {
		b.effective[player] = EffectiveEntry{Player: player, Score: score, At: at}
	}
}

// NewScoreGuard builds an empty guard.
func NewScoreGuard(opts Options) *ScoreGuard {
	return &ScoreGuard{
		opts:   opts,
		boards: make(map[string]*boardState),
	}
}

// Submit screens one score submission and files it in the ledger.
func (g *ScoreGuard) Submit(board, player string, score, durationMs int64, at int64) (Verdict, error) {
	if isBlank(board) || isBlank(player) {
		return Verdict{}, ErrBlankKey
	}
	if durationMs <= 0 {
		return Verdict{}, ErrBadDuration
	}
	if score < 0 {
		return Verdict{}, ErrBadScore
	}

	id := deriveSubmissionID(board, player, score, durationMs, at)

	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.boardForWrite(board)
	if existing, ok := b.records[id]; ok {
		b.duplicates++
		return Verdict{
			SubmissionID: id,
			Board:        board,
			Player:       player,
			Score:        score,
			Status:       StatusDuplicate,
			Reason:       primaryReason(existing.reasons),
		}, nil
	}

	var reasons []string
	var currentScore int64
	if current, hasCurrent := b.effective[player]; hasCurrent {
		currentScore = current.Score
	}
	if g.opts.MaxSingleGain > 0 && score-currentScore > g.opts.MaxSingleGain {
		reasons = append(reasons, ReasonSingleGain)
	}

	if g.opts.WindowMs > 0 {
		lower := at - g.opts.WindowMs
		if windowBest, ok := bestScoreInWindow(b.points[player], lower, at); ok &&
			score-windowBest > g.opts.WindowGain {
			reasons = append(reasons, ReasonWindowGain)
		}
	}

	if g.opts.MaxScorePerMinute > 0 &&
		exceedsScoreRate(score, g.opts.MaxScorePerMinute, durationMs) {
		reasons = append(reasons, ReasonDuration)
	}

	submission := &record{
		id:         id,
		board:      board,
		player:     player,
		score:      score,
		durationMs: durationMs,
		at:         at,
		reasons:    reasons,
	}
	b.records[id] = submission

	verdict := Verdict{
		SubmissionID: id,
		Board:        board,
		Player:       player,
		Score:        score,
	}

	if len(reasons) == 0 {
		submission.status = StatusAdmitted
		verdict.Status = StatusAdmitted
		verdict.Reason = ReasonOK
		b.addEffective(player, score, at)
		return verdict, nil
	}

	submission.status = StatusQuarantined
	verdict.Status = StatusQuarantined
	verdict.Reason = reasons[0]
	b.queue = append(b.queue, id)

	limit := g.opts.QuarantineLimit
	if limit < 1 {
		limit = 16
	}
	for len(b.queue) > limit {
		oldestID := b.queue[0]
		b.queue = b.queue[1:]
		if oldest, ok := b.records[oldestID]; ok {
			oldest.status = StatusDropped
		}
	}

	return verdict, nil
}

// Quarantine lists the pending quarantine entries of one board, oldest first.
func (g *ScoreGuard) Quarantine(board string) []Quarantined {
	g.mu.RLock()
	defer g.mu.RUnlock()

	b := g.boards[board]
	if b == nil {
		return []Quarantined{}
	}

	out := make([]Quarantined, 0, len(b.queue))
	for _, id := range b.queue {
		if submission := b.records[id]; submission != nil &&
			submission.status == StatusQuarantined {
			out = append(out, submission.toQuarantined())
		}
	}
	return out
}

// Submission reads one ledger record by submission id.
func (g *ScoreGuard) Submission(board, submissionID string) (Quarantined, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	b := g.boards[board]
	if b == nil {
		return Quarantined{}, false
	}
	submission, ok := b.records[submissionID]
	if !ok {
		return Quarantined{}, false
	}
	return submission.toQuarantined(), true
}

// Release admits one pending submission with its submitted score.
func (g *ScoreGuard) Release(board, submissionID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.boards[board]
	if b == nil {
		return ErrUnknownSubmission
	}
	submission, ok := b.records[submissionID]
	if !ok {
		return ErrUnknownSubmission
	}
	if submission.status != StatusQuarantined {
		return ErrBadState
	}

	submission.status = StatusReleased
	b.removeFromQueue(submissionID)
	b.addEffective(submission.player, submission.score, submission.at)
	return nil
}

// Reject discards one pending submission with an operator reason.
func (g *ScoreGuard) Reject(board, submissionID, reason string) error {
	if isBlank(reason) {
		return ErrBadReason
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.boards[board]
	if b == nil {
		return ErrUnknownSubmission
	}
	submission, ok := b.records[submissionID]
	if !ok {
		return ErrUnknownSubmission
	}
	if submission.status != StatusQuarantined {
		return ErrBadState
	}

	submission.status = StatusRejected
	submission.rejectReason = reason
	b.removeFromQueue(submissionID)
	return nil
}

// Effective reports the best admitted score of one player.
func (g *ScoreGuard) Effective(board, player string) (int64, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	b := g.boards[board]
	if b == nil {
		return 0, false
	}
	entry, ok := b.effective[player]
	if !ok {
		return 0, false
	}
	return entry.Score, true
}

// EffectiveAll lists admitted scores of one board, highest first.
func (g *ScoreGuard) EffectiveAll(board string) []EffectiveEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	b := g.boards[board]
	if b == nil {
		return []EffectiveEntry{}
	}

	out := make([]EffectiveEntry, 0, len(b.effective))
	for _, entry := range b.effective {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Player < out[j].Player
	})
	return out
}

// Watermark reports the highest admitted score of one board.
func (g *ScoreGuard) Watermark(board string) (int64, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	b := g.boards[board]
	if b == nil {
		return 0, false
	}

	var watermark int64
	found := false
	for _, entry := range b.effective {
		if !found || entry.Score > watermark {
			watermark = entry.Score
			found = true
		}
	}
	return watermark, found
}

// Stats summarises the ledger of one board.
func (g *ScoreGuard) Stats(board string) Stats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var stats Stats
	b := g.boards[board]
	if b == nil {
		return stats
	}

	stats.Duplicates = b.duplicates
	for _, submission := range b.records {
		stats.Submitted++
		switch submission.status {
		case StatusAdmitted:
			stats.Admitted++
		case StatusQuarantined:
			stats.Quarantined++
		case StatusReleased:
			stats.Released++
		case StatusRejected:
			stats.Rejected++
		case StatusDropped:
			stats.Dropped++
		}
	}
	return stats
}

type snapshotEnvelope struct {
	Version int                      `json:"version"`
	Boards  map[string]snapshotBoard `json:"boards"`
}

type snapshotBoard struct {
	Records    map[string]snapshotRecord `json:"records"`
	Queue      []string                  `json:"queue"`
	Effective  map[string]EffectiveEntry `json:"effective"`
	Points     map[string][]scorePoint   `json:"points"`
	Duplicates int                       `json:"duplicates"`
}

type snapshotRecord struct {
	ID           string   `json:"id"`
	Board        string   `json:"board"`
	Player       string   `json:"player"`
	Score        int64    `json:"score"`
	DurationMs   int64    `json:"duration_ms"`
	At           int64    `json:"at"`
	Reasons      []string `json:"reasons"`
	Status       string   `json:"status"`
	RejectReason string   `json:"reject_reason,omitempty"`
}

// Snapshot serialises the ledger, quarantine queues and board state.
func (g *ScoreGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	envelope := snapshotEnvelope{
		Version: snapshotVersion,
		Boards:  make(map[string]snapshotBoard, len(g.boards)),
	}

	for boardName, b := range g.boards {
		boardSnapshot := snapshotBoard{
			Records:    make(map[string]snapshotRecord, len(b.records)),
			Queue:      cloneStrings(b.queue),
			Effective:  make(map[string]EffectiveEntry, len(b.effective)),
			Points:     make(map[string][]scorePoint, len(b.points)),
			Duplicates: b.duplicates,
		}

		for id, submission := range b.records {
			boardSnapshot.Records[id] = snapshotRecord{
				ID:           submission.id,
				Board:        submission.board,
				Player:       submission.player,
				Score:        submission.score,
				DurationMs:   submission.durationMs,
				At:           submission.at,
				Reasons:      cloneStrings(submission.reasons),
				Status:       submission.status,
				RejectReason: submission.rejectReason,
			}
		}
		for player, entry := range b.effective {
			boardSnapshot.Effective[player] = entry
		}
		for player, points := range b.points {
			boardSnapshot.Points[player] = append([]scorePoint(nil), points...)
		}

		envelope.Boards[boardName] = boardSnapshot
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole guard state with a snapshot.
func (g *ScoreGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	if envelope.Version != snapshotVersion {
		return fmt.Errorf("%w: unsupported snapshot version %d", ErrBadPayload, envelope.Version)
	}

	nextBoards := make(map[string]*boardState, len(envelope.Boards))
	for boardName, boardData := range envelope.Boards {
		if isBlank(boardName) || boardData.Duplicates < 0 {
			return ErrBadPayload
		}

		b := newBoardState()
		b.duplicates = boardData.Duplicates

		for id, recordData := range boardData.Records {
			if id == "" || recordData.ID != id || recordData.Board != boardName ||
				isBlank(recordData.Player) || recordData.Score < 0 ||
				recordData.DurationMs <= 0 || !isLedgerStatus(recordData.Status) {
				return ErrBadPayload
			}

			b.records[id] = &record{
				id:           recordData.ID,
				board:        recordData.Board,
				player:       recordData.Player,
				score:        recordData.Score,
				durationMs:   recordData.DurationMs,
				at:           recordData.At,
				reasons:      cloneStrings(recordData.Reasons),
				status:       recordData.Status,
				rejectReason: recordData.RejectReason,
			}
		}

		queued := make(map[string]struct{}, len(boardData.Queue))
		for _, id := range boardData.Queue {
			submission, ok := b.records[id]
			if !ok || submission.status != StatusQuarantined {
				return ErrBadPayload
			}
			if _, duplicated := queued[id]; duplicated {
				return ErrBadPayload
			}
			queued[id] = struct{}{}
			b.queue = append(b.queue, id)
		}

		for id, submission := range b.records {
			if submission.status == StatusQuarantined {
				if _, ok := queued[id]; !ok {
					return ErrBadPayload
				}
			}
			if submission.status == StatusAdmitted || submission.status == StatusReleased {
				b.addEffective(submission.player, submission.score, submission.at)
			}
		}

		nextBoards[boardName] = b
	}

	g.mu.Lock()
	g.boards = nextBoards
	g.mu.Unlock()
	return nil
}

func (g *ScoreGuard) boardForWrite(board string) *boardState {
	if g.boards == nil {
		g.boards = make(map[string]*boardState)
	}
	b := g.boards[board]
	if b == nil {
		b = newBoardState()
		g.boards[board] = b
	}
	return b
}

func (b *boardState) removeFromQueue(submissionID string) {
	next := make([]string, 0, len(b.queue))
	for _, id := range b.queue {
		if id != submissionID {
			next = append(next, id)
		}
	}
	b.queue = next
}

func deriveSubmissionID(board, player string, score, durationMs, at int64) string {
	hasher := sha256.New()

	writeString := func(value string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	writeInt64 := func(value int64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(value))
		_, _ = hasher.Write(encoded[:])
	}

	writeString(board)
	writeString(player)
	writeInt64(score)
	writeInt64(durationMs)
	writeInt64(at)

	return hex.EncodeToString(hasher.Sum(nil))
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func primaryReason(reasons []string) string {
	if len(reasons) == 0 {
		return ReasonOK
	}
	return reasons[0]
}

func bestScoreInWindow(points []scorePoint, lower, upper int64) (int64, bool) {
	var best int64
	found := false
	for _, point := range points {
		if point.At < lower || point.At > upper {
			continue
		}
		if !found || point.Score > best {
			best = point.Score
			found = true
		}
	}
	return best, found
}

func exceedsScoreRate(score, maxScorePerMinute, durationMs int64) bool {
	scaledScore := new(big.Int).Mul(big.NewInt(score), big.NewInt(60000))
	scaledLimit := new(big.Int).Mul(big.NewInt(maxScorePerMinute), big.NewInt(durationMs))
	return scaledScore.Cmp(scaledLimit) > 0
}

func isLedgerStatus(status string) bool {
	switch status {
	case StatusAdmitted, StatusQuarantined, StatusReleased, StatusRejected, StatusDropped:
		return true
	default:
		return false
	}
}
