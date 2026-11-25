package task

import (
	"errors"
	"sync"
)

// 公开错误，供调用方按类别判断失败原因。
var (
	ErrInvalidPPM     = errors.New("drop_rate_modifier: ppm must not be negative")
	ErrInvalidSource  = errors.New("drop_rate_modifier: player, item, kind and source id are required")
	ErrSourceNotFound = errors.New("drop_rate_modifier: source not found")
	errNotImplemented = errors.New("drop_rate_modifier: not implemented")
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

// NewDropRateModifier 构造掉率修正器。
func NewDropRateModifier(opts Options) *DropRateModifier {
	return &DropRateModifier{opts: opts}
}

// Cap 返回最终概率硬顶。
func (m *DropRateModifier) Cap() int {
	return 0
}

// SetBase 设置某物品的基础概率。
func (m *DropRateModifier) SetBase(itemID string, ppm int) error {
	return errNotImplemented
}

// Base 返回某物品的基础概率。
func (m *DropRateModifier) Base(itemID string) (int, error) {
	return 0, errNotImplemented
}

// AddSource 为玩家增加一个掉率来源。
func (m *DropRateModifier) AddSource(player, itemID, kind, sourceID string, ppm int, until int64) error {
	return errNotImplemented
}

// RemoveSource 删除玩家名下的来源。
func (m *DropRateModifier) RemoveSource(player, sourceID string) error {
	return errNotImplemented
}

// SourceCount 返回玩家当前的来源条数。
func (m *DropRateModifier) SourceCount(player string) int {
	return 0
}

// Effective 计算给定时刻的最终概率。
func (m *DropRateModifier) Effective(player, itemID string, at int64) (int, error) {
	return 0, errNotImplemented
}

// Breakdown 返回给定时刻的贡献明细。
func (m *DropRateModifier) Breakdown(player, itemID string, at int64) []Contribution {
	return nil
}

// Snapshot 序列化基础表与全部来源。
func (m *DropRateModifier) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore 从快照恢复基础表与来源。
func (m *DropRateModifier) Restore(data []byte) error {
	return errNotImplemented
}
