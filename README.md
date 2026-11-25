# 调音台场景保存、召回与参数斜坡

## 题面

直播导播台切场之后推子自己往回爬，几路发送量停在上一套的值上，重启调音台又恢复正常，下一次切场照旧。问题落在 gsb/batch49-go-game-c-01 的 mixer_scene_recall.go，需要把 MixerSceneRecall 的保存与召回链路补齐，对外保持这些签名：NewMixerSceneRecall() *MixerSceneRecall；AddTrack(trackID string) error；SetTrackEnabled(trackID string, enabled bool) error；SetParam(trackID, param string, v float64) error；SetRamp(ms int) error；Save(name string) (Version, error)；Recall(name string, fields []string) (RecallReport, error)；Advance(ms int)；Value(trackID, param string) float64；Version(name string) (Version, bool)；Scenes() []string；Dirty() bool；Snapshot() ([]byte, error)；Restore([]byte) error。参数只有 volume、pan、send、fx 四类，音量与发送量取值 0 到 1，声像 -1 到 1，效果按 0 或 1 记；轨道先用 AddTrack 注册，重复注册报错。设值直接落在当前值与目标值上，斜坡只由召回触发。

要修好的行为：同名场景再次保存要覆盖内容并分配更大的版本号，版本号全局单调，重启后不回退；场景名为空或全空白要报错；未知轨道、未知参数、越界取值一律拒绝，不能静默截断；保存记录的是各参数的目标值，保存完成清掉脏标记；召回不给字段列表时动全部参数，给了列表只动列表内的字段；召回从当前值起坡，按毫秒线性推进，推满后精确落在目标并不再变化；斜坡进行中把某条轨道禁用，它的参数不参与过渡而直接取目标值，召回报告里单列跳变的轨道；斜坡进行中再次保存，写进场景的是目标值而不是当时插值出来的中间值；同一场景同一版本重复召回要幂等，不得把已经在跑的斜坡拉长或重设起点；部分召回时没有被覆盖的字段，脏标记必须保留，不能因为整表回写被一起清掉；快照要带走场景表、版本计数、各轨目标与正在推进的斜坡，恢复要全有或全无，坏数据报错且不动现有状态。召回报告里的 Fields 按参数名升序，Jumped 只列出真正跳变的轨道；Value 对未注册的轨道返回 0 而不报错；SetRamp 的毫秒数只接受 1 到 10000，越界报错且不改变当前斜坡长度；场景表最多保留 32 个名字，超出时淘汰最久没有保存的那一个，其旧版本查询随之落空。

硬约束：并发的设值、保存、召回与快照按同一把锁串行，读值不能看到半更新的组合；同名场景覆盖后仍沿用原来的名字，已经发出去的版本号不再复用；Advance 的单次推进量为负时忽略，不倒退斜坡；单次召回要么整体生效要么整体报错，不允许先改一半再失败。验收以 go test ./... 的 15 条全部通过为准，测试不得修改、跳过或按固定输入写死；并发与快照恢复自行验证；只做本地实现，交付源码、运行输出与简短说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-c-01`
- 实现文件：`mixer_scene_recall.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`MixerSceneRecall`，辅助类型 `Version`、`RecallReport`
