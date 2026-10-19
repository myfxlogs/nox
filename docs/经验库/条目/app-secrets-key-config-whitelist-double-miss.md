---
schema_version: 2
id: app-secrets-key-config-whitelist-double-miss
kind: pitfall
risk: high
status: observed
keywords: [app_secrets, IsAppSecretKey, appSecretSeedKeys, SecretResolver, 白名单, 播种集, 静默no-op, descs副本, NOX_TOTP_SECRET, TOTP, 恒501, fail-closed假象]
scope: [internal/platform/config, app_secrets 新键接入, SecretResolver 消费点, 密钥管理面]
summary: 新密钥键只补 dashboard appSecretDescs（展示副本）而漏补 config 权威双表（IsAppSecretKey+appSecretSeedKeys）→ resolver Get/Set 静默 no-op：写入恒丢、读出恒空，消费端 fail-closed 表现为「未配置」而非报错，三通道（UpdateAppSecret/boot 播种/resolver 读写）同时拦死。
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: commit
    ref: e45ae2f31
  - type: doc
    ref: internal/platform/config/app_secret_overlay.go:20
  - type: server_fact
    source: rpc
    query: POST /totp/login {"code":"000000"} @SG localhost:50051
    observed_at: 2026-10-19T11:15Z
    server_revision: nox-core image 2026-10-19T11:05Z
    response_hash: ""
---

## 事实/症状

TOTP 登录票全件落码并入 master 后，生产 `/totp/login` 恒 501 "totp not configured"（2026-10-19T11:15Z SG 实拍，DB 无行交叉确认）。单测 `TestTOTPSetupPersistsAndIdCode` 红：`secRes.Set("NOX_TOTP_SECRET", …)` 后 `secRes.Get` 仍空——Set 对白名单外键静默 no-op。同键 `NOX_TOTP_SECRET` 已在 dashboard `appSecretDescs`（设置页文案）与 `hotSecretKeys`（热更标注）两处出现，看似接入完毕。

## 根因或策略解释

app_secrets 键的权威闸门在 config 包**两张表**：`IsAppSecretKey`（UpdateAppSecret 写面+SecretResolver.Get/Set 闸）与 `appSecretSeedKeys`（boot env→PG 播种+LoadAppSecrets 装配）。dashboard 的 descs/hotSecretKeys 只是设置页展示副本——补了副本不等于接入通道。SecretResolver 对白名单外键 fail-closed 返回空/不写（防跨域键复用静默 miss 的既定设计），错误形态是**静默的「未配置」**而非报错，测试不过才暴露。D-095 的 NOX_AUTH_TOKEN 当时同形状（补齐时专门写了 channel test），但教训未泛化成接入清单。

## 处置

双表补键 + D-095 同型对抗测试 `TestTOTPSecretResolverChannel`（白名单含键/播种集含键/Set→Get 回读三断言，再漏任一处即红）。新密钥键接入清单（此后照走）：①config `IsAppSecretKey`；②config `appSecretSeedKeys`；③dashboard `appSecretDescs`（文案）；④`hotSecretKeys`（若热读）；⑤消费点走 `secRes.Get` 不直读 env。

## 证据

- check: `go test ./internal/platform/config -run TestTOTPSecretResolverChannel -count=1`
- result: pass（2026-10-19，e45ae2f31）；修前同族断言 `TestTOTPSetupPersistsAndIdCode` 红=病灶可复现
- ref: e45ae2f31；SG RPC 501 基线 observed_at 2026-10-19T11:15Z

## 反证条件

- check: 新键双表齐全后 `NewSecretResolver(nil).Set(k,v)`→`Get(k)` 回读
- falsifies_when: 双表齐全仍回空（存在第三道闸或种子覆盖逻辑）→ 本条「双表即全闸」结论失效
