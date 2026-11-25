# batch49-go-fix-a-17

模块路径：`gsb/batch49-go-fix-a-17`

包名：`task`

源文件：`waf_template_escaper.go`

测试目录：`tests/`

## 题面

规则运营同学把一个刚上线的 WAF 模板回滚了：模板本身只有一行，参数位置却允许业务方自己填值，有人填了一个带引号和反斜杠的路径，整条规则在解析阶段就被截断，后面的匹配条件全部失效；还有人提交了百分号双重编码的值，解码一次之后仍然是编码串，规则看着正常，实际匹配的是另一段内容。安全组要求把模板编译和取值转义拆开，任何越界取值都要在编译期或渲染期被挡住。改动落在 gsb/batch49-go-fix-a-17 的 waf_template_escaper.go，类型 WafTemplateEscaper。关键签名保持：NewWafTemplateEscaper(limits Limits) *WafTemplateEscaper、Compile(t Template) (CompiledRule, error)、Activate(ruleID string) error、Deactivate(ruleID string) error、Render(ruleID string, values map[string]string) (string, error)、Preview(t Template, values map[string]string) (string, error)、Canonicalize(raw string) (Canonical, error)、Rule(ruleID string) (CompiledRule, bool)、Snapshot() ([]byte, error)、Restore([]byte) error。

逐条要求：
① 规则 ID 空白报 ErrBlankRuleID，重复 ID 报 ErrDuplicateRule，源为空或超过长度预算报 ErrBadTemplate 与 ErrLimitExceeded，规则总数或槽位数超限同样报 ErrLimitExceeded。
② 占位符写成 %{name}，%% 表示字面百分号；占位符未声明报 ErrUndeclaredSlot，声明了却没用上报 ErrUnusedSlot，槽位名重复报 ErrDuplicateSlot，类别不是 literal、regex、header、number 之一报 ErrBadSlotClass。
③ 源里的字面部分要检查引号与括号是否配对，出现落单的引号、负深度括号、结尾孤立的 % 都报 ErrBadTemplate，并且不得注册半成品规则。
④ 编译成功的规则初始为停用，代次为 1，摘要非空；未知规则报 ErrUnknownRule，未激活的规则渲染报 ErrRuleNotActive，重复激活报 ErrAlreadyActive，重复停用报 ErrNotActive。
⑤ literal 槽位把反斜杠与双引号转义后放进双引号里；regex 槽位转义正则元字符；header 槽位只接受字母数字与连字符并统一小写；number 槽位按十进制无符号整数规范化，前导零要去掉；缺值报 ErrMissingValue，多余的值忽略。
⑥ 任何槽位出现控制字符、非法 header 名、非法数字都报 ErrInjectionBlocked，错误要能定位到出问题的槽位。
⑦ 同一个槽位在模板里出现多次时，每次出现都要用同一份值并按各自位置正确转义，不能只在第一处替换，也不能把未转义的原值拼进去。
⑧ 渲染前先做一层百分号解码：解码层数记在 Layers（无编码为 0），解码后仍带 %XX 形态的记 Suspicious 但不再解第二层；非法转义报 ErrBadEscape，不能原样透传。
⑨ 渲染结果超过 MaxRenderedBytes 报 ErrRenderTooLong；Preview 走同一套校验与转义但不进注册表，也不要求规则已激活。
⑩ 槽位名判重与报错都按规范化后的小写比较，只有大小写不同的两个声明视为同一个槽位，不能各留一份。
⑪ 规则停用后再次激活不得重新编译，摘要与首次编译逐字节一致；停用期间做过快照恢复，重新激活后渲染结果与首次一致。
⑫ Snapshot 覆盖规则源、槽位、摘要与激活状态，Restore 整份替换；损坏快照报 ErrBadSnapshot 且不动现网。

硬约束：激活、停用与渲染并发时，渲染只能看到激活前或激活后的完整规则，不能读到半张槽位表；同一规则被数十个请求并发渲染，输出必须逐字节一致，不能出现共享缓冲区串值。

验收以 go test ./... 的 15 条用例全绿为准，测试不许改、不许跳过、不许按固定输入写死；并发与重启恢复自行验证；只做本地实现。

## 运行

```bash
go test ./...
```
