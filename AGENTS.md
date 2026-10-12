# NOX — 协作契约

> 唯一真相源。`CLAUDE.md` / `.windsurfrules` 只做一件事：加载本文件。冲突以本文件为准。
> 2026-09-30 重置（D-001）：旧契约、旧 D-001~D-599、旧流程文档全部封存于 `docs/archive/2026-09-reset/`，只读、只作出处引证，**不作规则来源**。

## 1. 定位（方向锚 · 修改须 D# 留痕 + 业主明示）

> **NOX = 跨市场套利套息「自动发现 + 自动执行 + 自动平仓」系统。**
> **资产**：CFD（MT5）+ 加密货币（CEX/DeFi），两侧并进。
> **第一性原则**：**盈利**。自动执行+自动平仓+自动淘汰亏损策略；风控优先于速度。
> **收益来源**：价差收敛 + 息差（funding/swap/carry）。
> **闭环**（业主令 2026-10-11 修正）：发现 → 评估验证 → **纸面沙箱** → 自动执行 → 持仓 → 自动平仓 → 账本 → 复盘进化 → 策略淘汰。发现/验证/纸面三段**零业主接触**——投资人唯一接口=真金动用审批与条件请求响应（D-184）。
> **经营目标（D-030，业主令 2026-10-03）**：资金基数 $10k，净年化 **≥8%**（费用全计、D-013 单源账本口径）。实现路径由决策席全权裁定，业主只配合不操作。每张票、每次复审、每条告警的取舍都以「是否让这个数字更可信、更接近」为准；风控硬线（30 天 −3% 全停 / 事故仓停新开 / CEX≥35%·CFD≥25% 权益 / 90 天内 ≤3 oz）高于目标本身——不为追数字加风险。

> **项目总纲（D-184，业主令 2026-10-11，终极宗旨——此后一切开发以此为纲）**：分工=**基金公司×投资人**。系统（决策席+实施席+本代码库）=基金管理人：机会发现（**全模板枚举×能力账本匹配**）、评估、配置、下单、持仓、平仓、账本、复盘、策略淘汰——全权由系统完成；业主=投资人：**职责只两件——出资、满足系统提出的条件请求**（开户/凭据/权限/资金划转/约束喂入）。约束与能力一律落**能力账本**（机器可读）——不进账本的对系统不存在（BTC 托管线零发现的病灶=约束只在对话里）。**条件缺口→业主请求**为系统强制义务：机会因缺条件不可行时必须显式提案业主，禁静默跳过。净年化 ≥8%（D-030 口径）为最低益利线，风控硬线高于目标本身不变；实现路径（机会模板册/能力账本/匹配引擎）以本纲衍生 D# 票展开。


> **组织与职责=`docs/公司架构.md`（D-212）。**

## 2. 事实优先级

1. 生产实拍（服务器 / DB / venue 返回值）
2. 代码 + 测试（`internal/` `cmd/` `web/src` `migrations/` `proto/`）
3. `docs/modules/<pkg>.md`（每模块一页，阶段 2 逐步填充）
4. `docs/handoff/STATE.md`、`docs/handoff/decisions.md`
5. `docs/经验库/`（踩坑条目，`tools/noxkb gen` 生成索引，禁手编）
6. `docs/archive/`（历史，只读）

同一事实只存一处，其余用指针。文档与代码不一致时改文档，不改代码去迎合文档。

## 3. 角色（agent 无关，两个）

| 角色 | 做什么 | 不做什么 |
|---|---|---|
| **实施** | 领票 → 改代码/测试/模块页 → 自检 → push 分支 → 报审 | 不合并 master；遇设计分叉停手回问，不自行拍板 |
| **复审** | 读 diff → 独立跑机检 → 合并或退回 → 删分支 → 部署 | 不替实施方改代码（退回附清单） |

- 一票的实施与复审**不能是同一会话**。业主逐终端指定角色；未指定=实施。
- 业主只提需求。需求有误时指出并给更优解，不盲从。业主固有域：§1 方向锚、真金动用、凭据密钥、权限条款本身。
- 开工指令格式：`[角色:实施|复审|决策] <一句话目标>`，其余上下文在票（STATE 行）里，不在聊天里。

## 4. 工作流

```
领票（STATE 加一行，owner=会话）→ 分支 wip/<票名>（可用 worktree /opt/nox-wt/<票名>）
→ 改动 + 测试 → 机检（§6）→ commit + push（产出即提交，不攒）→ 报审
→ 复审：机检独立重跑 → merge master → 删分支 + worktree → 部署 → STATE 行翻 ✅
```

- `/opt/nox` = master 部署树：只 pull / 构建 / 部署，不在里面开发。
- 决策/复审会话周首查 `docs/经验库/待归纳*.md` 未消解行（D-172：积压闸推送为主、会话查为辅）。
- 每个可独立交付单位单独 commit；只 `git add` 显式文件，禁 `-A`。
- 涉及生产取证（声明运行时状态/部署后验证/bug 取证）时先调 `.devin/skills/生产取证/` skill（D-173 试点）。
- 涉及策略评审/淘汰裁定/仓位与敞口上限调整时先调 `.devin/skills/策略评估与仓位/` skill（D-179；统计判据只武装证据不定阈值，kill 扳机数学化另票）。
- 报审尾行必须带 `[施工完成:<票号>] @<commit hash>`（如 `[施工完成:D-010] @e2cbb27b`）；缺标记/hash=退回补述。尾行前顺列：遗留疑问、生产取证/部署前置归属、分支与未部署声明。
- 需要决策的改动写 `decisions.md` 一行（D-NNN 顺序取号，引用旧决策写 `旧D-xxx`），不写长文。
- 设计需要讨论时写在票的 STATE 行或 `docs/modules/<pkg>.md`，**不新建流程文档**（施工提示词/复审纪要/互审/返修单一律不再产生）。

## 5. 代码纪律

- 复用已有；最简等价方案；无死代码 / TODO / 调试残留；err 默认 fail-closed，禁吞。
- 前端零信任：运算/校验/分页/排序全后端。
- 腿级数据只认该腿所属 venue 返回值；不用硬编码表替代 venue 值。
- **系统自盯（D-033）**：需要周期性查看的指标/条件由系统计算并推送（飞书/alerts），终端只做决策；票与计划中禁止出现「某时去查」。

**实时性（业主令，D-004；交易系统零延时，前后端同规）**
- **数据流为唯一正道**：有 WS / 推送 / 事件源的数据必须走流。判定、执行、风控、展示路径**禁止**消费轮询结果、定时刷新值、快照或缓存旧值。
- **无流才可拉取，例外须举证**：只有 venue 确实不提供流的数据才允许低频 REST 拉取，且只能用于统计/复盘/参考。实施方负举证：代码注释处附 venue 官方 WS 频道清单出处（文档 URL 或实拍），说明该数据不在任何频道内；复审席核验。「没找到流」≠「没有流」，无举证 = 违规。
- **禁硬编码域值**：venue / 品种 / 费率 / 杠杆 / 额度取自 API、配置或 DB；缺证据 fail-closed 报缺，禁静默默认值。
- **推不设读**：venue 读不回的参数由系统显式设定后用配置值。
- **定时器唯一合法形态**：检测流断线并触发重连/告警的看门狗。看门狗不产出、不携带、不刷新任何业务数据；「ticker 兜底刷数据」「先轮询顶着以后再接流」都属违规，复审直接退回。
- **前端**：只订阅服务端流（StreamUpdates），禁 `setInterval` 拉数据、禁用本地缓存旧值当现值渲染。
- **例外零默认**：以上任何例外都不能由实施方自裁，须在 `decisions.md` 留一行 D#（数据项 + 无流证据 + 用途限制）。
- 功能消失/退化 → 先 `git log` + `blame`，禁不查历史直接重写。
- 文件 ≤300 行（pre-commit 拦）；proto 改动走 `buf generate`。
- 业务配置唯一真相=PG（`app_config`/`app_secrets`，seed-once 后 env 改名失效），env 仅引导件（`scripts/env-allowlist.txt` 只减不增）；新增 env 消费点须业主令。
- **装配点禁 env 驱动**（D-162 死装配实证 `setupMTAdapters`/`MT5_VENUE`）：venue/账户/凭据/订阅集一律 DB 驱动（`broker_account`/`exchange_config`/`broker_symbol_map`）——env 引导件只允许「系统级一次性入参」（凭据首挂、测试库 DSN 类），**不得用 env 决定装哪条 venue、装不装**；只挂在 env 分支上的功能=默认死代码，复审直接退回并要求改 DB 驱动或删除。

## 6. 机检（复审独立重跑，退出码贴进报审）

```
go build ./... && go vet ./... && gofmt -l $(git diff --name-only master -- '*.go')
go test ./... -count=1                     # 测试库=独立实例 postgres://nox:nox@127.0.0.1:5434/nox_test（D-021；env 未设自动缺省，生产 5433 零测试库）
go test -race ./internal/<触碰包>/... -count=1
cd web && npm run build                    # 触碰前端时
```

## 7. 门禁（`git config core.hooksPath scripts/hooks`，禁 `--no-verify`）

pre-commit（10 道）：kill_switch · 凭据直读 · 秘钥扫描 · staticcheck 基线 · 文件行数 · 迁移契约 · gofmt · `_ =` 吞错 · import 方向 · 经验库 schema。
pre-push：master 只允许从 `/opt/nox` 推。reference-transaction：master 禁非 ff 回退。

## 8. 环境

| 项 | 值 |
|---|---|
| 后端 | Go + ConnectRPC + PostgreSQL；`cmd/core` 单二进制，`//go:embed web/dist` |
| 前端 | React 19 + 纯 CSS（不用 Tailwind），`cd web && npm install && npm run build` |
| 生产部署 | **2026-10-07 起双机拓扑（D-105）**：生产=SG `192.229.85.35`（cex-proxy 不落生产，M3 业主令=直连）；本机=开发/测试席。发布流程=本机 `docker build --network=host -t nox-core:latest -f Dockerfile.core .`（`--network=host` 必需——宿主流量走 mihomo 代理而 docker NAT 裸出，npm/go 拉包 ECONNRESET 实证；镜像内 buf regen 已钉 `@bufbuild/buf@1.68.3`） → `docker save nox-core:latest \| gzip -1 \| ssh -i ~/.ssh/id_ed25519 root@192.229.85.35 'docker load'` → 改动文件 rsync 到 SG `/opt/nox` → `ssh root@192.229.85.35 'cd /opt/nox && docker compose -f docker-compose.yml -f docker-compose.sg.yml up -d'`（sg override：pg=15433 避让 alphaforge 5433、mem 适配 4C/7G）。本机 `docker compose up -d --build` 仅用于本机起服务（非生产）。源码同步**唯一入口 `scripts/deploy_sync.sh`**（只推 `git ls-files` 跟踪面；`.env`、`bin/`、`logs/`、`docs/报告` 等 SG 侧运行时/构建产物不推不删——2026-10-09 手写 rsync 多源参数曾毁树+把 bin/ wrapper 盖回 5433 化石版致 xasset_monitor 断写 1h）；SG 二进制只走 `scripts/build_host_tools.sh` 就地重建；SG cron 一律指 `scripts/` 权威版不指 `bin/` 副本。 |
| 生产访问 | SG pg `localhost:15433`（alphaforge 占 5433）；RPC `localhost:50051`；Cloudflare Tunnel 跑在 SG（cloudflared-nox.service）→ `nox.myfxlogs.org` |
| cron | digest 06:10 / backup 03:00 / test-db-gc 04:45 / snapshot 20:17 / xasset-monitor */5 / universe-radar 每时 17 分（Asia/Shanghai；SG crontab 用 CRON_TZ 段定界，勿按宿主 UTC 解读） |
| 备份三落点（D-110） | ① SG 本机 `/opt/nox-backups/nox_*.sql.gz` 14 份滚动（600）；② R2 `r2:nox-backup` age 加密 14 份（配置=仓库外 `/opt/nox-backups/backup-r2.env`，rclone remote=r2）；③ github 代码。还原=`age -d -i <私钥> x.age \| gunzip \| psql`——**私钥业主离线持有，全系统零副本**；外推失败=critical 告警+本地照常 |
| 运行时读写 docs 的路径 | `docs/经验库/`、`docs/报告/`（symdig 产物，不入 git）、`docs/handoff/STATE.md` — 搬动前先改代码 |
| 参考 | `/opt/arbcn`（前端架构 + 加密采集） |
