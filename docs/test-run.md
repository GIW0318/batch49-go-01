# go test ./... 跑测记录

日期: 2026-11-26 05:40:02 CST
Go: go version go1.23.4 linux/amd64

```
?   	gsb/batch49-go-safety-a-25	[no test files]
=== RUN   TestRegisterKeyStartsAtVersionOne
--- PASS: TestRegisterKeyStartsAtVersionOne (0.00s)
=== RUN   TestRegisterRejectsBlankKey
--- PASS: TestRegisterRejectsBlankKey (0.00s)
=== RUN   TestRegisterRejectsBadScope
--- PASS: TestRegisterRejectsBadScope (0.00s)
=== RUN   TestRegisterEnforcesKeyLimit
--- PASS: TestRegisterEnforcesKeyLimit (0.00s)
=== RUN   TestRegisterEnforcesScopeLimit
--- PASS: TestRegisterEnforcesScopeLimit (0.00s)
=== RUN   TestAuthorizeAllowsIntersectedScope
--- PASS: TestAuthorizeAllowsIntersectedScope (0.00s)
=== RUN   TestAuthorizeDeniesUnlistedAction
--- PASS: TestAuthorizeDeniesUnlistedAction (0.00s)
=== RUN   TestAuthorizeRejectsUnknownKey
--- PASS: TestAuthorizeRejectsUnknownKey (0.00s)
=== RUN   TestImpactListsLostScopesWithoutChangingKey
--- PASS: TestImpactListsLostScopesWithoutChangingKey (0.00s)
=== RUN   TestReconcileBuildsDiffWithoutApplying
--- PASS: TestReconcileBuildsDiffWithoutApplying (0.00s)
=== RUN   TestApplyBumpsVersionAndShrinksScopes
--- PASS: TestApplyBumpsVersionAndShrinksScopes (0.00s)
=== RUN   TestApplyUnknownDiff
--- PASS: TestApplyUnknownDiff (0.00s)
=== RUN   TestPriorityBoundsAndDecision
--- PASS: TestPriorityBoundsAndDecision (0.00s)
=== RUN   TestSnapshotRestoreKeepsVersion
--- PASS: TestSnapshotRestoreKeepsVersion (0.00s)
=== RUN   TestRestoreRejectsCorruptSnapshot
--- PASS: TestRestoreRejectsCorruptSnapshot (0.00s)
PASS
ok  	gsb/batch49-go-safety-a-25/tests	0.003s
```
