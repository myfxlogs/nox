# cex-proxy — 静态 IP CEX API 通用反向代理（D-055 升级）

> 目的：arb 服务器出口 IP 不固定或被 CEX 地区限制（如 Binance 对美区 IP 返回 451）。
> 本代理部署在固定公网 IP 的机器上，把 arb 的 CEX REST 请求转发到各交易所——
> **交易所看到源 IP = 代理机固定 IP**，可在 key 白名单锁定。
>
> 前身 `tools/binance-proxy`（Binance 专用）已升级为配置驱动的通用 CEX 代理。
> 加新 CEX = `config.json` 加一行 + DB 设 base_url + 白名单加 IP，不改代码不重新编译。
>
> **当前部署形态（9-28 校正——下文若与本节冲突以此为准）**：公网入口=
> `https://cex.myfxlogs.org`（CF 隧道→代理机 loopback:8443，D-522 收编）；
> 裸 IP:8443 已被代理机 iptables 对外 DROP（仅 lo 放行，9-27 加固）。
> **客户端禁写裸 IP**——D-538 化石端点教训：写死 `192.229.85.35:8443` 的调用方（fossil-allow：引证化石本体）
> 全线 EOF。TLS 由 CF 边缘终结走系统 CA 链，`ARB_BINANCE_PROXY_CA` pin 仅
> 无 CF 裸连场景需要。

## 支持的 CEX

| CEX | 路径前缀 | 上游 | strip_prefix |
|-----|---------|------|-------------|
| OKX REST | `/api/v5` | `https://www.okx.com` | 否 |
| OKX WS（public+private） | `/okxws` | `https://ws.okx.com` | 是 |
| Gate REST（spot+futures） | `/api/v4` | `https://api.gateio.ws` | 否 |
| Gate 现货 WS | `/gatespotws` | `https://api.gateio.ws` | 是 |
| Gate 合约 WS | `/v4/ws` | `https://fx-ws.gateio.ws` | 否 |
| Binance 现货 REST | `/api` `/sapi` | `https://api.binance.com` | 否 |
| Binance 合约 REST | `/fapi` `/public` `/market` `/private` | `https://fapi.binance.com` | 否 |
| Binance 合约用户数据流 WS | `/ws` | `https://fstream.binance.com` | 否 |
| Binance 现货用户数据流 WS | `/spotws` | `https://stream.binance.com:9443` | 是 |
| Binance 现货用户数据流 ws-api 控制面 | `/wsapi` | `https://ws-api.binance.com` | 是 |

WS 路由经 `httputil.ReverseProxy` Upgrade 透传，`tokenGate` 同护（WS 握手一样要 `X-Proxy-Token`）。
`/ws` 是字符串前缀匹配非段匹配——保留给 fstream 用户流，勿新增 `/ws*` 其他路由。

最长前缀匹配：`/api/v5` 命中 OKX 先于 `/api` 命中 Binance，零冲突。

## 配置

`config.json`（工作目录下，不存在则用内置默认）：

```json
{
  "upstreams": [
    {"prefix": "/spotws", "target": "https://stream.binance.com:9443", "strip_prefix": true},
    {"prefix": "/wsapi", "target": "https://ws-api.binance.com", "strip_prefix": true},
    {"prefix": "/okxws", "target": "https://ws.okx.com", "strip_prefix": true},
    {"prefix": "/api/v5", "target": "https://www.okx.com"},
    {"prefix": "/v4/ws", "target": "https://fx-ws.gateio.ws"},
    {"prefix": "/api/v4", "target": "https://api.gateio.ws"},
    {"prefix": "/gatespotws", "target": "https://api.gateio.ws", "strip_prefix": true},
    {"prefix": "/fapi", "target": "https://fapi.binance.com"},
    {"prefix": "/public", "target": "https://fstream.binance.com"},
    {"prefix": "/market", "target": "https://fstream.binance.com"},
    {"prefix": "/private", "target": "https://fstream.binance.com"},
    {"prefix": "/sapi", "target": "https://api.binance.com"},
    {"prefix": "/ws", "target": "https://fstream.binance.com"},
    {"prefix": "/api", "target": "https://api.binance.com"}
  ]
}
```

加新 CEX 示例（假设新交易所用 `/api/v3/`）：

```json
{"prefix": "/api/v3", "target": "https://api.newcex.com"}
```

## 部署步骤（Linux 服务器）

> 注：本节描述**无 CF 的裸 IP 拓扑**（历史形态，`-sans` 自签证书场景）。
> 现网拓扑=CF 前置：代理只绑 loopback:8443（iptables 对外 DROP），公网经
> `cex.myfxlogs.org` 域名进。裸 IP 部署仍适用于无 CF 场景，客户端须配自签 CA。

1. **预检**（确认代理机能到各 CEX API）：
   ```
   curl https://www.okx.com/api/v5/public/time
   curl https://api.binance.com/api/v3/ping
   ```

2. 把 `cex-proxy` 二进制和 `config.json` 拷到固定目录（如 `/opt/cex-proxy/`）。

3. 首次运行（IP 为历史形态示例 fossil-allow）：
   ```
   cd /opt/cex-proxy
   ./cex-proxy -listen=:8443 -sans=192.229.85.35
   ```
   首次运行自动在 `certs/` 生成自签 CA+证书，并打印代理令牌。

4. **放行端口**（防火墙 + 云安全组放行 `8443/TCP`）。

5. **确认从公网可达**（IP 为历史形态示例 fossil-allow）：
   ```
   curl -k https://192.229.85.35:8443/ca-cert
   ```

6. **保持常驻**（systemd）：
   ```ini
   [Unit]
   Description=arb CEX static reverse proxy
   After=network-online.target
   Wants=network-online.target

   [Service]
   Type=simple
   WorkingDirectory=/opt/cex-proxy
   ExecStart=/opt/cex-proxy/cex-proxy -listen=:8443 -sans=192.229.85.35  # 历史形态示例 fossil-allow
   Restart=always
   RestartSec=5
   NoNewPrivileges=true
   ProtectSystem=full
   ProtectHome=true
   PrivateTmp=true

   [Install]
   WantedBy=multi-user.target
   ```

## 客户端（arb 侧）配套配置

- `.env` 权威键（appConfig 白名单可管，**禁脚本/代码硬编码覆盖**——D-538 教训）：
  ```
  ARB_BINANCE_PROXY_TOKEN=<代理令牌>
  BINANCE_PROXY_URL=https://cex.myfxlogs.org
  ARB_OKX_PROXY_URL=https://cex.myfxlogs.org
  ARB_CEX_WS_BASE=wss://cex.myfxlogs.org     # okx/binance WS 代理基址（+_BACKUP 可选；gate 常态直连不走代理——VPS1 IP 被 gate 封禁，D-541 终局）
  # ARB_BINANCE_PROXY_CA=/opt/arb/...       # 仅自签裸连场景需要；CF 域名走系统 CA
  ```
- `exchange_configs.base_url` 同样只写域名（生产现值）：binance=
  `https://cex.myfxlogs.org/fapi` 与 `/api`；okx=`https://cex.myfxlogs.org`；
  gate=`https://cex.myfxlogs.org/api/v4`。
- 各 CEX key 白名单加**代理机出口 IP**（交易所只见代理 IP——固定 IP 之本意；
  域名只是 arb→代理机这一段的外衣）。

## 安全说明

- 自签证书私钥只存于代理机 `certs/`，不进 git。
- `X-Proxy-Token` 鉴权防开放中继——**务必设令牌**。
- 请求带各 CEX 的 API key 头；密钥 HMAC 永不离开 arb 侧。
- WS 代理面：okx/binance 经本代理（握手一样过 `X-Proxy-Token` 闸——token 不泄往
  CEX 原生域，见 D-541 止血约束）；gate 9-28 终局改常态直连（VPS1 IP 被 gate 封禁），
  `/gatespotws`·`/v4/ws` 路由保留备 ARB_GATE_WS_MODE=proxy 回切。
