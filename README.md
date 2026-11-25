# 字幕与语音活动锚点的自动重对齐

## 题面

海外剧集上线后字幕整条慢半拍，越到后面越明显，手工对齐一次下一集又歪回去，已经锁定的字幕条还会被再推一次。问题落在 gsb/batch49-go-game-c-05 的 caption_auto_resync.go，需要把 CaptionAutoResync 补齐。对外签名：NewCaptionAutoResync(cfg Config) *CaptionAutoResync；SubmitAnchor(a Anchor) error；Estimate() (Estimate, error)；ApplyStep() (Shift, error)；Lock(cueID string) error；Unlock(cueID string) error；Locked(cueID string) bool；AnchorCount() int；CueShift(cueID string) (int64, bool)；MarkTruncated(cueID string) error；Reset() error；Snapshot() ([]byte, error)；Restore([]byte) error。

要修好的行为：锚点时间以毫秒计，cue 编号为空或全空白报错，任一时间为负报错，同一条 cue 只允许提交一个锚点，重复提交报错；每条锚点的偏移等于语音起点减字幕起点；Estimate 取匹配对偏移的中位数，样本数少于 MinAnchors 要明确报不足，不能用零偏移冒充成功；偏离中位数超过阈值的异常对先剔除再重算中位数，剔除后剩余样本不足时要回退到全部样本的中位数，不能直接报失败；Samples 是参与最终计算的样本数，Outliers 是被剔除的条数，最终样本完全一致时 Confidence 为 1；Confidence 按最终样本最大值减最小值除以两倍阈值从 1 递减，最低到 0；ApplyStep 的单帧推进量有上限，每次朝估计偏移靠近最多 MaxStepMs，AppliedMs 与 RemainingMs 如实返回；反复推进收敛到估计偏移后不再移动，已经对齐时返回 0 与 0；Lock 的 cue 不参与校正，累计位移停在锁定当时的值，Unlock 之后从下一步继续累计；被截断的 cue 在后续帧继续推进时，累计位移不得再次超过单帧上限；锁定、解锁不存在的 cue 报错，同一 cue 的截断标记可以重复设置；配置为零值时取默认：步长 40 毫秒、异常阈值 250 毫秒、最少 3 条锚点，负步长也退回默认；AnchorCount 返回已存锚点条数，CueShift 的第二个返回值表示该 cue 是否在表中，没有登记过的 cue 返回 0 与 false；每次推进量按估计偏移与已经推进量之差计算，不累计误差；Confidence 用浮点计算，样本为空时不得除零；MarkTruncated 只改变该 cue 后续推进的上限口径，不参与锚点估算；快照要带走锚点、锁定状态、截断标记、每条 cue 的累计位移与全局推进量，恢复全有或全无，坏数据报错且不动现有状态。

硬约束：并发提交锚点与并发推进按同一把锁串行，锚点计数与累计位移不能丢，同一 cue 的并发重复提交只能有一个成功，其余报重复；单帧推进上限是硬边界，任何路径都不得一次越过；快照与推进并发时只能看到全量旧状态或全量新状态。验收以 go test ./... 的 15 条全部通过为准，测试不得修改、跳过或按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、运行输出与偏移估算说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-05`
- 实现文件：`caption_auto_resync.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CaptionAutoResync`，辅助类型 `Anchor`、`Estimate`、`Shift`、`Config`
