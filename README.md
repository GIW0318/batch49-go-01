# 材质变体关键字集合与裁剪

## 题面

打包工具换了一版实现之后，同一份材质在编辑器里预览正常，进包却少了夜间和雾天两套组合，玩家看到的是纯黑物件；另一头又有两套互相排斥的关键字被同时点亮。美术在打包清单里还发现，有些组合明明没有任何材质引用，却仍然被编进了包里，构建时间无谓地翻了一倍。落点 gsb/batch49-go-game-b-07 的 material_variant_stripper.go，类型 VariantStripper，公开签名保持：NewVariantStripper() *VariantStripper、DeclareKeyword(name, group string, deps []string) error、SetDependencies(name string, deps []string) error、Group(name string) string、Resolve(keywords map[string]bool) (VariantKey, error)、Strip(refs []MaterialRef) (VariantSet, error)、RefCount(key VariantKey) int、Variants() []VariantKey、DefaultVariant() VariantKey、Explain(key VariantKey) (VariantInfo, error)、Validate(key VariantKey) error、Snapshot() ([]byte, error)、Restore([]byte) error。

要落实的行为：一，关键字声明要带互斥组与依赖列表，名字为空报 ErrBlankKeyword，组为空报 ErrBlankGroup，重名报 ErrDuplicateKeyword，依赖指向未声明的关键字报 ErrUnknownKeyword 且该次声明不生效，查未声明的关键字时 Group 返回空串。二，依赖成环包含自依赖和多跳回边，一律报 ErrDependencyCycle，被拒绝的改动不能污染已有的依赖图。三，解析时把显式置位的关键字连同它的依赖闭包合成一个变体，闭包要递归到最后一层，变体键按关键字名升序用加号拼接，同一组输入换遍历顺序得到的键逐字节一致。四，同一互斥组里两个关键字同时置位报 ErrExclusiveConflict 且不产出变体，置位未声明的关键字报 ErrUnknownKeyword，失败的一次解析不能留下半个变体。五，空集解析出基础变体，键为空串，DefaultVariant 返回同一个键。六，裁剪按引用表统计每个变体的引用次数，只保留引用次数大于零的变体，输出按变体键升序排列，同一批引用换顺序结果不变，被裁掉的个数记在 Dropped 里。七，引用里出现从未解析过的变体键报 ErrUnknownVariant，这次裁剪整体不生效，已有的引用计数保持原样。八，重复引用同一变体累加计数，重新裁剪时计数按本次引用表整体重算，不能残留上一轮的数字。九，依赖闭包自动补进来的关键字不计入显式置位数，回落时也不能拿来替代用户显式置位的关键字。十，互斥组里显式置零与从未出现要分开记录，显式置零表示覆盖默认值，不能当成缺省处理。十一，关键字全空的基础变体始终保留，引用次数为零也不裁掉。

硬约束：声明、解析、裁剪会在资源热更线程与打包线程之间并发调用，同一变体的引用计数增减必须原子，-race 下不得出现计数漂移或读到半份关键字表；Snapshot 要保存关键字表、依赖关系、已解析变体与引用计数，Restore 后同一批输入解析出的键与恢复前逐字节一致，损坏负载报 ErrBadSnapshot 且不动现有表。

验收：go test ./... 的 15 条用例全部通过，测试文件不得修改、跳过或按固定组合写死；并发声明与重启恢复请自行构造场景验证；只做本地实现。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-game-b-07`
- 实现文件：`material_variant_stripper.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`VariantStripper`、`VariantKey`、`MaterialRef`、`VariantSet`、`VariantInfo`
