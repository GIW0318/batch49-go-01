package task

import (
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"sync"
)

// 公开错误，供调用方按类别判断失败原因。
var (
	ErrInvalidPPM     = errors.New("drop_rate_modifier: ppm must not be negative")
	ErrInvalidSource  = errors.New("drop_rate_modifier: player, item, kind and source id are required")
	ErrSourceNotFound = errors.New("drop_rate_modifier: source not found")
)

const (
	defaultCapPPM = 1_000_000
	ppmScale      = 1_000_000
)

// Options 是构造掉率修正器的配置。
type Options struct {
	Cap int
}

// Contribution 是一个来源对最终概率的贡献明细。
type Contribution struct {
	Kind     string
	SourceID string
	PPM      int
	Applied  int
	Expired  bool
}

// DropRateModifier 按玩家与物品管理掉率来源。
type DropRateModifier struct {
	mu      sync.Mutex
	opts    Options
	base    map[string]int
	sources map[string]map[string]*sourceEntry
}

type sourceEntry struct {
	itemID   string
	kind     string
	sourceID string
	ppm      int
	until    int64
}

type snapshotData struct {
	Cap     int                                  `json:"cap"`
	Base    map[string]int                       `json:"base"`
	Sources map[string]map[string]sourceSnapshot `json:"sources"`
}

type sourceSnapshot struct {
	ItemID string `json:"item_id"`
	Kind   string `json:"kind"`
	PPM    int    `json:"ppm"`
	Until  int64  `json:"until"`
}

// NewDropRateModifier 构造掉率修正器。
func NewDropRateModifier(opts Options) *DropRateModifier {
	return &DropRateModifier{
		opts:    opts,
		base:    make(map[string]int),
		sources: make(map[string]map[string]*sourceEntry),
	}
}

// Cap 返回最终概率硬顶。
func (m *DropRateModifier) Cap() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capLocked()
}

func (m *DropRateModifier) capLocked() int {
	if m.opts.Cap > 0 {
		return m.opts.Cap
	}
	return defaultCapPPM
}

// SetBase 设置某物品的基础概率。
func (m *DropRateModifier) SetBase(itemID string, ppm int) error {
	if ppm < 0 {
		return ErrInvalidPPM
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.base == nil {
		m.base = make(map[string]int)
	}
	m.base[itemID] = ppm
	return nil
}

// Base 返回某物品的基础概率。
func (m *DropRateModifier) Base(itemID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base[itemID], nil
}

// AddSource 为玩家增加一个掉率来源。
func (m *DropRateModifier) AddSource(player, itemID, kind, sourceID string, ppm int, until int64) error {
	if player == "" || itemID == "" || kind == "" || sourceID == "" {
		return ErrInvalidSource
	}
	if ppm < 0 {
		return ErrInvalidPPM
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sources == nil {
		m.sources = make(map[string]map[string]*sourceEntry)
	}
	playerSources, ok := m.sources[player]
	if !ok {
		playerSources = make(map[string]*sourceEntry)
		m.sources[player] = playerSources
	}
	playerSources[sourceID] = &sourceEntry{
		itemID:   itemID,
		kind:     kind,
		sourceID: sourceID,
		ppm:      ppm,
		until:    until,
	}
	return nil
}

// RemoveSource 删除玩家名下的来源。
func (m *DropRateModifier) RemoveSource(player, sourceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	playerSources := m.sources[player]
	if playerSources == nil {
		return ErrSourceNotFound
	}
	if _, ok := playerSources[sourceID]; !ok {
		return ErrSourceNotFound
	}

	delete(playerSources, sourceID)
	if len(playerSources) == 0 {
		delete(m.sources, player)
	}
	return nil
}

// SourceCount 返回玩家当前的来源条数。
func (m *DropRateModifier) SourceCount(player string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sources[player])
}

// Effective 计算给定时刻的最终概率。
func (m *DropRateModifier) Effective(player, itemID string, at int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	base := m.base[itemID]
	capPPM := m.capLocked()
	entries := m.itemSourcesLocked(player, itemID)
	value := calculateEffectivePPM(base, entries, at)

	capValue := big.NewInt(int64(capPPM))
	if value.Cmp(capValue) > 0 {
		return capPPM, nil
	}
	return int(value.Int64()), nil
}

// Breakdown 返回给定时刻的贡献明细。
func (m *DropRateModifier) Breakdown(player, itemID string, at int64) []Contribution {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries := m.itemSourcesLocked(player, itemID)
	contributions := make([]Contribution, 0, len(entries))
	ranks := make(map[string]int)

	for _, entry := range entries {
		expired := sourceExpired(entry.until, at)
		applied := 0
		if !expired {
			applied = entry.ppm / (ranks[entry.kind] + 1)
			ranks[entry.kind]++
		}

		contributions = append(contributions, Contribution{
			Kind:     entry.kind,
			SourceID: entry.sourceID,
			PPM:      entry.ppm,
			Applied:  applied,
			Expired:  expired,
		})
	}
	return contributions
}

// Snapshot 序列化基础表与全部来源。
func (m *DropRateModifier) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data := snapshotData{
		Cap:     m.capLocked(),
		Base:    make(map[string]int, len(m.base)),
		Sources: make(map[string]map[string]sourceSnapshot, len(m.sources)),
	}
	for itemID, ppm := range m.base {
		data.Base[itemID] = ppm
	}
	for player, sources := range m.sources {
		copied := make(map[string]sourceSnapshot, len(sources))
		for sourceID, entry := range sources {
			copied[sourceID] = sourceSnapshot{
				ItemID: entry.itemID,
				Kind:   entry.kind,
				PPM:    entry.ppm,
				Until:  entry.until,
			}
		}
		data.Sources[player] = copied
	}
	return json.Marshal(data)
}

// Restore 从快照恢复基础表与来源。
func (m *DropRateModifier) Restore(data []byte) error {
	var snapshot snapshotData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}

	base := make(map[string]int, len(snapshot.Base))
	for itemID, ppm := range snapshot.Base {
		if ppm < 0 {
			return ErrInvalidPPM
		}
		base[itemID] = ppm
	}

	sources := make(map[string]map[string]*sourceEntry, len(snapshot.Sources))
	for player, playerSources := range snapshot.Sources {
		if player == "" {
			return ErrInvalidSource
		}
		restored := make(map[string]*sourceEntry, len(playerSources))
		for sourceID, source := range playerSources {
			if sourceID == "" || source.ItemID == "" || source.Kind == "" {
				return ErrInvalidSource
			}
			if source.PPM < 0 {
				return ErrInvalidPPM
			}
			restored[sourceID] = &sourceEntry{
				itemID:   source.ItemID,
				kind:     source.Kind,
				sourceID: sourceID,
				ppm:      source.PPM,
				until:    source.Until,
			}
		}
		sources[player] = restored
	}

	capPPM := snapshot.Cap
	if capPPM <= 0 {
		capPPM = defaultCapPPM
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.base = base
	m.sources = sources
	m.opts.Cap = capPPM
	return nil
}

func (m *DropRateModifier) itemSourcesLocked(player, itemID string) []sourceEntry {
	var entries []sourceEntry
	for _, entry := range m.sources[player] {
		if entry.itemID == itemID {
			entries = append(entries, *entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].kind != entries[j].kind {
			return entries[i].kind < entries[j].kind
		}
		return entries[i].sourceID < entries[j].sourceID
	})
	return entries
}

func sourceExpired(until, at int64) bool {
	return until != 0 && at >= until
}

func calculateEffectivePPM(base int, entries []sourceEntry, at int64) *big.Int {
	type category struct {
		sum  *big.Int
		rank int
	}

	categories := make(map[string]*category)
	kinds := make([]string, 0)
	for _, entry := range entries {
		if sourceExpired(entry.until, at) {
			continue
		}

		current := categories[entry.kind]
		if current == nil {
			current = &category{sum: new(big.Int)}
			categories[entry.kind] = current
			kinds = append(kinds, entry.kind)
		}

		applied := entry.ppm / (current.rank + 1)
		current.rank++
		current.sum.Add(current.sum, big.NewInt(int64(applied)))
	}

	sort.Strings(kinds)
	result := big.NewInt(int64(base))
	scale := big.NewInt(ppmScale)
	for _, kind := range kinds {
		factor := new(big.Int).Add(scale, categories[kind].sum)
		result.Mul(result, factor)
		result.Div(result, scale)
	}
	return result
}
