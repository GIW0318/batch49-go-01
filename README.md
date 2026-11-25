# 多设备同步播放的组时间线与追赶

## 题面

多房间同步播放上线后，客厅和书房的音箱总要差半拍，一个房间退出再进来要从头播一遍，改由另一台设备带队之后整组的时间线又跳回起点。落点 gsb/batch49-go-game-c-14 的 group_playback_sync.go，需要把 GroupPlaybackSync 补齐。

成员管理：NewGroupPlaybackSync(cfg Config) *GroupPlaybackSync 用 MaxMembers、MaxDriftMs、CatchupStepMs、StartTimeoutMs 界定组；Join(devID string, caps Caps) (MemberID, error) 把设备拉进组，设备号去空格后为空报 ErrBadDevice，重复加入报 ErrDuplicateDevice，超过 MaxMembers 报 ErrGroupFull，第一个加入的成员当组长，设备能力里带的缓冲区时长只作记录、不改变组的判定阈值；Leave(devID string) error 移出设备，设备不在组里报 ErrMemberNotFound，组长离开时由剩下成员里最早加入的那位接任，组时间线不得因此跳变，正在追赶的成员已经完成的那部分进度也不得清零；成员全部退出后整组回到未起播状态、时间线归零。

就绪与起播：每个成员刚加入时都不算就绪，ReportOffset(devID string, d time.Duration) error 记录设备上报的漂移，绝对值不超过 MaxDriftMs 时成员转为就绪并继续播放，超过就转入暂停等待复校，重新上报回到阈值以内时立刻恢复播放资格；Start(at GroupTime) error 只在全员就绪时把组时间线钉到 at，未就绪报 ErrNotReady 并保留缺失名单，at 早于当前组时间线报 ErrBadStartTime，重复起播报 ErrAlreadyStarted；Ready() bool 与 Missing() []MemberID 给出整体就绪状态与缺失成员，全员就绪之前不得起播，拒绝起播时必须能用 Missing 列出缺席成员，不能静默放过。起播之后才加入的成员从组时间线当前位置起播，跳过的那段时长要计入它的播放位置，之后的漂移判定以这个位置为基准。

推进与追赶：Advance(d time.Duration) error 推进组时间线，只有未暂停的成员跟着前进，暂停成员的播放位置冻结；未起播时推进报 ErrNotStarted，非正推进量报 ErrBadAdvance。CatchupStep() CatchupReport 每帧只把暂停成员往前带一个不超过 CatchupStepMs 的步长，报告里给出本步推进量、剩余差距与是否追平，追平后成员恢复就绪与播放；成员追平之前不得越过组时间线，追上之后停在时间线上。

快照与并发：Snapshot() ([]byte, error) 与 Restore([]byte) error 覆盖成员表、组长、起播状态、组时间线与每个成员的播放位置和漂移，恢复后追赶进度要能接着走，快照要能被同一模块的新实例读取，损坏内容报 ErrBadSnapshot 且不动现网。多个设备会同时上报漂移，成员表与时间线的更新要串行一致，Members 与 Missing 不能读到半更新的成员表。

验收口径：go test ./... 的 15 条用例全过，测试文件不得修改、跳过或按固定输入写死；并发上报与组长变更自行验证；只做本地实现，交付源码与运行记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-14`
- 实现文件：`group_playback_sync.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过模块路径导入实现
- 主要类型：`GroupPlaybackSync`
