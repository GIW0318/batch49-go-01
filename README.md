# 跨租户访客邀请：身份复用、席位排队与撤销边界

## 题面

跨租户协作上线一周，客户成功团队报了三类问题：合作伙伴用公司邮箱接受邀请后，系统里多出一个和同名个人账号并存的影子成员，权限对不上；席位买的是二十个，第三个人接受时却直接顶掉了名单里最早的一位；还有个客户把已经入职的访客邀请撤销了，想收回权限，结果对方第二天照样能进后台。请在模块 gsb/batch49-go-safety-a-28 的 cross_tenant_guest_inviter.go 中补全类型 CrossTenantGuestInviter，签名保持：NewCrossTenantGuestInviter(Limits) *CrossTenantGuestInviter、Invite(fromTenant, email, role string) (Invitation, error)、Accept(invitationID, identityID string) (Member, error)、Revoke(invitationID, actorID string) error、AssignRole(memberID, role string) error、Remove(memberID, actorID, reason string) error、Members(tenantID string) []Member、Invitations(tenantID string) []Invitation、ChangeHomeTenant(memberID, newHomeTenant string) error、SeatsUsed(tenantID string) int、Snapshot() ([]byte, error)、LoadSnapshot([]byte) error。

要修的行为：

1. 租户、邮箱、角色任一为空返回 ErrBlankField；跨租户只允许访客、只读、编辑三种角色，其它角色一律返回 ErrBadRole。
2. 邮箱先去掉首尾空白再转小写后入库，Player-1@Example.COM 与 player-1@example.com 视为同一人。
3. 同一租户对同一邮箱重复邀请合并成一条，返回同一邀请编号，角色沿用第一次的，不额外占席位。
4. 新邀请是待接受态并带递增序号，同时占用一个席位，SeatsUsed 如实反映；单租户待接受邀请数受 MaxPendingInvitations 硬顶，超限返回 ErrInvitationLimit。
5. 接受不存在的邀请返回 ErrUnknownInvitation，空身份返回 ErrBlankIdentity。
6. 接受成功后成员继承邀请里的角色与租户，状态为在职，邀请转为已接受，重复接受返回 ErrAlreadyAccepted；成员列表按成员编号升序稳定返回。
7. 撤销未接受的邀请要带操作人，空操作人返回 ErrBlankField；撤销后席位释放、邀请转为已撤销，再用该邀请接受返回 ErrRevoked，未知邀请返回 ErrUnknownInvitation。
8. 改角色只接受合法角色，非法角色返回 ErrBadRole，未知成员返回 ErrUnknownMember。
9. 移除成员必须同时给操作人与原因，任一为空返回 ErrBlankField；移除后释放席位，重复移除返回 ErrUnknownMember。
10. home tenant 变更后按映射重算角色：变成内部租户成员时访客角色转正式成员，重新变回外部访客时再降回访客。
11. 受邀邮箱已有个人账号时，接受邀请必须复用该身份，不得新建影子账号。
12. 席位满时新邀请进等待队列，有人离开后按入队顺序放行。
13. 撤销邀请只影响未接受者，已接受成员的现有权限不受影响。

硬约束：同一租户的席位计数与邀请表在并发邀请、接受、移除下必须一致，不能出现两个请求同时抢到最后一个席位；进程重启后邀请态、成员、席位占用与入队顺序全部延续，损坏快照返回 ErrBadSnapshot 且不改动现网；待接受邀请数与席位占用都受上限硬顶，不允许把超额邀请挤进邀请表。

验收：go test ./... 的 15 条用例全过，测试不得修改、跳过或按固定输入写死；并发抢席位与重启恢复自行验证，只做本地实现，交付源码与跑测记录。

## 运行方式

```bash
go test ./...
```

## 模块与包

- 模块路径：`gsb/batch49-go-safety-a-28`
- 实现文件：`cross_tenant_guest_inviter.go`
- 包名：`task`；测试目录 `tests/` 同样声明 `package task`，通过导入路径引用本模块
- 主要类型：`CrossTenantGuestInviter`
