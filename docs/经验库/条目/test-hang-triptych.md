---
schema_version: 2
id: test-hang-triptych
kind: pitfall
risk: medium
status: verified
keywords: [测试挂起, TRUNCATE 锁, 并发测试, SASL auth timeout, 主机负载, 2GB, grep 吞输出, 假红]
scope: [测试基建, 复审机检, ops]
summary: 包级测试「挂起/假红」三因：重叠发起的运行互抢 TRUNCATE 锁／主机高负载下测试库 SASL 连接饿死（failed SASL auth：timeout）／grep 管道吞 panic 细节——三招排查+三条纪律
first_seen: 2026-09-08
evidence:
  - type: log
    ref: /tmp/d261_test.log——"failed SASL auth：timeout：context deadline exceeded"（120.01s）
  - type: proc
    query: "pgrep -af go-test —— 发现两个并发包级运行（后台任务叠加）"
    observed_at: 2026-09-08
---

## 事实/症状

一日内 dashboard 包级测试多次「挂起 120-600s」或 0.00s 假红，单测隔离跑全过、净态包跑 7s 过——伪装成测试缺陷，实则三个环境因：①我方重叠发起的包级运行（以为死掉的 `&` 尾巴+后台任务）在同一测试库互抢 TRUNCATE 锁 ②主机 load 5-6 时 arb-test-pg2 连 SASL 握手都无法完成（2GB 资源饿死）③`go test | grep` 管道把 panic 栈/失败信息过滤掉导致误判「无输出=挂起」。

## 根因或策略解释

共享测试库+共享 2GB 主机+管道便利，三者叠加时「测试红」的第一嫌疑应是环境而非代码（尤其单测过/净态过/代码与失败面无交集时）。

## 处置

- 排查三招：①`pgrep -af 'go test'` 清点并发运行 ②失败看原文——输出直落文件再读，禁 grep 管道中转 ③挂起时 `pg_stat_activity` 看锁等待 vs 零连接（零连接+失败=SASL 饿死=等负载窗口）。
- 纪律：机检前 load>3 错峰（内存够也不够）；**禁重叠发起同包测试**；pkill 模式要防自匹配（曾自杀 shell）。
- 已入 Claude 私有记忆 ops-2gb-resource-discipline 升级段。

## 证据

- check: 清场+等载后同代码 7.1s/7.8s 全过（多轮复现）
- ref: 会话 2026-09-08（D-259/260/261 收编期三幕剧全记录）
