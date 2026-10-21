---
schema_version: 2
id: selective-git-add-missed-file
kind: pitfall
risk: medium
status: verified
keywords: [git add, 漏提交, 编译断, fake stub, 接口扩展]
scope: [git-workflow, commit-hygiene]
summary: 显式 git add 清单漏文件→工作区测试过但 commit 后 master 断编译；接口扩展时「测试桩完备件」最易漏
first_seen: 2026-10-21
last_seen: 2026-10-21
---

## 显式 add 清单漏件：工作区绿≠提交内容绿

- **病灶**：§4 纪律「只 add 显式文件禁 -A」+接口扩展产生「适配桩文件」（fake 实现补接口方法）——文件列清单手工枚举，桩文件在测试文件而非主件目录，极易漏 add。commit 后 master 上测试包编译断（fake 不实现接口）。
- **实证（10-21 D-652-C 票）**：`service_graduation_store.go` graduationStore 扩 4 方法→fakeGradDB 桩落 `service_carry_graduation_test.go`；commit 清单 17 文件漏它→本机测试全绿（工作区有桩）而 master 断编译；后续 docs commit 被 staticcheck 基线闸如实拦下暴露。
- **修法**：①commit 前 `go build ./... && go vet ./...` 对**已暂存态**核（`git stash -u` 后跑或 CI 闸）；②接口扩展票把「全 fake/桩文件」列入 add 清单模板；③提交后立刻远端对拍（本例 wip 分支同一断点被 staticcheck 再抓）。
- **教训**：工作区测试绿不证明暂存内容完整——暂存面与机检面必须同快照。
