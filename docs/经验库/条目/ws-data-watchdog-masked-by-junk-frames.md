---
schema_version: 2
id: ws-data-watchdog-masked-by-junk-frames
kind: pitfall
risk: high
status: verified
keywords: [WS 僵尸连接, dataIdle, lastDataAt, channel-conn-count, 订阅态丢失, 看门狗误遮蔽, SIGQUIT dump]
scope: [exchange wsrunner 骨架, okx/gate/binance 行情 WS, 连接保活]
summary: 「数据帧到达=通道活」的判据会被非业务帧遮蔽——WS 服务端订阅态丢失后，channel-conn-count/ack/funding/instruments 帧照流，readIdle 与 dataIdle 双狗全哑，报价断流 20min 零日志；判活须按「业务帧」计，SIGQUIT goroutine dump 是无 pprof 时装死的唯一取证径。
first_seen: 2026-10-09
last_verified: 2026-10-09
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-154
  - type: commit
    ref: af1b88fbd
  - type: server_fact
    source: postgres
    query: "ticks venue='okx' max(ts)=12:47:16 停滞 20min+，其余 venue 0-1s 新鲜；SIGQUIT dump goroutine 31=okx 读循环 IO wait、60731=ping select（结构健康）"
    observed_at: 2026-10-09T13:06:00Z
---
## 事实/症状
okx 行情 12:47 断流，mt-health 5min 阈正确 warn，但共享骨架三重判死（readIdle 60s / dataIdle 10min / DeadRounds 通道死告警）**全部哑火 20min+**，零重连零日志——连接 ESTABLISHED、pong 续期正常、偶发杂帧不断。
## 根因
`wsrunner.go` `lastDataAt` 对**任意** `ReadMessage` 返回帧刷新；判活语义本应是「业务帧」（tickers/funding），实际混入 `channel-conn-count`（OKX 公开端点订阅数变动推送，高频）、subscribe ack、instruments 帧。服务端订阅态半残时杂帧照样到 → 狗永远在「刚见过数据」状态。此前一次「订阅被拒+pong 遮读限 18min」是同族坑的不同变体——判活口径从未升到业务层。
## 修法
D-154：业务帧过滤器（OnFrame 回报有效帧布尔或骨架 DataFilter 钩子），lastDataAt 只由业务帧刷新。取证：无 pprof 时 `kill -QUIT <pid>` 打全 goroutine 栈到 stderr（docker logs 可见）再靠 restart 策略自愈——本次 13:06 实证一击两用。
## 反证条件 / 适用边界
D-154 修复部署后本条退休；venue 把所有频道都计入业务帧的适配器不受影响。
## 泛化判断
通用坑：凡「连接级活性指标」消费「帧到达」事件的系统都要问一句——这帧是业务数据吗？keepalive/conn-count/ack 类控制帧高频且无害，专杀业务判活口径。
