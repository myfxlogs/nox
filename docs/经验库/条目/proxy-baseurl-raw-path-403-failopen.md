---
schema_version: 2
id: proxy-baseurl-raw-path-403-failopen
kind: pitfall
risk: high
status: verified
keywords: [cex-proxy, BinanceBaseURL, BinanceApiSegBase, exchangeInfo, 403, fail-open, 闸静默失效, 段基址]
scope: [collect, binance, proxy, rest, catalog-gate]
summary: 新 REST 消费面裸拼 BinanceBaseURL+"/v1/..."——生产该值=cex-proxy 基址（缺 /fapi 段+restNative 裸 client 无 token）→403→fail-open 使存在性闸**静默失效全量订阅**；SG 实证 perp 目录闸上线首轮即因 403 空转
first_seen: 2026-10-22
last_verified: 2026-10-22
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: SG nox-core 日志 `perp exchangeInfo 拉取失败 http 403（fail-open 全量订阅）`@07:34 前一轮；同秒 tradifi_syms 经 BinanceApiSegBase+httpx.Client 枚举 225 品种成功=路由差异实证
  - type: commit
    ref: 57c9ecd82（改 BinanceApiSegBase 段基址+isNativeBinance 判 client——代理域 cfg.Client 带 token+CA/原生域 restNative 裸 client）
repro_or_symptom: 新 REST 拉取代码用 BinanceBaseURL 直拼路径；生产 BinanceBaseURL=cex-proxy 基址而非 fapi.binance.com，代理按 /<seg> 前缀路由且要 X-Proxy-Token——缺段+裸 client=403，配 fail-open 设计=闸装上了但从不生效（全量放行无告警级差异）
root_cause: URL 构造跳过了项目既有单源（httpx.BinanceApiSegBase——已处理原生域 host 映射+代理前缀追加双形）+client 选择跳过了 proxyHosts 判定范式（spotInfoClient 先例在同文件）；作者按「baseURL+fapi 路径」的直觉拼写，未对拍同包既有径
fix_and_prevention: ①binance REST URL 一律过 `httpx.BinanceApiSegBase(base,"fapi"|"api"|...)` 得段基址再拼路径；②client 按 isNativeBinance(解析后 host) 二分——代理域 cfg.Client（token+CA）、原生域裸 client；③**fail-open 闸的拉取败因要进验收面**——「闸装上了」≠「闸在拦」，部署验收锚=quarantine 计数>0 或目录拉取成功日志，不能只验无报错
notes: fail-open 式设计（目录缺期不阻塞采集）正确但伴生风险=败时零业务差异，实证锚必须显式查「目录拉取成功」正事件；tradifi_syms.go:33-54 是同包同端点正确范式可直接照抄
---

## 现象

indiv perp 存在性闸上线后 SG 日志：`perp exchangeInfo 拉取失败 http 403（fail-open 全量订阅）`——闸从未生效，幽灵符号照常入 desired 集。dial 谱同拍见 `xpdusdusdt` 类目录外名依旧建连。

## 根因

`ensurePerpInfo` 两处错配：URL=`BinanceBaseURL+"/v1/exchangeInfo"`（生产 BinanceBaseURL=cex-proxy 基址，代理路由按 `/<seg>` 前缀——正确形=段基址+fapi+v1）+client=`restNative`（裸 client 无 X-Proxy-Token——代理拒 403）。同文件 tradifi_syms 用 `BinanceApiSegBase(base,"fapi")`+`httpx.Client()` 同秒成功枚举 225 品种，两径一对拍即分尸。

## 修法与预防

URL 走 `httpx.BinanceApiSegBase` 单源（原生域→`fapi.binance.com/fapi`，代理域→`base/fapi`）；client 按 `isNativeBinance(host)` 二分（代理→cfg.Client 带 token+CA／原生→restNative 防泄）。fail-open 闸验收锚=**目录拉取成功的正事件证据**（quarantine 计数或枚举行），非「无 panic」。
