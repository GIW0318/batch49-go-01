// Package task stages decoded texture pages from the staging buffer into
// resident GPU pages with parent first ordering and a retirement window.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Page lifecycle states.
const (
	PageQueued    = "queued"
	PageUploading = "uploading"
	PageResident  = "resident"
	PageRetired   = "retired"
	PageFree      = "free"
	PageCancelled = "cancelled"
)

// Defaults applied when the configuration leaves a field at zero.
const (
	DefaultStagingBudget  = 4096
	DefaultRetireWindowMs = 120
)

// Errors returned by the stager.
var (
	ErrBlankTexture     = errors.New("texture id is required")
	ErrInvalidLevel     = errors.New("mip level is invalid")
	ErrInvalidBytes     = errors.New("page byte size is invalid")
	ErrInvalidBudget    = errors.New("upload budget must be positive")
	ErrDuplicatePage    = errors.New("page is already tracked")
	ErrUnknownTicket    = errors.New("page ticket is unknown")
	ErrUploadInProgress = errors.New("page is already uploading")
	ErrNotResident      = errors.New("page is not resident")
	ErrNoTexture        = errors.New("texture is not tracked")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// PageRequest describes one decoded page waiting for upload.
type PageRequest struct {
	TexID string `json:"tex_id"`
	Level int    `json:"level"`
	Bytes int    `json:"bytes"`
}

// PageTicket is the handle a caller keeps to cancel or query one page.
type PageTicket struct {
	ID    int64  `json:"id"`
	TexID string `json:"tex_id"`
	Level int    `json:"level"`
}

// UploadReport describes what one UploadStep call did.
type UploadReport struct {
	Bytes    int  `json:"bytes"`
	Uploaded int  `json:"uploaded"`
	Deferred int  `json:"deferred"`
	Blocked  int  `json:"blocked"`
	Partial  bool `json:"partial"`
}

// PageHandle names one resident page with its generation.
type PageHandle struct {
	TexID string `json:"tex_id"`
	Level int    `json:"level"`
	Gen   uint64 `json:"gen"`
}

// StageConfig bounds the staging buffer and the retirement window.
type StageConfig struct {
	StagingBudget  int
	RetireWindowMs int64
}

// TextureUploadStager owns the page table, the staging counters and the
// retirement timers.
type TextureUploadStager struct {
	mu            sync.Mutex
	pages         map[string]map[int]*page
	nextTicket    int64
	clock         int64
	staged        int
	stagingBudget int
	retireWindow  int64
}

// page is one tracked mip page of a texture.
type page struct {
	texID       string
	level       int
	bytes       int
	remaining   int
	state       string
	ticketID    int64
	gen         uint64
	retireStart int64
}

// NewTextureUploadStager builds a stager with the given bounds.
func NewTextureUploadStager(cfg StageConfig) *TextureUploadStager {
	stagingBudget := cfg.StagingBudget
	if stagingBudget <= 0 {
		stagingBudget = DefaultStagingBudget
	}
	retireWindow := cfg.RetireWindowMs
	if retireWindow <= 0 {
		retireWindow = DefaultRetireWindowMs
	}
	return &TextureUploadStager{
		pages:         make(map[string]map[int]*page),
		nextTicket:    1,
		stagingBudget: stagingBudget,
		retireWindow:  retireWindow,
	}
}

// SubmitPage queues one decoded page and returns its ticket.
func (s *TextureUploadStager) SubmitPage(req PageRequest) (PageTicket, error) {
	if strings.TrimSpace(req.TexID) == "" {
		return PageTicket{}, ErrBlankTexture
	}
	if req.Level < 0 {
		return PageTicket{}, ErrInvalidLevel
	}
	if req.Bytes <= 0 {
		return PageTicket{}, ErrInvalidBytes
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	texPages := s.pages[req.TexID]
	if texPages == nil {
		texPages = make(map[int]*page)
		s.pages[req.TexID] = texPages
	}

	ticket := PageTicket{ID: s.nextTicket, TexID: req.TexID, Level: req.Level}
	s.nextTicket++

	if existing, ok := texPages[req.Level]; ok {
		switch existing.state {
		case PageQueued, PageUploading, PageResident:
			return PageTicket{}, ErrDuplicatePage
		case PageRetired:
			// Reuse the cached page: it goes straight back to resident
			// without queuing and without taking staging bytes.
			existing.state = PageResident
			existing.ticketID = ticket.ID
			return ticket, nil
		}
		// Cancelled pages fall through and are replaced by a fresh page.
	}

	texPages[req.Level] = &page{
		texID:     req.TexID,
		level:     req.Level,
		bytes:     req.Bytes,
		remaining: req.Bytes,
		state:     PageQueued,
		ticketID:  ticket.ID,
	}
	s.staged += req.Bytes
	return ticket, nil
}

// Cancel drops a page that has not started uploading.
func (s *TextureUploadStager) Cancel(ticket PageTicket) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.findPage(ticket.TexID, ticket.Level)
	if p == nil || p.ticketID != ticket.ID {
		return ErrUnknownTicket
	}
	switch p.state {
	case PageUploading:
		return ErrUploadInProgress
	case PageQueued:
		s.staged -= p.remaining
		p.remaining = 0
		p.state = PageCancelled
		return nil
	default:
		return ErrUnknownTicket
	}
}

// UploadStep moves staged pages into resident pages within a byte budget.
func (s *TextureUploadStager) UploadStep(bytesBudget int) (UploadReport, error) {
	if bytesBudget <= 0 {
		return UploadReport{}, ErrInvalidBudget
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var pending []*page
	for _, texPages := range s.pages {
		for _, p := range texPages {
			if p.state == PageQueued || p.state == PageUploading {
				pending = append(pending, p)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].texID != pending[j].texID {
			return pending[i].texID < pending[j].texID
		}
		return pending[i].level < pending[j].level
	})

	var report UploadReport
	remaining := bytesBudget
	for _, p := range pending {
		eligible := p.state == PageUploading || p.level == 0 || s.isResidentLocked(p.texID, p.level-1)
		if !eligible {
			report.Blocked++
			continue
		}
		if remaining <= 0 {
			report.Deferred++
			continue
		}
		chunk := p.remaining
		if chunk > remaining {
			chunk = remaining
		}
		p.remaining -= chunk
		s.staged -= chunk
		remaining -= chunk
		report.Bytes += chunk
		if p.remaining == 0 {
			p.state = PageResident
			p.gen++
			report.Uploaded++
		} else {
			p.state = PageUploading
			report.Partial = true
		}
	}
	return report, nil
}

// Evict retires one resident page.
func (s *TextureUploadStager) Evict(texID string, level int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.findPage(texID, level)
	if p == nil || p.state != PageResident {
		return ErrNotResident
	}
	texPages := s.pages[texID]
	// Retire the page and cascade to every resident child mip.
	levels := make([]int, 0, len(texPages))
	for lv, candidate := range texPages {
		if lv >= level && candidate.state == PageResident {
			levels = append(levels, lv)
		}
	}
	sort.Ints(levels)
	for _, lv := range levels {
		child := texPages[lv]
		child.state = PageRetired
		child.retireStart = s.clock
	}
	return nil
}

// Resident lists the resident mip levels of one texture in ascending order.
func (s *TextureUploadStager) Resident(texID string) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	texPages, ok := s.pages[texID]
	if !ok {
		return nil, ErrNoTexture
	}
	levels := make([]int, 0, len(texPages))
	for lv, p := range texPages {
		if p.state == PageResident {
			levels = append(levels, lv)
		}
	}
	sort.Ints(levels)
	return levels, nil
}

// State reports the lifecycle state of one page.
func (s *TextureUploadStager) State(texID string, level int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p := s.findPage(texID, level); p != nil {
		return p.state
	}
	return PageFree
}

// Handle reads the page handle of one resident page.
func (s *TextureUploadStager) Handle(texID string, level int) (PageHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.findPage(texID, level)
	if p == nil || p.state != PageResident {
		return PageHandle{}, ErrNotResident
	}
	return PageHandle{TexID: p.texID, Level: p.level, Gen: p.gen}, nil
}

// DestroyTexture releases every page of a texture.
func (s *TextureUploadStager) DestroyTexture(texID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	texPages, ok := s.pages[texID]
	if !ok {
		return nil
	}
	for _, p := range texPages {
		if p.state == PageQueued || p.state == PageUploading {
			s.staged -= p.remaining
			p.remaining = 0
		}
	}
	delete(s.pages, texID)
	return nil
}

// StagedBytes reports how many staging bytes are currently held.
func (s *TextureUploadStager) StagedBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.staged
}

// Tick advances the retirement clock.
func (s *TextureUploadStager) Tick(ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clock += ms
	for _, texPages := range s.pages {
		for lv, p := range texPages {
			if p.state == PageRetired && s.clock-p.retireStart >= s.retireWindow {
				delete(texPages, lv)
			}
		}
	}
}

// snapshotPage is the serialised form of one tracked page.
type snapshotPage struct {
	TexID       string `json:"tex_id"`
	Level       int    `json:"level"`
	Bytes       int    `json:"bytes"`
	Remaining   int    `json:"remaining"`
	State       string `json:"state"`
	TicketID    int64  `json:"ticket_id"`
	Gen         uint64 `json:"gen"`
	RetireStart int64  `json:"retire_start"`
}

// snapshot is the serialised form of the whole stager.
type snapshot struct {
	Version    int            `json:"version"`
	Clock      int64          `json:"clock"`
	NextTicket int64          `json:"next_ticket"`
	Staged     int            `json:"staged"`
	Pages      []snapshotPage `json:"pages"`
}

const snapshotVersion = 1

// Snapshot serialises the page table, the tickets and the retirement timers.
func (s *TextureUploadStager) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := snapshot{
		Version:    snapshotVersion,
		Clock:      s.clock,
		NextTicket: s.nextTicket,
		Staged:     s.staged,
	}
	for _, texPages := range s.pages {
		for _, p := range texPages {
			switch p.state {
			case PageQueued, PageUploading, PageResident, PageRetired:
				snap.Pages = append(snap.Pages, snapshotPage{
					TexID:       p.texID,
					Level:       p.level,
					Bytes:       p.bytes,
					Remaining:   p.remaining,
					State:       p.state,
					TicketID:    p.ticketID,
					Gen:         p.gen,
					RetireStart: p.retireStart,
				})
			}
		}
	}
	sort.Slice(snap.Pages, func(i, j int) bool {
		if snap.Pages[i].TexID != snap.Pages[j].TexID {
			return snap.Pages[i].TexID < snap.Pages[j].TexID
		}
		return snap.Pages[i].Level < snap.Pages[j].Level
	})
	return json.Marshal(snap)
}

// Restore replaces the whole stager state with a snapshot, all or nothing.
func (s *TextureUploadStager) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion || snap.Clock < 0 || snap.NextTicket <= 0 || snap.Staged < 0 {
		return ErrBadSnapshot
	}

	pages := make(map[string]map[int]*page)
	staged := 0
	for _, sp := range snap.Pages {
		if strings.TrimSpace(sp.TexID) == "" || sp.Level < 0 || sp.Bytes <= 0 ||
			sp.Remaining < 0 || sp.Remaining > sp.Bytes || sp.RetireStart < 0 {
			return ErrBadSnapshot
		}
		switch sp.State {
		case PageQueued, PageUploading:
			if sp.Remaining <= 0 {
				return ErrBadSnapshot
			}
			staged += sp.Remaining
		case PageResident, PageRetired:
		default:
			return ErrBadSnapshot
		}
		texPages := pages[sp.TexID]
		if texPages == nil {
			texPages = make(map[int]*page)
			pages[sp.TexID] = texPages
		}
		if _, dup := texPages[sp.Level]; dup {
			return ErrBadSnapshot
		}
		texPages[sp.Level] = &page{
			texID:       sp.TexID,
			level:       sp.Level,
			bytes:       sp.Bytes,
			remaining:   sp.Remaining,
			state:       sp.State,
			ticketID:    sp.TicketID,
			gen:         sp.Gen,
			retireStart: sp.RetireStart,
		}
	}
	if staged != snap.Staged {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = pages
	s.clock = snap.Clock
	s.nextTicket = snap.NextTicket
	s.staged = staged
	return nil
}

// findPage returns the tracked page for texID/level, or nil.
// Callers must hold s.mu.
func (s *TextureUploadStager) findPage(texID string, level int) *page {
	if texPages, ok := s.pages[texID]; ok {
		return texPages[level]
	}
	return nil
}

// isResidentLocked reports whether the given page is currently resident.
// Callers must hold s.mu.
func (s *TextureUploadStager) isResidentLocked(texID string, level int) bool {
	p := s.findPage(texID, level)
	return p != nil && p.state == PageResident
}
