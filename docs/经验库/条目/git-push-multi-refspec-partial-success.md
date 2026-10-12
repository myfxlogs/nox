---
schema_version: 2
id: git-push-multi-refspec-partial-success
kind: pitfall
risk: medium
status: verified
keywords: [git push, 多 refspec, 部分成功, tail 误判, fast-forward, force-with-lease, 输出核对]
scope: [git push 多分支/多 refspec 场景, 管道输出判断]
summary: "`git push origin A A:B` 多 refspec **部分成功**（A 被拒、B 成功）时，`| tail` 只看到 A 的 rejection hint，误判为全败——实际 B 已上远端。后续按「全败」处置（rebase/amend/重推）会制造 non-fast-forward 或覆盖刚推成功的内容（当日实证：误推 amend 前脏版本上 master，又需修复 commit 收尾）。修法：①判 push 结果看每个 refspec 自己的 `ok/rejected` 行，禁用 tail 尾行判断（同 bash-pipeline-exit-masking 家族：判断的是 tail 的退出码/尾行，不是 push 的）②push 后必 `git fetch && git status -sb` 或 `git log origin/X -1` 核对远端实况③amend 前先确认远端没有已推的同名 commit。"
first_seen: 2026-09-09
last_verified: 2026-09-09
skills: [验证与交接]

---
