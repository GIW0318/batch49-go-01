// Package task stages decoded texture pages from the staging buffer into
// resident GPU pages with parent first ordering and a retirement window.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
type TextureUploadStager struct{}

// NewTextureUploadStager builds a stager with the given bounds.
func NewTextureUploadStager(cfg StageConfig) *TextureUploadStager {
	return &TextureUploadStager{}
}

// SubmitPage queues one decoded page and returns its ticket.
func (s *TextureUploadStager) SubmitPage(req PageRequest) (PageTicket, error) {
	return PageTicket{}, errNotImplemented
}

// Cancel drops a page that has not started uploading.
func (s *TextureUploadStager) Cancel(ticket PageTicket) error {
	return errNotImplemented
}

// UploadStep moves staged pages into resident pages within a byte budget.
func (s *TextureUploadStager) UploadStep(bytesBudget int) (UploadReport, error) {
	return UploadReport{}, errNotImplemented
}

// Evict retires one resident page.
func (s *TextureUploadStager) Evict(texID string, level int) error {
	return errNotImplemented
}

// Resident lists the resident mip levels of one texture in ascending order.
func (s *TextureUploadStager) Resident(texID string) ([]int, error) {
	return nil, errNotImplemented
}

// State reports the lifecycle state of one page.
func (s *TextureUploadStager) State(texID string, level int) string {
	return ""
}

// Handle reads the page handle of one resident page.
func (s *TextureUploadStager) Handle(texID string, level int) (PageHandle, error) {
	return PageHandle{}, errNotImplemented
}

// DestroyTexture releases every page of a texture.
func (s *TextureUploadStager) DestroyTexture(texID string) error {
	return errNotImplemented
}

// StagedBytes reports how many staging bytes are currently held.
func (s *TextureUploadStager) StagedBytes() int {
	return 0
}

// Tick advances the retirement clock.
func (s *TextureUploadStager) Tick(ms int64) {}

// Snapshot serialises the page table, the tickets and the retirement timers.
func (s *TextureUploadStager) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole stager state with a snapshot, all or nothing.
func (s *TextureUploadStager) Restore(data []byte) error {
	return errNotImplemented
}
