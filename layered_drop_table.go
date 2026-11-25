package task

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"math/rand/v2"
	"sync"
)

// 公开错误，供调用方按类别判断失败原因。
var (
	ErrLayerExists   = errors.New("layered_drop_table: layer id already exists")
	ErrLayerNotFound = errors.New("layered_drop_table: layer not found")
	ErrInvalidCond   = errors.New("layered_drop_table: unknown layer condition kind")
	ErrInvalidWeight = errors.New("layered_drop_table: weight must not be negative")
	ErrInvalidEntry  = errors.New("layered_drop_table: entry id is required and count range must be valid")
	ErrEntryNotFound = errors.New("layered_drop_table: entry not found in effective table")
)

// Options 是构造分层掉落表的配置。
type Options struct {
	GroupLimit int
}

// LayerCond 声明一层生效的条件。
type LayerCond struct {
	Kind       string
	Difficulty string
}

// Entry 是掉落表里的一条条目。
type Entry struct {
	ID       string
	Weight   int
	MinCount int
	MaxCount int
	Group    string
}

// Context 是一次抽取所处的击杀上下文。
type Context struct {
	Difficulty string
	FirstKill  bool
}

// Table 是叠加后的有效掉落表。
type Table struct {
	Entries     []Entry
	TotalWeight int
}

// DropItem 是一次抽取里落出的单件。
type DropItem struct {
	EntryID string
	Count   int
	Group   string
}

// Drop 是一次抽取的完整结果。
type Drop struct {
	Seed  uint64
	Items []DropItem
}

// LayeredDropTable 管理多层掉落表及其叠加结果。
type LayeredDropTable struct {
	mu     sync.RWMutex
	opts   Options
	layers []layer
}

type layer struct {
	id      string
	cond    LayerCond
	entries []Entry
}

// NewLayeredDropTable 构造分层掉落表。
func NewLayeredDropTable(opts Options) *LayeredDropTable {
	return &LayeredDropTable{opts: opts}
}

// AddLayer 追加一层掉落表。
func (t *LayeredDropTable) AddLayer(id string, cond LayerCond, entries []Entry) error {
	if !validCond(cond) {
		return ErrInvalidCond
	}
	loaded := make([]Entry, len(entries))
	for index, entry := range entries {
		if entry.Weight < 0 {
			return ErrInvalidWeight
		}
		if entry.ID == "" || entry.MaxCount < entry.MinCount {
			return ErrInvalidEntry
		}
		loaded[index] = entry
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.layers {
		if existing.id == id {
			return ErrLayerExists
		}
	}
	t.layers = append(t.layers, layer{id: id, cond: cond, entries: loaded})
	return nil
}

// RemoveLayer 删除指定层。
func (t *LayeredDropTable) RemoveLayer(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for index, existing := range t.layers {
		if existing.id == id {
			t.layers = append(t.layers[:index], t.layers[index+1:]...)
			return nil
		}
	}
	return ErrLayerNotFound
}

// LayerIDs 按加入顺序返回层 ID。
func (t *LayeredDropTable) LayerIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ids := make([]string, len(t.layers))
	for index, existing := range t.layers {
		ids[index] = existing.id
	}
	return ids
}

// Effective 返回给定上下文下的有效表。
func (t *LayeredDropTable) Effective(ctx Context) (Table, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	table := effectiveLocked(t.layers, ctx)
	table.Entries = append([]Entry(nil), table.Entries...)
	return table, nil
}

// EntryCount 返回有效表的条目数。
func (t *LayeredDropTable) EntryCount(ctx Context) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(effectiveLocked(t.layers, ctx).Entries), nil
}

// TotalWeight 返回有效表的权重总和。
func (t *LayeredDropTable) TotalWeight(ctx Context) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return effectiveLocked(t.layers, ctx).TotalWeight, nil
}

// Roll 按种子抽取一次掉落。
func (t *LayeredDropTable) Roll(ctx Context, seed uint64) (Drop, error) {
	t.mu.RLock()
	table := effectiveLocked(t.layers, ctx)
	groupLimit := t.opts.GroupLimit
	t.mu.RUnlock()

	type slot struct {
		group   string
		entries []Entry
	}
	slots := make([]slot, 0)
	slotIndex := make(map[string]int)
	for _, entry := range table.Entries {
		index, ok := slotIndex[entry.Group]
		if !ok {
			index = len(slots)
			slotIndex[entry.Group] = index
			slots = append(slots, slot{group: entry.Group})
		}
		slots[index].entries = append(slots[index].entries, entry)
	}
	if groupLimit > 0 && len(slots) > groupLimit {
		slots = slots[:groupLimit]
	}

	items := make([]DropItem, 0, len(slots))
	for _, candidate := range slots {
		weightTotal := 0
		for _, entry := range candidate.entries {
			weightTotal += entry.Weight
		}
		if weightTotal == 0 {
			continue
		}
		rng := rand.New(rand.NewPCG(mixSeed(seed, candidate.group), mixSeed(seed^0x9e3779b97f4a7c15, candidate.group)))
		target := rng.IntN(weightTotal)
		cumulative := 0
		for _, entry := range candidate.entries {
			cumulative += entry.Weight
			if target < cumulative {
				count := entry.MinCount
				if span := entry.MaxCount - entry.MinCount + 1; span > 1 {
					count += rng.IntN(span)
				}
				items = append(items, DropItem{
					EntryID: entry.ID,
					Count:   count,
					Group:   entry.Group,
				})
				break
			}
		}
	}

	return Drop{Seed: seed, Items: items}, nil
}

// Explain 返回某条目在有效表里的权重。
func (t *LayeredDropTable) Explain(ctx Context, entryID string) (int, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, entry := range effectiveLocked(t.layers, ctx).Entries {
		if entry.ID == entryID {
			return entry.Weight, nil
		}
	}
	return 0, ErrEntryNotFound
}

// Groups 返回互斥组到条目 ID 的映射。
func (t *LayeredDropTable) Groups(ctx Context) map[string][]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	groups := make(map[string][]string)
	for _, entry := range effectiveLocked(t.layers, ctx).Entries {
		groups[entry.Group] = append(groups[entry.Group], entry.ID)
	}
	return groups
}

type snapshotLayer struct {
	ID      string    `json:"id"`
	Cond    LayerCond `json:"cond"`
	Entries []Entry   `json:"entries"`
}

type snapshotPayload struct {
	Opts   Options         `json:"opts"`
	Layers []snapshotLayer `json:"layers"`
}

// Snapshot 序列化全部层。
func (t *LayeredDropTable) Snapshot() ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	payload := snapshotPayload{Opts: t.opts, Layers: make([]snapshotLayer, 0, len(t.layers))}
	for _, existing := range t.layers {
		payload.Layers = append(payload.Layers, snapshotLayer{
			ID:      existing.id,
			Cond:    existing.cond,
			Entries: append([]Entry(nil), existing.entries...),
		})
	}
	return json.Marshal(payload)
}

// Restore 从快照恢复层。
func (t *LayeredDropTable) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	loaded := make([]layer, 0, len(payload.Layers))
	seen := make(map[string]struct{}, len(payload.Layers))
	for _, snapshot := range payload.Layers {
		if !validCond(snapshot.Cond) {
			return ErrInvalidCond
		}
		if _, ok := seen[snapshot.ID]; ok {
			return ErrLayerExists
		}
		entries := make([]Entry, len(snapshot.Entries))
		for index, entry := range snapshot.Entries {
			if entry.Weight < 0 {
				return ErrInvalidWeight
			}
			if entry.ID == "" || entry.MaxCount < entry.MinCount {
				return ErrInvalidEntry
			}
			entries[index] = entry
		}
		seen[snapshot.ID] = struct{}{}
		loaded = append(loaded, layer{id: snapshot.ID, cond: snapshot.Cond, entries: entries})
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.opts = payload.Opts
	t.layers = loaded
	return nil
}

func validCond(cond LayerCond) bool {
	switch cond.Kind {
	case "base", "difficulty", "firstkill":
		return true
	default:
		return false
	}
}

func layerActive(existing layer, ctx Context) bool {
	switch existing.cond.Kind {
	case "base":
		return true
	case "difficulty":
		return existing.cond.Difficulty == ctx.Difficulty
	case "firstkill":
		return ctx.FirstKill
	default:
		return false
	}
}

// effectiveLocked 按层加入顺序叠加有效层：同名条目整条覆盖且保留首次出现位置，
// 新条目按出现顺序追加在末尾。调用方必须持有读锁或写锁。
func effectiveLocked(layers []layer, ctx Context) Table {
	entries := make([]Entry, 0)
	indexByID := make(map[string]int)
	for _, existing := range layers {
		if !layerActive(existing, ctx) {
			continue
		}
		for _, entry := range existing.entries {
			if index, ok := indexByID[entry.ID]; ok {
				entries[index] = entry
			} else {
				indexByID[entry.ID] = len(entries)
				entries = append(entries, entry)
			}
		}
	}

	totalWeight := 0
	for _, entry := range entries {
		totalWeight += entry.Weight
	}
	return Table{Entries: entries, TotalWeight: totalWeight}
}

// mixSeed 把组名与种子混合成确定性的槽位种子，使各槽相互独立且不依赖 map 顺序。
func mixSeed(seed uint64, group string) uint64 {
	hasher := fnv.New64a()
	hasher.Write([]byte(group))
	mixed := seed ^ hasher.Sum64()
	mixed ^= mixed >> 30
	mixed *= 0xbf58476d1ce4e5b9
	mixed ^= mixed >> 27
	mixed *= 0x94d049bb133111eb
	mixed ^= mixed >> 31
	return mixed
}
