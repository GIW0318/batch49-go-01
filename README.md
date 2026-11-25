# batch49-go-safety-d-14

- 模块路径：`gsb/batch49-go-safety-d-14`
- 包名：`task`
- 实现文件：`isolation_profile_validator.go`
- 测试目录：`tests/`

## 运行方式

在本目录执行：

```bash
go test ./...
```

## 题面

<!--PROMPT-START-->
上周把一批服务迁到共享节点后，发版流水线连续拒绝了十几个隔离画像，运维打开日志只看到一句不合法，看不出是命名空间写错还是挂载冲突；更麻烦的是两份画像单独校验都能通过，合并到同一个 Pod 后立刻起不来，原因却是只读根文件系统又声明了可写临时挂载。还有一个集群因为用户映射写错，容器以镜像内用户启动时直接拿到权限错误，流水线却把它归成普通不合法。请在 gsb/batch49-go-safety-d-14 的 isolation_profile_validator.go 中实现 IsolationProfileValidator，把画像校验、规范化摘要与兼容性判断做成可定位的结果。

需要实现的接口：NewIsolationProfileValidator() *IsolationProfileValidator；Validate(p Profile) (Verdict, Reason, error)；Normalize(p Profile) (Profile, string, error)；Digest(p Profile) (string, error)；Compatible(a, b Profile) bool；Explain(reason Reason) string；Batch(ps []Profile) []Verdict；Namespaces() []string；DefaultProfile() Profile；ValidateNamespace(name string) error；Severity(reason Reason) int；Reasons() []Reason。

要处理的故障与边界：一、画像名为空直接拒绝，原因码为 blank_name，不得回落到别的检查；二、命名空间列表为空返回 no_namespace；三、出现不在已知集合里的命名空间返回 unknown_namespace，并指出具体名字（定位信息写入 Verdict 的 Fields），原因码要稳定，不能随提示文案变化；四、同一命名空间重复声明返回 duplicate_namespace，不能靠后续去重掩盖；五、挂载源为空或目标不是绝对路径返回 bad_mount，原因定位到挂载字段；六、多个挂载指向同一目标返回 mount_overlap，并给出冲突的目标路径；七、用户映射区间为负或长度为零返回 invalid_id_map，整份画像拒绝；八、规范化要把集合字段排序去重、画像名去掉首尾空白，挂载按目标排序后去重、映射按容器起始号排序，同一画像任意字段顺序必须得到同一摘要，摘要只由规范化后的内容决定；九、摘要为 16 位十六进制串，Validate 与 Digest 对同一画像必须一致，画像名为空或命名空间集为空时 Normalize 与 Digest 返回 ErrBadProfile；十、只读根文件系统与需要写入的临时挂载同时声明时冲突，校验要跨字段判定，而不是逐项检查；十一、用户命名空间映射未覆盖镜像内运行用户属于不可用而不是不合法，原因码要与非法映射区分开；十二、批量校验中一条失败不影响其它条目返回，结果与输入同序；Compatible 要求命名空间集合一致、同一目标挂载的可写标志一致，比较必须对称；两个画像各自兼容不代表可以合并，合并后字段可能触发新的冲突，必须重新整份校验。

硬约束：并发校验只读取入参快照，调用方在另开的协程里修改自己的切片不得影响校验结果。规范化摘要不得依赖 map 遍历顺序，同一输入重复校验必须逐字节一致。批量结果要能按输入下标直接对上原始画像，方便流水线回填到具体提交。

验收：go test ./... 15 条必须全过，不得修改测试、跳过或按固定输入写死；并发与重复校验自行验证；只做本地实现。
<!--PROMPT-END-->
