# 交付说明

## 实现范围

- `smuggle_framing_analyzer.go:188`：严格解析请求行、头区上限、头数上限、obs-fold 与冒号前空白；不修改输入切片，`Body` 为头部分隔符后的原始字节。
- `smuggle_framing_analyzer.go:293`：计算帧内体长；CL 取声明值，chunked 只累计数据块，校验十六进制长度、扩展、单块上限、块数上限和截断。
- `smuggle_framing_analyzer.go:319`：每次分析在同一互斥区间完成判定、计数、序号递增和审计环形缓冲追加。
- `smuggle_framing_analyzer.go:356`：识别 CL 冲突、`cl_te`、`te_cl`、`te_te`、重复/异常 TE、体长不足等判定。
- `smuggle_framing_analyzer.go:453`：规范化头名，折叠 CL，剥离 TE、固定逐跳头及 `Connection` 点名头，并复制 Body，避免改调用方数据。
- `smuggle_framing_analyzer.go:590`：JSON 快照保存序号、计数和有序审计；`Restore` 先完整校验再整份替换，损坏快照返回 `ErrBadSnapshot` 且不影响现网。

## 并发与恢复

- `Analyze`、`Counts`、`Audit`、`Reset`、`Snapshot`、`Restore` 共用同一把互斥锁。
- 已用临时并发测试验证 16 goroutine × 100 次 `Analyze` 与并发快照/读取，计数总量等于调用次数；另验证 `MaxAudit=2` 时快照恢复后下一条序号为 `4`，审计保持 `seq=3,4`。
- 临时测试已删除，未修改或跳过 `tests/test_smuggle_framing_analyzer_test.go`。

## 跑测记录

```text
$ go test ./...
?   gsb/batch49-go-fix-a-05 [no test files]
ok  gsb/batch49-go-fix-a-05/tests

$ go test ./... -v
--- PASS: TestParseSplitsRequest
--- PASS: TestParseRejectsMalformedLineAndVersion
--- PASS: TestParseEnforcesHeadCaps
--- PASS: TestParseRejectsObsoleteFolding
--- PASS: TestParseRejectsWhitespaceBeforeColon
--- PASS: TestAnalyzeRejectsConflictingContentLength
--- PASS: TestAnalyzeDetectsCLTE
--- PASS: TestAnalyzeDetectsTECL
--- PASS: TestAnalyzeDetectsTETE
--- PASS: TestChunkedBodyWithExtensionsIsAccepted
--- PASS: TestChunkSizeOverflowIsRejected
--- PASS: TestChunkCountLimitIsEnforced
--- PASS: TestBodyLengthMismatchIsRejected
--- PASS: TestCanonicalizeNormalizesAndStripsHopByHop
--- PASS: TestSnapshotRestoreKeepsCountersAndAudit
PASS: 15/15

$ go test -race ./...
?   gsb/batch49-go-fix-a-05 [no test files]
ok  gsb/batch49-go-fix-a-05/tests
```

临时并发自检存在时，`go test -race ./...` 的根包与 `tests` 包均为 `ok`；验证完成后已删除该临时测试。
