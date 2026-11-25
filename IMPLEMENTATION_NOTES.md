# 实现说明

## 校验链路

- 按题面顺序裁决：空链/超长、叶子 SAN 主机名、各证书有效期、叶子 CA/serverAuth、签发关系、中间 CA、PathLen、钉扎锚、锚有效期/PathLen/NameConstraints，最后查叶子吊销状态。
- SAN 精确匹配使用大小写不敏感比较；`*.example.com` 只匹配 `a.example.com` 这类单标签主机，不匹配裸域 `example.com` 或多层 `a.b.example.com`。
- PathLen 按叶子在前的索引统计该 CA 之下的 CA 证书；叶子 CA 会先报 `ErrLeafIsCA`。锚的 PathLen 与 NameConstraints 同样生效，名称后缀按点分标签边界匹配。
- `Verify` 只读取入参切片，不排序、不写入；成功时 `ChainLen=len(chain)+1`。

## 吊销缓存

- 以 `{Issuer, Serial}` 结构体为键，两个字符串均原样比较；`Verify` 只查询 `chain[0]`。
- good 在 `StatusTTL` 内新鲜；超过 TTL 但未超过 `StatusTTL+MaxStaleness` 时放行并增加 `Stale`；超过硬期限返回 `ErrStatusStale`。
- revoked 永不过期、不被 good/unknown 覆盖；重复写入 revoked 保持幂等。unknown 作为可淘汰的否定缓存，按 `StatusTTL` 供 `Prune` 清理。
- LRU 链表前端为最久未用；`Status`、`Verify` 命中和状态更新都会续用。容量满时从旧到新跳过 revoked，淘汰第一个 good/unknown；全为 revoked 时返回 `ErrCacheFull`。
- `Prune` 只删除过硬期限的 good/unknown，不增加 `Evictions`。

## 并发与快照

- 单个 `sync.Mutex` 串行化 `Verify`、`RecordStatus`、`Status`、`Prune`、`SetNow`、统计、快照和恢复；命中/未命中、LRU 与计数在同一临界区更新。
- JSON 快照包含版本、锚证书、状态条目、LRU 顺序、单调序号和全部计数。恢复时先完整解析并校验，再整体替换映射、链表和计数；坏快照返回包装后的 `ErrBadSnapshot`，现网状态不变。
- 恢复后沿用快照计数，并用快照序号/`LastUsed` 的最大值继续分配 LRU 序号，因此下一次淘汰仍按恢复出的最近使用顺序执行。

## 验证记录

- `go test -count=1 -v ./...`：`tests` 包 15/15 通过，记录见 `artifacts/go-test-run.log`。
- `go test -count=1 -race ./...`：通过，记录见 `artifacts/go-test-race.log`。
- 额外临时并发/重启 LRU 自检通过后已删除临时测试，记录见 `artifacts/concurrency-restart-self-check.log`。
- `go vet ./...`：无输出、无问题。
