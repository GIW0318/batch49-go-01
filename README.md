# 元素附着衰减与反应冷却

## 题面

元素战斗改版后，训练场假人的附着条会自己变长，同一套连招的蒸发伤害一场高一场低，切到双目标时更明显。策划按旧公式核对，发现有些命中明明还在技能冷却里，却仍然吃掉了附着，反应伤害凭空多出一截。重开一局后附着又会莫名清零，像是状态只挂在某个客户端上。落点在 gsb/batch49-go-game-a-12 的 element_reaction_resolver.go，类型 ReactionResolver，需要在现有签名上把行为补齐。
公开入口：NewReactionResolver(Options) *ReactionResolver、Attach(target, element string, gauge int, at int64) error、React(hit ReactHit) (ReactionResult, error)、Aura(target string, at int64) []Aura、SetICD(skillID string, frames int) error、ICDWindow(skillID string) int、SetDecay(element string, perTick int) error、DecayOf(element string) int、Clear(target string) error、SuppressedCount(target string) int、ReactionPriority() []string、Snapshot() ([]byte, error)、Restore([]byte) error。
要落到的行为：1) 附着量用整数单位，时间按一百毫秒对齐，每种元素按自己的速率衰减，衰减到零立即移除，折算只在查询或结算时按时间差进行，不依赖后台定时器；2) 同元素再次附着取较大值而不是相加，否则连续小附着会把量堆爆；3) 每个目标最多保留两个附着，超出时替换当前量最小的，同量按元素名排序，替换时新附着从当前时刻重新计时；4) Aura 按元素名升序返回当前量，查询时间早于附着时间时按附着时的量处理；5) 反应按固定优先级结算，顺序是 vaporize、melt、overload、freeze、electrocharged、superconduct，先命中高优先级；6) 反应消耗附着量与命中量中的较小者，伤害等于命中伤害乘反应倍率再除以一百，倍率表固定，蒸发与融化记两百，过载记一百五十，冻结、感电与超导记一百，未知反应返回零倍率；7) 没有可用附着时不产生反应，也不启动冷却；8) 同一技能对同一目标的反应受内置冷却限制，窗口按帧数乘一百毫秒，从第一次真正产生反应的时刻开始；9) 被冷却抑制的命中不消耗附着量，也不推进窗口起点，只累加一次抑制计数；10) 双元素共存时一次反应只消耗主元素，次要元素的衰减起点不重置；11) SetICD 负帧数报错，未配置技能使用默认窗口，SetDecay 非正速率或未知元素报错，Clear 清空目标且可重复调用；12) 快照保存附着量、衰减起点、冷却窗口与抑制计数，恢复后的第一次反应与重启前一致，抑制计数按目标累计。
硬约束：同一目标的写入必须串行，React 与 Attach 并发时不能看到半份附着表；快照是全量状态，恢复要么整体成功要么保持原状；附着位数与目标数都是硬上限，超限替换或拒绝而不是无限增长；冷却窗口按技能粒度记录，不同技能互不覆盖；并发反应与快照请自行构造验证。
验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；只做本地实现，交付源码、测试记录与衰减冷却口径说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-a-12`
- 实现文件：`element_reaction_resolver.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`ReactionResolver`
