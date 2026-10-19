---
schema_version: 2
id: git-push-multi-refspec-partial-success
kind: pitfall
risk: medium
status: verified
keywords: [git push, 多 refspec, 部分成功, tail 误判, fast-forward, force-with-lease, 输出核对]
scope: [git push 多分支/多 refspec 场景, 管道输出判断]
summary: "`git push` 多 refspec **部分成功**（A 拒 B 成）时 `| tail` 只见 A 拒因误判全败——按全败处置（rebase/amend/重推）会制造 non-ff 或覆盖已推内容。判果须逐 refspec 看 ok/rejected 行+push 后 fetch 核远端（同 bash-pipeline-exit-masking 家族）。"
first_seen: 2026-09-09
last_verified: 2026-09-09
skills: [验证与交接]

---
