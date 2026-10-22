# seats/ —— 席位台账（D-652-D③）

一席一文件 `seats/<席位名>.md`：开工写一行（席位/票/时点/分支），收口删行。
digest 聚合列出在册行——避 STATE 热文件并发冲突（10-21 三撞病灶）。
例：`seats/impl-devin.md` 内行「2026-10-22 03:00Z wip/rejects-counter D-652-A」。
席位开机先 `bash scripts/check_stale_base.sh`（stale 基探针——红=先 pull 再读 TICKETS/STATE；D-651-B②④）。
