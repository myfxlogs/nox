---
schema_version: 2
id: go-transport-clone-h2-contamination
kind: pitfall
risk: critical
status: verified
keywords: [http2, DefaultTransport, Clone, ALPN, malformed response, TLSNextProto, NextProtos, httpx, ForceAttemptHTTP2, transport, SG]
scope: [platform, transport, httpx, go-stdlib]
summary: http.DefaultTransport 一经使用即被懒配 h2（onceSetNextProtoDefaults 注 TLSNextProto+NextProtos），Clone() 浅继中毒字段→ALPN 供 h2→服务端回 h2 帧落 h1 解析器=malformed；ForceAttemptHTTP2=false 只挡懒配时机不挡字段继承——clone 后须显式钉 NextProtos=[http/1.1]+TLSNextProto 空表
first_seen: 2026-10-23
---

# Go http.DefaultTransport Clone 携 h2 感染——malformed 响应病灶

**签名**：`net/http: HTTP/1.x transport connection broken: malformed HTTP response "\x00\x00\x12\x04\x00\x00\x00\x00\x00..."`（字节=h2 SETTINGS/WINDOW_UPDATE 服务器前导帧）

**现象**：Go client 走 h1（ForceAttemptHTTP2=false 已设），服务端却回 h2 裸帧→解析崩。宿主 curl/wget/裸 Go 探针全通，进程内装配栈确定性败——易被误判为「网络/中间盒病」。

**根因**：`http.DefaultTransport` 一经使用即被懒配 h2——`onceSetNextProtoDefaults`→`http2configureTransports` 往 `TLSNextProto` 注 h2 handler、往 `TLSClientConfig.NextProtos` 注 `"h2"`。**此后任何 `Clone()` 浅继中毒字段**：`ForceAttemptHTTP2=false` 只挡懒配时机不挡已染字段继承→clone 的 ClientHello 仍供 h2→server 挑 h2→client 侧无有效 h2 处理→h1 解析器吃到 h2 帧=malformed。

**修法**（assembleRT 钦定式，nox commit 22b0f5920）：
```go
tr = tr.Clone()
tr.ForceAttemptHTTP2 = false
tr.TLSNextProto = map[string]func(string,*tls.Conn) http.RoundTripper{} // 清染
tc := tr.TLSClientConfig; if tc==nil {tc=&tls.Config{}} else {tc=tc.Clone()}
tc.NextProtos = []string{"http/1.1"} // 钉死 h1
tr.TLSClientConfig = tc
```
CA 并入 `tc.RootCAs`——勿整体替换 TLSClientConfig（丢 h1 钉）。

**排查法（分层否证）**：宿主 curl h1/h2 通 → 容器 wget 通 → 容器同 netns 裸 Go 探针通 → httpx 装配栈确定性败 → **同进程交错对照**（fresh×N 败 vs bare×N 通）钉出装配层 → dump DefaultTransport 字段实证中毒。

**教训**：环境病灶假设（GFW/中间盒/IPv6）花最多——真实病灶是进程内标准库感染。传输类怪错先做「同进程同栈对照」，再谈环境。
