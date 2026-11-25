// Package task parses H.264 SPS and PPS parameter sets and tracks which set is
// currently active for downstream decoders.
package task

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"sort"
	"sync"
)

// Errors returned by the tracker.
var (
	ErrBadSPS      = errors.New("sps payload is invalid")
	ErrBadPPS      = errors.New("pps payload is invalid")
	ErrUnknownPPS  = errors.New("pps is not known")
	ErrUnknownSPS  = errors.New("sps is not known")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

const (
	nalTypeSPS = 7
	nalTypePPS = 8

	maxParamSetID = 31
	maxDimension  = 4096
	maxParamSets  = 32
)

// SPSInfo is the decoded subset of one sequence parameter set.
type SPSInfo struct {
	ID           int
	ProfileIDC   int
	LevelIDC     int
	Width        int
	Height       int
	FrameMBSOnly bool
	NumRefFrames int
	Version      int
}

// PPSInfo is the decoded subset of one picture parameter set.
type PPSInfo struct {
	ID            int
	SPSID         int
	EntropyCoding bool
	Deblocking    bool
	Version       int
}

type spsEntry struct {
	info     SPSInfo
	content  []byte
	lastUsed uint64
}

type ppsEntry struct {
	info     PPSInfo
	content  []byte
	lastUsed uint64
}

// ParamSetTracker owns the parsed sets and the activation state.
type ParamSetTracker struct {
	mu sync.Mutex

	sps map[int]*spsEntry
	pps map[int]*ppsEntry

	activeSPS SPSInfo
	activePPS PPSInfo
	hasActive bool

	resolutionChanged bool
	lastWidth         int
	lastHeight        int
	useTick           uint64
}

// NewParamSetTracker builds an empty tracker with no active set.
func NewParamSetTracker() *ParamSetTracker {
	return &ParamSetTracker{
		sps: make(map[int]*spsEntry),
		pps: make(map[int]*ppsEntry),
	}
}

// ParseSPS decodes one SPS NAL unit and stores it under its id.
func (t *ParamSetTracker) ParseSPS(nal []byte) (SPSInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rbsp, err := nalPayload(nal, nalTypeSPS)
	if err != nil {
		return SPSInfo{}, ErrBadSPS
	}
	info, content, err := parseSPS(rbsp)
	if err != nil {
		return SPSInfo{}, ErrBadSPS
	}

	t.ensureMapsLocked()
	existing, ok := t.sps[info.ID]
	if ok {
		if bytes.Equal(existing.content, content) {
			existing.lastUsed = t.nextUseLocked()
			return existing.info, nil
		}
		info.Version = existing.info.Version + 1
		existing.info = info
		existing.content = content
		existing.lastUsed = t.nextUseLocked()
		return info, nil
	}

	if !t.makeRoomLocked() {
		return SPSInfo{}, ErrBadSPS
	}
	info.Version = 1
	t.sps[info.ID] = &spsEntry{
		info:     info,
		content:  content,
		lastUsed: t.nextUseLocked(),
	}
	return info, nil
}

// ParsePPS decodes one PPS NAL unit and stores it under its id.
func (t *ParamSetTracker) ParsePPS(nal []byte) (PPSInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rbsp, err := nalPayload(nal, nalTypePPS)
	if err != nil {
		return PPSInfo{}, ErrBadPPS
	}
	info, content, err := parsePPS(rbsp)
	if err != nil {
		return PPSInfo{}, ErrBadPPS
	}

	t.ensureMapsLocked()
	existing, ok := t.pps[info.ID]
	if ok {
		if bytes.Equal(existing.content, content) {
			existing.lastUsed = t.nextUseLocked()
			return existing.info, nil
		}
		info.Version = existing.info.Version + 1
		existing.info = info
		existing.content = content
		existing.lastUsed = t.nextUseLocked()
		return info, nil
	}

	if !t.makeRoomLocked() {
		return PPSInfo{}, ErrBadPPS
	}
	info.Version = 1
	t.pps[info.ID] = &ppsEntry{
		info:     info,
		content:  content,
		lastUsed: t.nextUseLocked(),
	}
	return info, nil
}

// Activate makes the SPS referenced by the given PPS the current parameter set.
func (t *ParamSetTracker) Activate(ppsID int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ensureMapsLocked()
	pps, ok := t.pps[ppsID]
	if !ok {
		return ErrUnknownPPS
	}
	sps, ok := t.sps[pps.info.SPSID]
	if !ok {
		return ErrUnknownSPS
	}

	changed := false
	if t.hasActive {
		changed = sps.info.Width != t.activeSPS.Width ||
			sps.info.Height != t.activeSPS.Height
	}

	t.activeSPS = sps.info
	t.activePPS = pps.info
	t.hasActive = true
	t.resolutionChanged = changed
	t.lastWidth = sps.info.Width
	t.lastHeight = sps.info.Height

	use := t.nextUseLocked()
	sps.lastUsed = use
	pps.lastUsed = use
	return nil
}

// ActiveSPS returns the currently active sequence parameter set.
func (t *ParamSetTracker) ActiveSPS() (SPSInfo, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasActive {
		return SPSInfo{}, false
	}
	return t.activeSPS, true
}

// ActivePPS returns the currently active picture parameter set.
func (t *ParamSetTracker) ActivePPS() (PPSInfo, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasActive {
		return PPSInfo{}, false
	}
	return t.activePPS, true
}

// ResolutionChanged reports whether the last activation changed the frame size.
func (t *ParamSetTracker) ResolutionChanged() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resolutionChanged
}

// SPSVersion reports the stored revision of one SPS id.
func (t *ParamSetTracker) SPSVersion(id int) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.sps[id]
	if !ok {
		return 0, false
	}
	return entry.info.Version, true
}

// PPSVersion reports the stored revision of one PPS id.
func (t *ParamSetTracker) PPSVersion(id int) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.pps[id]
	if !ok {
		return 0, false
	}
	return entry.info.Version, true
}

// Dimensions returns the active frame size, or zeroes when nothing is active.
func (t *ParamSetTracker) Dimensions() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasActive {
		return 0, 0
	}
	return t.activeSPS.Width, t.activeSPS.Height
}

// Reset clears every parsed set and the activation state.
func (t *ParamSetTracker) Reset() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sps = make(map[int]*spsEntry)
	t.pps = make(map[int]*ppsEntry)
	t.activeSPS = SPSInfo{}
	t.activePPS = PPSInfo{}
	t.hasActive = false
	t.resolutionChanged = false
	t.lastWidth = 0
	t.lastHeight = 0
	t.useTick = 0
	return nil
}

const (
	snapshotMagic   = "PSST1"
	snapshotHeader  = len(snapshotMagic) + 4
	snapshotTrailer = 4
)

type snapshotSPS struct {
	Info     SPSInfo `json:"info"`
	Content  []byte  `json:"content"`
	LastUsed uint64  `json:"last_used"`
}

type snapshotPPS struct {
	Info     PPSInfo `json:"info"`
	Content  []byte  `json:"content"`
	LastUsed uint64  `json:"last_used"`
}

type snapshotData struct {
	SPS               []snapshotSPS `json:"sps"`
	PPS               []snapshotPPS `json:"pps"`
	HasActive         bool          `json:"has_active"`
	ActiveSPS         SPSInfo       `json:"active_sps"`
	ActivePPS         PPSInfo       `json:"active_pps"`
	ResolutionChanged bool          `json:"resolution_changed"`
	LastWidth         int           `json:"last_width"`
	LastHeight        int           `json:"last_height"`
	UseTick           uint64        `json:"use_tick"`
}

// Snapshot serialises the parsed sets, versions and activation state.
func (t *ParamSetTracker) Snapshot() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := snapshotData{
		SPS:               make([]snapshotSPS, 0, len(t.sps)),
		PPS:               make([]snapshotPPS, 0, len(t.pps)),
		HasActive:         t.hasActive,
		ActiveSPS:         t.activeSPS,
		ActivePPS:         t.activePPS,
		ResolutionChanged: t.resolutionChanged,
		LastWidth:         t.lastWidth,
		LastHeight:        t.lastHeight,
		UseTick:           t.useTick,
	}

	spsIDs := make([]int, 0, len(t.sps))
	for id := range t.sps {
		spsIDs = append(spsIDs, id)
	}
	sort.Ints(spsIDs)
	for _, id := range spsIDs {
		entry := t.sps[id]
		snap.SPS = append(snap.SPS, snapshotSPS{
			Info:     entry.info,
			Content:  append([]byte(nil), entry.content...),
			LastUsed: entry.lastUsed,
		})
	}

	ppsIDs := make([]int, 0, len(t.pps))
	for id := range t.pps {
		ppsIDs = append(ppsIDs, id)
	}
	sort.Ints(ppsIDs)
	for _, id := range ppsIDs {
		entry := t.pps[id]
		snap.PPS = append(snap.PPS, snapshotPPS{
			Info:     entry.info,
			Content:  append([]byte(nil), entry.content...),
			LastUsed: entry.lastUsed,
		})
	}

	payload, err := json.Marshal(snap)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return nil, ErrBadSnapshot
	}

	out := make([]byte, 0, snapshotHeader+len(payload)+snapshotTrailer)
	out = append(out, snapshotMagic...)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	out = append(out, length[:]...)
	out = append(out, payload...)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc32.ChecksumIEEE(out))
	out = append(out, sum[:]...)
	return out, nil
}

// Restore replaces the whole tracker state from a snapshot, all or nothing.
func (t *ParamSetTracker) Restore(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	snap, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	newSPS := make(map[int]*spsEntry, len(snap.SPS))
	for _, stored := range snap.SPS {
		if err := validateSPSInfo(stored.Info); err != nil {
			return ErrBadSnapshot
		}
		if len(stored.Content) == 0 {
			return ErrBadSnapshot
		}
		if _, exists := newSPS[stored.Info.ID]; exists {
			return ErrBadSnapshot
		}
		newSPS[stored.Info.ID] = &spsEntry{
			info:     stored.Info,
			content:  append([]byte(nil), stored.Content...),
			lastUsed: stored.LastUsed,
		}
	}

	newPPS := make(map[int]*ppsEntry, len(snap.PPS))
	for _, stored := range snap.PPS {
		if err := validatePPSInfo(stored.Info); err != nil {
			return ErrBadSnapshot
		}
		if len(stored.Content) == 0 {
			return ErrBadSnapshot
		}
		if _, exists := newPPS[stored.Info.ID]; exists {
			return ErrBadSnapshot
		}
		newPPS[stored.Info.ID] = &ppsEntry{
			info:     stored.Info,
			content:  append([]byte(nil), stored.Content...),
			lastUsed: stored.LastUsed,
		}
	}

	if len(newSPS)+len(newPPS) > maxParamSets {
		return ErrBadSnapshot
	}
	if snap.LastWidth < 0 || snap.LastWidth > maxDimension ||
		snap.LastHeight < 0 || snap.LastHeight > maxDimension {
		return ErrBadSnapshot
	}

	if snap.HasActive {
		if err := validateSPSInfo(snap.ActiveSPS); err != nil {
			return ErrBadSnapshot
		}
		if err := validatePPSInfo(snap.ActivePPS); err != nil {
			return ErrBadSnapshot
		}
		if snap.ActivePPS.SPSID != snap.ActiveSPS.ID {
			return ErrBadSnapshot
		}
		if _, ok := newSPS[snap.ActiveSPS.ID]; !ok {
			return ErrBadSnapshot
		}
		if _, ok := newPPS[snap.ActivePPS.ID]; !ok {
			return ErrBadSnapshot
		}
	}

	maxUse := snap.UseTick
	for _, entry := range newSPS {
		if entry.lastUsed > maxUse {
			maxUse = entry.lastUsed
		}
	}
	for _, entry := range newPPS {
		if entry.lastUsed > maxUse {
			maxUse = entry.lastUsed
		}
	}

	t.sps = newSPS
	t.pps = newPPS
	t.hasActive = snap.HasActive
	t.activeSPS = snap.ActiveSPS
	t.activePPS = snap.ActivePPS
	t.resolutionChanged = snap.ResolutionChanged
	t.lastWidth = snap.LastWidth
	t.lastHeight = snap.LastHeight
	t.useTick = maxUse
	return nil
}

func decodeSnapshot(data []byte) (snapshotData, error) {
	var snap snapshotData
	if len(data) < snapshotHeader+snapshotTrailer {
		return snap, ErrBadSnapshot
	}
	if string(data[:len(snapshotMagic)]) != snapshotMagic {
		return snap, ErrBadSnapshot
	}

	length := binary.BigEndian.Uint32(data[len(snapshotMagic):snapshotHeader])
	payloadStart := snapshotHeader
	payloadEnd := payloadStart + int(length)
	if payloadEnd < payloadStart || payloadEnd+snapshotTrailer != len(data) {
		return snap, ErrBadSnapshot
	}

	wantSum := binary.BigEndian.Uint32(data[len(data)-snapshotTrailer:])
	if crc32.ChecksumIEEE(data[:len(data)-snapshotTrailer]) != wantSum {
		return snap, ErrBadSnapshot
	}

	if err := json.Unmarshal(data[payloadStart:payloadEnd], &snap); err != nil {
		return snap, ErrBadSnapshot
	}
	return snap, nil
}

func validateSPSInfo(info SPSInfo) error {
	if info.ID < 0 || info.ID > maxParamSetID {
		return ErrBadSnapshot
	}
	if info.ProfileIDC < 0 || info.ProfileIDC > 0xff ||
		info.LevelIDC < 0 || info.LevelIDC > 0xff {
		return ErrBadSnapshot
	}
	if info.Version < 1 || info.NumRefFrames < 0 {
		return ErrBadSnapshot
	}
	if info.Width <= 0 || info.Width > maxDimension ||
		info.Height <= 0 || info.Height > maxDimension {
		return ErrBadSnapshot
	}
	return nil
}

func validatePPSInfo(info PPSInfo) error {
	if info.ID < 0 || info.ID > maxParamSetID {
		return ErrBadSnapshot
	}
	if info.SPSID < 0 || info.SPSID > maxParamSetID {
		return ErrBadSnapshot
	}
	if info.Version < 1 {
		return ErrBadSnapshot
	}
	return nil
}

func (t *ParamSetTracker) ensureMapsLocked() {
	if t.sps == nil {
		t.sps = make(map[int]*spsEntry)
	}
	if t.pps == nil {
		t.pps = make(map[int]*ppsEntry)
	}
}

func (t *ParamSetTracker) nextUseLocked() uint64 {
	t.useTick++
	return t.useTick
}

func (t *ParamSetTracker) makeRoomLocked() bool {
	for len(t.sps)+len(t.pps) >= maxParamSets {
		if !t.evictOldestLocked() {
			return false
		}
	}
	return true
}

// evictOldestLocked removes the least recently used set that is not currently
// active. SPS entries win ties because they are considered before PPS entries.
func (t *ParamSetTracker) evictOldestLocked() bool {
	kind := byte(0)
	id := 0
	var oldest uint64
	found := false

	for candidateID, entry := range t.sps {
		if t.hasActive && t.activeSPS.ID == candidateID {
			continue
		}
		if !found || entry.lastUsed < oldest ||
			(entry.lastUsed == oldest && candidateID < id) {
			kind = 's'
			id = candidateID
			oldest = entry.lastUsed
			found = true
		}
	}
	for candidateID, entry := range t.pps {
		if t.hasActive && t.activePPS.ID == candidateID {
			continue
		}
		if !found || entry.lastUsed < oldest ||
			(entry.lastUsed == oldest && kind == 'p' && candidateID < id) {
			kind = 'p'
			id = candidateID
			oldest = entry.lastUsed
			found = true
		}
	}
	if !found {
		return false
	}
	if kind == 's' {
		delete(t.sps, id)
	} else {
		delete(t.pps, id)
	}
	return true
}

func nalPayload(nal []byte, wantType byte) ([]byte, error) {
	if len(nal) == 0 {
		return nil, errors.New("empty nal")
	}
	if nal[0]&0x80 != 0 {
		return nil, errors.New("forbidden_zero_bit set")
	}
	if nal[0]&0x1f != wantType {
		return nil, errors.New("unexpected nal type")
	}
	return removeEmulationPrevention(nal[1:]), nil
}

func removeEmulationPrevention(in []byte) []byte {
	out := make([]byte, 0, len(in))
	zeros := 0
	for _, b := range in {
		if zeros >= 2 && b == 0x03 {
			zeros = 0
			continue
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type bitReader struct {
	data   []byte
	bitPos uint64
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

func (r *bitReader) readBit() (uint32, error) {
	if r.bitPos >= uint64(len(r.data))*8 {
		return 0, errors.New("truncated bitstream")
	}
	byteIndex := r.bitPos / 8
	bitIndex := uint(7 - r.bitPos%8)
	r.bitPos++
	return uint32((r.data[byteIndex] >> bitIndex) & 1), nil
}

func (r *bitReader) readBits(n uint) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	if n > 64 {
		return 0, errors.New("invalid bit count")
	}
	if r.bitPos+uint64(n) > uint64(len(r.data))*8 {
		return 0, errors.New("truncated bitstream")
	}
	var value uint64
	for i := uint(0); i < n; i++ {
		byteIndex := r.bitPos / 8
		bitIndex := uint(7 - r.bitPos%8)
		value = value<<1 | uint64((r.data[byteIndex]>>bitIndex)&1)
		r.bitPos++
	}
	return value, nil
}

func (r *bitReader) readUE() (uint64, error) {
	leadingZeros := uint(0)
	for {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			break
		}
		leadingZeros++
		if leadingZeros > 31 {
			return 0, errors.New("exp-golomb value too large")
		}
	}
	if leadingZeros == 0 {
		return 0, nil
	}
	suffix, err := r.readBits(leadingZeros)
	if err != nil {
		return 0, err
	}
	return (uint64(1) << leadingZeros) + suffix - 1, nil
}

func (r *bitReader) readSE() (int64, error) {
	value, err := r.readUE()
	if err != nil {
		return 0, err
	}
	if value&1 == 1 {
		return int64((value + 1) / 2), nil
	}
	return -int64(value / 2), nil
}

func parseSPS(rbsp []byte) (SPSInfo, []byte, error) {
	r := newBitReader(rbsp)

	profile, err := r.readBits(8)
	if err != nil {
		return SPSInfo{}, nil, err
	}
	if _, err = r.readBits(8); err != nil {
		return SPSInfo{}, nil, err
	}
	level, err := r.readBits(8)
	if err != nil {
		return SPSInfo{}, nil, err
	}

	id, err := r.readUE()
	if err != nil || id > maxParamSetID {
		return SPSInfo{}, nil, errors.New("invalid sps id")
	}
	if _, err = r.readUE(); err != nil {
		return SPSInfo{}, nil, err
	}

	pocType, err := r.readUE()
	if err != nil {
		return SPSInfo{}, nil, err
	}
	switch pocType {
	case 0:
		if _, err = r.readUE(); err != nil {
			return SPSInfo{}, nil, err
		}
	case 1:
		if _, err = r.readBit(); err != nil {
			return SPSInfo{}, nil, err
		}
		if _, err = r.readSE(); err != nil {
			return SPSInfo{}, nil, err
		}
		if _, err = r.readSE(); err != nil {
			return SPSInfo{}, nil, err
		}
		cycleLength, err := r.readUE()
		if err != nil {
			return SPSInfo{}, nil, err
		}
		if cycleLength > uint64(len(rbsp))*8 {
			return SPSInfo{}, nil, errors.New("invalid poc cycle")
		}
		for i := uint64(0); i < cycleLength; i++ {
			if _, err = r.readSE(); err != nil {
				return SPSInfo{}, nil, err
			}
		}
	case 2:
	default:
		return SPSInfo{}, nil, errors.New("invalid pic_order_cnt_type")
	}

	numRefFrames, err := r.readUE()
	if err != nil {
		return SPSInfo{}, nil, err
	}
	if numRefFrames > uint64(^uint(0)>>1) {
		return SPSInfo{}, nil, errors.New("num_ref_frames too large")
	}

	if _, err = r.readBit(); err != nil {
		return SPSInfo{}, nil, err
	}
	widthMBsMinus1, err := r.readUE()
	if err != nil {
		return SPSInfo{}, nil, err
	}
	heightMapUnitsMinus1, err := r.readUE()
	if err != nil {
		return SPSInfo{}, nil, err
	}

	frameMBSOnlyBit, err := r.readBit()
	if err != nil {
		return SPSInfo{}, nil, err
	}
	frameMBSOnly := frameMBSOnlyBit == 1
	if !frameMBSOnly {
		if _, err = r.readBit(); err != nil {
			return SPSInfo{}, nil, err
		}
	}
	if _, err = r.readBit(); err != nil {
		return SPSInfo{}, nil, err
	}

	cropping, err := r.readBit()
	if err != nil {
		return SPSInfo{}, nil, err
	}
	var cropLeft, cropRight, cropTop, cropBottom uint64
	if cropping == 1 {
		if cropLeft, err = r.readUE(); err != nil {
			return SPSInfo{}, nil, err
		}
		if cropRight, err = r.readUE(); err != nil {
			return SPSInfo{}, nil, err
		}
		if cropTop, err = r.readUE(); err != nil {
			return SPSInfo{}, nil, err
		}
		if cropBottom, err = r.readUE(); err != nil {
			return SPSInfo{}, nil, err
		}
	}

	// vui_parameters_present_flag is the last field needed to validate that the
	// fixed part of the SPS is present. VUI contents do not alter dimensions.
	vuiPresent, err := r.readBit()
	if err != nil {
		return SPSInfo{}, nil, err
	}
	if r.bitPos >= uint64(len(rbsp))*8 {
		return SPSInfo{}, nil, errors.New("truncated rbsp trailing bits")
	}

	widthMBs := widthMBsMinus1 + 1
	heightMapUnits := heightMapUnitsMinus1 + 1
	rawWidth := widthMBs * 16
	heightFactor := uint64(1)
	if !frameMBSOnly {
		heightFactor = 2
	}
	rawHeight := heightMapUnits * heightFactor * 16

	// The stream is treated as 4:2:0. Progressive frames crop in units of two
	// pixels; interlaced frames use four rows per unit.
	cropUnitX := uint64(2)
	cropUnitY := uint64(2)
	if !frameMBSOnly {
		cropUnitY = 4
	}

	cropX := (cropLeft + cropRight) * cropUnitX
	cropY := (cropTop + cropBottom) * cropUnitY
	if cropX >= rawWidth || cropY >= rawHeight {
		return SPSInfo{}, nil, errors.New("cropping exceeds frame")
	}

	width := rawWidth - cropX
	height := rawHeight - cropY
	if width == 0 || height == 0 || width > maxDimension || height > maxDimension {
		return SPSInfo{}, nil, errors.New("invalid dimensions")
	}
	if width > uint64(^uint(0)>>1) || height > uint64(^uint(0)>>1) {
		return SPSInfo{}, nil, errors.New("dimensions overflow")
	}

	content := append([]byte(nil), rbsp...)
	if vuiPresent == 0 {
		content = canonicalBits(rbsp, r.bitPos)
	}
	return SPSInfo{
		ID:           int(id),
		ProfileIDC:   int(profile),
		LevelIDC:     int(level),
		Width:        int(width),
		Height:       int(height),
		FrameMBSOnly: frameMBSOnly,
		NumRefFrames: int(numRefFrames),
	}, content, nil
}

func parsePPS(rbsp []byte) (PPSInfo, []byte, error) {
	r := newBitReader(rbsp)

	id, err := r.readUE()
	if err != nil || id > maxParamSetID {
		return PPSInfo{}, nil, errors.New("invalid pps id")
	}
	spsID, err := r.readUE()
	if err != nil || spsID > maxParamSetID {
		return PPSInfo{}, nil, errors.New("invalid sps id")
	}

	entropy, err := r.readBit()
	if err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readBit(); err != nil {
		return PPSInfo{}, nil, err
	}

	numSliceGroupsMinus1, err := r.readUE()
	if err != nil {
		return PPSInfo{}, nil, err
	}
	if numSliceGroupsMinus1 > 7 {
		return PPSInfo{}, nil, errors.New("too many slice groups")
	}
	if numSliceGroupsMinus1 > 0 {
		if err := skipSliceGroupMap(r, numSliceGroupsMinus1); err != nil {
			return PPSInfo{}, nil, err
		}
	}

	if _, err = r.readUE(); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readUE(); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readBit(); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readBits(2); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readSE(); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readSE(); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readSE(); err != nil {
		return PPSInfo{}, nil, err
	}

	deblocking, err := r.readBit()
	if err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readBit(); err != nil {
		return PPSInfo{}, nil, err
	}
	if _, err = r.readBit(); err != nil {
		return PPSInfo{}, nil, err
	}

	// Everything after redundant_pic_cnt_present_flag is an extension and is
	// deliberately ignored.
	if r.bitPos >= uint64(len(rbsp))*8 {
		return PPSInfo{}, nil, errors.New("truncated rbsp trailing bits")
	}
	return PPSInfo{
		ID:            int(id),
		SPSID:         int(spsID),
		EntropyCoding: entropy == 1,
		Deblocking:    deblocking == 1,
	}, canonicalBits(rbsp, r.bitPos), nil
}

func canonicalBits(data []byte, bits uint64) []byte {
	if bits == 0 {
		return nil
	}
	n := int((bits + 7) / 8)
	if n > len(data) {
		n = len(data)
	}
	out := append([]byte(nil), data[:n]...)
	if rem := uint(bits % 8); rem != 0 && len(out) > 0 {
		out[len(out)-1] &= byte(0xff << (8 - rem))
	}
	return out
}

func skipSliceGroupMap(r *bitReader, numSliceGroupsMinus1 uint64) error {
	mapType, err := r.readUE()
	if err != nil {
		return err
	}
	switch mapType {
	case 0:
		for i := uint64(0); i <= numSliceGroupsMinus1; i++ {
			if _, err := r.readUE(); err != nil {
				return err
			}
		}
	case 2:
		for i := uint64(0); i < numSliceGroupsMinus1; i++ {
			if _, err := r.readUE(); err != nil {
				return err
			}
			if _, err := r.readUE(); err != nil {
				return err
			}
		}
	case 3, 4, 5:
		if _, err := r.readBit(); err != nil {
			return err
		}
		if _, err := r.readUE(); err != nil {
			return err
		}
	case 6:
		picSizeMinus1, err := r.readUE()
		if err != nil {
			return err
		}
		count := picSizeMinus1 + 1
		groupCount := numSliceGroupsMinus1 + 1
		var bits uint
		for (uint64(1) << bits) < groupCount {
			bits++
		}
		if bits == 0 {
			return errors.New("invalid slice group map")
		}
		if count > uint64(len(r.data))*8 {
			return errors.New("slice group map truncated")
		}
		for i := uint64(0); i < count; i++ {
			if _, err := r.readBits(bits); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid slice group map type")
	}
	return nil
}
