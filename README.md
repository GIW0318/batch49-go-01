# HTTP 请求走私检测与前后端解析差异

## 题面

护网前的最后一次压测里，边缘网关和后端服务对同一条请求给出了两种帧长：边缘按 Content-Length 截断，后端按 Transfer-Encoding 继续读，多出来的那段被后端当成下一条请求；另一组样本里，两个 Content-Length 值不一样，网关只取了第一个，后端却取了最后一个；还有把 Transfer-Encoding 拆成两个头、把 chunked 塞在中间的做法，两边都放行了，值班同学只能靠抓包逐条比对。改动落在 gsb/batch49-go-fix-a-05 的 smuggle_framing_analyzer.go，类型 SmuggleFramingAnalyzer。关键签名保持：NewSmuggleFramingAnalyzer(cfg AnalyzerConfig) *SmuggleFramingAnalyzer、Parse(raw []byte) (Request, error)、BodyLength(req Request) (int, error)、Analyze(raw []byte) (Verdict, error)、Canonicalize(req Request) (Request, error)、Counts() []ClassCount、Audit() []AuditEntry、Reset()、Snapshot() ([]byte, error)、Restore(data []byte) error。

要修的点：一、空报文报 ErrEmptyRequest；请求行必须三个字段且版本为 HTTP/1.1，否则报 ErrMalformedHead 或 ErrBadVersion。二、头区超过 MaxHeaderBytes 报 ErrHeaderTooLarge，头数超过 MaxHeaders 报 ErrTooManyHeaders；折叠续行报 ErrObsFold，冒号前带空白报 ErrHeaderWhitespace。三、同名 Content-Length 值不一致报 ErrAmbiguousFraming 并归入 cl_conflict，值相同则保留一条并在规范化时折叠。四、同时出现 Content-Length 与 Transfer-Encoding 时按两侧画像判定：前端 TE 后端 CL 归 cl_te，前端 CL 后端 TE 归 te_cl，两侧都 TE 归 te_te，一律报 ErrSmugglingDetected 并拒收。五、Transfer-Encoding 出现多次、最终编码不是 chunked、或含非 chunked 的未知 token，按 te_te 或 ErrBadFraming 处理。六、分块体按十六进制长度解析，分号起头的扩展允许；单块超过 MaxChunkBytes 报 ErrChunkOverflow，数据块数超过 MaxChunks 报 ErrChunkCount；扩展内容必须是 token 或带引号串，出现非法字符报 ErrMalformedHead，长度字母大小写混写合法。七、按 Content-Length 取体时，实际可用字节少于声明值报 ErrBodyLengthMismatch；多出的字节不得吞掉，应留给下一条请求。八、BodyLength 返回帧内体字节数：定长取声明值，分块取各数据块长度之和，两种模式都不得把分块头尾计入。九、Canonicalize 输出前把头名规范成词首大写，Content-Length 折叠为一条且等于体长，剥掉 Transfer-Encoding 与固定逐跳头名单；Connection 头里点名的自定义头也必须剥离。十、Parse 不得修改入参切片，返回的 Body 是头区之后的原始字节；请求目标可以是以斜杠开头的路径。十一、Counts 按类名升序返回，每次 Analyze 无论放行还是拒收都计数并追加一条审计，审计只保留最近 MaxAudit 条，序号单调递增。十二、Snapshot 覆盖计数与审计环形缓冲，Restore 整份替换，损坏快照报 ErrBadSnapshot 且不动现网，恢复后审计序号继续递增。

硬约束：并发 Analyze 与快照必须互斥，计数与审计的更新原子生效；单次分析不得修改调用方传入的报文切片；恢复后序号与环形缓冲次序延续，下一次追加不得覆盖更早的有效条目。验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现，交付源码、跑测记录和一页以内说明。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-fix-a-05`
- 实现文件：`smuggle_framing_analyzer.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`SmuggleFramingAnalyzer`
