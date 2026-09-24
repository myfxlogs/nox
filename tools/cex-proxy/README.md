# cex-proxy — 静态 IP CEX API 通用反向代理（D-055 升级）

> 目的：arb 服务器出口 IP 不固定或被 CEX 地区限制（如 Binance 对美区 IP 返回 451）。
> 本代理部署在固定公网 IP 的机器上，把 arb 的 CEX REST 请求转发到各交易所——
> **交易所看到源 IP = 代理机固定 IP**，可在 key 白名单锁定。
>
> 前身 `tools/binance-proxy`（Binance 专用）已升级为配置驱动的通用 CEX 代理。
> 加新 CEX = `config.json` 加一行 + DB 设 base_url + 白名单加 IP，不改代码不重新编译。

## 支持的 CEX

| CEX | 路径前缀 | 上游 | strip_prefix |
|-----|---------|------|-------------|
| OKX | `/api/v5` | `https://www.okx.com` | 否 |
| Gate | `/api/v4` | `https://api.gateio.ws` | 否 |
| Binance 现货 | `/api` | `https://api.binance.com` | 否 |
| Binance 合约 | `/fapi` | `https://fapi.binance.com` | 否 |
| Binance 模拟现货 | `/demo` | `https://demo-api.binance.com` | 是 |
| Binance 模拟合约 | `/demofapi` | `https://demo-fapi.binance.com` | 是 |
| Gate 合约 WS | `/v4/ws` | `https://fx-ws.gateio.ws` | 否 |
| Binance 合约用户数据流 WS | `/ws` | `https://fstream.binance.com` | 否 |
| Binance 模拟合约用户数据流 WS | `/demofws` | `https://demo-fstream.binance.com` | 是 |
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
    {"prefix": "/api/v5", "target": "https://www.okx.com"},
    {"prefix": "/v4/ws", "target": "https://fx-ws.gateio.ws"},
    {"prefix": "/api/v4", "target": "https://api.gateio.ws"},
    {"prefix": "/demofapi", "target": "https://demo-fapi.binance.com", "strip_prefix": true},
    {"prefix": "/demofws", "target": "https://demo-fstream.binance.com", "strip_prefix": true},
    {"prefix": "/demo", "target": "https://demo-api.binance.com", "strip_prefix": true},
    {"prefix": "/fapi", "target": "https://fapi.binance.com"},
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

1. **预检**（确认代理机能到各 CEX API）：
   ```
   curl https://www.okx.com/api/v5/public/time
   curl https://api.binance.com/api/v3/ping
   ```

2. 把 `cex-proxy` 二进制和 `config.json` 拷到固定目录（如 `/opt/cex-proxy/`）。

3. 首次运行：
   ```
   cd /opt/cex-proxy
   ./cex-proxy -listen=:8443 -sans=192.229.85.35
   ```
   首次运行自动在 `certs/` 生成自签 CA+证书，并打印代理令牌。

4. **放行端口**（防火墙 + 云安全组放行 `8443/TCP`）。

5. **确认从公网可达**：
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
   ExecStart=/opt/cex-proxy/cex-proxy -listen=:8443 -sans=192.229.85.35
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

- `.env` 追加：
  ```
  ARB_BINANCE_PROXY_CA=/opt/arb/cex-proxy-ca.pem
  ARB_BINANCE_PROXY_TOKEN=<代理令牌>
  ```
- `exchange_configs` 中各 CEX 的 `base_url` 设为代理地址：
  - Binance spot: `https://192.229.85.35:8443/api`
  - Binance perp: `https://192.229.85.35:8443/fapi`
  - OKX spot/perp: `https://192.229.85.35:8443`（OKX 签名用 `/api/v5/...`，代理透传）
  - Gate spot/perp: `https://192.229.85.35:8443`（Gate 签名用 `/api/v4/...`，代理透传）
- 各 CEX key 白名单加代理机 IP（如 `192.229.85.35`）。

## 安全说明

- 自签证书私钥只存于代理机 `certs/`，不进 git。
- `X-Proxy-Token` 鉴权防开放中继——**务必设令牌**。
- 请求带各 CEX 的 API key 头；密钥 HMAC 永不离开 arb 侧。
- 不代理 WebSocket（CEX 私有 API 全是 REST；公开行情 WS 不需白名单）。
